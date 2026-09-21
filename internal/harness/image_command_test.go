package harness

import (
	"testing"

	"fastllm/internal/config"
)

func TestConfiguredImageEndpointPrefersConventionalID(t *testing.T) {
	settings := &config.Settings{Models: []config.ModelEndpoint{
		{ID: "other-image", Name: "Other Image", URL: "http://other.invalid/v1"},
		{ID: "qwen-image", Name: "Qwen Image", URL: "http://qwen.invalid/v1"},
	}}

	got := configuredImageEndpoint(settings)
	if got == nil || got.ID != "qwen-image" {
		t.Fatalf("endpoint = %#v, want qwen-image", got)
	}
}

func TestConfiguredImageEndpointFallsBackToImageNamedModel(t *testing.T) {
	settings := &config.Settings{Models: []config.ModelEndpoint{
		{ID: "chat", Name: "Chat Model", URL: "http://chat.invalid/v1"},
		{ID: "art", Name: "Local Image Generator", URL: "http://image.invalid/v1"},
	}}

	got := configuredImageEndpoint(settings)
	if got == nil || got.ID != "art" {
		t.Fatalf("endpoint = %#v, want art", got)
	}
}

func TestConfiguredImageEndpointReturnsNilWithoutMatch(t *testing.T) {
	settings := &config.Settings{Models: []config.ModelEndpoint{{ID: "chat", Name: "Chat Model"}}}
	if got := configuredImageEndpoint(settings); got != nil {
		t.Fatalf("endpoint = %#v, want nil", got)
	}
}
