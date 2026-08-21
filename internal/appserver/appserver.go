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
	"fastllm/internal/lsp"
	"fastllm/internal/store"
	"fastllm/internal/terminal"
	"fastllm/internal/vector"
	"fastllm/web"
)

// Config is every environment-derived setting the app needs before it can
// start serving. ConfigFromEnv reads it the same way cmd/server always
// has; cmd/desktop calls that and then overrides DBPath, since a
// double-clicked binary has no reliable working directory to resolve a
// relative path against.
type Config struct {
	DBPath          string
	LLMBaseURL      string
	LLMAPIKey       string
	LLMChatModel    string
	LLMEmbedModel   string
	FilesRoot       string
	FilesWrite      bool
	TerminalEnabled bool
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
		DBPath:          getenv("FASTLLM_DB", "fastllm.db"),
		LLMBaseURL:      getenv("LLM_BASE_URL", "http://localhost:11434/v1"),
		LLMAPIKey:       getenv("LLM_API_KEY", ""),
		LLMChatModel:    getenv("LLM_CHAT_MODEL", "llama3.1"),
		LLMEmbedModel:   getenv("LLM_EMBED_MODEL", "nomic-embed-text"),
		FilesRoot:       getenv("FASTLLM_FILES_ROOT", ""),
		FilesWrite:      getenv("FASTLLM_FILES_WRITE", "") != "",
		TerminalEnabled: getenv("FASTLLM_TERMINAL_ENABLED", "") != "",
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
// serve, plus the pieces an entrypoint needs for its own lifecycle
// (closing the DB, tearing down live terminal sessions on shutdown) or to
// override afterward (cmd/desktop replaces Handler.FolderChooser with a
// Wails-native dialog before wails.Run starts serving — see that field's
// doc comment in internal/chat/handler.go).
type Built struct {
	Mux              *http.ServeMux
	DB               *sql.DB
	TerminalRegistry *terminal.Registry
	// LSPRegistry is nil if gopls wasn't found on PATH at startup (see
	// lsp.Available) — CloseAll is nil-receiver-safe, so shutdown paths
	// can call it unconditionally either way.
	LSPRegistry *lsp.Registry
	Handler     *chat.Handler
	// TerminalBaseURL must be set (via .Set("http://127.0.0.1:<port>")) by
	// the entrypoint once it knows its own actual bound loopback address —
	// see terminal.BaseURLHolder's doc comment for why Build can't do this
	// itself. Left unset, Settings → Terminal's "point local AI CLIs at
	// fastllm" toggle has no effect (handler.go treats an empty base URL as
	// "nothing to inject").
	TerminalBaseURL *terminal.BaseURLHolder
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

	if err := store.SeedTerminalSettingsFromEnv(db, cfg.TerminalEnabled); err != nil {
		log.Printf("seed terminal settings from env: %v", err)
	}
	terminalSettings, err := store.GetTerminalSettings(db)
	if err != nil {
		log.Printf("load terminal settings: %v", err)
		terminalSettings = store.DefaultTerminalSettings
	}
	terminalGate := terminal.NewGate(terminalSettings.Enabled)
	terminalGate.SetInjectEnv(terminalSettings.InjectLiveChatEnv)
	if terminalGate.Enabled() {
		log.Printf("terminal enabled — /api/terminal/ws will spawn an interactive shell session for any loopback connection")
	}

	handler := chat.New(db, llmRouter, vecStore, fileReader, terminalGate)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", handler.Chat)
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
	mux.HandleFunc("GET /api/settings/terminal", handler.GetTerminalSettings)
	mux.HandleFunc("PUT /api/settings/terminal", handler.UpdateTerminalSettings)
	mux.HandleFunc("GET /api/settings/editor", handler.GetEditorSettings)
	mux.HandleFunc("PUT /api/settings/editor", handler.UpdateEditorSettings)
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
	mux.HandleFunc("GET /api/editor/tree", handler.EditorTree)
	mux.HandleFunc("GET /api/editor/file", handler.EditorReadFile)
	mux.HandleFunc("PUT /api/editor/file", handler.EditorSaveFile)
	mux.HandleFunc("DELETE /api/editor/file", handler.EditorDeleteFile)
	mux.HandleFunc("GET /api/editor/folder/file-count", handler.EditorFolderFileCount)
	mux.HandleFunc("DELETE /api/editor/folder", handler.EditorDeleteFolder)
	mux.HandleFunc("POST /api/editor/file/rename", handler.EditorRenameFile)
	mux.HandleFunc("GET /api/editor/search", handler.EditorSearch)
	mux.HandleFunc("GET /api/editor/git/status", handler.EditorGitStatus)
	mux.HandleFunc("GET /api/editor/git/watch", handler.EditorGitWatch)
	mux.HandleFunc("POST /api/editor/complete", handler.EditorComplete)
	mux.HandleFunc("POST /api/editor/test", handler.EditorRunTests)
	mux.HandleFunc("GET /api/editor/git/diff", handler.EditorGitDiff)
	mux.HandleFunc("POST /api/editor/git/stage", handler.EditorGitStage)
	mux.HandleFunc("POST /api/editor/git/unstage", handler.EditorGitUnstage)
	mux.HandleFunc("POST /api/editor/git/discard", handler.EditorGitDiscard)
	mux.HandleFunc("POST /api/editor/git/commit", handler.EditorGitCommit)
	mux.HandleFunc("POST /api/editor/git/push", handler.EditorGitPush)
	mux.HandleFunc("POST /api/editor/git/push-set-upstream", handler.EditorGitPushSetUpstream)
	mux.HandleFunc("GET /api/editor/git/branches", handler.EditorGitBranches)
	mux.HandleFunc("POST /api/editor/git/switch", handler.EditorGitSwitchBranch)
	mux.HandleFunc("POST /api/editor/git/branch", handler.EditorGitCreateBranch)

	terminalRegistry := terminal.NewRegistry()
	// The entrypoint (cmd/server, cmd/desktop) only learns its own actual
	// bound loopback address after Build returns and it starts listening —
	// see BaseURLHolder's doc comment — so it's created empty here and set
	// once the caller knows it (via Built.TerminalBaseURL below).
	terminalBaseURL := terminal.NewBaseURLHolder()
	mux.HandleFunc("GET /api/terminal/ws", terminal.NewHandler(terminalRegistry, terminalGate, fileReader, terminalBaseURL))

	// No settings toggle for this one, unlike terminalGate above — LSP has
	// no destructive capability a user would ever want to keep off, so
	// "is gopls installed" is the only gate needed. When it's missing the
	// route simply isn't registered, rather than being registered and
	// 404ing at request time the way the terminal route does for its own
	// (live-toggleable) gate.
	var lspRegistry *lsp.Registry
	if lsp.Available() {
		lspRegistry = lsp.NewRegistry()
		mux.HandleFunc("GET /api/editor/lsp/ws", lsp.NewHandler(lspRegistry, fileReader))
		log.Printf("gopls found — /api/editor/lsp/ws will provide Go diagnostics, completion, hover, and go-to-definition")
	} else {
		log.Printf("lsp: gopls not found on PATH — Go language features (diagnostics, completion, hover, go-to-definition) disabled")
	}

	// OpenAI-compatible proxy an external tool (e.g. a terminal-based AI
	// CLI, run inside the built-in Terminal pane) can point its API base
	// URL at instead of the local model server directly — see
	// chat.Handler.ExternalChatCompletions's doc comment. GET /api/live/stream
	// is the SSE feed the Chat panel subscribes to so that traffic shows
	// up live instead of only after a reload.
	mux.HandleFunc("POST /v1/chat/completions", handler.ExternalChatCompletions)
	mux.HandleFunc("GET /v1/models", handler.ExternalModels)
	// Anthropic wire format (ANTHROPIC_BASE_URL) — what Claude Code itself
	// speaks, distinct from the OpenAI-shaped pair above.
	mux.HandleFunc("POST /v1/messages", handler.AnthropicMessages)
	mux.HandleFunc("GET /api/live/stream", handler.LiveStream)
	mux.HandleFunc("PUT /api/live/target", handler.SetLiveTarget)

	mux.Handle("/", http.FileServer(http.FS(web.FS())))

	return &Built{Mux: mux, DB: db, TerminalRegistry: terminalRegistry, LSPRegistry: lspRegistry, Handler: handler, TerminalBaseURL: terminalBaseURL}, nil
}
