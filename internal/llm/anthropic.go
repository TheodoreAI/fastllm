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

// AnthropicClient talks to Anthropic's native Messages API
// (api.anthropic.com/v1/messages), which is not OpenAI-wire-compatible:
// different request/response shape, a top-level "system" field instead of
// a system-role message, its own SSE event framing, and an
// x-api-key/anthropic-version header pair instead of Bearer auth.
type AnthropicClient struct {
	APIKey     string
	HTTPClient *http.Client
}

func NewAnthropicClient(apiKey string) *AnthropicClient {
	return &AnthropicClient{APIKey: apiKey, HTTPClient: &http.Client{Timeout: 5 * time.Minute}}
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
// blocks — a leading text block (if Content is non-empty) followed by one
// image block per attached Image, skipping any image whose data URI
// doesn't parse (malformed input should never abort the whole request).
func anthropicBlocksFor(m Message) []anthropicContentBlock {
	blocks := make([]anthropicContentBlock, 0, len(m.Images)+1)
	if m.Content != "" {
		blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
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

type anthropicRequest struct {
	Model     string             `json:"model"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream"`
	Thinking  *anthropicThinking `json:"thinking,omitempty"`
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
func toAnthropicRequest(model string, messages []Message, stream bool, thinkLevel string) anthropicRequest {
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
		blocks := anthropicBlocksFor(m)
		if n := len(converted); n > 0 && converted[n-1].Role == m.Role {
			blockLists[n-1] = append(blockLists[n-1], blocks...)
			continue
		}
		converted = append(converted, anthropicMessage{Role: m.Role})
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)
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

// anthropicStreamEvent covers the two event types StreamChat cares about
// out of Anthropic's several SSE event kinds (message_start,
// content_block_start/delta/stop, message_delta, message_stop, ping) —
// unrecognized fields are simply left zero-valued and skipped.
type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
}

// StreamChat implements the same signature as Client.StreamChat so both
// can sit behind Router — see that type's doc comment for the dispatch
// logic. Reasoning text streams via Anthropic's "thinking" delta type,
// analogous to Ollama's separate "reasoning" field.
func (c *AnthropicClient) StreamChat(ctx context.Context, model string, messages []Message, thinkLevel string, onToken func(string), onReasoning func(string)) error {
	body, err := json.Marshal(toAnthropicRequest(model, messages, true, thinkLevel))
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
		if event.Type != "content_block_delta" {
			continue
		}
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
	return scanner.Err()
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// Chat sends a single non-streaming completion request and returns the
// model's reply as plain text. Anthropic tool-calling isn't implemented
// yet (see SupportsToolsForModel), so unlike Client.Chat/GeminiClient.Chat
// this never needs to carry Tools or parse a tool call back out of the
// response — Router.Chat never reaches this method with tools to pass
// along, since SupportsToolsForModel already excludes "anthropic:"
// models from the file-tool loop before it ever calls in.
func (c *AnthropicClient) Chat(ctx context.Context, model string, messages []Message, thinkLevel string) (Message, error) {
	body, err := json.Marshal(toAnthropicRequest(model, messages, false, thinkLevel))
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
		return Message{}, anthropicError(resp)
	}

	var ar anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return Message{}, err
	}
	var text strings.Builder
	for _, block := range ar.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return Message{Role: "assistant", Content: text.String()}, nil
}
