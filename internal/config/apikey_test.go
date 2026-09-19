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
		DefaultModel: "tunnel-model",
		Models:       []ModelEndpoint{{ID: "tunnel-model", URL: "http://127.0.0.1:8010/v1", APIKeyFile: keyPath}},
	}
	if _, err := SaveSettings(configPath, settings); err != nil {
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
	endpoint := loaded.FindModel("tunnel-model")
	if endpoint == nil {
		t.Fatal("model not found after reload")
	}
	if got := endpoint.ResolveAPIKey(); got != "tunnel-key" {
		t.Fatalf("expected the key resolved from file, got %q", got)
	}
}

// withTempHome points the key store at a temporary directory so tests never write
// into the real ~/.fastllm/keys.
func withTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	previous := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = previous })
	return home
}

func TestSaveSettingsRelocatesInlineKeyOutOfConfig(t *testing.T) {
	home := withTempHome(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".fastllm", "config.json")

	settings := &Settings{
		DefaultModel: "tunnel",
		Models:       []ModelEndpoint{{ID: "tunnel", URL: "http://127.0.0.1:8010/v1", APIKey: "super-secret"}},
	}
	relocated, err := SaveSettings(configPath, settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(relocated) != 1 || relocated[0] != "tunnel" {
		t.Fatalf("expected the relocated model reported, got %v", relocated)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret") {
		t.Fatalf("the secret was written into config.json:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"api_key_file": "~/.fastllm/keys/tunnel"`) {
		t.Fatalf("config.json does not reference the key file:\n%s", raw)
	}

	keyPath := filepath.Join(home, ".fastllm", "keys", "tunnel")
	stored, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("key file was not written: %v", err)
	}
	if strings.TrimSpace(string(stored)) != "super-secret" {
		t.Fatalf("key file holds %q", stored)
	}

	// The relocation must preserve what the key resolved to before the save.
	loaded, _, err := LoadSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.FindModel("tunnel").ResolveAPIKey(); got != "super-secret" {
		t.Fatalf("key no longer resolves after relocation, got %q", got)
	}
}

func TestSaveSettingsDoesNotMutateCallerSettings(t *testing.T) {
	withTempHome(t)
	settings := &Settings{Models: []ModelEndpoint{{ID: "tunnel", APIKey: "super-secret"}}}
	if _, err := SaveSettings(filepath.Join(t.TempDir(), "config.json"), settings); err != nil {
		t.Fatal(err)
	}
	if settings.Models[0].APIKey != "super-secret" {
		t.Fatal("the live in-memory settings lost their key; the running session would break")
	}
}

func TestSaveSettingsSanitizesModelIDIntoFilename(t *testing.T) {
	home := withTempHome(t)
	settings := &Settings{Models: []ModelEndpoint{{ID: "vendor-models/Example-Model:30B", APIKey: "k"}}}
	if _, err := SaveSettings(filepath.Join(t.TempDir(), "config.json"), settings); err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(home, ".fastllm", "keys", "vendor-models_Example-Model_30B")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("expected sanitized key file at %s: %v", expected, err)
	}
}

func TestSaveSettingsLeavesKeylessModelsAlone(t *testing.T) {
	withTempHome(t)
	path := filepath.Join(t.TempDir(), "config.json")
	settings := &Settings{Models: []ModelEndpoint{{ID: "ollama", URL: "http://localhost:11434/v1"}}}
	relocated, err := SaveSettings(path, settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(relocated) != 0 {
		t.Fatalf("nothing should be relocated, got %v", relocated)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "api_key") {
		t.Fatalf("empty key fields should be omitted entirely:\n%s", raw)
	}
}

func TestDefaultSettingsCarryNoMachineSpecificEndpoints(t *testing.T) {
	defaults := DefaultSettings()
	if defaults.FindModel(defaults.DefaultModel) == nil {
		t.Fatalf("default model %q is not among the shipped endpoints", defaults.DefaultModel)
	}
	for _, endpoint := range defaults.Models {
		if endpoint.APIKey != "" || endpoint.APIKeyFile != "" {
			t.Errorf("%s ships a credential reference", endpoint.ID)
		}
		// Canonical provider hosts and the stock Ollama port only. A forwarded or
		// site-specific port here becomes every user's default.
		switch {
		case strings.Contains(endpoint.URL, "localhost:11434"),
			strings.HasPrefix(endpoint.URL, "https://api."):
		default:
			t.Errorf("%s points at a non-canonical endpoint %q", endpoint.ID, endpoint.URL)
		}
	}
}

func TestResolveProviderReadsTaggedConfigEntry(t *testing.T) {
	home := withTempHome(t)
	keyPath := filepath.Join(home, "tunnel-key")
	if err := os.WriteFile(keyPath, []byte("tunnel-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := &Settings{Models: []ModelEndpoint{
		{ID: "llama3.1", URL: "http://localhost:11434/v1"},
		{ID: "tunnel-model", URL: "http://127.0.0.1:8010/v1", APIKeyFile: keyPath, Provider: "selfhosted"},
	}}
	got := ResolveProvider(settings, "selfhosted")
	if got.BaseURL != "http://127.0.0.1:8010/v1" || got.APIKey != "tunnel-secret" || got.Model != "tunnel-model" {
		t.Fatalf("unexpected resolution: %+v", got)
	}
	if !got.Configured() {
		t.Fatal("a tagged entry should count as configured")
	}
}

func TestResolveProviderIsEmptyWithoutConfigOrEnv(t *testing.T) {
	for _, name := range []string{"SELFHOSTED_LLM_BASE_URL", "LLM_SELFHOSTED_BASE_URL", "SELFHOSTED_LLM_API_KEY", "LLM_SELFHOSTED_API_KEY"} {
		t.Setenv(name, "")
	}
	for _, settings := range []*Settings{nil, {}, {Models: []ModelEndpoint{{ID: "llama3.1", URL: "http://localhost:11434/v1"}}}} {
		got := ResolveProvider(settings, "selfhosted")
		if got.Configured() || got.BaseURL != "" || got.APIKey != "" {
			t.Fatalf("an unconfigured machine must resolve to nothing, got %+v", got)
		}
	}
}

func TestResolveProviderPrefersEnvironmentOverConfig(t *testing.T) {
	t.Setenv("SELFHOSTED_LLM_BASE_URL", "http://10.0.0.5:9000/v1")
	t.Setenv("SELFHOSTED_LLM_API_KEY", "env-secret")
	settings := &Settings{Models: []ModelEndpoint{
		{ID: "tunnel-model", URL: "http://127.0.0.1:8010/v1", APIKey: "config-secret", Provider: "selfhosted"},
	}}
	got := ResolveProvider(settings, "selfhosted")
	if got.BaseURL != "http://10.0.0.5:9000/v1" || got.APIKey != "env-secret" {
		t.Fatalf("environment should win: %+v", got)
	}
	// The model id still comes from config; only the transport is overridden.
	if got.Model != "tunnel-model" {
		t.Fatalf("expected the configured model id, got %q", got.Model)
	}
}

func TestFindByProviderIgnoresUntaggedEntries(t *testing.T) {
	settings := &Settings{Models: []ModelEndpoint{{ID: "a"}, {ID: "b", Provider: "SELFHOSTED"}}}
	if got := settings.FindByProvider("selfhosted"); got == nil || got.ID != "b" {
		t.Fatalf("provider match should be case-insensitive, got %+v", got)
	}
	if got := settings.FindByProvider("nope"); got != nil {
		t.Fatalf("unknown provider should resolve to nil, got %+v", got)
	}
	if got := (*Settings)(nil).FindByProvider("selfhosted"); got != nil {
		t.Fatal("nil settings must be safe")
	}
}
