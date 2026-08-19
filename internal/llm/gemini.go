package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// GeminiModels lists the Gemini/Gemma chat models offered in the model
// picker when a Gemini API key is configured — see AnthropicModels's doc
// comment for why this is a fixed list rather than a live query. Confirmed
// against a real key's ListModels response (2026-08) rather than
// guessed — that response also included several TTS/image/video/robotics/
// research preview models, deliberately left out here since they aren't
// plain chat models. The whole 2.5 generation was dropped after Google's
// API started rejecting gemini-2.5-flash for this key with "no longer
// available to new users... use models/gemini-3.6-flash" — the
// "-latest" aliases below exist specifically so this list doesn't need
// hand-updating every time Google rotates which dated snapshot is
// current; the explicit 3.x versions are kept alongside them only as a
// pinned fallback. Includes the Gemma 4 open-weight models (served
// through the same Gemini API, on the same free tier as the hosted Gemini
// models) alongside the hosted Gemini models themselves.
var GeminiModels = []string{
	"gemini-pro-latest",
	"gemini-flash-latest",
	"gemini-flash-lite-latest",
	"gemini-3.6-flash",
	"gemini-3.5-flash",
	"gemini-3.5-flash-lite",
	"gemini-3.1-pro-preview",
	"gemma-4-31b-it",
	"gemma-4-26b-a4b-it",
}

// GeminiClient talks to Google's Generative Language API
// (generativelanguage.googleapis.com), a third distinct wire format:
// "contents"/"parts" instead of a flat messages array, a top-level
// systemInstruction, an API key passed as a URL query parameter instead
// of a header, and streamed responses framed as a JSON array rather than
// text/event-stream "data:" lines.
type GeminiClient struct {
	APIKey     string
	HTTPClient *http.Client
}

func NewGeminiClient(apiKey string) *GeminiClient {
	return &GeminiClient{APIKey: apiKey, HTTPClient: &http.Client{Timeout: 5 * time.Minute}}
}

// geminiPart is a union of every part shape this file sends or receives —
// Gemini's actual API discriminates by which field is present rather than
// a "type" tag, so one struct with omitempty on every field (rather than
// several part types plus a wrapper) matches the wire format directly and
// avoids writing a custom (Un)MarshalJSON. ThoughtSignature is an opaque,
// per-functionCall-part token some Gemini models (observed on the 3.x
// generation) attach to their own function calls — required to be echoed
// back verbatim on that same part when the call/result pair is replayed
// in a later turn's history, or the API rejects the request with
// "Function call is missing a thought_signature" even though nothing
// about the call itself changed. See
// https://ai.google.dev/gemini-api/docs/thought-signatures and
// toGeminiToolCallID's doc comment for how it survives the round trip
// through fastllm's provider-agnostic ToolCall.
type geminiPart struct {
	Text             string                `json:"text,omitempty"`
	Thought          bool                  `json:"thought,omitempty"`
	ThoughtSignature string                `json:"thoughtSignature,omitempty"`
	FunctionCall     *geminiFunctionCall   `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResult `json:"functionResponse,omitempty"`
	InlineData       *geminiInlineData     `json:"inlineData,omitempty"`
}

// geminiInlineData is Gemini's image-part shape — the API's own field
// names (mime_type/data, base64 payload with no data: URI prefix), unlike
// OpenAI's single data-URI string or Anthropic's {type,media_type,data}.
type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type geminiFunctionResult struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiSystemInstruction struct {
	Parts []geminiPart `json:"parts"`
}

type geminiThinkingConfig struct {
	ThinkingBudget  int  `json:"thinkingBudget"`
	IncludeThoughts bool `json:"includeThoughts"`
}

type geminiGenerationConfig struct {
	ThinkingConfig *geminiThinkingConfig `json:"thinkingConfig,omitempty"`
}

type geminiFunctionDeclaration struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

type geminiRequest struct {
	Contents          []geminiContent          `json:"contents"`
	SystemInstruction *geminiSystemInstruction `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig  `json:"generationConfig,omitempty"`
	Tools             []geminiTool             `json:"tools,omitempty"`
}

// geminiToolCallMeta is everything about a Gemini functionCall part that
// fastllm's provider-agnostic ToolCall (just ID/Type/Function.Name/
// Function.Arguments — see client.go) has nowhere to carry: the function
// Name (needed again for the matching functionResponse part later — see
// toGeminiRequest's "tool" case) and the ThoughtSignature Gemini requires
// echoed back verbatim on replay (see geminiPart's doc comment). Rather
// than adding Gemini-specific fields to the shared ToolCall type fastllm
// uses for every provider, this rides along packed into ToolCall.ID
// itself (see toGeminiToolCallID/parseGeminiToolCallID) — ID is already a
// synthesized, Gemini-only-meaningful string with no format any other
// caller depends on.
type geminiToolCallMeta struct {
	Name             string
	ThoughtSignature string
}

// geminiToolCallIDPrefix marks a ToolCall.ID as one of these packed
// IDs — used defensively in parseGeminiToolCallID so a plain, unpacked ID
// (from a code path that doesn't go through toGeminiToolCallID) decodes
// to a zero-value geminiToolCallMeta instead of panicking or silently
// misparsing.
const geminiToolCallIDPrefix = "gemini:"

// toGeminiToolCallID packs a geminiToolCallMeta into a fastllm-internal
// ToolCall.ID string, base64-encoding the pieces so neither the function
// name nor the opaque, format-unspecified ThoughtSignature can break the
// encoding regardless of what characters they contain. index (the part's
// position within the candidate's content) is folded in too, purely to
// keep IDs unique within one turn if the same function is called more
// than once — it plays no role in decoding.
func toGeminiToolCallID(name, thoughtSignature string, index int) string {
	payload := fmt.Sprintf("%d\x1f%s\x1f%s", index, name, thoughtSignature)
	return geminiToolCallIDPrefix + base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// parseGeminiToolCallID reverses toGeminiToolCallID. Returns the
// zero-value geminiToolCallMeta if id isn't one of these packed IDs (e.g.
// it's blank, or came from a different code path) — callers treat that
// the same as "no name/signature known", not an error, since the two
// fields this recovers are both optional context, not required for the
// tool call to function at all.
func parseGeminiToolCallID(id string) geminiToolCallMeta {
	encoded, ok := strings.CutPrefix(id, geminiToolCallIDPrefix)
	if !ok {
		return geminiToolCallMeta{}
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return geminiToolCallMeta{}
	}
	parts := strings.SplitN(string(decoded), "\x1f", 3)
	if len(parts) != 3 {
		return geminiToolCallMeta{}
	}
	return geminiToolCallMeta{Name: parts[1], ThoughtSignature: parts[2]}
}

// geminiCallIDMeta recovers, for one assistant turn's tool calls, the
// geminiToolCallMeta packed into each fastllm-internal call ID — needed
// because a later "tool" role Message in fastllm's flat history carries
// only a ToolCallID, but reconstructing that turn's functionCall part
// (see toGeminiRequest's "assistant" case) needs the function Name and
// ThoughtSignature back out of it. Built fresh from the message list on
// every request rather than threaded through as extra state, since
// toGeminiRequest already has to walk the whole list once anyway.
func geminiCallIDMeta(messages []Message) map[string]geminiToolCallMeta {
	meta := make(map[string]geminiToolCallMeta)
	for _, m := range messages {
		for _, call := range m.ToolCalls {
			meta[call.ID] = parseGeminiToolCallID(call.ID)
		}
	}
	return meta
}

// toGeminiRequest converts fastllm's flat message list into Gemini's
// contents/systemInstruction shape, and its provider-agnostic Tool
// schemas into Gemini's functionDeclarations. Gemini has only two content
// roles ("user" and "model") — an assistant message becomes "model"
// (translating its ToolCalls into functionCall parts), and both a plain
// user message AND a tool-result message become "user" (Gemini represents
// a tool result as a functionResponse part inside a user-role content,
// there being no third role for it) — like Anthropic, Gemini also expects
// a single system instruction rather than a system-role message
// interleaved into the turn sequence. model selects whether a
// thinkingConfig is included at all: it's a hosted-Gemini-only feature,
// not part of the open-weight Gemma models also served through this same
// API (see GeminiModels) — sending it for a "gemma-*" model risks the API
// rejecting the request over an unrecognized field, so it's left off
// entirely rather than assuming the API will just ignore it.
func toGeminiRequest(model string, messages []Message, tools []Tool, thinkLevel string) geminiRequest {
	callMeta := geminiCallIDMeta(messages)

	var systemParts []geminiPart
	contents := make([]geminiContent, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case "system":
			systemParts = append(systemParts, geminiPart{Text: m.Content})

		case "assistant":
			var parts []geminiPart
			if m.Content != "" {
				parts = append(parts, geminiPart{Text: m.Content})
			}
			for _, call := range m.ToolCalls {
				var args map[string]any
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				// ThoughtSignature must be echoed back verbatim on this
				// same part for the API to accept the replayed history —
				// see geminiPart's doc comment. callMeta[call.ID] recovers
				// it from the packed ID this same call.ID was minted with
				// in GeminiClient.Chat.
				parts = append(parts, geminiPart{
					FunctionCall:     &geminiFunctionCall{Name: call.Function.Name, Args: args},
					ThoughtSignature: callMeta[call.ID].ThoughtSignature,
				})
			}
			contents = append(contents, geminiContent{Role: "model", Parts: parts})

		case "tool":
			// Gemini's functionResponse.response is a JSON object, not a
			// bare string — runFileTools's results are always plain text
			// (a file's contents, a build-check report, ...), so it's
			// wrapped under a single "result" key rather than trying to
			// parse it as structured JSON that it never actually is.
			contents = append(contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{{FunctionResponse: &geminiFunctionResult{
					Name:     callMeta[m.ToolCallID].Name,
					Response: map[string]any{"result": m.Content},
				}}},
			})

		default: // "user"
			var parts []geminiPart
			if m.Content != "" {
				parts = append(parts, geminiPart{Text: m.Content})
			}
			for _, img := range m.Images {
				mediaType, data, ok := splitDataURI(img.DataURI)
				if !ok {
					continue
				}
				parts = append(parts, geminiPart{InlineData: &geminiInlineData{MimeType: mediaType, Data: data}})
			}
			contents = append(contents, geminiContent{Role: "user", Parts: parts})
		}
	}

	req := geminiRequest{Contents: contents}
	if len(systemParts) > 0 {
		req.SystemInstruction = &geminiSystemInstruction{Parts: systemParts}
	}
	if len(tools) > 0 {
		decls := make([]geminiFunctionDeclaration, len(tools))
		for i, t := range tools {
			decls[i] = geminiFunctionDeclaration{Name: t.Function.Name, Description: t.Function.Description, Parameters: t.Function.Parameters}
		}
		req.Tools = []geminiTool{{FunctionDeclarations: decls}}
	}
	if thinkLevel != "" && !strings.HasPrefix(model, "gemma-") {
		budget := map[string]int{"low": 1024, "medium": 4096, "high": 16384}[thinkLevel]
		if budget > 0 {
			req.GenerationConfig = &geminiGenerationConfig{
				ThinkingConfig: &geminiThinkingConfig{ThinkingBudget: budget, IncludeThoughts: true},
			}
		}
	}
	return req
}

type geminiErrorBody struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func geminiError(resp *http.Response) error {
	var eb geminiErrorBody
	if json.NewDecoder(resp.Body).Decode(&eb) == nil && eb.Error.Message != "" {
		return fmt.Errorf("gemini: %s", eb.Error.Message)
	}
	return fmt.Errorf("gemini: chat completion failed: %s", resp.Status)
}

type geminiStreamChunk struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	// UsageMetadata is cumulative and repeated on every chunk (not just the
	// last one) — StreamChat just keeps overwriting its local copy, so
	// whatever was parsed from the final chunk naturally wins.
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

// StreamChat implements the same signature as Client.StreamChat — see
// Router's doc comment for the dispatch logic. Gemini's streamGenerateContent
// endpoint (with alt=sse) frames each chunk as a standard SSE "data:"
// line carrying one geminiStreamChunk, so despite the very different
// request shape the actual line-scanning loop looks like the other two
// clients'. A part is reasoning text (not the visible answer) when its
// "thought" flag is set. onUsage (if non-nil) is called once at the end
// with the last chunk's usageMetadata (see geminiStreamChunk's doc
// comment for why the last one is always the complete one).
func (c *GeminiClient) StreamChat(ctx context.Context, model string, messages []Message, thinkLevel string, onToken func(string), onReasoning func(string), onUsage func(Usage)) error {
	body, err := json.Marshal(toGeminiRequest(model, messages, nil, thinkLevel))
	if err != nil {
		return err
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:streamGenerateContent?alt=sse&key=%s", model, c.APIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return geminiError(resp)
	}

	var usage Usage
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var chunk geminiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // skip malformed/keepalive lines
		}
		if chunk.UsageMetadata.TotalTokenCount > 0 {
			usage = Usage{
				PromptTokens:     chunk.UsageMetadata.PromptTokenCount,
				CompletionTokens: chunk.UsageMetadata.CandidatesTokenCount,
				TotalTokens:      chunk.UsageMetadata.TotalTokenCount,
			}
		}
		if len(chunk.Candidates) == 0 {
			continue
		}
		for _, part := range chunk.Candidates[0].Content.Parts {
			if part.Text == "" {
				continue
			}
			if part.Thought {
				if onReasoning != nil {
					onReasoning(part.Text)
				}
			} else {
				onToken(part.Text)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if onUsage != nil && usage.TotalTokens > 0 {
		onUsage(usage)
	}
	return nil
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

// Chat sends a single non-streaming completion request with the given
// tools declared, and returns the model's reply — which may contain
// ToolCalls instead of (or in addition to) Content, translated from
// Gemini's functionCall parts back into fastllm's provider-agnostic
// ToolCall shape (see toGeminiFunctionCallID's doc comment for why the ID
// is synthesized rather than coming from Gemini itself). Mirrors
// Client.Chat's role in internal/chat.Handler.runFileTools's tool loop.
func (c *GeminiClient) Chat(ctx context.Context, model string, messages []Message, tools []Tool, thinkLevel string) (Message, error) {
	body, err := json.Marshal(toGeminiRequest(model, messages, tools, thinkLevel))
	if err != nil {
		return Message{}, err
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", model, c.APIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Message{}, geminiError(resp)
	}

	var gr geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return Message{}, err
	}

	var text strings.Builder
	var calls []ToolCall
	if len(gr.Candidates) > 0 {
		for i, part := range gr.Candidates[0].Content.Parts {
			switch {
			case part.FunctionCall != nil:
				argsJSON, _ := json.Marshal(part.FunctionCall.Args)
				call := ToolCall{ID: toGeminiToolCallID(part.FunctionCall.Name, part.ThoughtSignature, i), Type: "function"}
				call.Function.Name = part.FunctionCall.Name
				call.Function.Arguments = string(argsJSON)
				calls = append(calls, call)
			case !part.Thought:
				text.WriteString(part.Text)
			}
		}
	}
	return Message{Role: "assistant", Content: text.String(), ToolCalls: calls}, nil
}
