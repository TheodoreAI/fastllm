// Package chat wires together the LLM client, files sandbox, and SQLite
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
	"io/fs"
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
	"fastllm/internal/webtools"
)

// maxToolRounds caps how many tool round-trips a single chat turn gets
// before whatever the model has produced is shown as-is.
const maxToolRounds = 4

const defaultWorkspace = "default"

const systemPrompt = `You are an expert programmer acting as a copilot-style code assistant. Default to succinct answers: lead with the fix or the direct answer, skip preamble and restating the question, and don't pad with obvious explanation. Use the provided context when it's relevant. If you don't know something or the context doesn't cover it, say so plainly instead of guessing — then either answer from general knowledge if that's good enough, or ask a targeted follow-up question to get what you need. Prefer a short code snippet or a one-line answer over a paragraph when either would do; expand only when the problem genuinely needs it.`

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

var writeFileTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "write_file",
		Description: "Write the full content of a file in the local project directory. Automatically creates parent directories if needed. Path is relative to the project root.",
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

var editFileTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "edit_file",
		Description: "Edit an existing file by replacing an exact block of its current content with new content. search must match the file's current content exactly.",
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

var runBuildTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "run_build",
		Description: "Run \"go build ./...\" against the project to check it compiles. Only available for Go projects.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
}

var runTestTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "run_test",
		Description: "Run \"go test ./...\" against the project. Only available for Go projects. Returns the test output.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
}

var webSearchTool = webtools.SearchTool
var webFetchTool = webtools.FetchTool

type Handler struct {
	DB            *sql.DB
	LLM           *llm.Router
	Files         *files.Reader
	FolderChooser func(ctx context.Context) (string, error)
	Live          *LiveBroadcaster

	liveConvMu       sync.Mutex
	liveConvID       int64
	liveTargetConvID int64
}

func New(db *sql.DB, llmRouter *llm.Router, fileReader *files.Reader) *Handler {
	return &Handler{
		DB:            db,
		LLM:           llmRouter,
		Files:         fileReader,
		FolderChooser: folderpicker.Choose,
		Live:          NewLiveBroadcaster(),
	}
}

func newWriteID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}


type chatRequest struct {
	Message        string   `json:"message"`
	Model          string   `json:"model"`
	ConversationID int64    `json:"conversation_id"`
	ThinkLevel     string   `json:"think_level"`
	Images         []string `json:"images,omitempty"`
	ActiveFile     string   `json:"active_file,omitempty"`
}

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
	messages := h.buildPrompt(ctx, convID, req.Message, req.ActiveFile)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if isNewConversation {
		payload, _ := json.Marshal(map[string]int64{"conversation_id": convID})
		fmt.Fprintf(w, "event: conversation\ndata: %s\n\n", payload)
		flusher.Flush()
	}

	effectiveModel := req.Model
	if effectiveModel == "" {
		effectiveModel = h.LLM.ChatModel()
	}

	if h.Files.Enabled() && llm.SupportsToolsForModel(effectiveModel) {
		reads, buildChecks, testChecks, budgetExhausted := h.runFileTools(ctx, req.Model, &messages, normalizeThinkLevel(req.ThinkLevel), convID)
		for _, fr := range reads {
			payload, _ := json.Marshal(fr)
			fmt.Fprintf(w, "event: tool_call\ndata: %s\n\n", payload)
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

	if err != nil && ctx.Err() == nil {
		payload, _ := json.Marshal(map[string]string{"error": err.Error()})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
		flusher.Flush()
		return
	}

	if full.Len() > 0 {
		_, _ = store.SaveMessage(h.DB, defaultWorkspace, convID, "assistant", full.String(), nil)
	}
	fmt.Fprintf(w, "event: done\ndata: {}\n\n")
	flusher.Flush()
}

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

func (h *Handler) buildPrompt(ctx context.Context, conversationID int64, question string, activeFile string) []llm.Message {
	messages := []llm.Message{{Role: "system", Content: systemPrompt}}
	if activeFile != "" {
		messages = append(messages, llm.Message{
			Role:    "system",
			Content: fmt.Sprintf("The user currently has this file open in their editor: %s\nIf their message refers to \"this file\", \"the current file\", or similar, they mean this path. Use the file-read tool to view its contents if you need them.", activeFile),
		})
	}

	history, err := store.LoadMessages(h.DB, defaultWorkspace, conversationID)
	if err == nil {
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

	return messages
}

type fileRead struct {
	Path      string `json:"path"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
}

type buildCheckReport struct {
	Passed bool   `json:"passed"`
	Output string `json:"output"`
}

type testCheckReport struct {
	Passed bool   `json:"passed"`
	Output string `json:"output"`
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

func hashContent(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func checkWriteFreshness(lastRead map[string]string, path, currentContent string, fileExists bool) string {
	if !fileExists {
		return ""
	}
	readHash, ok := lastRead[path]
	if !ok {
		return fmt.Sprintf("Error: you must call read_file on %q earlier in this turn before writing to it.", path)
	}
	if readHash != hashContent(currentContent) {
		return fmt.Sprintf("Error: %q has changed since you read it — call read_file on it again before writing.", path)
	}
	return ""
}

func (h *Handler) runFileTools(ctx context.Context, model string, messages *[]llm.Message, thinkLevel string, conversationID int64) ([]fileRead, []buildCheckReport, []testCheckReport, bool) {
	root, writesEnabled := h.Files.Snapshot()
	_, statErr := os.Stat(filepath.Join(root, "go.mod"))
	hasGoModule := statErr == nil

	var reads []fileRead
	var buildChecks []buildCheckReport
	var testChecks []testCheckReport
	lastReadHash := map[string]string{}

	for round := 0; round < maxToolRounds; round++ {
		tools := []llm.Tool{readFileTool, listFilesTool, searchFilesTool, webSearchTool, webFetchTool}
		if writesEnabled {
			tools = append(tools, writeFileTool, editFileTool)
			if hasGoModule {
				tools = append(tools, runBuildTool)
			}
		}
		if hasGoModule {
			tools = append(tools, runTestTool)
		}

		reply, err := h.LLM.Chat(ctx, model, *messages, tools, thinkLevel)
		if err != nil || len(reply.ToolCalls) == 0 {
			return reads, buildChecks, testChecks, false
		}

		*messages = append(*messages, reply)

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
						result = fmt.Sprintf("Error: %q is too large to safely overwrite via write_file.", args.Path)
					} else if msg := checkWriteFreshness(lastReadHash, args.Path, existing, exists); msg != "" {
						result = msg
					} else {
						if err := h.Files.Write(args.Path, args.Content); err != nil {
							result = "Error writing file: " + err.Error()
						} else {
							lastReadHash[args.Path] = hashContent(args.Content)
							result = fmt.Sprintf("Successfully wrote %d bytes to %s.", len(args.Content), args.Path)
						}
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
						result = fmt.Sprintf("Error: %q is too large to safely edit via edit_file.", args.Path)
					} else if !exists {
						result = fmt.Sprintf("Error: %q doesn't exist yet — use write_file to create a new file.", args.Path)
					} else if msg := checkWriteFreshness(lastReadHash, args.Path, existing, exists); msg != "" {
						result = msg
					} else if n := strings.Count(existing, args.Search); n != 1 {
						if n == 0 {
							result = fmt.Sprintf("Error: search text not found in %q.", args.Path)
						} else {
							result = fmt.Sprintf("Error: search text appears %d times in %q, but must match exactly once.", n, args.Path)
						}
					} else {
						newContent := strings.Replace(existing, args.Search, args.Replace, 1)
						if len(newContent) > files.MaxWriteBytes {
							result = fmt.Sprintf("Error: resulting file would be too large (%d bytes, max %d).", len(newContent), files.MaxWriteBytes)
						} else if err := h.Files.Write(args.Path, newContent); err != nil {
							result = "Error editing file: " + err.Error()
						} else {
							lastReadHash[args.Path] = hashContent(newContent)
							result = fmt.Sprintf("Successfully edited %s.", args.Path)
						}
					}
				}
				results[call.ID] = result

			case "run_build":
				result, err := buildcheck.Run(ctx, root, nil)
				var text string
				switch {
				case err != nil:
					text = "Error running build: " + err.Error()
					buildChecks = append(buildChecks, buildCheckReport{Passed: false, Output: text})
				case result.Passed:
					text = "Build succeeded."
					buildChecks = append(buildChecks, buildCheckReport{Passed: true, Output: result.Output})
				default:
					text = "Build failed:\n\n" + result.Output
					buildChecks = append(buildChecks, buildCheckReport{Passed: false, Output: result.Output})
				}
				results[call.ID] = text

			case "run_test":
				result, err := buildcheck.RunTests(ctx, root, nil)
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

			case "web_search":
				var args struct {
					Query      string `json:"query"`
					MaxResults int    `json:"max_results"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				results[call.ID] = webtools.SearchFormatted(ctx, args.Query, args.MaxResults)

			case "web_fetch":
				var args struct {
					URL      string `json:"url"`
					MaxBytes int    `json:"max_bytes"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				content, err := webtools.Fetch(ctx, args.URL, args.MaxBytes)
				if err != nil {
					results[call.ID] = fmt.Sprintf("Error fetching %s: %v", args.URL, err)
				} else {
					results[call.ID] = content
				}
			}
		}

		for _, call := range reply.ToolCalls {
			if result, ok := results[call.ID]; ok {
				*messages = append(*messages, llm.Message{Role: "tool", ToolCallID: call.ID, Content: result})
			}
		}
	}
	return reads, buildChecks, testChecks, true
}

const listFilesMaxEntries = 500

var walkIgnoredDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"dist":         true,
	"build":        true,
	"vendor":       true,
}

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
		result += fmt.Sprintf("\n\n[truncated — showing first %d files; specify a path to narrow]", listFilesMaxEntries)
	}
	return result
}

const (
	searchFilesMaxMatches   = 100
	searchFilesMaxFileBytes = 512 * 1024
)

func (h *Handler) searchFiles(ctx context.Context, root, pattern, requestedPath string) string {
	if strings.TrimSpace(pattern) == "" {
		return "Error: pattern is required."
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
		return "Error searching files: " + err.Error()
	}

	prefix := path.Clean(strings.Trim(requestedPath, "/"))
	var matches []string

scan:
	for _, rel := range all {
		if prefix != "" && prefix != "." && rel != prefix && !strings.HasPrefix(rel, prefix+"/") {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(full)
		if err != nil || info.IsDir() || info.Size() > searchFilesMaxFileBytes {
			continue
		}
		f, err := os.Open(full)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", rel, lineNum, strings.TrimSpace(line)))
				if len(matches) >= searchFilesMaxMatches {
					f.Close()
					break scan
				}
			}
		}
		f.Close()
	}

	if len(matches) == 0 {
		if prefix == "" || prefix == "." {
			return "(no matches found)"
		}
		return fmt.Sprintf("No matches found under %q.", requestedPath)
	}

	truncated := len(matches) >= searchFilesMaxMatches
	result := strings.Join(matches, "\n")
	if truncated {
		result += fmt.Sprintf("\n\n[truncated at %d matches — refine your pattern or use path to narrow]", searchFilesMaxMatches)
	}
	return result
}

func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.LLM.ListModels(r.Context())
	if err != nil {
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

func (h *Handler) Settings(w http.ResponseWriter, r *http.Request) {
	username := "unknown"
	if u, err := user.Current(); err == nil {
		name := u.Username
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

func (h *Handler) GetCloudProviderSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := store.GetCloudProviderSettings(h.DB)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, toCloudProviderSettingsResponse(settings))
}

type cloudProviderSettingsResponse struct {
	AnthropicConfigured  bool   `json:"anthropic_configured"`
	OpenAIConfigured     bool   `json:"openai_configured"`
	GeminiConfigured     bool   `json:"gemini_configured"`
	NvidiaConfigured     bool   `json:"nvidia_configured"`
	CloudflareConfigured bool   `json:"cloudflare_configured"`
	OSUConfigured        bool   `json:"osu_configured"`
	OSUBaseURL           string `json:"osu_base_url,omitempty"`
}

func toCloudProviderSettingsResponse(s store.CloudProviderSettings) cloudProviderSettingsResponse {
	osuConfigured := s.OSUAPIKey != "" || s.OSUBaseURL != ""
	if !osuConfigured {
		if home, err := os.UserHomeDir(); err == nil {
			if _, err := os.Stat(filepath.Join(home, ".osu-llm", "vllm-api-key")); err == nil {
				osuConfigured = true
			}
		}
	}
	baseURL := s.OSUBaseURL
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8010/v1"
	}
	return cloudProviderSettingsResponse{
		AnthropicConfigured:  s.AnthropicAPIKey != "",
		OpenAIConfigured:     s.OpenAIAPIKey != "",
		GeminiConfigured:     s.GeminiAPIKey != "",
		NvidiaConfigured:     s.NvidiaAPIKey != "",
		CloudflareConfigured: s.CloudflareAPIKey != "" && s.CloudflareAccountID != "",
		OSUConfigured:        osuConfigured,
		OSUBaseURL:           baseURL,
	}
}

func (h *Handler) UpdateCloudProviderSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AnthropicAPIKey     *string `json:"anthropic_api_key"`
		OpenAIAPIKey        *string `json:"openai_api_key"`
		GeminiAPIKey        *string `json:"gemini_api_key"`
		NvidiaAPIKey        *string `json:"nvidia_api_key"`
		CloudflareAPIKey    *string `json:"cloudflare_api_key"`
		CloudflareAccountID *string `json:"cloudflare_account_id"`
		OSUAPIKey           *string `json:"osu_api_key"`
		OSUBaseURL          *string `json:"osu_base_url"`
	}
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
	if req.OSUAPIKey != nil {
		settings.OSUAPIKey = *req.OSUAPIKey
	}
	if req.OSUBaseURL != nil {
		settings.OSUBaseURL = *req.OSUBaseURL
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
		OSUBaseURL:          settings.OSUBaseURL,
		OSUAPIKey:           settings.OSUAPIKey,
	})
	writeJSON(w, toCloudProviderSettingsResponse(settings))
}

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

func (h *Handler) ClearConversations(w http.ResponseWriter, r *http.Request) {
	if err := store.ClearConversations(h.DB, defaultWorkspace); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

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

func (h *Handler) ListConversations(w http.ResponseWriter, r *http.Request) {
	convs, err := store.ListConversations(h.DB, defaultWorkspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, convs)
}

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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
