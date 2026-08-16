// Package chat wires together the LLM client, vector store, and SQLite
// persistence behind HTTP handlers, including SSE streaming for chat.
package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"fastllm/internal/llm"
	"fastllm/internal/store"
	"fastllm/internal/vector"
)

const defaultWorkspace = "default"

const systemPrompt = `You are a helpful assistant. Use the provided context to answer
the user's question when it's relevant. If the context doesn't contain the
answer, say so and answer from general knowledge instead.`

type Handler struct {
	DB     *sql.DB
	LLM    *llm.Client
	Vector *vector.Store
}

func New(db *sql.DB, llmClient *llm.Client, vec *vector.Store) *Handler {
	return &Handler{DB: db, LLM: llmClient, Vector: vec}
}

type chatRequest struct {
	Message string `json:"message"`
	Model   string `json:"model"`
}

// Chat streams the assistant's reply back to the client as Server-Sent
// Events, one small chunk of text per event, so the UI can render tokens
// as they arrive instead of waiting for the full response.
func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	if err := store.SaveMessage(h.DB, defaultWorkspace, "user", req.Message); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	messages, sources := h.buildPrompt(ctx, req.Message)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if len(sources) > 0 {
		payload, _ := json.Marshal(map[string]any{"sources": sources})
		fmt.Fprintf(w, "event: sources\ndata: %s\n\n", payload)
		flusher.Flush()
	}

	var full strings.Builder
	err := h.LLM.StreamChat(ctx, req.Model, messages, func(token string) {
		full.WriteString(token)
		payload, _ := json.Marshal(map[string]string{"token": token})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	})
	if err != nil {
		payload, _ := json.Marshal(map[string]string{"error": err.Error()})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
		flusher.Flush()
		return
	}

	if full.Len() > 0 {
		_ = store.SaveMessage(h.DB, defaultWorkspace, "assistant", full.String())
	}
	fmt.Fprintf(w, "event: done\ndata: {}\n\n")
	flusher.Flush()
}

// source is a chunk retrieved for a chat answer, reported to the client
// so the UI can show what informed the response.
type source struct {
	DocumentID int64  `json:"document_id"`
	Content    string `json:"content"`
}

// buildPrompt embeds the user's question, retrieves the most relevant
// chunks from the vector store, and assembles the full message list. It
// also returns the chunks that were retrieved, for display in the UI.
func (h *Handler) buildPrompt(ctx context.Context, question string) ([]llm.Message, []source) {
	messages := []llm.Message{{Role: "system", Content: systemPrompt}}
	var sources []source

	if h.LLM.EmbedModel != "" {
		if embedding, err := h.LLM.Embed(ctx, question); err == nil {
			chunks := h.Vector.Search(embedding, 4)
			if len(chunks) > 0 {
				var b strings.Builder
				b.WriteString("Relevant context:\n\n")
				for _, c := range chunks {
					b.WriteString("- ")
					b.WriteString(c.Content)
					b.WriteString("\n")
					sources = append(sources, source{DocumentID: c.DocumentID, Content: c.Content})
				}
				messages = append(messages, llm.Message{Role: "system", Content: b.String()})
			}
		}
	}

	history, err := store.LoadMessages(h.DB, defaultWorkspace)
	if err == nil {
		// Keep the prompt bounded: last 20 messages plus the new one already saved.
		start := 0
		if len(history) > 20 {
			start = len(history) - 20
		}
		for _, m := range history[start:] {
			messages = append(messages, llm.Message{Role: m.Role, Content: m.Content})
		}
	} else {
		messages = append(messages, llm.Message{Role: "user", Content: question})
	}

	return messages, sources
}

// ListModels returns the chat-capable models available on the LLM backend.
func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.LLM.ListModels(r.Context())
	if err != nil {
		// Backend doesn't expose a model list (e.g. not Ollama) — not fatal,
		// the UI falls back to the configured default model.
		writeJSON(w, []llm.Model{})
		return
	}
	writeJSON(w, models)
}

// ListDocuments returns every indexed document with its chunk count.
func (h *Handler) ListDocuments(w http.ResponseWriter, r *http.Request) {
	docs, err := store.ListDocuments(h.DB, defaultWorkspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, docs)
}

func (h *Handler) ListMessages(w http.ResponseWriter, r *http.Request) {
	msgs, err := store.LoadMessages(h.DB, defaultWorkspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, msgs)
}

type uploadRequest struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

// UploadDocument chunks the provided text, embeds each chunk, and stores
// it both in SQLite and the in-memory vector index.
func (h *Handler) UploadDocument(w http.ResponseWriter, r *http.Request) {
	var req uploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Content) == "" {
		http.Error(w, "filename and content are required", http.StatusBadRequest)
		return
	}

	docID, err := store.SaveDocument(h.DB, defaultWorkspace, req.Filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	chunks := chunkText(req.Content, 800, 100)
	for _, text := range chunks {
		embedding, err := h.LLM.Embed(r.Context(), text)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		chunkID, err := store.SaveChunk(h.DB, docID, text, embedding)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.Vector.Add(vector.Chunk{ID: chunkID, DocumentID: docID, Content: text, Embedding: embedding})
	}

	writeJSON(w, map[string]any{"document_id": docID, "chunks": len(chunks)})
}

// chunkText splits text into overlapping windows of roughly `size` runes.
func chunkText(text string, size, overlap int) []string {
	runes := []rune(text)
	if len(runes) <= size {
		return []string{text}
	}
	var chunks []string
	for start := 0; start < len(runes); start += size - overlap {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
		if end == len(runes) {
			break
		}
	}
	return chunks
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
