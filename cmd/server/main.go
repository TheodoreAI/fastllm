// Command server runs the fastllm backend: a chat API backed by an
// OpenAI-compatible LLM, SQLite persistence, and an in-memory vector
// index for retrieval-augmented answers. It also serves the built
// React frontend from web/dist when present.
package main

import (
	"log"
	"net/http"
	"os"

	"fastllm/internal/chat"
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
	handler := chat.New(db, llmClient, vecStore)

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
	mux.HandleFunc("DELETE /api/conversations", handler.ClearConversations)
	mux.HandleFunc("GET /api/skills", handler.ListSkills)
	mux.HandleFunc("POST /api/skills", handler.CreateSkill)
	mux.HandleFunc("DELETE /api/skills/{id}", handler.DeleteSkill)
	mux.HandleFunc("GET /api/conversations", handler.ListConversations)
	mux.HandleFunc("DELETE /api/conversations/{id}", handler.DeleteConversation)

	serveFrontend(mux)

	log.Printf("fastllm listening on %s (llm backend: %s)", addr, baseURL)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func serveFrontend(mux *http.ServeMux) {
	mux.Handle("/", http.FileServer(http.FS(web.FS())))
}
