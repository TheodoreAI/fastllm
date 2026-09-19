// Package appserver holds the core server wiring for fastllm: opening the DB,
// constructing the LLM router, setting up the sandboxed file system, and
// registering every HTTP route on an http.ServeMux.
package appserver

import (
	"database/sql"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"fastllm/internal/chat"
	"fastllm/internal/config"
	"fastllm/internal/files"
	"fastllm/internal/llm"
	"fastllm/internal/store"
	"fastllm/web"
)

// Config holds environment-derived settings.
type Config struct {
	DBPath       string
	LLMBaseURL   string
	LLMAPIKey    string
	LLMChatModel string
	FilesRoot    string
	FilesWrite   bool
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
	}, nil
}

// Built holds the constructed mux, DB handle, and chat handler.
type Built struct {
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
	userSettings, _, _ := config.LoadSettings("")
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

	mux.Handle("/", http.FileServer(http.FS(web.FS())))

	return &Built{Mux: mux, DB: db, Handler: handler}, nil
}

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
