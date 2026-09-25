package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func probeServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL
}

func TestDetectAnthropicReadsMaxInputTokens(t *testing.T) {
	base := probeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models/claude-opus-5" || r.Header.Get("x-api-key") != "sk-test" || r.Header.Get("anthropic-version") == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"id":"claude-opus-5","max_input_tokens":1000000,"max_tokens":128000}`))
	})
	tokens, source, err := DetectContextWindow(context.Background(), ContextProbe{Provider: "anthropic", BaseURL: base + "/v1/messages", APIKey: "sk-test", Model: "claude-opus-5"})
	if err != nil || tokens != 1000000 || !strings.Contains(source, "max_input_tokens") {
		t.Fatalf("got %d %q %v", tokens, source, err)
	}
}

func TestDetectGeminiReadsInputTokenLimitWithTheKeyInAHeader(t *testing.T) {
	base := probeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models/gemini-3.7-flash" || r.Header.Get("x-goog-api-key") != "g-test" || r.URL.RawQuery != "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"name":"models/gemini-3.7-flash","inputTokenLimit":1048576,"outputTokenLimit":65536}`))
	})
	tokens, _, err := DetectContextWindow(context.Background(), ContextProbe{Provider: "gemini", BaseURL: base, APIKey: "g-test", Model: "gemini-3.7-flash"})
	if err != nil || tokens != 1048576 {
		t.Fatalf("got %d %v", tokens, err)
	}
}

func TestDetectOllamaReadsNumCtxAndRefusesToGuessWithoutIt(t *testing.T) {
	base := probeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/show" {
			http.NotFound(w, r)
			return
		}
		var body strings.Builder
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		body.Write(buf[:n])
		if strings.Contains(body.String(), "pinned") {
			w.Write([]byte(`{"parameters":"temperature                    0.6\nnum_ctx                        16384","model_info":{"qwen35.context_length":262144}}`))
			return
		}
		w.Write([]byte(`{"parameters":"temperature 1","model_info":{"qwen35.context_length":262144}}`))
	})
	tokens, source, err := DetectContextWindow(context.Background(), ContextProbe{BaseURL: base + "/v1", Model: "pinned:9b"})
	if err != nil || tokens != 16384 || source != "Ollama num_ctx" {
		t.Fatalf("pinned: got %d %q %v", tokens, source, err)
	}
	// The model's 262144 maximum is not what the server serves; say so.
	if tokens, _, err := DetectContextWindow(context.Background(), ContextProbe{BaseURL: base + "/v1", Model: "stock:9b"}); err == nil || tokens != 0 || !strings.Contains(err.Error(), "num_ctx") {
		t.Fatalf("unpinned: got %d %v, want an error explaining num_ctx", tokens, err)
	}
}

func TestDetectVLLMReadsMaxModelLen(t *testing.T) {
	base := probeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r) // not Ollama: /api/show is absent
			return
		}
		w.Write([]byte(`{"data":[{"id":"other"},{"id":"zai-org/GLM-5.3-Flash","max_model_len":131072}]}`))
	})
	tokens, source, err := DetectContextWindow(context.Background(), ContextProbe{BaseURL: base + "/v1", Model: "zai-org/GLM-5.3-Flash"})
	if err != nil || tokens != 131072 || !strings.Contains(source, "max_model_len") {
		t.Fatalf("got %d %q %v", tokens, source, err)
	}
}

func TestDetectReportsWhenNoWindowIsAvailable(t *testing.T) {
	if _, _, err := DetectContextWindow(context.Background(), ContextProbe{Provider: "openai", Model: "gpt-5"}); !errors.Is(err, ErrContextNotReported) {
		t.Fatalf("openai: err = %v, want ErrContextNotReported", err)
	}
	base := probeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"plain-model"}]}`))
	})
	if _, _, err := DetectContextWindow(context.Background(), ContextProbe{BaseURL: base + "/v1", Model: "plain-model"}); !errors.Is(err, ErrContextNotReported) {
		t.Fatalf("plain server: err = %v, want ErrContextNotReported", err)
	}
	if _, _, err := DetectContextWindow(context.Background(), ContextProbe{BaseURL: base + "/v1", Model: "missing"}); err == nil {
		t.Fatal("a model the server does not list was given a window")
	}
	unauthorized := probeServer(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusUnauthorized) })
	if _, _, err := DetectContextWindow(context.Background(), ContextProbe{Provider: "anthropic", BaseURL: unauthorized, Model: "claude-opus-5"}); err == nil {
		t.Fatal("a rejected key produced a window")
	}
}
