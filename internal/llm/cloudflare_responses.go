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

// responsesOnlyModels lists Cloudflare Workers AI models confirmed to
// reject the standard OpenAI Chat Completions body ({"messages": [...]})
// and only accept the newer Responses API body ({"input": ...}) instead —
// see CloudflareModels' doc comment for how gpt-5.6-luna was found to need
// this. Router checks this set (via NeedsResponsesAPI) to decide whether a
// "cloudflare:"-prefixed model should go through CloudflareResponsesClient
// instead of the plain Chat-Completions Client every other Cloudflare
// model uses.
var responsesOnlyModels = map[string]bool{
	"openai/gpt-5.6-luna": true,
}

// NeedsResponsesAPI reports whether a bare (prefix-stripped) Cloudflare
// model name only works through the Responses API rather than Chat
// Completions.
func NeedsResponsesAPI(bareModel string) bool {
	return responsesOnlyModels[bareModel]
}

// CloudflareResponsesClient talks to Cloudflare Workers AI's Responses API
// (/ai/v1/responses), which — despite living right next to the
// OpenAI-compatible Chat Completions endpoint the plain Client above talks
// to for most Workers AI models — uses a different, non-chat-completions
// wire format for a growing number of newer models (confirmed so far:
// gpt-5.6-luna): a flat "input" string instead of a "messages" array, and
// "output_text"/an "output" item array instead of "choices[].message" in
// the response. See responsesOnlyModels above for which models need this.
type CloudflareResponsesClient struct {
	BaseURL    string // e.g. https://api.cloudflare.com/client/v4/accounts/{id}/ai/v1
	APIKey     string
	HTTPClient *http.Client
}

func NewCloudflareResponsesClient(baseURL, apiKey string) *CloudflareResponsesClient {
	return &CloudflareResponsesClient{
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

type responsesRequest struct {
	Model  string `json:"model"`
	Input  string `json:"input"`
	Stream bool   `json:"stream"`
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

func (c *CloudflareResponsesClient) newRequest(ctx context.Context, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	return req, nil
}

// responsesStreamEvent covers the two SSE event shapes StreamChat cares
// about: "response.output_text.delta" (Type carries the event name here
// since Cloudflare/OpenAI's Responses API puts it in the JSON payload
// itself, unlike Chat Completions' separate "event:" SSE line) and
// "error". Every other event type (response.created, response.completed,
// etc.) is ignored — none of them carry incremental text.
type responsesStreamEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// StreamChat implements the same signature as Client.StreamChat so it can
// sit behind Router — see that type's doc comment for the dispatch logic.
// onReasoning is accepted for interface symmetry with the other clients
// but never called: no confirmed reasoning-delta event exists for this
// schema on Cloudflare's hosted models yet (only a request-side
// "reasoning.effort" field, which this client doesn't send since fastllm
// has no per-request UI for it here).
func (c *CloudflareResponsesClient) StreamChat(ctx context.Context, model string, messages []Message, thinkLevel string, onToken func(string), onReasoning func(string)) error {
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
		case "error":
			if event.Error.Message != "" {
				return fmt.Errorf("llm: chat completion failed: %s", event.Error.Message)
			}
		}
	}
	return scanner.Err()
}

type responsesResponse struct {
	OutputText string `json:"output_text"`
}

// Chat sends a single non-streaming request and returns the model's reply
// as plain text. Tool calling isn't implemented for this schema (mirrors
// AnthropicClient.Chat — see SupportsToolsForModel, which already excludes
// every "cloudflare:" model from the file-tool loop regardless of which
// underlying wire format it uses).
func (c *CloudflareResponsesClient) Chat(ctx context.Context, model string, messages []Message, thinkLevel string) (Message, error) {
	body, err := json.Marshal(responsesRequest{Model: model, Input: responsesInput(messages), Stream: false})
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
	return Message{Role: "assistant", Content: rr.OutputText}, nil
}
