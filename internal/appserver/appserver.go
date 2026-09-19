// Package appserver holds the wiring shared by fastllm's two entrypoints
// (cmd/server, the plain HTTP/browser binary, and cmd/desktop, the Wails
// native app): opening the DB, constructing the LLM client, loading the
// vector store, applying file-access/terminal settings, and registering
// every HTTP route on a *http.ServeMux. Both entrypoints call Build with
// their own Config and get back the exact same mux and handler wiring —
// this is what guarantees a route added for one never gets forgotten in
// the other.
package appserver

import (
	"database/sql"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"fastllm/internal/chat"
	"fastllm/internal/files"
	"fastllm/internal/llm"
	"fastllm/internal/store"
	"fastllm/internal/vector"
	"fastllm/web"
)

// Config is every environment-derived setting the app needs before it can
// start serving. ConfigFromEnv reads it the same way cmd/server always
// has; cmd/desktop calls that and then overrides DBPath, since a
// double-clicked binary has no reliable working directory to resolve a
// relative path against.
type Config struct {
	DBPath        string
	LLMBaseURL    string
	LLMAPIKey     string
	LLMChatModel  string
	LLMEmbedModel string
	FilesRoot     string
	FilesWrite    bool
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ConfigFromEnv reads the same environment variables cmd/server has
// always used. FASTLLM_ADDR is deliberately not part of Config — it only
// means something to an entrypoint that binds a TCP listener, which
// cmd/desktop does not.
func ConfigFromEnv() (Config, error) {
	return Config{
		DBPath:        getenv("FASTLLM_DB", "fastllm.db"),
		LLMBaseURL:    getenv("LLM_BASE_URL", "http://localhost:11434/v1"),
		LLMAPIKey:     getenv("LLM_API_KEY", ""),
		LLMChatModel:  getenv("LLM_CHAT_MODEL", "llama3.1"),
		LLMEmbedModel: getenv("LLM_EMBED_MODEL", "nomic-embed-text"),
		FilesRoot:     getenv("FASTLLM_FILES_ROOT", ""),
		FilesWrite:    getenv("FASTLLM_FILES_WRITE", "") != "",
	}, nil
}

// DesktopDBPath resolves the desktop app's database path under the OS
// user-config directory (e.g. %AppData%\fastllm\fastllm.db on Windows)
// rather than cmd/server's CWD-relative default, since a double-clicked
// .exe has no predictable working directory. Creates the directory if
// it doesn't exist yet.
func DesktopDBPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	appDir := filepath.Join(dir, "fastllm")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(appDir, "fastllm.db"), nil
}

// Built is everything Build hands back: the fully-routed mux ready to
// serve, plus the DB handle and chat handler.
type Built struct {
	Mux     *http.ServeMux
	DB      *sql.DB
	Handler *chat.Handler
}

// Build opens the DB, applies persisted (or env-seeded) file-access and
// terminal settings, and registers every route fastllm serves. This is
// the exact wiring cmd/server's main() used to do inline before
// cmd/desktop needed the same setup — see git history on cmd/server/main.go
// for the pre-extraction version if a diff is ever needed.
func Build(cfg Config) (*Built, error) {
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	vecStore := vector.NewStore()
	chunks, err := store.LoadAllChunks(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	vecStore.Load(chunks)
	log.Printf("loaded %d chunks into vector store", len(chunks))

	llmClient := llm.New(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMChatModel, cfg.LLMEmbedModel)
	// The local backend is assumed to be Ollama (or an Ollama-compatible
	// server), which understands the "think" reasoning-effort field —
	// unlike the cloud clients llm.Router constructs below, which are
	// real OpenAI-wire-compatible APIs with no such field and, at least
	// for NVIDIA Build, a confirmed 400 if it's sent anyway. See
	// llm.Client.SendThink's doc comment.
	llmClient.SendThink = true
	cloudSettings, err := store.GetCloudProviderSettings(db)
	if err != nil {
		log.Printf("load cloud provider settings: %v", err)
		cloudSettings = store.DefaultCloudProviderSettings
	}
	llmRouter := llm.NewRouter(llmClient, llm.CloudProviderConfig{
		AnthropicAPIKey:     cloudSettings.AnthropicAPIKey,
		OpenAIAPIKey:        cloudSettings.OpenAIAPIKey,
		GeminiAPIKey:        cloudSettings.GeminiAPIKey,
		NvidiaAPIKey:        cloudSettings.NvidiaAPIKey,
		CloudflareAPIKey:    cloudSettings.CloudflareAPIKey,
		CloudflareAccountID: cloudSettings.CloudflareAccountID,
		OSUBaseURL:          cloudSettings.OSUBaseURL,
		OSUAPIKey:           cloudSettings.OSUAPIKey,
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
		log.Printf("file-write tool enabled (manual approval required for every write)")
	}

	handler := chat.New(db, llmRouter, vecStore, fileReader)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", handler.Chat)
	mux.HandleFunc("POST /api/harness/run", handler.HarnessRun)
	mux.HandleFunc("GET /api/messages", handler.ListMessages)
	mux.HandleFunc("POST /api/documents", handler.UploadDocument)
	mux.HandleFunc("POST /api/documents/upload", handler.UploadFile)
	mux.HandleFunc("GET /api/documents", handler.ListDocuments)
	mux.HandleFunc("DELETE /api/documents", handler.ClearKnowledgeBase)
	mux.HandleFunc("GET /api/models", handler.ListModels)
	mux.HandleFunc("GET /api/notes", handler.GetNotes)
	mux.HandleFunc("PUT /api/notes", handler.SaveNotes)
	mux.HandleFunc("POST /api/notes/append", handler.AppendNotes)
	mux.HandleFunc("GET /api/settings", handler.Settings)
	mux.HandleFunc("GET /api/settings/rag", handler.GetRAGSettings)
	mux.HandleFunc("PUT /api/settings/rag", handler.UpdateRAGSettings)
	mux.HandleFunc("GET /api/settings/files", handler.GetFileAccessSettings)
	mux.HandleFunc("PUT /api/settings/files", handler.UpdateFileAccessSettings)
	mux.HandleFunc("GET /api/settings/cloud-providers", handler.GetCloudProviderSettings)
	mux.HandleFunc("PUT /api/settings/cloud-providers", handler.UpdateCloudProviderSettings)
	mux.HandleFunc("POST /api/settings/files/browse", handler.BrowseForFolder)
	mux.HandleFunc("DELETE /api/conversations", handler.ClearConversations)
	mux.HandleFunc("GET /api/skills", handler.ListSkills)
	mux.HandleFunc("POST /api/skills", handler.CreateSkill)
	mux.HandleFunc("DELETE /api/skills/{id}", handler.DeleteSkill)
	mux.HandleFunc("GET /api/conversations", handler.ListConversations)
	mux.HandleFunc("DELETE /api/conversations/{id}", handler.DeleteConversation)
	mux.HandleFunc("POST /api/writes/{id}/approve", handler.ApproveWrite)
	mux.HandleFunc("POST /api/writes/{id}/reject", handler.RejectWrite)

	// OpenAI-compatible proxy an external tool (e.g. a terminal-based AI
	// CLI) can point its API base URL at instead of the local model server
	// directly. GET /api/live/stream is the SSE feed the Chat panel
	// subscribes to so that traffic shows up live.
	mux.HandleFunc("POST /v1/chat/completions", handler.ExternalChatCompletions)
	mux.HandleFunc("GET /v1/models", handler.ExternalModels)
	// Anthropic wire format (ANTHROPIC_BASE_URL) — what Claude Code itself
	// speaks, distinct from the OpenAI-shaped pair above.
	mux.HandleFunc("POST /v1/messages", handler.AnthropicMessages)
	mux.HandleFunc("GET /api/live/stream", handler.LiveStream)
	mux.HandleFunc("PUT /api/live/target", handler.SetLiveTarget)

	mux.Handle("/", http.FileServer(http.FS(web.FS())))

	return &Built{Mux: mux, DB: db, Handler: handler}, nil
}
