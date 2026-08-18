// Package chat wires together the LLM client, vector store, and SQLite
// persistence behind HTTP handlers, including SSE streaming for chat.
package chat

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"fastllm/internal/buildcheck"
	"fastllm/internal/files"
	"fastllm/internal/folderpicker"
	"fastllm/internal/llm"
	"fastllm/internal/store"
	"fastllm/internal/terminal"
	"fastllm/internal/vector"
)

// maxToolRounds caps how many tool round-trips (propose write, run_build,
// see failure, propose a corrected write, ...) a single chat turn gets
// before whatever the model has produced is shown as-is. Bounds worst-case
// turn latency on local hardware to a handful of non-streaming Chat calls
// plus build time, rather than letting a model loop indefinitely.
const maxToolRounds = 4

const defaultWorkspace = "default"

const systemPrompt = `You are a helpful assistant. Use the provided context to answer
the user's question when it's relevant. If the context doesn't contain the
answer, say so and answer from general knowledge instead.`

// readFileTool is the schema advertised to the model when file access is
// enabled. Read-only, sandboxed to Handler.Files's configured root — see
// internal/files.
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

// writeFileTool is the schema advertised to the model when file writes
// are enabled. Never executed directly from a tool call — every write is
// held as a PendingWrite until a human approves it via the API.
var writeFileTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "write_file",
		Description: "Propose writing content to a file in the local project directory. This does not write immediately — a human must review and approve the change first. Path is relative to the project root.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file, relative to the project root (e.g. \"notes.md\" or \"src/util.go\"). Created if it doesn't exist.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The full new content of the file.",
				},
			},
			"required": []string{"path", "content"},
		},
	},
}

// runBuildTool lets the model verify its own proposed (not yet approved)
// writes before finishing its turn. Only advertised once at least one
// write_file call has been made this turn — see runFileTools — since
// there's nothing to check otherwise. Runs against a throwaway copy of
// the project with pending writes overlaid; never touches the real
// sandbox root (see internal/buildcheck).
var runBuildTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "run_build",
		Description: "Run \"go build ./...\" against the project with your proposed (not yet approved) file writes applied, to check they compile before finishing. Only available for Go projects, and only after you've proposed at least one write_file change. Returns the build output. Does not affect real files.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
}

// PendingWrite is a model-proposed file write awaiting human approval.
// Held in memory only — never touches disk until Approve is called, and
// is discarded (not persisted) on server restart.
type PendingWrite struct {
	ID              string `json:"id"`
	Path            string `json:"path"`
	NewContent      string `json:"new_content"`
	ExistingContent string `json:"existing_content"`
	FileExists      bool   `json:"file_exists"`
	Resolved        bool   `json:"-"`

	// root is the sandbox root this write was validated and diffed
	// against when proposed. If the live file-access root has changed by
	// the time it's approved (via PUT /api/settings/files), applying it
	// against the new root could silently write somewhere the reviewer
	// never saw in the diff — ApproveWrite refuses the write instead. Not
	// exported: this is an internal consistency check, not part of the
	// API response the frontend renders.
	root string
}

type Handler struct {
	DB       *sql.DB
	LLM      *llm.Router
	Vector   *vector.Store
	Files    *files.Reader
	Terminal *terminal.Gate

	// FolderChooser shows the native "choose a folder" dialog used by
	// Settings → File access. Defaults to folderpicker.Choose (spawns a
	// hidden powershell.exe hosting a WinForms dialog) so cmd/server keeps
	// working exactly as before — that subprocess approach is the only
	// option available outside of a Wails window. cmd/desktop overrides
	// this with Wails' own runtime.OpenDirectoryDialog instead, since that
	// runs in-process, owned by the app's actual window, rather than
	// spawning a separate top-level window from a freshly started
	// PowerShell/CLR/WinForms process — which is what was causing the
	// window flashing/flicker reported when clicking "Choose folder".
	FolderChooser func(ctx context.Context) (string, error)

	writesMu sync.Mutex
	writes   map[string]*PendingWrite
}

func New(db *sql.DB, llmRouter *llm.Router, vec *vector.Store, fileReader *files.Reader, terminalGate *terminal.Gate) *Handler {
	return &Handler{DB: db, LLM: llmRouter, Vector: vec, Files: fileReader, Terminal: terminalGate, FolderChooser: folderpicker.Choose, writes: make(map[string]*PendingWrite)}
}

func newWriteID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type chatRequest struct {
	Message        string `json:"message"`
	Model          string `json:"model"`
	SkillID        int64  `json:"skill_id"`
	ConversationID int64  `json:"conversation_id"`
	ThinkLevel     string `json:"think_level"`
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

	effectiveModel := req.Model
	if effectiveModel == "" {
		effectiveModel = h.LLM.ChatModel()
	}
	if h.Files.Enabled() && llm.SupportsToolsForModel(effectiveModel) {
		reads, writes, buildChecks, budgetExhausted := h.runFileTools(ctx, req.Model, &messages, normalizeThinkLevel(req.ThinkLevel))
		for _, fr := range reads {
			payload, _ := json.Marshal(fr)
			fmt.Fprintf(w, "event: tool_call\ndata: %s\n\n", payload)
			flusher.Flush()
		}
		for _, pw := range writes {
			payload, _ := json.Marshal(pw)
			fmt.Fprintf(w, "event: pending_write\ndata: %s\n\n", payload)
			flusher.Flush()
		}
		for _, bc := range buildChecks {
			payload, _ := json.Marshal(bc)
			fmt.Fprintf(w, "event: build_check\ndata: %s\n\n", payload)
			flusher.Flush()
		}
		// The tool loop hit its round cap mid-work — nudge the model to
		// summarize what it did and where it left off instead of letting
		// the final streamed answer risk coming back empty (see
		// runFileTools). Appended as a system message, not shown to the
		// user directly, so it doesn't read like the model talking to
		// itself.
		if budgetExhausted {
			messages = append(messages, llm.Message{
				Role:    "system",
				Content: "You've used all your available tool calls for this turn. Stop calling tools now and reply to the user in plain text: summarize what you changed (if anything), whether it passed your last build check, and what — if anything — still needs to be done.",
			})
		}
	}

	var full strings.Builder
	err := h.LLM.StreamChat(ctx, req.Model, messages, normalizeThinkLevel(req.ThinkLevel), func(token string) {
		full.WriteString(token)
		payload, _ := json.Marshal(map[string]string{"token": token})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}, func(reasoning string) {
		payload, _ := json.Marshal(map[string]string{"reasoning": reasoning})
		fmt.Fprintf(w, "event: reasoning\ndata: %s\n\n", payload)
		flusher.Flush()
	})
	// A client-initiated stop (the Stop button) cancels ctx, which surfaces
	// here as a context-canceled error from StreamChat — that's an
	// intentional stop, not a failure, so still save whatever partial
	// answer was generated (same as a normal completion) instead of
	// discarding it. Writing an SSE event at this point is a harmless
	// no-op: the client already closed its end of the connection.
	if err != nil && ctx.Err() == nil {
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

	if h.LLM.EmbedModel() != "" {
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

// buildCheckReport reports one run_build call the model made against its
// own pending writes, for display in the UI.
type buildCheckReport struct {
	Passed bool   `json:"passed"`
	Output string `json:"output"`
}

// runFileTools makes repeated non-streaming pre-flight requests (up to
// maxToolRounds) with the read_file (and, if enabled, write_file and
// run_build) tools declared, appending each round's tool-call and
// tool-result messages to *messages so the subsequent streamed answer can
// see the outcome. Models without tool support, or that choose not to
// call a tool, leave messages untouched after the first round.
//
// read_file is executed immediately (read-only, low risk). write_file is
// never executed here — it's recorded as a PendingWrite awaiting human
// approval via the /api/writes endpoints, and the model is told exactly
// that in its tool result, so its final answer can honestly say the
// change is pending review rather than claiming it already happened.
// run_build lets the model check its own proposed writes compile before
// finishing — it runs against a throwaway copy of the project with this
// turn's pending writes overlaid (see internal/buildcheck), never the
// real sandbox, so it's safe to offer without a separate approval step.
func (h *Handler) runFileTools(ctx context.Context, model string, messages *[]llm.Message, thinkLevel string) ([]fileRead, []*PendingWrite, []buildCheckReport, bool) {
	// Snapshot once so every check below (which tools to advertise, whether
	// writes are allowed, which root a proposed write is validated/diffed
	// against) agrees with itself for this whole turn, even if a
	// concurrent PUT /api/settings/files changes config mid-flight.
	root, writesEnabled := h.Files.Snapshot()
	_, statErr := os.Stat(filepath.Join(root, "go.mod"))
	hasGoModule := statErr == nil

	var reads []fileRead
	var pending []*PendingWrite
	var buildChecks []buildCheckReport
	// pendingByPath tracks the latest proposed content per path this turn
	// (a later write_file call for the same path supersedes an earlier
	// one), used to build the run_build overlay.
	pendingByPath := map[string]string{}

	for round := 0; round < maxToolRounds; round++ {
		tools := []llm.Tool{readFileTool}
		if writesEnabled {
			tools = append(tools, writeFileTool)
			if hasGoModule && len(pendingByPath) > 0 {
				tools = append(tools, runBuildTool)
			}
		}

		reply, err := h.LLM.Chat(ctx, model, *messages, tools, thinkLevel)
		if err != nil || len(reply.ToolCalls) == 0 {
			return reads, pending, buildChecks, false
		}

		*messages = append(*messages, reply)

		// Two passes: write_file and read_file calls are resolved first
		// (in original order) so pendingByPath reflects every write this
		// round before any run_build call is evaluated, regardless of
		// which order the model listed the calls in — a model that
		// requests a fix and a verification in the same round must not
		// have the verification see stale, pre-fix content.
		results := make(map[string]string, len(reply.ToolCalls))
		for _, call := range reply.ToolCalls {
			switch call.Function.Name {
			case "read_file":
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
				results[call.ID] = result

			case "write_file":
				var args struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)

				var result string
				if !writesEnabled {
					result = "Error: file writes are not enabled."
				} else if len(args.Content) > files.MaxWriteBytes {
					result = fmt.Sprintf("Error: proposed content is too large (%d bytes, max %d).", len(args.Content), files.MaxWriteBytes)
				} else if _, err := h.Files.ResolveForWrite(args.Path); err != nil {
					result = "Error: " + err.Error()
				} else {
					existing, exists, _ := h.Files.ExistingContent(args.Path)
					pw := &PendingWrite{
						ID:              newWriteID(),
						Path:            args.Path,
						NewContent:      args.Content,
						ExistingContent: existing,
						FileExists:      exists,
						root:            root,
					}
					h.writesMu.Lock()
					h.writes[pw.ID] = pw
					h.writesMu.Unlock()
					pending = append(pending, pw)
					pendingByPath[args.Path] = args.Content
					result = fmt.Sprintf("Change to %q proposed and awaiting human approval (id: %s). Do not tell the user it has been written yet — it is pending review. You can call run_build to check it compiles before finishing.", args.Path, pw.ID)
				}
				results[call.ID] = result
			}
		}

		for _, call := range reply.ToolCalls {
			if call.Function.Name != "run_build" {
				continue
			}
			var overlays []buildcheck.Overlay
			for path, content := range pendingByPath {
				overlays = append(overlays, buildcheck.Overlay{Path: path, Content: content})
			}
			result, err := buildcheck.Run(ctx, root, overlays)
			var text string
			switch {
			case err != nil:
				text = "Error running build check: " + err.Error()
				buildChecks = append(buildChecks, buildCheckReport{Passed: false, Output: text})
			case result.Passed:
				text = "Build passed.\n\n" + result.Output
				buildChecks = append(buildChecks, buildCheckReport{Passed: true, Output: result.Output})
			default:
				text = "Build failed:\n\n" + result.Output
				buildChecks = append(buildChecks, buildCheckReport{Passed: false, Output: result.Output})
			}
			results[call.ID] = text
		}

		// Emit tool-result messages in the model's original call order —
		// required so each "tool" message's position corresponds to the
		// assistant message's tool_calls order that most backends expect.
		for _, call := range reply.ToolCalls {
			if result, ok := results[call.ID]; ok {
				*messages = append(*messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: result})
			}
		}
	}
	// The round budget ran out while the model was still actively calling
	// tools (as opposed to naturally stopping with a text reply) — the
	// message history now ends on unresolved tool results with no
	// assistant turn addressing them. Some models (observed with
	// gemma4:12b) produce an empty completion when asked to continue from
	// there without an explicit nudge, leaving the user with silence
	// after e.g. a failed build check. Report this so the caller can
	// inject a summarize-what-happened instruction before the final
	// streamed answer.
	return reads, pending, buildChecks, true
}

// takePendingWrite removes and returns a pending write by ID, or nil if
// it doesn't exist or was already resolved (approve/reject is one-shot).
func (h *Handler) takePendingWrite(id string) *PendingWrite {
	h.writesMu.Lock()
	defer h.writesMu.Unlock()
	pw, ok := h.writes[id]
	if !ok || pw.Resolved {
		return nil
	}
	pw.Resolved = true
	delete(h.writes, id)
	return pw
}

// ApproveWrite performs a pending model-proposed file write to disk. This
// is the only path in the whole file-write feature that actually touches
// disk — everything upstream (the tool call, runFileTools) only ever
// stages a PendingWrite in memory.
func (h *Handler) ApproveWrite(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pw := h.takePendingWrite(id)
	if pw == nil {
		http.Error(w, "no such pending write (already resolved or unknown id)", http.StatusNotFound)
		return
	}
	// The sandbox root may have changed (via PUT /api/settings/files)
	// since this write was proposed and diffed for review — approving it
	// against a different root than the one shown to the reviewer would
	// silently write somewhere they never saw. Refuse instead; the model
	// can re-propose the write against the new root if still wanted.
	if currentRoot, _ := h.Files.Snapshot(); currentRoot != pw.root {
		http.Error(w, "file access settings changed since this write was proposed — re-ask the model to make this change so it can be reviewed against the current settings", http.StatusConflict)
		return
	}
	if err := h.Files.Write(pw.Path, pw.NewContent); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"path": pw.Path, "written": true})
}

// RejectWrite discards a pending write without touching disk.
func (h *Handler) RejectWrite(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pw := h.takePendingWrite(id)
	if pw == nil {
		http.Error(w, "no such pending write (already resolved or unknown id)", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
		ChatModel:  h.LLM.ChatModel(),
		EmbedModel: h.LLM.EmbedModel(),
		LLMBaseURL: h.LLM.BaseURL(),
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

func normalizeThinkLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(level))
	default:
		return ""
	}
}

func (h *Handler) GetFileAccessSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := store.GetFileAccessSettings(h.DB)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, settings)
}

func (h *Handler) UpdateFileAccessSettings(w http.ResponseWriter, r *http.Request) {
	var settings store.FileAccessSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		http.Error(w, "invalid file access settings payload", http.StatusBadRequest)
		return
	}
	if err := store.SaveFileAccessSettings(h.DB, settings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.Files.SetConfig(settings.Root, settings.ReadEnabled, settings.WriteEnabled); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, settings)
}

// GetCloudProviderSettings returns whether each cloud provider has an API
// key configured, WITHOUT ever sending the keys themselves back to the
// browser — see cloudProviderSettingsResponse's doc comment.
func (h *Handler) GetCloudProviderSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := store.GetCloudProviderSettings(h.DB)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, toCloudProviderSettingsResponse(settings))
}

// cloudProviderSettingsResponse reports only whether a key is set, not
// its value — once saved, a key has no reason to ever round-trip back to
// the browser again (the Settings form shows a masked placeholder for an
// already-configured provider instead of the real key; see
// SettingsPanel.jsx). Saving still goes through the real
// store.CloudProviderSettings shape (see UpdateCloudProviderSettings).
type cloudProviderSettingsResponse struct {
	AnthropicConfigured bool `json:"anthropic_configured"`
	OpenAIConfigured    bool `json:"openai_configured"`
	GeminiConfigured    bool `json:"gemini_configured"`
}

func toCloudProviderSettingsResponse(s store.CloudProviderSettings) cloudProviderSettingsResponse {
	return cloudProviderSettingsResponse{
		AnthropicConfigured: s.AnthropicAPIKey != "",
		OpenAIConfigured:    s.OpenAIAPIKey != "",
		GeminiConfigured:    s.GeminiAPIKey != "",
	}
}

// cloudProviderSettingsRequest uses *string (rather than plain string, as
// the persisted store.CloudProviderSettings does) specifically so
// UpdateCloudProviderSettings can tell "field omitted" (nil — leave
// whatever key is already saved alone) apart from "field sent as an empty
// string" (non-nil, points at "" — explicitly clear that key). A plain
// string field can't distinguish those two cases, but the form needs to:
// GetCloudProviderSettings never sends real key values back to the
// browser (see that handler), so the only way an already-configured
// provider's key survives an unrelated field's edit is for "didn't send
// it" to mean "don't touch it" rather than "clear it".
type cloudProviderSettingsRequest struct {
	AnthropicAPIKey *string `json:"anthropic_api_key"`
	OpenAIAPIKey    *string `json:"openai_api_key"`
	GeminiAPIKey    *string `json:"gemini_api_key"`
}

// UpdateCloudProviderSettings merges the given fields into the persisted
// API keys (see cloudProviderSettingsRequest's doc comment for the
// omitted-vs-empty distinction that makes "merge" the right verb here)
// and immediately rebuilds h.LLM's cloud clients around the result, so a
// newly-entered key is usable for the very next chat message without a
// server restart — same save-then-apply-live pattern as
// UpdateFileAccessSettings/UpdateTerminalSettings.
func (h *Handler) UpdateCloudProviderSettings(w http.ResponseWriter, r *http.Request) {
	var req cloudProviderSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid cloud provider settings payload", http.StatusBadRequest)
		return
	}

	settings, err := store.GetCloudProviderSettings(h.DB)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if req.AnthropicAPIKey != nil {
		settings.AnthropicAPIKey = *req.AnthropicAPIKey
	}
	if req.OpenAIAPIKey != nil {
		settings.OpenAIAPIKey = *req.OpenAIAPIKey
	}
	if req.GeminiAPIKey != nil {
		settings.GeminiAPIKey = *req.GeminiAPIKey
	}

	if err := store.SaveCloudProviderSettings(h.DB, settings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.LLM.SetCloudProviders(llm.CloudProviderConfig{
		AnthropicAPIKey: settings.AnthropicAPIKey,
		OpenAIAPIKey:    settings.OpenAIAPIKey,
		GeminiAPIKey:    settings.GeminiAPIKey,
	})
	writeJSON(w, toCloudProviderSettingsResponse(settings))
}

func (h *Handler) GetTerminalSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := store.GetTerminalSettings(h.DB)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, settings)
}

func (h *Handler) UpdateTerminalSettings(w http.ResponseWriter, r *http.Request) {
	var settings store.TerminalSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		http.Error(w, "invalid terminal settings payload", http.StatusBadRequest)
		return
	}
	if err := store.SaveTerminalSettings(h.DB, settings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.Terminal.SetEnabled(settings.Enabled)
	writeJSON(w, settings)
}

// BrowseForFolder shows a native OS folder-picker dialog on the machine
// running the server and returns the chosen absolute path. This exists
// because a browser's own folder input can't expose a real filesystem
// path (see internal/folderpicker) — only meaningful for a locally-run
// server like this one, since the dialog appears on the server's own
// desktop session, not the browser's.
func (h *Handler) BrowseForFolder(w http.ResponseWriter, r *http.Request) {
	path, err := h.FolderChooser(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, folderpicker.ErrCancelled):
			writeJSON(w, map[string]any{"cancelled": true})
		case errors.Is(err, folderpicker.ErrUnsupported):
			http.Error(w, "folder picker is only available when running fastllm on Windows", http.StatusNotImplemented)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, map[string]any{"path": path})
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
//
// An optional "path" field carries the file's folder-relative path (e.g.
// "app/routes/home.tsx") for folder uploads, used as the stored filename
// in place of the bare basename — this can't just be the multipart
// filename field itself, since Go's mime/multipart deliberately runs that
// through filepath.Base() while parsing (path-traversal hardening), so
// any directory component in header.Filename is already gone by the time
// it reaches this handler. Validated against traversal here because,
// unlike header.Filename, this field isn't sanitized upstream.
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

	filename := header.Filename
	if p := strings.TrimSpace(r.FormValue("path")); p != "" {
		clean := path.Clean(p)
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		filename = clean
	}

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

	n, err := h.indexText(r.Context(), filename, text)
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
