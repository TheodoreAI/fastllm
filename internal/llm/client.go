// Package llm provides a thin client for OpenAI-compatible chat/embedding
// APIs. It works unmodified against OpenAI, Ollama (localhost:11434/v1),
// LM Studio, and any other OpenAI-compatible server.
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

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Tool describes one function the model may call, in OpenAI's tool schema.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// ToolCall is one function-call request from the model, as returned in a
// non-streaming completion's message.tool_calls.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Client struct {
	BaseURL    string
	APIKey     string
	ChatModel  string
	EmbedModel string
	HTTPClient *http.Client
}

func New(baseURL, apiKey, chatModel, embedModel string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		APIKey:     apiKey,
		ChatModel:  chatModel,
		EmbedModel: embedModel,
		HTTPClient: &http.Client{Timeout: 5 * time.Minute},
	}
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
	Tools    []Tool    `json:"tools,omitempty"`
	Think    string    `json:"think,omitempty"`
}

// Model describes one chat-capable model available on the backend.
type Model struct {
	Name              string   `json:"name"`
	Capabilities      []string `json:"capabilities,omitempty"`
	SupportsFileTools bool     `json:"supports_file_tools"`
}

type tagsResponse struct {
	Models []struct {
		Name         string   `json:"name"`
		Capabilities []string `json:"capabilities"`
	} `json:"models"`
}

// ListModels queries Ollama's native /api/tags endpoint and returns every
// model that is not embedding-only. It relies on the Ollama-specific
// endpoint, so it only works when BaseURL points at an Ollama server
// (returns an error otherwise, which callers should treat as "unavailable").
func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	root := strings.TrimSuffix(c.BaseURL, "/v1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm: list models failed: %s", resp.Status)
	}

	var tr tagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, err
	}

	out := make([]Model, 0, len(tr.Models))
	for _, m := range tr.Models {
		if containsString(m.Capabilities, "embedding") && !containsString(m.Capabilities, "completion") {
			continue // embedding-only model, not usable for chat
		}
		out = append(out, Model{Name: m.Name, Capabilities: m.Capabilities, SupportsFileTools: SupportsTools(m.Name)})
	}
	return out, nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// toolCapableModelPrefixes lists the model families confirmed (by hand)
// to reliably populate Ollama's tool_calls field rather than writing tool
// invocations as plain-text JSON content. Ollama's own "tools" capability
// flag is not sufficient evidence: qwen2.5-coder:7b advertises "tools" in
// /api/tags but consistently ignores tool_calls in practice (see
// parseFallbackToolCall) — so file read/write access is only offered to
// models on this allowlist, matched by name prefix (e.g. "gemma4" matches
// "gemma4:12b").
var toolCapableModelPrefixes = []string{
	"gemma4",
	"gpt-oss",
}

// SupportsTools reports whether model is on the allowlist of models
// confirmed to reliably use tool_calls, so callers can decide whether to
// offer file read/write tools at all.
func SupportsTools(model string) bool {
	for _, prefix := range toolCapableModelPrefixes {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
		} `json:"delta"`
	} `json:"choices"`
}

// StreamChat sends messages to the chat completion endpoint and calls
// onToken for every incremental piece of answer text, and onReasoning (if
// non-nil) for every incremental piece of a thinking-capable model's
// reasoning trace, as they stream in. Ollama's OpenAI-compatible endpoint
// sends reasoning as a "reasoning" delta field alongside "content", ahead
// of and separate from the actual answer; models without thinking support
// simply never populate it. If model is empty, c.ChatModel is used.
func (c *Client) StreamChat(ctx context.Context, model string, messages []Message, thinkLevel string, onToken func(string), onReasoning func(string)) error {
	if model == "" {
		model = c.ChatModel
	}
	body, err := json.Marshal(chatRequest{Model: model, Messages: messages, Stream: true, Think: thinkLevel})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("llm: chat completion failed: %s", resp.Status)
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
		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // skip malformed/keepalive lines
		}
		if len(chunk.Choices) > 0 {
			if chunk.Choices[0].Delta.Content != "" {
				onToken(chunk.Choices[0].Delta.Content)
			}
			if chunk.Choices[0].Delta.Reasoning != "" && onReasoning != nil {
				onReasoning(chunk.Choices[0].Delta.Reasoning)
			}
		}
	}
	return scanner.Err()
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

// Chat sends a single non-streaming completion request with the given
// tools declared, and returns the model's reply message — which may
// contain ToolCalls instead of (or in addition to) Content if the model
// wants to invoke a tool. Used as a pre-flight step before StreamChat so
// tool calls (which arrive as accumulated JSON, awkward to stream) are
// resolved before the user-facing streamed answer begins. If model is
// empty, c.ChatModel is used.
func (c *Client) Chat(ctx context.Context, model string, messages []Message, tools []Tool, thinkLevel string) (Message, error) {
	if model == "" {
		model = c.ChatModel
	}
	body, err := json.Marshal(chatRequest{Model: model, Messages: messages, Stream: false, Tools: tools, Think: thinkLevel})
	if err != nil {
		return Message{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Message{}, fmt.Errorf("llm: chat completion failed: %s", resp.Status)
	}

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return Message{}, err
	}
	if len(cr.Choices) == 0 {
		return Message{}, fmt.Errorf("llm: empty response")
	}

	reply := cr.Choices[0].Message
	if len(reply.ToolCalls) == 0 && len(tools) > 0 {
		if call, ok := parseFallbackToolCall(reply.Content, tools); ok {
			reply.ToolCalls = []ToolCall{call}
			reply.Content = ""
		}
	}
	return reply, nil
}

// parseFallbackToolCall recovers a tool call from models that don't
// support Ollama's tool_calls field and instead write the call out as
// plain message content — e.g. qwen2.5-coder:7b reliably does this
// despite advertising the "tools" capability. Deliberately strict to
// avoid misfiring on a model legitimately discussing JSON in an answer:
// the ENTIRE trimmed content (nothing before or after, no prose, no code
// fences) must parse as exactly {"name": "...", "arguments": {...}},
// and name must match one of the tools actually offered in this request.
func parseFallbackToolCall(content string, tools []Tool) (ToolCall, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed[0] != '{' {
		return ToolCall{}, false
	}

	var parsed struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	if err := dec.Decode(&parsed); err != nil || parsed.Name == "" || len(parsed.Arguments) == 0 {
		return ToolCall{}, false
	}
	// Reject trailing content after the JSON object — a model just
	// mentioning a tool-call-shaped example mid-explanation would still
	// fail this because real answers practically never end with nothing
	// but a bare JSON object and no closing remarks.
	if dec.More() {
		return ToolCall{}, false
	}

	known := false
	for _, t := range tools {
		if t.Function.Name == parsed.Name {
			known = true
			break
		}
	}
	if !known {
		return ToolCall{}, false
	}

	return ToolCall{
		ID:   "fallback_" + parsed.Name,
		Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: parsed.Name, Arguments: string(parsed.Arguments)},
	}, true
}

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed returns the embedding vector for a single piece of text.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: c.EmbedModel, Input: text})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm: embedding request failed: %s", resp.Status)
	}

	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, err
	}
	if len(er.Data) == 0 {
		return nil, fmt.Errorf("llm: empty embedding response")
	}
	return er.Data[0].Embedding, nil
}
