// Package chat wires together the LLM client, vector store, and SQLite
// persistence behind HTTP handlers, including SSE streaming for chat.
package chat

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"fastllm/internal/buildcheck"
	"fastllm/internal/files"
	"fastllm/internal/folderpicker"
	"fastllm/internal/gitrepo"
	"fastllm/internal/llm"
	"fastllm/internal/store"
	"fastllm/internal/vector"
)

// maxToolRounds caps how many tool round-trips (propose write, run_build,
// see failure, propose a corrected write, ...) a single chat turn gets
// before whatever the model has produced is shown as-is. Bounds worst-case
// turn latency on local hardware to a handful of non-streaming Chat calls
// plus build time, rather than letting a model loop indefinitely.
const maxToolRounds = 4

const defaultWorkspace = "default"

const systemPrompt = `You are an expert programmer acting as a copilot-style code assistant. Default to succinct answers: lead with the fix or the direct answer, skip preamble and restating the question, and don't pad with obvious explanation. Use the provided context when it's relevant. If you don't know something or the context doesn't cover it, say so plainly instead of guessing — then either answer from general knowledge if that's good enough, or ask a targeted follow-up question to get what you need. Prefer a short code snippet or a one-line answer over a paragraph when either would do; expand only when the problem genuinely needs it.`

// listFilesTool is the schema advertised alongside read_file when file
// access is enabled, so the model can discover what's in the sandboxed
// project directory instead of only being able to act on paths the user
// already told it — see listFilesMaxEntries's doc comment for the size
// cap. Read-only, same sandboxed root as read_file/write_file.
var listFilesTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "list_files",
		Description: "List files in the local project directory, recursively. Use this to discover what files exist before reading one, or to explore a subdirectory. Returns paths relative to the project root.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Subdirectory to list, relative to the project root (e.g. \"src\" or \"src/utils\"). Omit or use \"\" to list the entire project.",
				},
			},
		},
	},
}

// searchFilesTool is the schema advertised alongside list_files/read_file
// when file access is enabled, so the model can locate content across the
// project without already knowing which file it's in or reading files one
// at a time to find it. Read-only, same sandboxed root as the other file
// tools — see searchFiles.
var searchFilesTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "search_files",
		Description: "Search file contents in the local project directory for a regular expression (Go RE2 syntax), across all files (respecting .gitignore in a git project). Returns matching lines as \"path:line: text\". Use this to find where something is defined or used before reading files individually.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regular expression (Go RE2 syntax) to search for, matched against each line's content.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Subdirectory to search within, relative to the project root. Omit or use \"\" to search the entire project.",
				},
			},
			"required": []string{"pattern"},
		},
	},
}

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
// held as a PendingWrite until a human approves it via the API. For an
// existing file, this call is rejected unless read_file was called on the
// same path earlier in this turn and the file hasn't changed since — see
// runFileTools's lastReadHash/checkWriteFreshness. This exists because a
// local model asked to rewrite a whole file from memory (rather than a
// fresh read) is exactly where truncation and silently-reverted-changes
// happen: it reconstructs the file from what it recalls, which may be
// stale, incomplete, or missing content it never actually saw. Forcing a
// fresh read immediately before every write means the model is always
// working from the real current content, not its own recollection of it.
var writeFileTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "write_file",
		Description: "Propose writing the full content of a file in the local project directory. This does not write immediately — a human must review and approve the change first. Path is relative to the project root. For an EXISTING file, you must call read_file on this exact path earlier in this turn first — a write to a file you haven't just read will be rejected. Prefer edit_file for a small change to a large existing file; use write_file for a brand-new file or when the whole file is being replaced.",
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

// editFileTool lets the model change a small part of an existing file
// without regenerating the whole thing — the search text must appear
// exactly once in the file's current content (ambiguous or missing search
// text is rejected with an explanation, not silently applied against the
// wrong location), and, same as write_file, requires a fresh read_file on
// this exact path earlier in this turn. Preferred over write_file for
// editing anything but a small file, since asking the model to reproduce
// only the changed lines (rather than the entire file back) sharply
// reduces how much text it has to regenerate correctly.
var editFileTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "edit_file",
		Description: "Propose a small change to an existing file by replacing one exact block of its current content with new content — without rewriting the whole file. This does not write immediately — a human must review and approve the change first. You must call read_file on this exact path earlier in this turn first. search must match the file's CURRENT content exactly (including whitespace/indentation) and appear exactly once — if it doesn't match, or matches more than once, the call is rejected and you should re-read the file and try again with a more precise (or more unique) search block. Prefer this over write_file whenever you're changing only part of a file.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the existing file, relative to the project root.",
				},
				"search": map[string]any{
					"type":        "string",
					"description": "The exact text to find in the file's current content — must match exactly once, including whitespace and indentation.",
				},
				"replace": map[string]any{
					"type":        "string",
					"description": "The text to replace it with.",
				},
			},
			"required": []string{"path", "search", "replace"},
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

// runTestTool lets the model run the project's existing test suite,
// optionally with its proposed (not yet approved) writes applied — unlike
// run_build, this is offered as soon as the project is a Go module, with
// or without a pending write, since running the existing suite as-is is
// still useful on its own (e.g. checking it's green before proposing a
// change at all). Runs against a throwaway copy of the project with any
// pending writes overlaid; never touches the real sandbox root (see
// internal/buildcheck).
var runTestTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "run_test",
		Description: "Run \"go test ./...\" against the project, with any of your proposed (not yet approved) file writes applied on top. Only available for Go projects. Returns the test output. Does not affect real files.",
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

	// conversationID is the conversation this write was proposed in — so
	// ApproveWrite/RejectWrite can record the outcome back into that same
	// conversation's history (see the comment on that in ApproveWrite).
	// Not exported for the same reason root isn't: internal bookkeeping,
	// not part of the JSON the frontend renders.
	conversationID int64

	// messageID is the assistant message row this write was persisted
	// alongside (see SaveAssistantMessage) — set once that save completes,
	// just after this PendingWrite is created, since the row doesn't exist
	// yet at construction time. ApproveWrite/RejectWrite use it to patch
	// this write's Status in place via store.SetMessagePendingWrites, so a
	// reloaded conversation shows the resolved outcome instead of forever
	// showing "pending" for a write nothing can act on anymore (the
	// in-memory PendingWrite itself doesn't survive a restart either way —
	// see the Handler.writes doc comment). Zero until that save happens;
	// never set at all for a write whose turn's assistant message ended up
	// empty (see the full.Len() > 0 guard around SaveAssistantMessage) —
	// SetMessagePendingWrites below is skipped in that case for the same
	// reason: there is no row to patch.
	messageID int64
}

type Handler struct {
	DB       *sql.DB
	LLM      *llm.Router
	Vector   *vector.Store
	Files    *files.Reader

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

	// Live fans out LiveEvents (see live.go) to every subscribed Chat
	// panel — populated by ExternalChatCompletions, the /v1/chat/completions
	// proxy an external tool (e.g. a terminal-based AI CLI) can be pointed
	// at instead of the local model server directly, so its conversation
	// shows up live in fastllm's own Chat UI.
	Live *LiveBroadcaster

	liveConvMu sync.Mutex
	liveConvID int64
	// liveTargetConvID is an explicit override set via SetLiveTarget (see
	// live.go) — normally the conversation currently open in the Chat
	// panel, kept in sync by the frontend on every selection change so
	// bridge traffic lands wherever the human is actually looking instead
	// of always the fixed "Live Terminal" conversation. 0 means no
	// override is active; liveConversationID falls back to liveConvID/the
	// auto-created "Live Terminal" conversation in that case.
	liveTargetConvID int64

	writesMu sync.Mutex
	writes   map[string]*PendingWrite
}

func New(db *sql.DB, llmRouter *llm.Router, vec *vector.Store, fileReader *files.Reader) *Handler {
	return &Handler{DB: db, LLM: llmRouter, Vector: vec, Files: fileReader, FolderChooser: folderpicker.Choose, Live: NewLiveBroadcaster(), writes: make(map[string]*PendingWrite)}
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
	// Images is data URIs pasted/attached in the composer — see
	// llm.Image's doc comment for why data URIs are carried unmodified
	// end to end rather than decoded server-side. Only meaningful for a
	// model llm.SupportsVisionForModel reports true for; the composer is
	// expected to only offer image attachment for such a model in the
	// first place. Nothing here re-validates that server-side — sending
	// an image to a model that doesn't support vision is left to fail (or
	// be silently ignored) however that provider's own API handles it,
	// same as this handler doesn't re-validate SupportsToolsForModel
	// before letting a request through with tools attached.
	Images []string `json:"images,omitempty"`
	// ActiveFile is the path (relative to the file-access root) of
	// whatever file is currently open in the Editor tab, if any — see
	// EditorView's onOpenPathChange in the frontend. Purely informational:
	// it tells the model what "this file"/"the current file" refers to in
	// the user's message, so it can read it via the file-read tool without
	// being told the path explicitly. Not re-validated here; an unreadable
	// or stale path just means the model's own file-read call fails same
	// as any other bad path would.
	ActiveFile string `json:"active_file,omitempty"`
}

// Chat streams the assistant's reply back to the client as Server-Sent
// Events, one small chunk of text per event, so the UI can render tokens
// as they arrive instead of waiting for the full response. If no
// conversation_id is given, a new conversation is created and its ID is
// sent back to the client as a "conversation" event before streaming
// starts, so the frontend can track which thread it's now in.
func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (strings.TrimSpace(req.Message) == "" && len(req.Images) == 0) {
		http.Error(w, "message or an image is required", http.StatusBadRequest)
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

	if _, err := store.SaveMessage(h.DB, defaultWorkspace, convID, "user", req.Message, req.Images); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	messages, sources := h.buildPrompt(ctx, convID, req.Message, req.SkillID, req.ActiveFile)

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
	// Hoisted out of the block below so it's still in scope where the
	// assistant message is saved further down — every write proposed this
	// turn gets persisted alongside that message (see SaveAssistantMessage)
	// so a PendingWriteCard survives a reload instead of only ever living
	// in this one response's SSE stream and the browser's in-memory state.
	var writes []*PendingWrite
	if h.Files.Enabled() && llm.SupportsToolsForModel(effectiveModel) {
		var reads []fileRead
		var buildChecks []buildCheckReport
		var testChecks []testCheckReport
		var budgetExhausted bool
		reads, writes, buildChecks, testChecks, budgetExhausted = h.runFileTools(ctx, req.Model, &messages, normalizeThinkLevel(req.ThinkLevel), convID)
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
		for _, tc := range testChecks {
			payload, _ := json.Marshal(tc)
			fmt.Fprintf(w, "event: test_check\ndata: %s\n\n", payload)
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
	}, func(usage llm.Usage) {
		payload, _ := json.Marshal(usage)
		fmt.Fprintf(w, "event: usage\ndata: %s\n\n", payload)
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
		writeRows := make([]store.PendingWriteRow, len(writes))
		for i, pw := range writes {
			writeRows[i] = store.PendingWriteRow{
				ID:              pw.ID,
				Path:            pw.Path,
				NewContent:      pw.NewContent,
				ExistingContent: pw.ExistingContent,
				FileExists:      pw.FileExists,
				Status:          "pending",
			}
		}
		if msgID, err := store.SaveAssistantMessage(h.DB, defaultWorkspace, convID, full.String(), writeRows); err == nil {
			// ApproveWrite/RejectWrite need this to patch the right row's
			// Status later — see PendingWrite.messageID's doc comment.
			for _, pw := range writes {
				pw.messageID = msgID
			}
		}
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
// replaces the default system prompt. activeFile, when non-empty, is the
// path currently open in the Editor tab (see chatRequest.ActiveFile) — it's
// appended as its own system message so the model knows what "this
// file"/"the current file" means without the user having to spell out the
// path, and can fetch it via the file-read tool.
func (h *Handler) buildPrompt(ctx context.Context, conversationID int64, question string, skillID int64, activeFile string) ([]llm.Message, []source) {
	prompt := systemPrompt
	if skillID != 0 {
		if s, err := store.GetSkill(h.DB, defaultWorkspace, skillID); err == nil {
			prompt = s.Prompt
		}
	}
	messages := []llm.Message{{Role: "system", Content: prompt}}
	if activeFile != "" {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: fmt.Sprintf("The user currently has this file open in their editor: %s\nIf their message refers to \"this file\", \"the current file\", or similar, they mean this path. Use the file-read tool to view its contents if you need them.", activeFile),
		})
	}
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
			msg := llm.Message{Role: m.Role, Content: m.Content}
			for _, dataURI := range m.Images {
				msg.Images = append(msg.Images, llm.Image{DataURI: dataURI})
			}
			messages = append(messages, msg)
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

// testCheckReport reports one run_test call the model made, for display
// in the UI — same shape as buildCheckReport, kept as a separate type
// since the two are independent tool calls with their own UI events
// (build_check vs. test_check) rather than interchangeable.
type testCheckReport struct {
	Passed bool   `json:"passed"`
	Output string `json:"output"`
}

// truncationRatio is how much shorter (as a fraction of current line
// count) a proposed write's content can be before it's treated as
// probable truncation rather than an intentional shrink — e.g. 0.5 means
// a write_file/edit_file result that's less than half the current file's
// line count gets rejected. Deliberately conservative (a real "delete
// most of this file" edit is rare, and the model can always explain
// itself and retry — see the rejection message) since the failure mode
// this guards against (a local model silently dropping the untouched
// tail of a file it was asked to regenerate) is exactly the kind of
// mistake that's invisible in a diff until someone notices the file is
// now broken.
const truncationRatio = 0.5

// truncationMinLines is the current-file line count below which the
// truncation check is skipped entirely — a genuinely small file (a
// handful of lines) can legitimately shrink by half or more from a
// completely ordinary edit (e.g. deleting a couple of now-redundant
// lines), so guarding against "suspiciously shorter" only makes sense
// once there's enough content for a large drop to be meaningful rather
// than noise.
const truncationMinLines = 20

// countLines counts lines the way a normal text file's line count is
// understood: a trailing "\n" ends the last line rather than starting a
// new (empty) one, so "a\nb\n" is 2 lines, not 3.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

// checkTruncation reports a rejection message (empty if fine) when
// newContent looks like it dropped most of currentContent rather than
// making a deliberate, described change — see truncationRatio's doc
// comment. This is a blunt, content-agnostic heuristic (line-count ratio
// only, no understanding of what changed) by design: it doesn't need to
// understand the code to notice "a 400-line file just became 40 lines,"
// which is exactly the class of failure (a local model truncating output
// it was supposed to reproduce in full) it exists to catch.
func checkTruncation(currentContent, newContent string) string {
	currentLines := countLines(currentContent)
	if currentLines < truncationMinLines {
		return ""
	}
	newLines := countLines(newContent)
	if float64(newLines) >= float64(currentLines)*truncationRatio {
		return ""
	}
	return fmt.Sprintf("Error: this write looks like truncation, not an intentional edit — the current file has %d lines and the proposed content has only %d. If you really mean to remove most of this file's content, explain that explicitly and try again; otherwise re-read the file and make sure your write includes everything you didn't intend to change.", currentLines, newLines)
}

// hashContent returns a short, stable fingerprint of file content, used
// only to detect "has this file changed since the model last read it" —
// not a security hash, just cheap drift detection.
func hashContent(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// checkWriteFreshness enforces that a write_file/edit_file call on an
// existing file is only accepted immediately after a read_file on that
// exact path in this same turn, and that the file hasn't changed on disk
// since that read — see writeFileTool's doc comment for why. lastRead
// holds the hash of what read_file actually returned to the model for
// each path (see runFileTools); currentContent is the file's real content
// right now, read fresh at write time. Returns a rejection message (empty
// if the write should proceed) — new-file writes (fileExists false) are
// exempt entirely, since there's nothing to have drifted from.
func checkWriteFreshness(lastRead map[string]string, path, currentContent string, fileExists bool) string {
	if !fileExists {
		return ""
	}
	readHash, ok := lastRead[path]
	if !ok {
		return fmt.Sprintf("Error: you must call read_file on %q earlier in this turn before writing to it — this ensures your change is based on the file's real current content, not a guess or a memory of it from earlier in the conversation.", path)
	}
	if readHash != hashContent(currentContent) {
		return fmt.Sprintf("Error: %q has changed since you read it (possibly from one of your own earlier writes this turn, or an external change) — call read_file on it again before writing, so your change is based on its actual current content.", path)
	}
	return ""
}

// runFileTools makes repeated non-streaming pre-flight requests (up to
// maxToolRounds) with the read_file (and, if enabled, write_file,
// edit_file, and run_build) tools declared, appending each round's
// tool-call and tool-result messages to *messages so the subsequent
// streamed answer can see the outcome. Models without tool support, or
// that choose not to call a tool, leave messages untouched after the
// first round.
//
// read_file is executed immediately (read-only, low risk). write_file and
// edit_file are never executed here — each is recorded as a PendingWrite
// awaiting human approval via the /api/writes endpoints, and the model is
// told exactly that in its tool result, so its final answer can honestly
// say the change is pending review rather than claiming it already
// happened. Both are gated by checkWriteFreshness (must have freshly
// read_file'd the same path this turn) and checkTruncation (the proposed
// content can't be suspiciously shorter than what's there now) — see
// those functions' doc comments for why: local models are prone to
// silently truncating or reconstructing-from-memory exactly the file
// content they should be copying forward unchanged, and both checks catch
// that mechanically rather than trusting the model not to do it. run_build
// lets the model check its own proposed writes compile before finishing —
// it runs against a throwaway copy of the project with this turn's
// pending writes overlaid (see internal/buildcheck), never the real
// sandbox, so it's safe to offer without a separate approval step.
func (h *Handler) runFileTools(ctx context.Context, model string, messages *[]llm.Message, thinkLevel string, conversationID int64) ([]fileRead, []*PendingWrite, []buildCheckReport, []testCheckReport, bool) {
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
	var testChecks []testCheckReport
	// pendingByPath tracks the latest proposed content per path this turn
	// (a later write_file/edit_file call for the same path supersedes an
	// earlier one), used to build the run_build overlay.
	pendingByPath := map[string]string{}
	// lastReadHash tracks, per path, the hash of what read_file actually
	// returned to the model this turn — see checkWriteFreshness. Persists
	// across rounds within one call to runFileTools (i.e. for the whole
	// turn), so "read in round 1, write in round 2" is allowed as long as
	// nothing changed the file in between.
	lastReadHash := map[string]string{}

	for round := 0; round < maxToolRounds; round++ {
		tools := []llm.Tool{readFileTool, listFilesTool, searchFilesTool}
		if writesEnabled {
			tools = append(tools, writeFileTool, editFileTool)
			if hasGoModule && len(pendingByPath) > 0 {
				tools = append(tools, runBuildTool)
			}
		}
		if hasGoModule {
			// Unlike run_build, offered regardless of pending writes or
			// writesEnabled — running the project's existing test suite
			// as-is (e.g. "is it currently green?") is useful on its own,
			// not just as a check on an unapproved change.
			tools = append(tools, runTestTool)
		}

		reply, err := h.LLM.Chat(ctx, model, *messages, tools, thinkLevel)
		if err != nil || len(reply.ToolCalls) == 0 {
			return reads, pending, buildChecks, testChecks, false
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
			case "list_files":
				var args struct {
					Path string `json:"path"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				results[call.ID] = h.listFiles(ctx, root, args.Path)

			case "search_files":
				var args struct {
					Pattern string `json:"pattern"`
					Path    string `json:"path"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				results[call.ID] = h.searchFiles(ctx, root, args.Pattern, args.Path)

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
					// Record what the model actually saw, keyed by path, so a
					// later write_file/edit_file this turn can be checked
					// against it — see checkWriteFreshness. A truncated read
					// deliberately does NOT count as fresh enough to write
					// from: the model never saw the whole file, so a
					// write/edit built on it risks dropping the unseen tail,
					// exactly the truncation failure mode these checks exist
					// to prevent.
					if !truncated {
						lastReadHash[args.Path] = hashContent(content)
					} else {
						delete(lastReadHash, args.Path)
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
					existing, exists, existingTruncated, _ := h.Files.ExistingContent(args.Path)
					if existingTruncated {
						// existing is missing everything past MaxReadBytes —
						// writing args.Content (the model's full reconstruction
						// of a file it can only ever see truncated) would
						// silently discard the real tail on disk once approved.
						// Neither checkWriteFreshness nor checkTruncation below
						// can catch this on their own (see their doc comments),
						// so it's rejected here explicitly rather than relying
						// on that interaction.
						result = fmt.Sprintf("Error: %q is too large (over %d bytes) to safely overwrite via write_file — the chat tools can only see and reproduce the first %d bytes, so writing the full file back risks silently dropping everything past that point. Use edit_file for a targeted change instead, or edit it directly.", args.Path, files.MaxReadBytes, files.MaxReadBytes)
					} else if msg := checkWriteFreshness(lastReadHash, args.Path, existing, exists); msg != "" {
						result = msg
					} else if msg := checkTruncation(existing, args.Content); msg != "" {
						result = msg
					} else {
						pw := &PendingWrite{
							ID:              newWriteID(),
							Path:            args.Path,
							NewContent:      args.Content,
							ExistingContent: existing,
							FileExists:      exists,
							root:            root,
							conversationID:  conversationID,
						}
						h.writesMu.Lock()
						h.writes[pw.ID] = pw
						h.writesMu.Unlock()
						pending = append(pending, pw)
						pendingByPath[args.Path] = args.Content
						// The file's on-disk content is about to change (once
						// approved) to args.Content — update the tracked hash
						// so a same-turn run_build/edit_file/write_file
						// sequence on this path continues to see it as fresh
						// without needing another read_file round-trip.
						lastReadHash[args.Path] = hashContent(args.Content)
						result = fmt.Sprintf("Change to %q proposed and awaiting human approval (id: %s). Do not tell the user it has been written yet — it is pending review. You can call run_build to check it compiles before finishing.", args.Path, pw.ID)
					}
				}
				results[call.ID] = result

			case "edit_file":
				var args struct {
					Path    string `json:"path"`
					Search  string `json:"search"`
					Replace string `json:"replace"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)

				var result string
				if !writesEnabled {
					result = "Error: file writes are not enabled."
				} else if _, err := h.Files.ResolveForWrite(args.Path); err != nil {
					result = "Error: " + err.Error()
				} else {
					existing, exists, existingTruncated, _ := h.Files.ExistingContent(args.Path)
					if existingTruncated {
						// Same reasoning as write_file's identical check above:
						// existing is missing everything past MaxReadBytes, and
						// the replace below would become the file's ENTIRE new
						// content on disk once approved — silently dropping
						// that unseen tail. edit_file's smaller footprint
						// (a search/replace, not the whole file) doesn't help
						// here since the result still overwrites the file
						// wholesale via Files.Write.
						result = fmt.Sprintf("Error: %q is too large (over %d bytes) to safely edit via edit_file — the chat tools can only see the first %d bytes, so any edit would silently drop everything past that point when written. Edit this file directly instead.", args.Path, files.MaxReadBytes, files.MaxReadBytes)
					} else if msg := checkWriteFreshness(lastReadHash, args.Path, existing, exists); msg != "" {
						result = msg
					} else if !exists {
						result = fmt.Sprintf("Error: %q doesn't exist yet — use write_file to create a new file.", args.Path)
					} else if n := strings.Count(existing, args.Search); n != 1 {
						if n == 0 {
							result = fmt.Sprintf("Error: search text not found in %q. It must match the file's current content exactly (including whitespace/indentation) — re-read the file and copy the exact text you want to replace.", args.Path)
						} else {
							result = fmt.Sprintf("Error: search text appears %d times in %q, but must match exactly once — make the search text longer/more specific so it uniquely identifies the block you want to change.", n, args.Path)
						}
					} else {
						newContent := strings.Replace(existing, args.Search, args.Replace, 1)
						if len(newContent) > files.MaxWriteBytes {
							result = fmt.Sprintf("Error: resulting file would be too large (%d bytes, max %d).", len(newContent), files.MaxWriteBytes)
						} else if msg := checkTruncation(existing, newContent); msg != "" {
							result = msg
						} else {
							pw := &PendingWrite{
								ID:              newWriteID(),
								Path:            args.Path,
								NewContent:      newContent,
								ExistingContent: existing,
								FileExists:      true,
								root:            root,
								conversationID:  conversationID,
							}
							h.writesMu.Lock()
							h.writes[pw.ID] = pw
							h.writesMu.Unlock()
							pending = append(pending, pw)
							pendingByPath[args.Path] = newContent
							lastReadHash[args.Path] = hashContent(newContent)
							result = fmt.Sprintf("Change to %q proposed and awaiting human approval (id: %s). Do not tell the user it has been written yet — it is pending review. You can call run_build to check it compiles before finishing.", args.Path, pw.ID)
						}
					}
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

		for _, call := range reply.ToolCalls {
			if call.Function.Name != "run_test" {
				continue
			}
			var overlays []buildcheck.Overlay
			for path, content := range pendingByPath {
				overlays = append(overlays, buildcheck.Overlay{Path: path, Content: content})
			}
			result, err := buildcheck.RunTests(ctx, root, overlays)
			var text string
			switch {
			case err != nil:
				text = "Error running tests: " + err.Error()
				testChecks = append(testChecks, testCheckReport{Passed: false, Output: text})
			case result.Passed:
				text = "Tests passed.\n\n" + result.Output
				testChecks = append(testChecks, testCheckReport{Passed: true, Output: result.Output})
			default:
				text = "Tests failed:\n\n" + result.Output
				testChecks = append(testChecks, testCheckReport{Passed: false, Output: result.Output})
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
	return reads, pending, buildChecks, testChecks, true
}

// listFilesMaxEntries caps how many paths listFiles hands back in one
// call — a large repo's full recursive listing could otherwise blow past
// what's reasonable to put in front of a model in one tool result (both
// context-window size and the model's own ability to usefully digest a
// multi-thousand-line file list). The model is told when the list was
// truncated so it can narrow with a subdirectory path instead of assuming
// it saw everything.
const listFilesMaxEntries = 500

// listFiles implements the list_files tool: the same git-aware listing
// (falling back to a plain walk for a non-git folder) that backs the
var walkIgnoredDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"dist":         true,
	"build":        true,
	"vendor":       true,
}

// walkFiles lists every file under root (relative paths, forward
// slashes) for the non-git-repo fallback, skipping common
// build/dependency directories that have no .gitignore to exclude them.
func walkFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if d.IsDir() {
			if walkIgnoredDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// listFiles implements the list_files tool: lists every file under the
// project root (relative paths, forward slashes).
func (h *Handler) listFiles(ctx context.Context, root, requestedPath string) string {
	all, err := gitrepo.ListFiles(ctx, root)
	if errors.Is(err, gitrepo.ErrNotARepo) {
		all, err = walkFiles(root)
	}
	if err != nil {
		return "Error listing files: " + err.Error()
	}

	prefix := path.Clean(strings.Trim(requestedPath, "/"))
	var matched []string
	for _, f := range all {
		if prefix == "" || prefix == "." || f == prefix || strings.HasPrefix(f, prefix+"/") {
			matched = append(matched, f)
		}
	}

	if len(matched) == 0 {
		if prefix == "" || prefix == "." {
			return "(no files found)"
		}
		return fmt.Sprintf("No files found under %q.", requestedPath)
	}

	truncated := len(matched) > listFilesMaxEntries
	if truncated {
		matched = matched[:listFilesMaxEntries]
	}
	result := strings.Join(matched, "\n")
	if truncated {
		result += fmt.Sprintf("\n\n[truncated to %d of more entries — narrow with a subdirectory path]", listFilesMaxEntries)
	}
	return result
}

// searchFilesMaxMatches caps how many matching lines searchFiles hands
// back in one call — same reasoning as listFilesMaxEntries: bounds the
// tool result size regardless of how common the pattern is in the repo.
const searchFilesMaxMatches = 200

// searchFilesMaxFileBytes caps how much of any single file searchFiles
// scans — large binary or generated files shouldn't stall a search or
// produce nonsense matches; source and doc files are comfortably under
// this.
const searchFilesMaxFileBytes = 1024 * 1024

// searchFiles implements the search_files tool: a regex grep over every
// file in the project (same git-aware file set as listFiles, so it
// respects .gitignore), returning matching lines as "path:line: text".
// requestedPath, if non-empty, restricts the search to that subdirectory,
// same semantics as listFiles's prefix filter. root has already been
// validated/snapshotted by the caller (runFileTools), same as listFiles.
func (h *Handler) searchFiles(ctx context.Context, root, pattern, requestedPath string) string {
	if strings.TrimSpace(pattern) == "" {
		return "Error: pattern must not be empty."
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "Error: invalid regular expression: " + err.Error()
	}

	all, err := gitrepo.ListFiles(ctx, root)
	if errors.Is(err, gitrepo.ErrNotARepo) {
		all, err = walkFiles(root)
	}
	if err != nil {
		return "Error listing files: " + err.Error()
	}

	prefix := path.Clean(strings.Trim(requestedPath, "/"))
	var matches []string
	truncated := false
scan:
	for _, f := range all {
		if prefix != "" && prefix != "." && f != prefix && !strings.HasPrefix(f, prefix+"/") {
			continue
		}
		info, err := os.Stat(filepath.Join(root, f))
		if err != nil || info.IsDir() || info.Size() > searchFilesMaxFileBytes {
			continue
		}
		file, err := os.Open(filepath.Join(root, f))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", f, lineNum, strings.TrimSpace(line)))
				if len(matches) >= searchFilesMaxMatches {
					truncated = true
					file.Close()
					break scan
				}
			}
		}
		file.Close()
	}

	if len(matches) == 0 {
		return fmt.Sprintf("No matches for %q.", pattern)
	}
	result := strings.Join(matches, "\n")
	if truncated {
		result += fmt.Sprintf("\n\n[truncated to %d matches — narrow the pattern or path]", searchFilesMaxMatches)
	}
	return result
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
	// The tool result the model saw when it proposed this write explicitly
	// told it "Do not tell the user it has been written yet — it is
	// pending review" (see write_file/edit_file's tool result above), and
	// that's the LAST thing about this write that ever enters the
	// conversation's persisted history — approval/rejection previously
	// updated nothing the model could see. So on the next turn the model
	// still only knows "proposed, pending," with no way to tell it was
	// actually approved and written — it has to be told again by the
	// human, or it hedges/re-proposes. Recording the outcome as a system
	// message here (replayed on the next turn via buildPrompt/LoadMessages,
	// same as any other history) closes that gap.
	saveWriteOutcomeMessage(h.DB, pw, fmt.Sprintf("The human approved and wrote the proposed change to %q (id: %s). It is now saved on disk exactly as proposed.", pw.Path, pw.ID))
	updateStoredWriteStatus(h.DB, pw, "approved")
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
	// See the comment in ApproveWrite above — same gap, opposite outcome:
	// without this, the model has no way to learn a proposal it made was
	// turned down, and might act as though it's still pending or silently
	// assume it went through.
	saveWriteOutcomeMessage(h.DB, pw, fmt.Sprintf("The human rejected the proposed change to %q (id: %s). The file was NOT changed — it still has its original content.", pw.Path, pw.ID))
	updateStoredWriteStatus(h.DB, pw, "rejected")
	w.WriteHeader(http.StatusNoContent)
}

// updateStoredWriteStatus patches this one write's persisted Status (see
// store.PendingWriteRow) from "pending" to "approved"/"rejected" so a
// reloaded conversation's PendingWriteCard shows the resolved outcome
// instead of forever showing "pending" for a write nothing can act on
// anymore. No-op if this write's assistant message was never actually
// persisted (see PendingWrite.messageID's doc comment) or if the DB
// round-trip fails — logged, not returned, for the same reason
// saveWriteOutcomeMessage's failures are: the write to disk (or the
// rejection) already succeeded, and the human already sees the outcome
// directly in the PendingWriteCard for this session regardless of whether
// it persists cleanly.
func updateStoredWriteStatus(db *sql.DB, pw *PendingWrite, status string) {
	if pw.messageID == 0 {
		return
	}
	msgs, err := store.LoadMessages(db, defaultWorkspace, pw.conversationID)
	if err != nil {
		log.Printf("updateStoredWriteStatus: load: %v", err)
		return
	}
	for _, m := range msgs {
		if m.ID != pw.messageID {
			continue
		}
		found := false
		for i := range m.PendingWrites {
			if m.PendingWrites[i].ID == pw.ID {
				m.PendingWrites[i].Status = status
				found = true
				break
			}
		}
		if !found {
			return
		}
		if err := store.SetMessagePendingWrites(db, m.ID, m.PendingWrites); err != nil {
			log.Printf("updateStoredWriteStatus: save: %v", err)
		}
		return
	}
}

// saveWriteOutcomeMessage records a pending write's resolution into its
// originating conversation as a system message, so the model's next turn
// (via buildPrompt/store.LoadMessages) sees what actually happened instead
// of only ever seeing its own "proposed, awaiting approval" reply. Errors
// are logged, not returned — the write to disk (or the rejection) already
// succeeded by the time this runs, and losing this one follow-up note
// shouldn't fail the whole request; the human already sees the outcome
// directly in the PendingWriteCard regardless.
func saveWriteOutcomeMessage(db *sql.DB, pw *PendingWrite, note string) {
	if pw.conversationID == 0 {
		return
	}
	if _, err := store.SaveMessage(db, defaultWorkspace, pw.conversationID, "system", note, nil); err != nil {
		log.Printf("saveWriteOutcomeMessage: %v", err)
	}
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

// normalizeThinkLevel validates a client-supplied thinking-effort level.
// The three accepted values are also hardcoded as <option>s in
// SettingsPanel.jsx's "Reasoning effort" select — if a level is ever
// added/removed/renamed here, that select needs the matching edit, since
// nothing currently derives one list from the other.
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
	NvidiaConfigured    bool `json:"nvidia_configured"`
	// CloudflareConfigured requires both the API token and account ID to
	// be set — a token with no account ID (or vice versa) can't build a
	// working client, see llm.Router.SetCloudProviders.
	CloudflareConfigured bool `json:"cloudflare_configured"`
}

func toCloudProviderSettingsResponse(s store.CloudProviderSettings) cloudProviderSettingsResponse {
	return cloudProviderSettingsResponse{
		AnthropicConfigured:  s.AnthropicAPIKey != "",
		OpenAIConfigured:     s.OpenAIAPIKey != "",
		GeminiConfigured:     s.GeminiAPIKey != "",
		NvidiaConfigured:     s.NvidiaAPIKey != "",
		CloudflareConfigured: s.CloudflareAPIKey != "" && s.CloudflareAccountID != "",
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
	AnthropicAPIKey     *string `json:"anthropic_api_key"`
	OpenAIAPIKey        *string `json:"openai_api_key"`
	GeminiAPIKey        *string `json:"gemini_api_key"`
	NvidiaAPIKey        *string `json:"nvidia_api_key"`
	CloudflareAPIKey    *string `json:"cloudflare_api_key"`
	CloudflareAccountID *string `json:"cloudflare_account_id"`
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
	if req.NvidiaAPIKey != nil {
		settings.NvidiaAPIKey = *req.NvidiaAPIKey
	}
	if req.CloudflareAPIKey != nil {
		settings.CloudflareAPIKey = *req.CloudflareAPIKey
	}
	if req.CloudflareAccountID != nil {
		settings.CloudflareAccountID = *req.CloudflareAccountID
	}

	if err := store.SaveCloudProviderSettings(h.DB, settings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.LLM.SetCloudProviders(llm.CloudProviderConfig{
		AnthropicAPIKey:     settings.AnthropicAPIKey,
		OpenAIAPIKey:        settings.OpenAIAPIKey,
		GeminiAPIKey:        settings.GeminiAPIKey,
		NvidiaAPIKey:        settings.NvidiaAPIKey,
		CloudflareAPIKey:    settings.CloudflareAPIKey,
		CloudflareAccountID: settings.CloudflareAccountID,
	})
	writeJSON(w, toCloudProviderSettingsResponse(settings))
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
