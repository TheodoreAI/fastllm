package appserver

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fastllm/internal/llm"
	"fastllm/internal/store"
)

func TestConfigFromEnvDefaults(t *testing.T) {
	for _, key := range []string{
		"FASTLLM_DB", "LLM_BASE_URL", "LLM_API_KEY", "LLM_CHAT_MODEL",
		"FASTLLM_FILES_ROOT", "FASTLLM_FILES_WRITE",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key) // t.Setenv("") still leaves the var *set* to "" — Unsetenv is what getenv's os.Getenv check needs to see the fallback
	}

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv returned error: %v", err)
	}
	if cfg.DBPath != "fastllm.db" {
		t.Errorf("DBPath = %q, want default %q", cfg.DBPath, "fastllm.db")
	}
	if cfg.LLMBaseURL != "http://localhost:11434/v1" {
		t.Errorf("LLMBaseURL = %q, want the Ollama default", cfg.LLMBaseURL)
	}
	if cfg.LLMChatModel != "llama3.1" {
		t.Errorf("LLMChatModel = %q, want default %q", cfg.LLMChatModel, "llama3.1")
	}
	if cfg.FilesWrite {
		t.Error("FilesWrite should default to false when FASTLLM_FILES_WRITE is unset")
	}
}

func TestConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("FASTLLM_DB", "/tmp/custom.db")
	t.Setenv("LLM_BASE_URL", "http://example.com/v1")
	t.Setenv("LLM_CHAT_MODEL", "custom-model")
	t.Setenv("FASTLLM_FILES_ROOT", "/some/project")
	t.Setenv("FASTLLM_FILES_WRITE", "1")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv returned error: %v", err)
	}
	if cfg.DBPath != "/tmp/custom.db" {
		t.Errorf("DBPath = %q, want the env override", cfg.DBPath)
	}
	if cfg.LLMBaseURL != "http://example.com/v1" {
		t.Errorf("LLMBaseURL = %q, want the env override", cfg.LLMBaseURL)
	}
	if cfg.LLMChatModel != "custom-model" {
		t.Errorf("LLMChatModel = %q, want the env override", cfg.LLMChatModel)
	}
	if cfg.FilesRoot != "/some/project" {
		t.Errorf("FilesRoot = %q, want the env override", cfg.FilesRoot)
	}
	if !cfg.FilesWrite {
		t.Error("FilesWrite should be true when FASTLLM_FILES_WRITE is set to any non-empty value")
	}
}

func TestDesktopDBPathCreatesAndReturnsUnderUserConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// os.UserConfigDir() on Linux prefers XDG_CONFIG_HOME over $HOME/.config
	// when set — clear it so this test's HOME override is what actually
	// takes effect regardless of the host running it.
	t.Setenv("XDG_CONFIG_HOME", "")
	os.Unsetenv("XDG_CONFIG_HOME")

	path, err := DesktopDBPath()
	if err != nil {
		t.Fatalf("DesktopDBPath returned error: %v", err)
	}
	if filepath.Base(path) != "fastllm.db" {
		t.Errorf("path = %q, want it to end in fastllm.db", path)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		t.Errorf("DesktopDBPath should have created its parent directory: %v", err)
	}
}

// TestBuildRegistersRoutesAndReturnsUsableHandles is a smoke test: Build
// does a lot of setup (DB, vector store, LLM client, file settings)
// with nothing here to unit-test individually without a real
// Ollama/cloud backend, but it should never panic on a fresh in-memory DB,
// and the mux it hands back should actually route requests — catching the
// class of bug where a new endpoint gets implemented as a Handler method
// but the mux.HandleFunc registration is forgotten (or typo'd).
func TestBuildRegistersRoutesAndReturnsUsableHandles(t *testing.T) {
	cfg := Config{
		DBPath:       ":memory:",
		LLMBaseURL:   "http://127.0.0.1:0/v1", // never dialed during Build itself
		LLMChatModel: "test-model",
	}
	built, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	defer built.DB.Close()

	if built.Mux == nil {
		t.Fatal("Built.Mux is nil")
	}
	if built.Handler == nil {
		t.Fatal("Built.Handler is nil")
	}

	for _, route := range []struct {
		method, path string
	}{
		{"GET", "/api/conversations"},
		{"GET", "/api/models"},
		{"GET", "/api/notes"},
		{"POST", "/api/harness/run"},
	} {
		req := httptest.NewRequest(route.method, route.path, nil)
		rec := httptest.NewRecorder()
		built.Mux.ServeHTTP(rec, req)
		if rec.Code == 404 {
			t.Errorf("%s %s: got 404 — route not registered on the mux", route.method, route.path)
		}
	}
}

// TestBuildAppliesFileSettingsFromEnv guards against Build
// silently discarding the real persisted file-access settings and
// falling back to the (disabled) zero-value defaults instead — the two look
// identical unless a test actually seeds a non-default config through cfg
// and checks it took effect on the built Handler.
func TestBuildAppliesFileSettingsFromEnv(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fastllm.db")
	filesRoot := t.TempDir()

	cfg := Config{
		DBPath:       dbPath,
		LLMBaseURL:   "http://127.0.0.1:0/v1",
		LLMChatModel: "test-model",
		FilesRoot:    filesRoot,
		FilesWrite:   true,
	}
	built, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	defer built.DB.Close()

	if !built.Handler.Files.Enabled() {
		t.Error("Handler.Files.Enabled() = false, want true — FilesRoot was set in Config")
	}
	if !built.Handler.Files.WritesEnabled() {
		t.Error("Handler.Files.WritesEnabled() = false, want true — FilesWrite was set in Config")
	}
	if got := built.Handler.Files.GetRoot(); got != filesRoot {
		t.Errorf("Handler.Files.GetRoot() = %q, want %q — the seeded root, not the zero-value default", got, filesRoot)
	}
}

// TestBuildLoadsPersistedCloudProviderSettings guards against Build
// discarding a real, previously-saved cloud-provider config and building
// the LLM router as if no providers were configured at all — pre-seeding
// the DB with an Anthropic key and checking ListModels actually surfaces an
// anthropic-prefixed model is the only externally observable signal Build
// exposes for "the loaded cloud settings, not the defaults, took effect".
func TestBuildLoadsPersistedCloudProviderSettings(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fastllm.db")

	seedDB, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open for seeding returned error: %v", err)
	}
	if err := store.SaveCloudProviderSettings(seedDB, store.CloudProviderSettings{AnthropicAPIKey: "fake-test-key"}); err != nil {
		t.Fatalf("SaveCloudProviderSettings returned error: %v", err)
	}
	if err := seedDB.Close(); err != nil {
		t.Fatalf("closing seed DB: %v", err)
	}

	built, err := Build(Config{
		DBPath:       dbPath,
		LLMBaseURL:   "http://127.0.0.1:0/v1", // never dialed; ListModels swallows the local-listing error
		LLMChatModel: "test-model",
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	defer built.DB.Close()

	models, err := built.Handler.LLM.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
	found := false
	for _, m := range models {
		if strings.HasPrefix(m.Name, llm.AnthropicPrefix) {
			found = true
			break
		}
	}
	if !found {
		t.Error("ListModels contains no anthropic-prefixed model — the seeded AnthropicAPIKey was not applied (Build fell back to default/empty cloud settings)")
	}
}
