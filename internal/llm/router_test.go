package llm

import (
	"errors"
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
		SelfHostedModel:   "muse-glimmer",
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
