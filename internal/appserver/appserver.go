// Package appserver holds the core server wiring for fastllm: opening the DB,
// constructing the LLM router, setting up the sandboxed file system, and
// registering every HTTP route on an http.ServeMux.
package appserver

import (
	"database/sql"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"fastllm/internal/chat"
	"fastllm/internal/config"
	"fastllm/internal/files"
	"fastllm/internal/harness"
	"fastllm/internal/llm"
	"fastllm/internal/store"
)

// Config holds environment-derived settings.
type Config struct {
	DBPath       string
	LLMBaseURL   string
	LLMAPIKey    string
	LLMChatModel string
	FilesRoot    string
	FilesWrite   bool

	// Control-plane limits (see guard.go). Token is loaded by the caller so
	// reading the environment never touches the filesystem.
	Token        string
	MaxMode      harness.PermissionMode // highest mode a harness run may request
	AllowedRoots []string               // directories a run's working_dir must be inside
	AllowedHosts []string               // Host names accepted beyond loopback
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ConfigFromEnv reads standard fastllm environment variables.
func ConfigFromEnv() (Config, error) {
	return Config{
		DBPath:       getenv("FASTLLM_DB", "fastllm.db"),
		LLMBaseURL:   getenv("LLM_BASE_URL", "http://localhost:11434/v1"),
		LLMAPIKey:    getenv("LLM_API_KEY", ""),
		LLMChatModel: getenv("LLM_CHAT_MODEL", "llama3.1"),
		FilesRoot:    getenv("FASTLLM_FILES_ROOT", ""),
		FilesWrite:   getenv("FASTLLM_FILES_WRITE", "") != "",
		MaxMode:      harness.PermissionMode(getenv("FASTLLM_MAX_MODE", string(harness.PermissionEdit))),
		AllowedRoots: splitList(os.Getenv("FASTLLM_ALLOWED_ROOTS"), string(os.PathListSeparator)),
		AllowedHosts: splitList(os.Getenv("FASTLLM_ALLOWED_HOSTS"), ","),
	}, nil
}

// splitList splits a separator-joined environment value, dropping blanks.
func splitList(value, sep string) []string {
	var out []string
	for _, part := range strings.Split(value, sep) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Built holds the constructed mux, DB handle, and chat handler. HTTP is the
// mux behind the control-plane guard; serve HTTP, never Mux directly.
type Built struct {
	HTTP    http.Handler
	Mux     *http.ServeMux
	DB      *sql.DB
	Handler *chat.Handler
}

// Build initializes storage, LLM router, file sandbox, and HTTP routes.
func Build(cfg Config) (*Built, error) {
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	llmClient := llm.New(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMChatModel, "")
	llmClient.SendThink = true

	cloudSettings, err := store.GetCloudProviderSettings(db)
	if err != nil {
		log.Printf("load cloud provider settings: %v", err)
		cloudSettings = store.DefaultCloudProviderSettings
	}
	// Resolve the self-hosted endpoint once, here at the composition root, so no
	// lower layer has to guess a host or read a key path of its own.
	userSettings, _, err := config.LoadSettings("")
	if err != nil {
		db.Close()
		return nil, err
	}
	selfHostedDefaults := config.ResolveProvider(userSettings, "selfhosted")

	llmRouter := llm.NewRouter(llmClient, llm.CloudProviderConfig{
		AnthropicAPIKey:     cloudSettings.AnthropicAPIKey,
		OpenAIAPIKey:        cloudSettings.OpenAIAPIKey,
		GeminiAPIKey:        cloudSettings.GeminiAPIKey,
		NvidiaAPIKey:        cloudSettings.NvidiaAPIKey,
		CloudflareAPIKey:    cloudSettings.CloudflareAPIKey,
		CloudflareAccountID: cloudSettings.CloudflareAccountID,
		SelfHostedBaseURL:   firstNonEmpty(cloudSettings.SelfHostedBaseURL, selfHostedDefaults.BaseURL),
		SelfHostedAPIKey:    firstNonEmpty(cloudSettings.SelfHostedAPIKey, selfHostedDefaults.APIKey),
		SelfHostedModel:     selfHostedDefaults.Model,
	})

	if err := store.SeedFileAccessSettingsFromEnv(db, cfg.FilesRoot, cfg.FilesWrite); err != nil {
		log.Printf("seed file access settings from env: %v", err)
	}
	fileSettings, err := store.GetFileAccessSettings(db)
	if err != nil {
		log.Printf("load file access settings: %v", err)
		fileSettings = store.DefaultFileAccessSettings
	}
	fileReader := files.New("", false)
	if err := fileReader.SetConfig(fileSettings.Root, fileSettings.ReadEnabled, fileSettings.WriteEnabled); err != nil {
		log.Printf("apply file access settings: %v", err)
	}
	if fileReader.Enabled() {
		log.Printf("file-read tool enabled, sandboxed to %s", fileReader.GetRoot())
	}
	if fileReader.WritesEnabled() {
		log.Printf("file-write tool enabled")
	}

	handler := chat.New(db, llmRouter, fileReader)
	handler.SelfHostedDefaults = selfHostedDefaults
	handler.MaxMode = cfg.MaxMode
	handler.AllowedRoots = cfg.AllowedRoots
	if len(handler.AllowedRoots) == 0 {
		// Default to the one directory the server already works in.
		if root := fileReader.GetRoot(); root != "" {
			handler.AllowedRoots = []string{root}
		} else if cwd, err := os.Getwd(); err == nil {
			handler.AllowedRoots = []string{cwd}
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", handler.Chat)
	mux.HandleFunc("POST /api/harness/run", handler.HarnessRun)
	mux.HandleFunc("GET /api/messages", handler.ListMessages)
	mux.HandleFunc("GET /api/models", handler.ListModels)
	mux.HandleFunc("GET /api/notes", handler.GetNotes)
	mux.HandleFunc("PUT /api/notes", handler.SaveNotes)
	mux.HandleFunc("POST /api/notes/append", handler.AppendNotes)
	mux.HandleFunc("GET /api/settings", handler.Settings)
	mux.HandleFunc("GET /api/settings/files", handler.GetFileAccessSettings)
	mux.HandleFunc("PUT /api/settings/files", handler.UpdateFileAccessSettings)
	mux.HandleFunc("GET /api/settings/cloud-providers", handler.GetCloudProviderSettings)
	mux.HandleFunc("PUT /api/settings/cloud-providers", handler.UpdateCloudProviderSettings)
	mux.HandleFunc("POST /api/settings/files/browse", handler.BrowseForFolder)
	mux.HandleFunc("DELETE /api/conversations", handler.ClearConversations)
	mux.HandleFunc("GET /api/conversations", handler.ListConversations)
	mux.HandleFunc("DELETE /api/conversations/{id}", handler.DeleteConversation)

	// OpenAI-compatible and Anthropic-compatible proxy routes
	mux.HandleFunc("POST /v1/chat/completions", handler.ExternalChatCompletions)
	mux.HandleFunc("GET /v1/models", handler.ExternalModels)
	mux.HandleFunc("POST /v1/messages", handler.AnthropicMessages)
	mux.HandleFunc("GET /api/live/stream", handler.LiveStream)
	mux.HandleFunc("PUT /api/live/target", handler.SetLiveTarget)

	// The browser frontend is gone: the terminal UI is the interface now. The
	// root still answers rather than 404ing, so anyone opening the old bookmark
	// learns where the UI went instead of seeing a bare error.
	mux.HandleFunc("GET /", rootNotice)

	guarded := Guard(mux, GuardConfig{Token: cfg.Token, AllowedHosts: cfg.AllowedHosts})
	return &Built{HTTP: guarded, Mux: mux, DB: db, Handler: handler}, nil
}

// rootNotice explains that this server is headless and points at the API it
// still serves.
func rootNotice(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, headlessNotice)
}

const headlessNotice = `fastllm is running headless.

The browser UI has been replaced by the terminal UI; run "fastllm" with no arguments.

This server still provides:
  POST /api/chat                 PUT  /api/notes
  POST /api/harness/run          GET  /api/models
  GET  /api/conversations        GET  /api/settings
  POST /v1/chat/completions      GET  /v1/models
  POST /v1/messages
`

// DesktopDBPath returns the path to fastllm.db in the user's config directory,
// creating the directory if it does not already exist.
func DesktopDBPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(configDir, "fastllm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "fastllm.db"), nil
}

// firstNonEmpty returns the first non-blank value, letting an explicit setting in
// the database override the endpoint resolved from user config.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
