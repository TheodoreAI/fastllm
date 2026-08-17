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
	handler := chat.New(db, llmClient, vecStore, fileReader)

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
	mux.HandleFunc("POST /api/settings/files/browse", handler.BrowseForFolder)
	mux.HandleFunc("DELETE /api/conversations", handler.ClearConversations)
	mux.HandleFunc("GET /api/skills", handler.ListSkills)
	mux.HandleFunc("POST /api/skills", handler.CreateSkill)
	mux.HandleFunc("DELETE /api/skills/{id}", handler.DeleteSkill)
	mux.HandleFunc("GET /api/conversations", handler.ListConversations)
	mux.HandleFunc("DELETE /api/conversations/{id}", handler.DeleteConversation)
	mux.HandleFunc("POST /api/writes/{id}/approve", handler.ApproveWrite)
	mux.HandleFunc("POST /api/writes/{id}/reject", handler.RejectWrite)

	server := &http.Server{Addr: addr, Handler: mux}
	mux.HandleFunc("POST /api/quit", quitHandler(server))

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
func quitHandler(server *http.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		go func() {
			time.Sleep(200 * time.Millisecond)
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
