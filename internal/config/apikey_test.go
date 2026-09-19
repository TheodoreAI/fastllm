package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAPIKeyPrefersInlineKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	endpoint := ModelEndpoint{APIKey: "inline", APIKeyFile: path}
	if got := endpoint.ResolveAPIKey(); got != "inline" {
		t.Fatalf("inline key should win, got %q", got)
	}
}

func TestResolveAPIKeyReadsFileAndTrims(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("  secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	endpoint := ModelEndpoint{APIKeyFile: path}
	if got := endpoint.ResolveAPIKey(); got != "secret-value" {
		t.Fatalf("expected the trimmed file contents, got %q", got)
	}
}

func TestResolveAPIKeyExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	path := filepath.Join(home, ".fastllm-resolve-key-test")
	if err := os.WriteFile(path, []byte("home-secret"), 0o600); err != nil {
		t.Skip("home directory is not writable")
	}
	defer os.Remove(path)

	for _, spec := range []string{"~/.fastllm-resolve-key-test", `~\.fastllm-resolve-key-test`} {
		endpoint := ModelEndpoint{APIKeyFile: spec}
		if got := endpoint.ResolveAPIKey(); got != "home-secret" {
			t.Fatalf("%s did not expand to the home directory, got %q", spec, got)
		}
	}
}

func TestResolveAPIKeyIsEmptyWhenUnset(t *testing.T) {
	for _, endpoint := range []ModelEndpoint{
		{},
		{APIKey: "   "},
		{APIKeyFile: filepath.Join(t.TempDir(), "missing")},
	} {
		if got := endpoint.ResolveAPIKey(); got != "" {
			t.Fatalf("expected an empty key for %+v, got %q", endpoint, got)
		}
	}
}

func TestResolveAPIKeyRoundTripsThroughConfigFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "vllm-api-key")
	if err := os.WriteFile(keyPath, []byte("tunnel-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// LoadSettings only searches <dir>/.fastllm/, so anywhere else silently falls
	// through to the real global config.
	configPath := filepath.Join(dir, ".fastllm", "config.json")
	settings := &Settings{
		DefaultModel: "muse-glimmer",
		Models:       []ModelEndpoint{{ID: "muse-glimmer", URL: "http://127.0.0.1:8010/v1", APIKeyFile: keyPath}},
	}
	if err := SaveSettings(configPath, settings); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || !strings.Contains(string(raw), "api_key_file") {
		t.Fatalf("api_key_file was not persisted:\n%s", raw)
	}
	if strings.Contains(string(raw), "tunnel-key") {
		t.Fatalf("the secret itself leaked into config.json:\n%s", raw)
	}

	loaded, _, err := LoadSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := loaded.FindModel("muse-glimmer")
	if endpoint == nil {
		t.Fatal("model not found after reload")
	}
	if got := endpoint.ResolveAPIKey(); got != "tunnel-key" {
		t.Fatalf("expected the key resolved from file, got %q", got)
	}
}
