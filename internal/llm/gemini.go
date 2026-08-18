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

// GeminiModels lists the Gemini models offered in the model picker when a
// Gemini API key is configured — see AnthropicModels's doc comment for
// why this is a fixed list rather than a live query.
var GeminiModels = []string{
	"gemini-3-pro",
	"gemini-3-flash",
	"gemini-2.5-pro",
	"gemini-2.5-flash",
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

type geminiPart struct {
	Text string `json:"text"`
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

type geminiRequest struct {
	Contents          []geminiContent          `json:"contents"`
	SystemInstruction *geminiSystemInstruction `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig  `json:"generationConfig,omitempty"`
}

// toGeminiRequest converts fastllm's flat message list into Gemini's
// contents/systemInstruction shape. Gemini uses "model" (not
// "assistant") as the assistant role name, and — like Anthropic — expects
// a single system instruction rather than a system-role message
// interleaved into the turn sequence.
func toGeminiRequest(messages []Message, thinkLevel string) geminiRequest {
	var systemParts []geminiPart
	contents := make([]geminiContent, 0, len(messages))
	for _, m := range messages {
		if m.Role == "system" {
			systemParts = append(systemParts, geminiPart{Text: m.Content})
			continue
		}
		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		contents = append(contents, geminiContent{Role: role, Parts: []geminiPart{{Text: m.Content}}})
	}

	req := geminiRequest{Contents: contents}
	if len(systemParts) > 0 {
		req.SystemInstruction = &geminiSystemInstruction{Parts: systemParts}
	}
	if thinkLevel != "" {
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
	body, err := json.Marshal(toGeminiRequest(messages, thinkLevel))
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
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

// Chat sends a single non-streaming completion request and returns the
// model's reply as plain text — see AnthropicClient.Chat's doc comment
// for why this never needs to carry tool schemas.
func (c *GeminiClient) Chat(ctx context.Context, model string, messages []Message, thinkLevel string) (Message, error) {
	body, err := json.Marshal(toGeminiRequest(messages, thinkLevel))
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
	if len(gr.Candidates) > 0 {
		for _, part := range gr.Candidates[0].Content.Parts {
			if !part.Thought {
				text.WriteString(part.Text)
			}
		}
	}
	return Message{Role: "assistant", Content: text.String()}, nil
}
