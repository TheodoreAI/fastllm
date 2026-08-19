package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// responsesOnlyModels lists, per provider, which bare model names are
// confirmed to reject the standard OpenAI Chat Completions body
// ({"messages": [...]}) and only accept the newer Responses API body
// ({"input": ...}) instead. Keyed by provider because the same underlying
// model can appear under more than one provider with a different bare
// name — gpt-5.6-luna is both "openai:gpt-5.6-luna" (OpenAI's own hosted
// copy, confirmed 2026-08-19: "Function tools with reasoning_effort are
// not supported for gpt-5.6-luna in /v1/chat/completions... use
// /v1/responses") and "cloudflare:openai/gpt-5.6-luna" (Workers AI's
// hosted copy of the same model, confirmed 2026-08-19 the same way — see
// CloudflareModels' doc comment). Router checks this set (via
// NeedsResponsesAPI) to decide whether a model should go through
// ResponsesClient instead of the plain Chat-Completions Client.
var responsesOnlyModels = map[string]map[string]bool{
	"openai":     {"gpt-5.6-luna": true},
	"cloudflare": {"openai/gpt-5.6-luna": true},
}

// NeedsResponsesAPI reports whether a bare (prefix-stripped) model, for
// the given provider, only works through the Responses API rather than
// Chat Completions.
func NeedsResponsesAPI(provider, bareModel string) bool {
	return responsesOnlyModels[provider][bareModel]
}

// ResponsesClient talks to an OpenAI-compatible Responses API
// (POST {BaseURL}/responses) — a distinct, non-chat-completions wire
// format now used by a growing number of newer models (confirmed so far:
// gpt-5.6-luna, on both OpenAI's own API and Cloudflare Workers AI's
// hosted copy of it — see responsesOnlyModels above): a flat "input"
// string instead of a "messages" array, and "output_text"/an "output"
// item array instead of "choices[].message" in the response. Despite the
// name, this type is provider-agnostic — it's just BaseURL+APIKey, same
// as the plain Chat-Completions Client — so one instance is built per
// provider that has at least one Responses-only model (currently OpenAI
// and Cloudflare).
type ResponsesClient struct {
	BaseURL    string // e.g. https://api.openai.com/v1 or https://api.cloudflare.com/client/v4/accounts/{id}/ai/v1
	APIKey     string
	HTTPClient *http.Client
}

func NewResponsesClient(baseURL, apiKey string) *ResponsesClient {
	return &ResponsesClient{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		APIKey:     apiKey,
		HTTPClient: &http.Client{Timeout: 5 * time.Minute},
	}
}

// responsesInput flattens fastllm's message list into the single "input"
// string the Responses API expects — there's no messages array and no
// distinct system-message concept in this schema (aside from the separate
// top-level "instructions" field, left unused here since fastllm's system
// prompt already flows in as a regular message like every other
// provider's request-building code expects). Role labels are kept inline
// so multi-turn context isn't lost, mirroring how a plain-text transcript
// would read.
func responsesInput(messages []Message) string {
	var b strings.Builder
	for i, m := range messages {
		if i > 0 {
			b.WriteString("\n\n")
		}
		switch m.Role {
		case "system":
			b.WriteString(m.Content)
		case "user":
			b.WriteString("User: ")
			b.WriteString(m.Content)
		case "assistant":
			b.WriteString("Assistant: ")
			b.WriteString(m.Content)
		default:
			b.WriteString(m.Content)
		}
	}
	return b.String()
}

// responsesTool is the Responses API's flat tool schema — unlike Chat
// Completions' {"type":"function","function":{name,description,
// parameters}}, the Responses API puts name/description/parameters
// directly on the tool object alongside "type":"function". See
// toResponsesTools below for the translation from fastllm's Tool.
type responsesTool struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

func toResponsesTools(tools []Tool) []responsesTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]responsesTool, len(tools))
	for i, t := range tools {
		out[i] = responsesTool{Type: "function", Name: t.Function.Name, Description: t.Function.Description, Parameters: t.Function.Parameters}
	}
	return out
}

// responsesInputItem is one entry of the Responses API's "input" array —
// a tagged union covering the three shapes this client needs to send: a
// plain conversational turn (Type "message", Role+Content set), a
// replayed model-issued tool call (Type "function_call", CallID+Name+
// Arguments set — required so the model can see its own prior calls when
// asked to continue after a tool result), and a tool result being fed
// back (Type "function_call_output", CallID+Output set). Fields irrelevant
// to a given Type are simply left zero-valued and omitted from the JSON.
type responsesInputItem struct {
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`
	Content   string `json:"content,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
}

// toResponsesInput converts fastllm's flat Message list into the
// Responses API's input-item array, translating the two message shapes
// runFileTools produces that don't map to a plain role+content turn: an
// assistant message carrying ToolCalls becomes one function_call item per
// call (dropping any accompanying Content — in practice a tool-calling
// turn has none, matching how Chat Completions models behave here too),
// and a role:"tool" message (see handler.go's runFileTools) becomes a
// function_call_output item keyed by the same call ID the model used.
func toResponsesInput(messages []Message) []responsesInputItem {
	items := make([]responsesInputItem, 0, len(messages))
	for _, m := range messages {
		switch {
		case m.Role == "tool":
			items = append(items, responsesInputItem{Type: "function_call_output", CallID: m.ToolCallID, Output: m.Content})
		case len(m.ToolCalls) > 0:
			for _, call := range m.ToolCalls {
				items = append(items, responsesInputItem{Type: "function_call", CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
			}
		default:
			items = append(items, responsesInputItem{Type: "message", Role: m.Role, Content: m.Content})
		}
	}
	return items
}

type responsesRequest struct {
	Model  string `json:"model"`
	Input  any    `json:"input"` // string (StreamChat) or []responsesInputItem (Chat, when tools are involved)
	Tools  []responsesTool `json:"tools,omitempty"`
	Stream bool            `json:"stream"`
}

type responsesErrorBody struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func responsesError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var eb responsesErrorBody
	if json.Unmarshal(body, &eb) == nil && len(eb.Errors) > 0 && eb.Errors[0].Message != "" {
		return fmt.Errorf("llm: chat completion failed: %s", eb.Errors[0].Message)
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		return fmt.Errorf("llm: chat completion failed: %s: %s", resp.Status, trimmed)
	}
	return fmt.Errorf("llm: chat completion failed: %s", resp.Status)
}

func (c *ResponsesClient) newRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	return req, nil
}

// responsesStreamEvent covers the three SSE event shapes StreamChat cares
// about: "response.output_text.delta" (Type carries the event name here
// since Cloudflare/OpenAI's Responses API puts it in the JSON payload
// itself, unlike Chat Completions' separate "event:" SSE line), "error",
// and "response.completed" (carries final token usage nested under
// Response — see StreamChat). Every other event type (response.created,
// etc.) is ignored — none of them carry incremental text or usage.
type responsesStreamEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
	Response struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	} `json:"response"`
}

// StreamChat implements the same signature as Client.StreamChat so it can
// sit behind Router — see that type's doc comment for the dispatch logic.
// onReasoning is accepted for interface symmetry with the other clients
// but never called: no confirmed reasoning-delta event exists for this
// schema on Cloudflare's hosted models yet (only a request-side
// "reasoning.effort" field, which this client doesn't send since fastllm
// has no per-request UI for it here). onUsage (if non-nil) is called once
// with the response.completed event's nested usage.
func (c *ResponsesClient) StreamChat(ctx context.Context, model string, messages []Message, thinkLevel string, onToken func(string), onReasoning func(string), onUsage func(Usage)) error {
	body, err := json.Marshal(responsesRequest{Model: model, Input: responsesInput(messages), Stream: true})
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, body)
	if err != nil {
		return err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return responsesError(resp)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var event responsesStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue // skip malformed/keepalive lines
		}
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				onToken(event.Delta)
			}
		case "response.completed":
			if onUsage != nil && event.Response.Usage.TotalTokens > 0 {
				onUsage(Usage{
					PromptTokens:     event.Response.Usage.InputTokens,
					CompletionTokens: event.Response.Usage.OutputTokens,
					TotalTokens:      event.Response.Usage.TotalTokens,
				})
			}
		case "error":
			if event.Error.Message != "" {
				return fmt.Errorf("llm: chat completion failed: %s", event.Error.Message)
			}
		}
	}
	return scanner.Err()
}

// responsesOutputItem covers the two "output" item types Chat cares
// about: a plain assistant message (Type "message", Content holding its
// own nested content-part array — see extractText below) and a
// function_call the model wants to invoke. Every other item type
// Cloudflare/OpenAI's Responses API can emit (reasoning, web_search_call,
// etc.) is left unparsed and ignored, same as chatStreamChunk/
// anthropicStreamEvent only decode the fields their own callers use.
type responsesOutputItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type responsesResponse struct {
	OutputText string                 `json:"output_text"`
	Output     []responsesOutputItem  `json:"output"`
}

// extractText concatenates every text content part of a "message"-type
// output item — mirrors how Anthropic's Content []struct{Type,Text} is
// walked in anthropic.go's Chat, for the same reason: a message can carry
// more than one content part, and only the text ones matter here.
func extractText(items []responsesOutputItem) string {
	var b strings.Builder
	for _, item := range items {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" || part.Type == "text" {
				b.WriteString(part.Text)
			}
		}
	}
	return b.String()
}

// toToolCalls extracts every function_call output item as a fastllm
// ToolCall, in the same shape Client.Chat's chatResponse parsing produces
// — so runFileTools (handler.go) can treat a Responses-API tool call
// identically to a Chat-Completions one without knowing which schema
// produced it.
func toToolCalls(items []responsesOutputItem) []ToolCall {
	var calls []ToolCall
	for _, item := range items {
		if item.Type != "function_call" {
			continue
		}
		call := ToolCall{ID: item.CallID, Type: "function"}
		call.Function.Name = item.Name
		call.Function.Arguments = item.Arguments
		calls = append(calls, call)
	}
	return calls
}

// Chat sends a single non-streaming request, declaring tools (translated
// via toResponsesTools) when the caller offers any, and returns the
// model's reply — which may carry ToolCalls instead of (or in addition
// to) Content if the model wants to invoke one, same contract as
// Client.Chat. Only reached for a model NeedsResponsesAPI identifies (see
// Router.Chat) and that SupportsToolsForModel has separately allowlisted
// for tools — not every Responses-API model is assumed tool-capable just
// because this method accepts a tools argument.
func (c *ResponsesClient) Chat(ctx context.Context, model string, messages []Message, tools []Tool, thinkLevel string) (Message, error) {
	var input any = responsesInput(messages)
	if len(tools) > 0 {
		input = toResponsesInput(messages)
	}
	body, err := json.Marshal(responsesRequest{Model: model, Input: input, Tools: toResponsesTools(tools), Stream: false})
	if err != nil {
		return Message{}, err
	}
	req, err := c.newRequest(ctx, body)
	if err != nil {
		return Message{}, err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Message{}, responsesError(resp)
	}

	var rr responsesResponse
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return Message{}, err
	}

	if calls := toToolCalls(rr.Output); len(calls) > 0 {
		return Message{Role: "assistant", ToolCalls: calls}, nil
	}
	text := rr.OutputText
	if text == "" {
		text = extractText(rr.Output)
	}
	return Message{Role: "assistant", Content: text}, nil
}
