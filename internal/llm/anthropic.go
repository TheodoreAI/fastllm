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

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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
// since Anthropic requires strict user/assistant alternation.
func toAnthropicRequest(model string, messages []Message, stream bool, thinkLevel string) anthropicRequest {
	var system strings.Builder
	converted := make([]anthropicMessage, 0, len(messages))
	for _, m := range messages {
		if m.Role == "system" {
			if system.Len() > 0 {
				system.WriteString("\n\n")
			}
			system.WriteString(m.Content)
			continue
		}
		if n := len(converted); n > 0 && converted[n-1].Role == m.Role {
			converted[n-1].Content += "\n\n" + m.Content
			continue
		}
		converted = append(converted, anthropicMessage{Role: m.Role, Content: m.Content})
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
