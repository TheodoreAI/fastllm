package llm

import (
	"bufio"
	"bytes"
	"context"
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
// avoids writing a custom (Un)MarshalJSON.
type geminiPart struct {
	Text             string                `json:"text,omitempty"`
	Thought          bool                  `json:"thought,omitempty"`
	FunctionCall     *geminiFunctionCall   `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResult `json:"functionResponse,omitempty"`
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

// geminiCallIDsToNames recovers, for one assistant turn's tool calls,
// which function name each fastllm-internal call ID (see
// toGeminiFunctionCallID) refers to — needed because a later "tool" role
// Message in fastllm's flat history carries only a ToolCallID, but
// Gemini's functionResponse part needs the function Name instead (Gemini
// has no separate call-ID concept the way OpenAI/Anthropic do). Built
// fresh from the message list on every request rather than threaded
// through as extra state, since toGeminiRequest already has to walk the
// whole list once anyway.
func geminiCallIDsToNames(messages []Message) map[string]string {
	names := make(map[string]string)
	for _, m := range messages {
		for _, call := range m.ToolCalls {
			names[call.ID] = call.Function.Name
		}
	}
	return names
}

// toGeminiFunctionCallID synthesizes a fastllm-internal ToolCall.ID for a
// functionCall part in Gemini's response — Gemini doesn't hand back an ID
// of its own the way OpenAI/Anthropic do, only the function name and
// args, but fastllm's shared runFileTools loop (internal/chat/handler.go)
// keys everything (including the matching "tool" role reply) by
// ToolCall.ID. Index is the part's position within the candidate's
// content, which is enough to keep IDs unique within one turn even if the
// same function is called more than once.
func toGeminiFunctionCallID(name string, index int) string {
	return fmt.Sprintf("gemini_%s_%d", name, index)
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
	callNames := geminiCallIDsToNames(messages)

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
				parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{Name: call.Function.Name, Args: args}})
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
					Name:     callNames[m.ToolCallID],
					Response: map[string]any{"result": m.Content},
				}}},
			})

		default: // "user"
			contents = append(contents, geminiContent{Role: "user", Parts: []geminiPart{{Text: m.Content}}})
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
}

// StreamChat implements the same signature as Client.StreamChat — see
// Router's doc comment for the dispatch logic. Gemini's streamGenerateContent
// endpoint (with alt=sse) frames each chunk as a standard SSE "data:"
// line carrying one geminiStreamChunk, so despite the very different
// request shape the actual line-scanning loop looks like the other two
// clients'. A part is reasoning text (not the visible answer) when its
// "thought" flag is set.
func (c *GeminiClient) StreamChat(ctx context.Context, model string, messages []Message, thinkLevel string, onToken func(string), onReasoning func(string)) error {
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
	return scanner.Err()
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
				call := ToolCall{ID: toGeminiFunctionCallID(part.FunctionCall.Name, i), Type: "function"}
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
