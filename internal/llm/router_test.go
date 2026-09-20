package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestProviderErrorsNameNoMachineSpecificTooling(t *testing.T) {
	notConfigured := errProviderNotConfigured("selfhosted").Error()
	unreachable := errProviderUnreachable("selfhosted", "http://127.0.0.1:8010/v1", errors.New("connection refused")).Error()

	for _, message := range []string{notConfigured, unreachable} {
		for _, banned := range []string{"osu-llm", "muse", "vllm-api-key"} {
			if strings.Contains(strings.ToLower(message), banned) {
				t.Errorf("error copy names machine-specific tooling %q: %s", banned, message)
			}
		}
	}

	// The unconfigured case has to say how to fix it, or it just reads as broken.
	if !strings.Contains(notConfigured, "~/.fastllm/config.json") || !strings.Contains(notConfigured, "SELFHOSTED_LLM_BASE_URL") {
		t.Errorf("unconfigured error does not explain how to configure: %s", notConfigured)
	}
	if !strings.Contains(unreachable, "http://127.0.0.1:8010/v1") {
		t.Errorf("unreachable error should name the endpoint it tried: %s", unreachable)
	}
	if !errors.Is(errProviderUnreachable("selfhosted", "u", os.ErrDeadlineExceeded), os.ErrDeadlineExceeded) {
		t.Error("the unreachable error must keep wrapping its cause")
	}
}

func TestSelfHostedProviderIsSkippedWhenUnconfigured(t *testing.T) {
	router := NewRouter(New("http://localhost:11434/v1", "", "llama3.1", ""), CloudProviderConfig{})
	router.mu.RLock()
	selfHosted := router.clouds.selfHosted
	router.mu.RUnlock()
	if selfHosted != nil {
		t.Fatal("an empty SelfHostedBaseURL must leave the client unbuilt rather than dialing a default host")
	}
}

func TestSelfHostedProviderIsBuiltFromInjectedConfig(t *testing.T) {
	router := NewRouter(New("http://localhost:11434/v1", "", "llama3.1", ""), CloudProviderConfig{
		SelfHostedBaseURL: "http://10.0.0.5:9000/v1",
		SelfHostedAPIKey:  "secret",
		SelfHostedModel:   "example-model",
	})
	router.mu.RLock()
	selfHosted := router.clouds.selfHosted
	router.mu.RUnlock()
	if selfHosted == nil {
		t.Fatal("a configured endpoint should produce a client")
	}
	if selfHosted.BaseURL != "http://10.0.0.5:9000/v1" {
		t.Fatalf("client points at %q, not the injected endpoint", selfHosted.BaseURL)
	}
}

// A Router that reports no usage makes every caller fall back to estimating
// tokens, which silently turns real token counts and costs into guesses.
func TestRouterReportsUsageFromTheServingClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],` +
			`"usage":{"prompt_tokens":123,"completion_tokens":45,"total_tokens":168}}`))
	}))
	defer server.Close()

	local := New(server.URL, "", "local-model", "")
	router := NewRouter(local, CloudProviderConfig{})

	if _, ok := router.LastUsage(); ok {
		t.Fatal("usage reported before any request")
	}

	if _, err := router.Chat(context.Background(), "local-model", []Message{{Role: "user", Content: "hi"}}, nil, ""); err != nil {
		t.Fatal(err)
	}

	usage, ok := router.LastUsage()
	if !ok {
		t.Fatal("router reported no usage; callers would fall back to estimated tokens")
	}
	if usage.PromptTokens != 123 || usage.CompletionTokens != 45 {
		t.Fatalf("usage = %+v; want the counts the server reported", usage)
	}
}
