// Package chat wires together the LLM client, vector store, and SQLite
// persistence behind HTTP handlers, including SSE streaming for chat.
package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/user"
	"strconv"
	"strings"

	"fastllm/internal/files"
	"fastllm/internal/llm"
	"fastllm/internal/store"
	"fastllm/internal/vector"
)

const defaultWorkspace = "default"

const systemPrompt = `You are a helpful assistant. Use the provided context to answer
the user's question when it's relevant. If the context doesn't contain the
answer, say so and answer from general knowledge instead.`

// readFileTool is the schema advertised to the model when file access is
// enabled. Read-only, sandboxed to Handler.Files.Root — see internal/files.
var readFileTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "read_file",
		Description: "Read the contents of a text file from the local project directory. Path is relative to the project root.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file, relative to the project root (e.g. \"README.md\" or \"src/main.go\").",
				},
			},
			"required": []string{"path"},
		},
	},
}

type Handler struct {
	DB     *sql.DB
	LLM    *llm.Client
	Vector *vector.Store
	Files  *files.Reader
}

func New(db *sql.DB, llmClient *llm.Client, vec *vector.Store, fileReader *files.Reader) *Handler {
	return &Handler{DB: db, LLM: llmClient, Vector: vec, Files: fileReader}
}

type chatRequest struct {
	Message        string `json:"message"`
	Model          string `json:"model"`
	SkillID        int64  `json:"skill_id"`
	ConversationID int64  `json:"conversation_id"`
}

// Chat streams the assistant's reply back to the client as Server-Sent
// Events, one small chunk of text per event, so the UI can render tokens
// as they arrive instead of waiting for the full response. If no
// conversation_id is given, a new conversation is created and its ID is
// sent back to the client as a "conversation" event before streaming
// starts, so the frontend can track which thread it's now in.
func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	convID := req.ConversationID
	isNewConversation := convID == 0
	if isNewConversation {
		var err error
		convID, err = store.CreateConversation(h.DB, defaultWorkspace, conversationTitle(req.Message))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := store.SaveMessage(h.DB, defaultWorkspace, convID, "user", req.Message); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	messages, sources := h.buildPrompt(ctx, convID, req.Message, req.SkillID)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if isNewConversation {
		payload, _ := json.Marshal(map[string]int64{"conversation_id": convID})
		fmt.Fprintf(w, "event: conversation\ndata: %s\n\n", payload)
		flusher.Flush()
	}

	if len(sources) > 0 {
		payload, _ := json.Marshal(map[string]any{"sources": sources})
		fmt.Fprintf(w, "event: sources\ndata: %s\n\n", payload)
		flusher.Flush()
	}

	if h.Files.Enabled() {
		fileReads := h.runFileTools(ctx, req.Model, &messages)
		for _, fr := range fileReads {
			payload, _ := json.Marshal(fr)
			fmt.Fprintf(w, "event: tool_call\ndata: %s\n\n", payload)
			flusher.Flush()
		}
	}

	var full strings.Builder
	err := h.LLM.StreamChat(ctx, req.Model, messages, func(token string) {
		full.WriteString(token)
		payload, _ := json.Marshal(map[string]string{"token": token})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}, func(reasoning string) {
		payload, _ := json.Marshal(map[string]string{"reasoning": reasoning})
		fmt.Fprintf(w, "event: reasoning\ndata: %s\n\n", payload)
		flusher.Flush()
	})
	if err != nil {
		payload, _ := json.Marshal(map[string]string{"error": err.Error()})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
		flusher.Flush()
		return
	}

	if full.Len() > 0 {
		_ = store.SaveMessage(h.DB, defaultWorkspace, convID, "assistant", full.String())
	}
	fmt.Fprintf(w, "event: done\ndata: {}\n\n")
	flusher.Flush()
}

// conversationTitle derives a short thread title from the first message,
// the same way ChatGPT/Claude do — truncated to a single line.
func conversationTitle(message string) string {
	title := strings.TrimSpace(strings.SplitN(message, "\n", 2)[0])
	const maxLen = 48
	runes := []rune(title)
	if len(runes) > maxLen {
		title = string(runes[:maxLen]) + "…"
	}
	if title == "" {
		title = "New conversation"
	}
	return title
}

// source is a chunk retrieved for a chat answer, reported to the client
// so the UI can show what informed the response.
type source struct {
	DocumentID int64  `json:"document_id"`
	Content    string `json:"content"`
}

// buildPrompt embeds the user's question, retrieves the most relevant
// chunks from the vector store, and assembles the full message list. It
// also returns the chunks that were retrieved, for display in the UI. If
// skillID is non-zero and resolves to a saved skill, that skill's prompt
// replaces the default system prompt.
func (h *Handler) buildPrompt(ctx context.Context, conversationID int64, question string, skillID int64) ([]llm.Message, []source) {
	prompt := systemPrompt
	if skillID != 0 {
		if s, err := store.GetSkill(h.DB, defaultWorkspace, skillID); err == nil {
			prompt = s.Prompt
		}
	}
	messages := []llm.Message{{Role: "system", Content: prompt}}
	var sources []source

	if h.LLM.EmbedModel != "" {
		if embedding, err := h.LLM.Embed(ctx, question); err == nil {
			topK := store.DefaultRAGSettings.TopK
			if rag, err := store.GetRAGSettings(h.DB); err == nil {
				topK = rag.TopK
			}
			chunks := h.Vector.Search(embedding, topK)
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

	history, err := store.LoadMessages(h.DB, defaultWorkspace, conversationID)
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

// fileRead reports one file the model read via the read_file tool, for
// display in the UI.
type fileRead struct {
	Path      string `json:"path"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
}

// runFileTools makes a single non-streaming pre-flight request with the
// read_file tool declared. If the model requests one or more reads, each
// is executed against h.Files (sandboxed to its configured root) and the
// results are appended to *messages as a tool round-trip (the assistant's
// tool-call message, followed by one tool-result message per call) so the
// subsequent streamed answer can see file contents. Models without tool
// support, or that choose not to call the tool, leave messages untouched.
// Errors reading a file (not found, outside sandbox, etc.) are reported
// back to the model as the tool result rather than failing the request,
// so it can tell the user what went wrong.
func (h *Handler) runFileTools(ctx context.Context, model string, messages *[]llm.Message) []fileRead {
	reply, err := h.LLM.Chat(ctx, model, *messages, []llm.Tool{readFileTool})
	if err != nil || len(reply.ToolCalls) == 0 {
		return nil
	}

	*messages = append(*messages, reply)

	var reads []fileRead
	for _, call := range reply.ToolCalls {
		if call.Function.Name != "read_file" {
			continue
		}
		var args struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(call.Function.Arguments), &args)

		var result string
		fr := fileRead{Path: args.Path}
		content, truncated, readErr := h.Files.Read(args.Path)
		if readErr != nil {
			fr.Error = readErr.Error()
			result = "Error reading file: " + readErr.Error()
		} else {
			fr.Truncated = truncated
			result = content
			if truncated {
				result += "\n\n[truncated]"
			}
		}
		reads = append(reads, fr)
		*messages = append(*messages, llm.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    result,
		})
	}
	return reads
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

type settingsResponse struct {
	Username   string `json:"username"`
	ChatModel  string `json:"chat_model"`
	EmbedModel string `json:"embed_model"`
	LLMBaseURL string `json:"llm_base_url"`
}

// Settings returns read-only info about the current machine/environment
// for display in the app's settings page: the OS account name and the
// LLM backend configuration the server was started with.
func (h *Handler) Settings(w http.ResponseWriter, r *http.Request) {
	username := "unknown"
	if u, err := user.Current(); err == nil {
		name := u.Username
		// Windows usernames come back as "DOMAIN\\user" or "MACHINE\\user" —
		// keep just the account name for display.
		if idx := strings.LastIndexAny(name, `\/`); idx != -1 {
			name = name[idx+1:]
		}
		username = name
	}
	writeJSON(w, settingsResponse{
		Username:   username,
		ChatModel:  h.LLM.ChatModel,
		EmbedModel: h.LLM.EmbedModel,
		LLMBaseURL: h.LLM.BaseURL,
	})
}

// GetRAGSettings returns the current chunking/retrieval settings.
func (h *Handler) GetRAGSettings(w http.ResponseWriter, r *http.Request) {
	rag, err := store.GetRAGSettings(h.DB)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, rag)
}

// UpdateRAGSettings saves new chunking/retrieval settings. Chunk size and
// overlap only affect documents indexed after the change; top_k applies
// to the very next chat request.
func (h *Handler) UpdateRAGSettings(w http.ResponseWriter, r *http.Request) {
	var rag store.RAGSettings
	if err := json.NewDecoder(r.Body).Decode(&rag); err != nil {
		http.Error(w, "invalid settings payload", http.StatusBadRequest)
		return
	}
	if err := store.SaveRAGSettings(h.DB, rag); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, rag)
}

// ClearConversations deletes every conversation and message in the
// workspace. Irreversible — the frontend confirms before calling this.
func (h *Handler) ClearConversations(w http.ResponseWriter, r *http.Request) {
	if err := store.ClearConversations(h.DB, defaultWorkspace); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ClearKnowledgeBase deletes every indexed document and chunk, in both
// SQLite and the in-memory vector index. Irreversible — the frontend
// confirms before calling this.
func (h *Handler) ClearKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	if err := store.ClearKnowledgeBase(h.DB, defaultWorkspace); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.Vector.Clear()
	w.WriteHeader(http.StatusNoContent)
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

type skillRequest struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

// ListSkills returns every saved skill (named system-prompt preset).
func (h *Handler) ListSkills(w http.ResponseWriter, r *http.Request) {
	skills, err := store.ListSkills(h.DB, defaultWorkspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, skills)
}

// CreateSkill saves a new named system-prompt preset.
func (h *Handler) CreateSkill(w http.ResponseWriter, r *http.Request) {
	var req skillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Prompt) == "" {
		http.Error(w, "name and prompt are required", http.StatusBadRequest)
		return
	}

	id, err := store.SaveSkill(h.DB, defaultWorkspace, req.Name, req.Prompt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"id": id})
}

// DeleteSkill removes a saved skill by ID.
func (h *Handler) DeleteSkill(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid skill id", http.StatusBadRequest)
		return
	}
	if err := store.DeleteSkill(h.DB, defaultWorkspace, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListMessages returns every message in one conversation, given by the
// required ?conversation_id= query parameter.
func (h *Handler) ListMessages(w http.ResponseWriter, r *http.Request) {
	convID, err := strconv.ParseInt(r.URL.Query().Get("conversation_id"), 10, 64)
	if err != nil {
		http.Error(w, "conversation_id is required", http.StatusBadRequest)
		return
	}
	msgs, err := store.LoadMessages(h.DB, defaultWorkspace, convID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, msgs)
}

// ListConversations returns every conversation thread, most recently
// active first.
func (h *Handler) ListConversations(w http.ResponseWriter, r *http.Request) {
	convs, err := store.ListConversations(h.DB, defaultWorkspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, convs)
}

// DeleteConversation removes a conversation and all its messages.
func (h *Handler) DeleteConversation(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid conversation id", http.StatusBadRequest)
		return
	}
	if err := store.DeleteConversation(h.DB, defaultWorkspace, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type uploadRequest struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

// UploadDocument chunks the provided text, embeds each chunk, and stores
// it both in SQLite and the in-memory vector index. Used for pasted text.
func (h *Handler) UploadDocument(w http.ResponseWriter, r *http.Request) {
	var req uploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Content) == "" {
		http.Error(w, "filename and content are required", http.StatusBadRequest)
		return
	}
	n, err := h.indexText(r.Context(), req.Filename, req.Content)
	if err != nil {
		writeIndexError(w, err)
		return
	}
	writeJSON(w, map[string]any{"chunks": n})
}

// maxUploadSize bounds a single uploaded file (post base64/multipart
// decoding) to guard against pathological memory use from a bad drop.
const maxUploadSize = 25 << 20 // 25MB

// UploadFile accepts one real file via multipart/form-data (field "file"),
// extracting text server-side for PDFs and indexing it the same way as
// pasted text. Used by the file picker and folder/drag-and-drop upload.
func (h *Handler) UploadFile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		http.Error(w, "file too large or malformed upload", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read upload", http.StatusInternalServerError)
		return
	}

	var text string
	if strings.HasSuffix(strings.ToLower(header.Filename), ".pdf") {
		text, err = extractPDFText(data)
		if err != nil {
			http.Error(w, fmt.Sprintf("could not read PDF: %v", err), http.StatusBadRequest)
			return
		}
	} else {
		text = string(data)
	}

	if strings.TrimSpace(text) == "" {
		http.Error(w, "file has no extractable text", http.StatusBadRequest)
		return
	}

	n, err := h.indexText(r.Context(), header.Filename, text)
	if err != nil {
		writeIndexError(w, err)
		return
	}
	writeJSON(w, map[string]any{"chunks": n})
}

// indexText saves a document row, chunks its text per the current RAG
// settings, embeds each chunk, and stores it in both SQLite and the
// in-memory vector index. Returns the number of chunks created.
func (h *Handler) indexText(ctx context.Context, filename, text string) (int, error) {
	rag, err := store.GetRAGSettings(h.DB)
	if err != nil {
		rag = store.DefaultRAGSettings
	}

	docID, err := store.SaveDocument(h.DB, defaultWorkspace, filename)
	if err != nil {
		return 0, err
	}

	chunks := chunkText(text, rag.ChunkSize, rag.ChunkOverlap)
	for _, chunkContent := range chunks {
		embedding, err := h.LLM.Embed(ctx, chunkContent)
		if err != nil {
			return 0, err
		}
		chunkID, err := store.SaveChunk(h.DB, docID, chunkContent, embedding)
		if err != nil {
			return 0, err
		}
		h.Vector.Add(vector.Chunk{ID: chunkID, DocumentID: docID, Content: chunkContent, Embedding: embedding})
	}
	return len(chunks), nil
}

func writeIndexError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
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
