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
	Role    string `json:"role"`
	Content string `json:"content"`
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
}

// Model describes one chat-capable model available on the backend.
type Model struct {
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities,omitempty"`
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
		out = append(out, Model{Name: m.Name, Capabilities: m.Capabilities})
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
func (c *Client) StreamChat(ctx context.Context, model string, messages []Message, onToken func(string), onReasoning func(string)) error {
	if model == "" {
		model = c.ChatModel
	}
	body, err := json.Marshal(chatRequest{Model: model, Messages: messages, Stream: true})
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
