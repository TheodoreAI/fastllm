package harness

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fastllm/internal/config"
)

func TestDetectSavesTheWindowAndAFailureChangesNothing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			w.Write([]byte(`{"parameters":"num_ctx 16384"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	settings := &config.Settings{Models: []config.ModelEndpoint{
		{ID: "local:9b", URL: server.URL + "/v1"},
		{ID: "offline", URL: "http://127.0.0.1:1/v1"}, // nothing listens here
	}}
	if _, err := config.SaveSettings(configPath, settings); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(configPath)

	// The unreachable endpoint alone: nothing is written.
	targets, missing := contextProbeTargets(settings, []string{"offline", "nope"})
	lines, changed, err := applyContextWindows(settings, configPath, probeContextWindows(targets), missing)
	if err != nil || changed {
		t.Fatalf("failed probe changed config: changed=%v err=%v", changed, err)
	}
	if after, _ := os.ReadFile(configPath); string(after) != string(before) {
		t.Fatal("config was rewritten although nothing was detected")
	}
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "offline: kept") || !strings.Contains(joined, "nope: not configured") {
		t.Fatalf("lines = %q", lines)
	}

	targets, missing = contextProbeTargets(settings, []string{"local:9b"})
	lines, changed, err = applyContextWindows(settings, configPath, probeContextWindows(targets), missing)
	if err != nil || !changed || !strings.Contains(lines[0], "16384 tokens from Ollama num_ctx") {
		t.Fatalf("detect: lines=%q changed=%v err=%v", lines, changed, err)
	}
	data, _ := os.ReadFile(configPath)
	if !strings.Contains(string(data), `"context_window": 16384`) {
		t.Fatalf("saved config lacks the detected window:\n%s", data)
	}
}
