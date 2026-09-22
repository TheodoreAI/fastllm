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

// AnthropicModels lists the Claude models offered in the model picker when
// an Anthropic API key is configured. Anthropic has no public "list
// models" endpoint usable the way Ollama's /api/tags is, so — same as
// OpenAI/Gemini below — this is a hand-maintained list of current model
// IDs rather than a live query.
var AnthropicModels = []string{
	"claude-opus-5",
	"claude-sonnet-5",
	"claude-fable-5",
	"claude-haiku-4-5-20251001",
}

// cloudflareAnthropicModels lists which bare CloudflareModels entries are
// Claude models served through Workers AI's unified catalog — these speak
// Anthropic's native Messages wire format (POST {baseURL}/messages), not
// the OpenAI-compatible Chat Completions format the rest of
// CloudflareModels uses, so Router routes them to cloudflareAnthropic
// instead of the plain cloudflare *Client. Confirmed via Cloudflare's own
// docs (developers.cloudflare.com/ai/models/anthropic/claude-haiku-4.5/,
// 2026-08-19): endpoint is /accounts/{id}/ai/v1/messages, auth is a plain
// Cloudflare Bearer token (no separate Anthropic key needed), and the
// model advertises tool support.
var cloudflareAnthropicModels = map[string]bool{
	"anthropic/claude-haiku-4.5": true,
}

// NeedsAnthropicAPI reports whether a bare Cloudflare model name must go
// through the Anthropic Messages wire format rather than Chat Completions.
func NeedsAnthropicAPI(bareModel string) bool {
	return cloudflareAnthropicModels[bareModel]
}

// AnthropicClient talks to Anthropic's native Messages API, which is not
// OpenAI-wire-compatible: different request/response shape, a top-level
// "system" field instead of a system-role message, its own SSE event
// framing, and (for Anthropic's own API) an x-api-key/anthropic-version
// header pair instead of Bearer auth. The same wire format is also how
// Cloudflare Workers AI's unified model catalog exposes its hosted copy of
// Claude models (POST {baseURL}/ai/v1/messages with a Cloudflare Bearer
// token instead of an Anthropic key) — CloudflareBearer/BaseURL below
// exist so one implementation serves both, the same pattern ResponsesClient
// uses for OpenAI's own API vs. Cloudflare's hosted copy of gpt-5.6-luna.
type AnthropicClient struct {
	// BaseURL is the full Messages endpoint URL. Defaults to Anthropic's
	// own API when empty (see messagesURL) — only set for a
	// Cloudflare-routed instance.
	BaseURL string
	APIKey  string
	// CloudflareBearer, when true, sends APIKey as a plain
	// "Authorization: Bearer" header (Cloudflare's own auth scheme)
	// instead of Anthropic's x-api-key/anthropic-version header pair.
	CloudflareBearer bool
	HTTPClient       *http.Client
}

func NewAnthropicClient(apiKey string) *AnthropicClient {
	return &AnthropicClient{APIKey: apiKey, HTTPClient: &http.Client{Timeout: 5 * time.Minute}}
}

// NewCloudflareAnthropicClient builds an AnthropicClient pointed at
// Cloudflare Workers AI's unified catalog endpoint for its hosted copy of
// Claude models (e.g. "anthropic/claude-haiku-4.5"), authenticated with
// the Cloudflare API token rather than an Anthropic key.
func NewCloudflareAnthropicClient(baseURL, cloudflareToken string) *AnthropicClient {
	return &AnthropicClient{
		BaseURL:          baseURL + "/messages",
		APIKey:           cloudflareToken,
		CloudflareBearer: true,
		HTTPClient:       &http.Client{Timeout: 5 * time.Minute},
	}
}

func (c *AnthropicClient) messagesURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api.anthropic.com/v1/messages"
}

const anthropicAPIVersion = "2023-06-01"

// anthropicMessage's Content is `any` rather than a fixed type since
// Anthropic accepts either a plain string or an array of content blocks —
// toAnthropicRequest below sets it to a string for a text-only message
// and to []anthropicContentBlock once an image is involved, exactly
// mirroring the OpenAI-compatible wire format's own string-or-array
// content field (see Message.MarshalJSON in client.go).
type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicContentBlock struct {
	Type   string                `json:"type"`
	Text   string                `json:"text,omitempty"`
	Source *anthropicImageSource `json:"source,omitempty"`
	// tool_use fields (assistant -> API, describing a call the model wants
	// to make) and tool_result fields (API -> assistant on the next turn,
	// carrying that call's output) — see anthropicBlocksFor/toToolCalls for
	// how these round-trip through fastllm's provider-agnostic ToolCall.
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// collapseIfPlainText returns blocks[0].Text directly (Anthropic accepts
// a bare string as shorthand for a single text block) when blocks is
// exactly one text-only block, and blocks itself otherwise — the whole
// point being that a text-only conversation still marshals to the exact
// same "content": "..." shape it always has, only gaining the
// "content": [...] array shape once an image is actually involved.
func collapseIfPlainText(blocks []anthropicContentBlock) any {
	if len(blocks) == 0 {
		return ""
	}
	if len(blocks) == 1 && blocks[0].Type == "text" {
		return blocks[0].Text
	}
	return blocks
}

// anthropicBlocksFor converts one fastllm Message into Anthropic content
// blocks. A tool-result message (Role "tool", identified by a non-empty
// ToolCallID) becomes a single tool_result block instead of text/image
// blocks — Anthropic expects that role as "user" carrying tool_result
// content, not its own role. Otherwise: a leading text block (if Content
// is non-empty), one tool_use block per ToolCalls entry (an assistant
// message requesting calls), then one image block per attached Image,
// skipping any image whose data URI doesn't parse (malformed input should
// never abort the whole request).
func anthropicBlocksFor(m Message) []anthropicContentBlock {
	if m.ToolCallID != "" {
		return []anthropicContentBlock{{
			Type:      "tool_result",
			ToolUseID: m.ToolCallID,
			Content:   m.Content,
		}}
	}
	blocks := make([]anthropicContentBlock, 0, len(m.Images)+len(m.ToolCalls)+1)
	if m.Content != "" {
		blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
	}
	for _, call := range m.ToolCalls {
		input := json.RawMessage(call.Function.Arguments)
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		blocks = append(blocks, anthropicContentBlock{
			Type:  "tool_use",
			ID:    call.ID,
			Name:  call.Function.Name,
			Input: input,
		})
	}
	for _, img := range m.Images {
		mediaType, data, ok := splitDataURI(img.DataURI)
		if !ok {
			continue
		}
		blocks = append(blocks, anthropicContentBlock{
			Type:   "image",
			Source: &anthropicImageSource{Type: "base64", MediaType: mediaType, Data: data},
		})
	}
	return blocks
}

// anthropicRoleFor returns the Anthropic role a fastllm message maps to —
// every non-assistant role (including fastllm's "tool" role, carrying a
// tool_result block) is "user", since Anthropic has only user/assistant.
func anthropicRoleFor(m Message) string {
	if m.Role == "assistant" {
		return "assistant"
	}
	return "user"
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream"`
	Thinking  *anthropicThinking `json:"thinking,omitempty"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

// anthropicTool is fastllm's provider-agnostic Tool translated into
// Anthropic's tool schema, which flattens the OpenAI-style
// {"type":"function","function":{name,description,parameters}} wrapper
// into name/description/input_schema directly on the tool object.
type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

func toAnthropicTools(tools []Tool) []anthropicTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]anthropicTool, len(tools))
	for i, t := range tools {
		out[i] = anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		}
	}
	return out
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// anthropicMaxTokens is a generous fixed output cap. Anthropic (unlike
// the OpenAI-compatible path) requires max_tokens on every request; there
// is no per-request UI for it yet, so this just needs to be large enough
// to not truncate a normal answer.
const anthropicMaxTokens = 8192

// toAnthropicRequest splits fastllm's flat message list into Anthropic's
// shape: the (at most one, expected-first) system message becomes the
// top-level System field, and consecutive same-role messages are merged
// (block list concatenated, since Anthropic requires strict
// user/assistant alternation). Every message is built as a block list
// internally regardless of whether it carries images, purely so merging
// is one append instead of two different cases — collapseIfPlainText
// below then converts any message that ended up as exactly one text
// block back into a bare string, so a request with no images at all
// produces byte-identical JSON to before image support existed.
func toAnthropicRequest(model string, messages []Message, tools []Tool, stream bool, thinkLevel string) anthropicRequest {
	var system strings.Builder
	blockLists := make([][]anthropicContentBlock, 0, len(messages))
	converted := make([]anthropicMessage, 0, len(messages))
	for _, m := range messages {
		if m.Role == "system" {
			if system.Len() > 0 {
				system.WriteString("\n\n")
			}
			system.WriteString(m.Content)
			continue
		}
		role := anthropicRoleFor(m)
		blocks := anthropicBlocksFor(m)
		if n := len(converted); n > 0 && converted[n-1].Role == role {
			blockLists[n-1] = append(blockLists[n-1], blocks...)
			continue
		}
		converted = append(converted, anthropicMessage{Role: role})
		blockLists = append(blockLists, blocks)
	}
	for i, blocks := range blockLists {
		converted[i].Content = collapseIfPlainText(blocks)
	}

	req := anthropicRequest{
		Model:     model,
		System:    system.String(),
		Messages:  converted,
		MaxTokens: anthropicMaxTokens,
		Stream:    stream,
		Tools:     toAnthropicTools(tools),
	}
	if thinkLevel != "" {
		// Anthropic's extended-thinking budget is a token count, not a
		// named level — these map fastllm's low/medium/high picker onto
		// reasonable budgets under the fixed MaxTokens above (the budget
		// must be less than max_tokens).
		budget := map[string]int{"low": 1024, "medium": 3072, "high": 6144}[thinkLevel]
		if budget > 0 {
			req.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: budget}
		}
	}
	return req
}

func (c *AnthropicClient) newRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.messagesURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.CloudflareBearer {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	} else {
		req.Header.Set("x-api-key", c.APIKey)
		req.Header.Set("anthropic-version", anthropicAPIVersion)
	}
	return req, nil
}

type anthropicErrorBody struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func anthropicError(resp *http.Response) error {
	var eb anthropicErrorBody
	if json.NewDecoder(resp.Body).Decode(&eb) == nil && eb.Error.Message != "" {
		return fmt.Errorf("anthropic: %s", eb.Error.Message)
	}
	return fmt.Errorf("anthropic: chat completion failed: %s", resp.Status)
}

// anthropicStreamEvent covers the event types StreamChat cares about out
// of Anthropic's several SSE event kinds (message_start,
// content_block_start/delta/stop, message_delta, message_stop, ping) —
// unrecognized fields are simply left zero-valued and skipped. Usage
// arrives split across two event types rather than once at the end:
// message_start.message.usage.input_tokens is the prompt size (output_tokens
// there is always 0 — nothing generated yet), and each message_delta
// carries a cumulative usage.output_tokens as generation progresses, so
// the last message_delta seen has the final output count.
type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
	Message struct {
		Usage struct {
			InputTokens int `json:"input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// StreamChat implements the same signature as Client.StreamChat so both
// can sit behind Router — see that type's doc comment for the dispatch
// logic. Reasoning text streams via Anthropic's "thinking" delta type,
// analogous to Ollama's separate "reasoning" field. onUsage (if non-nil)
// is called once at the end of the stream with the input token count from
// message_start and the final cumulative output token count from the last
// message_delta seen (see anthropicStreamEvent's doc comment).
func (c *AnthropicClient) StreamChat(ctx context.Context, model string, messages []Message, thinkLevel string, onToken func(string), onReasoning func(string), onUsage func(Usage)) error {
	body, err := json.Marshal(toAnthropicRequest(model, messages, nil, true, thinkLevel))
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
		return anthropicError(resp)
	}

	rateLimit := parseAnthropicRateLimitHeaders(resp.Header)

	var inputTokens, outputTokens int
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue // skip malformed/keepalive lines
		}
		switch event.Type {
		case "message_start":
			inputTokens = event.Message.Usage.InputTokens
		case "message_delta":
			outputTokens = event.Usage.OutputTokens
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				if event.Delta.Text != "" {
					onToken(event.Delta.Text)
				}
			case "thinking_delta":
				if event.Delta.Thinking != "" && onReasoning != nil {
					onReasoning(event.Delta.Thinking)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if onUsage != nil && (inputTokens > 0 || outputTokens > 0) {
		onUsage(Usage{PromptTokens: inputTokens, CompletionTokens: outputTokens, TotalTokens: inputTokens + outputTokens, RateLimit: rateLimit})
	}
	return nil
}

type anthropicResponse struct {
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Chat sends a single non-streaming completion request with the given
// tools declared, and returns the model's reply — which may contain
// ToolCalls (translated from Anthropic's tool_use content blocks) instead
// of (or alongside) Content if the model wants to invoke a tool. Anthropic
// tool_use IDs already match fastllm's provider-agnostic ToolCall.ID/
// Message.ToolCallID shape directly (unlike Gemini, which has no native
// call ID and packs metadata into one — see toGeminiToolCallID's doc
// comment), so no ID-packing scheme is needed here: a tool_result message
// simply carries the same ID straight back.
func (c *AnthropicClient) Chat(ctx context.Context, model string, messages []Message, tools []Tool, thinkLevel string) (Message, error) {
	result, err := c.ChatWithUsage(ctx, model, messages, tools, thinkLevel)
	return result.Message, err
}

func (c *AnthropicClient) ChatWithUsage(ctx context.Context, model string, messages []Message, tools []Tool, thinkLevel string) (ChatResult, error) {
	body, err := json.Marshal(toAnthropicRequest(model, messages, tools, false, thinkLevel))
	if err != nil {
		return ChatResult{}, err
	}
	req, err := c.newRequest(ctx, body)
	if err != nil {
		return ChatResult{}, err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return ChatResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ChatResult{}, anthropicError(resp)
	}

	var ar anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return ChatResult{}, err
	}
	var text strings.Builder
	var calls []ToolCall
	for _, block := range ar.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			call := ToolCall{ID: block.ID, Type: "function"}
			call.Function.Name = block.Name
			call.Function.Arguments = string(block.Input)
			calls = append(calls, call)
		}
	}
	result := ChatResult{Message: Message{Role: "assistant", Content: text.String(), ToolCalls: calls}}
	if ar.Usage.InputTokens > 0 || ar.Usage.OutputTokens > 0 {
		result.Usage = Usage{
			PromptTokens:     ar.Usage.InputTokens,
			CompletionTokens: ar.Usage.OutputTokens,
			TotalTokens:      ar.Usage.InputTokens + ar.Usage.OutputTokens,
		}
		result.HasUsage = true
	}
	return result, nil
}
