// Command server runs the fastllm backend: a chat API backed by an
// OpenAI-compatible LLM, SQLite persistence, and an in-memory vector
// index for retrieval-augmented answers. It also serves the built
// React frontend from web/dist when present.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"fastllm/internal/chat"
	"fastllm/internal/files"
	"fastllm/internal/llm"
	"fastllm/internal/store"
	"fastllm/internal/terminal"
	"fastllm/internal/vector"
	"fastllm/web"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	dbPath := getenv("FASTLLM_DB", "fastllm.db")
	baseURL := getenv("LLM_BASE_URL", "http://localhost:11434/v1") // Ollama's OpenAI-compatible endpoint
	apiKey := getenv("LLM_API_KEY", "")
	chatModel := getenv("LLM_CHAT_MODEL", "llama3.1")
	embedModel := getenv("LLM_EMBED_MODEL", "nomic-embed-text")
	addr := getenv("FASTLLM_ADDR", ":8080")
	filesRoot := getenv("FASTLLM_FILES_ROOT", "")         // empty = file-read tool disabled
	filesWrite := getenv("FASTLLM_FILES_WRITE", "") != "" // also requires FASTLLM_FILES_ROOT; every write needs manual approval regardless
	terminalEnabled := getenv("FASTLLM_TERMINAL_ENABLED", "") != ""

	db, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	vecStore := vector.NewStore()
	chunks, err := store.LoadAllChunks(db)
	if err != nil {
		log.Fatalf("load chunks: %v", err)
	}
	vecStore.Load(chunks)
	log.Printf("loaded %d chunks into vector store", len(chunks))

	llmClient := llm.New(baseURL, apiKey, chatModel, embedModel)
	if err := store.SeedFileAccessSettingsFromEnv(db, filesRoot, filesWrite); err != nil {
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

	if err := store.SeedTerminalSettingsFromEnv(db, terminalEnabled); err != nil {
		log.Printf("seed terminal settings from env: %v", err)
	}
	terminalSettings, err := store.GetTerminalSettings(db)
	if err != nil {
		log.Printf("load terminal settings: %v", err)
		terminalSettings = store.DefaultTerminalSettings
	}
	terminalGate := terminal.NewGate(terminalSettings.Enabled)
	if terminalGate.Enabled() {
		log.Printf("terminal enabled — /api/terminal/ws will spawn an interactive PowerShell session for any loopback connection")
	}

	handler := chat.New(db, llmClient, vecStore, fileReader, terminalGate)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", handler.Chat)
	mux.HandleFunc("GET /api/messages", handler.ListMessages)
	mux.HandleFunc("POST /api/documents", handler.UploadDocument)
	mux.HandleFunc("POST /api/documents/upload", handler.UploadFile)
	mux.HandleFunc("GET /api/documents", handler.ListDocuments)
	mux.HandleFunc("DELETE /api/documents", handler.ClearKnowledgeBase)
	mux.HandleFunc("GET /api/models", handler.ListModels)
	mux.HandleFunc("GET /api/settings", handler.Settings)
	mux.HandleFunc("GET /api/settings/rag", handler.GetRAGSettings)
	mux.HandleFunc("PUT /api/settings/rag", handler.UpdateRAGSettings)
	mux.HandleFunc("GET /api/settings/files", handler.GetFileAccessSettings)
	mux.HandleFunc("PUT /api/settings/files", handler.UpdateFileAccessSettings)
	mux.HandleFunc("GET /api/settings/terminal", handler.GetTerminalSettings)
	mux.HandleFunc("PUT /api/settings/terminal", handler.UpdateTerminalSettings)
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
	mux.HandleFunc("POST /api/editor/file/rename", handler.EditorRenameFile)
	mux.HandleFunc("GET /api/editor/search", handler.EditorSearch)
	mux.HandleFunc("GET /api/editor/git/status", handler.EditorGitStatus)
	mux.HandleFunc("GET /api/editor/git/diff", handler.EditorGitDiff)
	mux.HandleFunc("POST /api/editor/git/stage", handler.EditorGitStage)
	mux.HandleFunc("POST /api/editor/git/unstage", handler.EditorGitUnstage)
	mux.HandleFunc("POST /api/editor/git/commit", handler.EditorGitCommit)
	mux.HandleFunc("POST /api/editor/git/push", handler.EditorGitPush)
	mux.HandleFunc("GET /api/editor/git/branches", handler.EditorGitBranches)
	mux.HandleFunc("POST /api/editor/git/switch", handler.EditorGitSwitchBranch)
	mux.HandleFunc("POST /api/editor/git/branch", handler.EditorGitCreateBranch)

	terminalRegistry := terminal.NewRegistry()
	mux.HandleFunc("GET /api/terminal/ws", terminal.NewHandler(terminalRegistry, terminalGate, fileReader))

	server := &http.Server{Addr: addr, Handler: mux}
	mux.HandleFunc("POST /api/quit", quitHandler(server, terminalRegistry))

	serveFrontend(mux)

	log.Printf("fastllm listening on %s (llm backend: %s)", addr, baseURL)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// quitHandler lets the frontend request a clean shutdown (used by the
// titlebar Quit button) — this is a locally-run desktop-style app with no
// remote exposure, so no auth is needed beyond it already listening on
// localhost. Responds first, then shuts down from a goroutine so the
// response actually reaches the browser before the process exits.
func quitHandler(server *http.Server, terminalRegistry *terminal.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		go func() {
			time.Sleep(200 * time.Millisecond)
			terminalRegistry.CloseAll()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(ctx)
			os.Exit(0)
		}()
	}
}

func serveFrontend(mux *http.ServeMux) {
	mux.Handle("/", http.FileServer(http.FS(web.FS())))
}
