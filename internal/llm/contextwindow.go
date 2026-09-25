package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrContextNotReported means the provider answered but does not report a
// context window for the model (OpenAI's API, or a server whose model list
// carries no length).
var ErrContextNotReported = errors.New("the provider does not report a context window for this model")

// ContextProbe identifies a model to ask its provider about.
type ContextProbe struct {
	// Provider is "anthropic", "gemini", or "openai" for those APIs; anything
	// else is an OpenAI-compatible server (Ollama, vLLM, llama.cpp, ...).
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	// HTTP overrides the client, for tests.
	HTTP *http.Client
}

// DetectContextWindow asks the model's provider how many input tokens a request
// may carry, with one request. It reports where the figure came from. It never
// guesses: a provider or server that does not say returns an error, so the
// caller can keep what it had.
func DetectContextWindow(ctx context.Context, probe ContextProbe) (int, string, error) {
	client := probe.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	model := strings.TrimSpace(probe.Model)
	if model == "" {
		return 0, "", errors.New("no model given")
	}
	switch strings.ToLower(strings.TrimSpace(probe.Provider)) {
	case "anthropic":
		return detectAnthropic(ctx, client, probe.BaseURL, probe.APIKey, model)
	case "gemini":
		return detectGemini(ctx, client, probe.BaseURL, probe.APIKey, model)
	case "openai":
		return 0, "", ErrContextNotReported
	}
	base := strings.TrimRight(strings.TrimSpace(probe.BaseURL), "/")
	if base == "" {
		return 0, "", errors.New("the endpoint has no URL")
	}
	if tokens, source, err, handled := detectOllama(ctx, client, base, model); handled {
		return tokens, source, err
	}
	return detectOpenAICompatible(ctx, client, base, probe.APIKey, model)
}

func detectAnthropic(ctx context.Context, client *http.Client, baseURL, key, model string) (int, string, error) {
	base := "https://api.anthropic.com/v1"
	if custom := strings.TrimRight(strings.TrimSpace(baseURL), "/"); custom != "" {
		base = strings.TrimSuffix(custom, "/messages")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models/"+url.PathEscape(model), nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", anthropicAPIVersion)
	var body struct {
		MaxInputTokens int `json:"max_input_tokens"`
	}
	if err := getJSON(client, req, &body); err != nil {
		return 0, "", err
	}
	if body.MaxInputTokens <= 0 {
		return 0, "", ErrContextNotReported
	}
	return body.MaxInputTokens, "Anthropic Models API (max_input_tokens)", nil
}

func detectGemini(ctx context.Context, client *http.Client, baseURL, key, model string) (int, string, error) {
	base := "https://generativelanguage.googleapis.com/v1beta"
	if custom := strings.TrimRight(strings.TrimSpace(baseURL), "/"); custom != "" {
		base = custom
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models/"+url.PathEscape(strings.TrimPrefix(model, "models/")), nil)
	if err != nil {
		return 0, "", err
	}
	// The key goes in a header, not the URL, so it cannot land in an error.
	req.Header.Set("x-goog-api-key", key)
	var body struct {
		InputTokenLimit int `json:"inputTokenLimit"`
	}
	if err := getJSON(client, req, &body); err != nil {
		return 0, "", err
	}
	if body.InputTokenLimit <= 0 {
		return 0, "", ErrContextNotReported
	}
	return body.InputTokenLimit, "Gemini API (inputTokenLimit)", nil
}

// numCtxParameter finds num_ctx in the parameters text /api/show returns.
var numCtxParameter = regexp.MustCompile(`(?m)^\s*num_ctx\s+(\d+)`)

// detectOllama asks an Ollama server for the context it serves the model at.
// handled is false when base is not an Ollama server.
func detectOllama(ctx context.Context, client *http.Client, base, model string) (int, string, error, bool) {
	root := strings.TrimSuffix(base, "/v1")
	payload, _ := json.Marshal(map[string]string{"model": model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, root+"/api/show", bytes.NewReader(payload))
	if err != nil {
		return 0, "", err, false
	}
	req.Header.Set("Content-Type", "application/json")
	var body struct {
		Parameters string         `json:"parameters"`
		ModelInfo  map[string]any `json:"model_info"`
	}
	if err := getJSON(client, req, &body); err != nil {
		return 0, "", nil, false
	}
	if match := numCtxParameter.FindStringSubmatch(body.Parameters); match != nil {
		tokens, _ := strconv.Atoi(match[1])
		return tokens, "Ollama num_ctx", nil, true
	}
	// Without num_ctx Ollama serves its own default (4096 unless
	// OLLAMA_CONTEXT_LENGTH is set), not the model's maximum; reporting the
	// maximum here would size the budget for a window the server lacks.
	return 0, "", fmt.Errorf("Ollama serves %s at its default context, not a fixed one: create an alias with PARAMETER num_ctx and use that", model), true
}

// detectOpenAICompatible reads the model's length from GET {base}/models, where
// vLLM reports max_model_len and other servers context_length.
func detectOpenAICompatible(ctx context.Context, client *http.Client, base, key, model string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return 0, "", err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	var body struct {
		Data []struct {
			ID            string `json:"id"`
			MaxModelLen   int    `json:"max_model_len"`
			ContextLength int    `json:"context_length"`
			ContextWindow int    `json:"context_window"`
		} `json:"data"`
	}
	if err := getJSON(client, req, &body); err != nil {
		return 0, "", err
	}
	for _, entry := range body.Data {
		if !strings.EqualFold(entry.ID, model) {
			continue
		}
		switch {
		case entry.MaxModelLen > 0:
			return entry.MaxModelLen, "server model list (max_model_len)", nil
		case entry.ContextLength > 0:
			return entry.ContextLength, "server model list (context_length)", nil
		case entry.ContextWindow > 0:
			return entry.ContextWindow, "server model list (context_window)", nil
		}
		return 0, "", ErrContextNotReported
	}
	return 0, "", fmt.Errorf("the server does not list a model named %s", model)
}

func getJSON(client *http.Client, req *http.Request, target any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: %s", req.Method, req.URL.Path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}
