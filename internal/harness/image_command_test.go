package harness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"fastllm/internal/config"
)

func TestGenerateConfiguredImageSendsConfiguredModel(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nvalid-signature")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["model"] != "qwen-image" {
			t.Fatalf("model = %#v, want qwen-image", request["model"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{
			"b64_json": base64.StdEncoding.EncodeToString(png),
		}}})
	}))
	defer server.Close()

	settings := &config.Settings{Models: []config.ModelEndpoint{{ID: "qwen-image", URL: server.URL}}}
	path, size, err := generateConfiguredImage(context.Background(), settings, t.TempDir(), "draw a robot")
	if err != nil {
		t.Fatal(err)
	}
	if path == "" || size != len(png) {
		t.Fatalf("path=%q size=%d, want a saved %d-byte image", path, size, len(png))
	}
}

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
