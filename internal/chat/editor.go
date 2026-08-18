// Text editor endpoints: file tree, open/save, cross-file search, and a
// git panel (status/diff/stage/unstage/commit). These operate directly
// on disk — unlike write_file (the chat model's tool), there's no
// PendingWrite approval step here, since a human typing into the editor
// and clicking Save is already the human-in-the-loop step. Everything
// still goes through h.Files' sandboxed root, so the editor can never
// reach outside the same directory the model's file tools are confined
// to (see internal/files).
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fastllm/internal/gitrepo"
	"fastllm/internal/lint"
	"fastllm/internal/store"
	"fastllm/internal/llm"
)

// editorTreeEntry is one file in the editor's file tree, relative to the
// sandbox root.
type editorTreeEntry struct {
	Path string `json:"path"`
}

// editorTreeIgnoredDirs are skipped when walking a non-git folder, since
// there's no .gitignore to rely on there — mirrors the most common
// build/dependency output directories so opening an arbitrary folder
// doesn't dump thousands of irrelevant entries into the tree.
var editorTreeIgnoredDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"dist":         true,
	"build":        true,
	"vendor":       true,
}

// EditorTree lists every file the editor's tree/search should show. If
// the sandbox root is a git repository, this is tracked and
// untracked-but-not-ignored files (see gitrepo.ListFiles) so build
// output and .git internals are excluded for free. Otherwise it falls
// back to a plain filesystem walk, so opening a folder that isn't a git
// repo still populates the tree — search and the git panel still
// require a real repo, since they have no non-git equivalent.
func (h *Handler) EditorTree(w http.ResponseWriter, r *http.Request) {
	root, ok := h.editorRoot(w)
	if !ok {
		return
	}
	files, err := gitrepo.ListFiles(r.Context(), root)
	if errors.Is(err, gitrepo.ErrNotARepo) {
		files, err = walkFiles(root)
	}
	if err != nil {
		h.writeGitError(w, err)
		return
	}
	entries := make([]editorTreeEntry, len(files))
	for i, f := range files {
		entries[i] = editorTreeEntry{Path: f}
	}
	writeJSON(w, entries)
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
			if editorTreeIgnoredDirs[d.Name()] {
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

// EditorReadFile returns one file's full content for the editor to open.
func (h *Handler) EditorReadFile(w http.ResponseWriter, r *http.Request) {
	if !h.Files.Enabled() {
		http.Error(w, "file access is not enabled", http.StatusForbidden)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	content, truncated, err := h.Files.ReadFull(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"path": path, "content": content, "truncated": truncated})
}

type editorSaveRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// EditorSaveFile writes a file's full content directly to disk — the
// human editing and clicking Save is the approval step, unlike the
// model's write_file which always goes through PendingWrite.
func (h *Handler) EditorSaveFile(w http.ResponseWriter, r *http.Request) {
	if !h.Files.WritesEnabled() {
		http.Error(w, "file writes are not enabled", http.StatusForbidden)
		return
	}
	var req editorSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		http.Error(w, "path and content are required", http.StatusBadRequest)
		return
	}
	if err := h.Files.Write(req.Path, req.Content); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Best-effort: lint the file we just saved so the editor can show
	// inline diagnostics. Only runs for JS/JSX-family files, and only if
	// the target project (not fastllm's own checkout) has its own oxlint
	// installed — see internal/lint's doc comment. Any failure here
	// (unsupported extension, no oxlint found, lint process error) is
	// silently treated as "no diagnostics," never blocking or failing the
	// save itself.
	diagnostics := []lint.Diagnostic{}
	if absPath, err := h.Files.Resolve(req.Path); err == nil {
		if found, err := lint.Lint(r.Context(), absPath); err == nil {
			diagnostics = found
		}
	}
	writeJSON(w, map[string]any{"path": req.Path, "saved": true, "diagnostics": diagnostics})
}

// editorCompleteTimeout bounds how long a single completion request may
// run — this fires on a debounce timer while the user is actively
// typing, so a slow/hung local model must not be allowed to pile up
// requests or leave the editor waiting indefinitely for ghost text that
// will be stale by the time it arrives.
const editorCompleteTimeout = 8 * time.Second

// editorCompletePrefixCap/editorCompleteSuffixCap bound how much
// surrounding code is sent per request — enough for real local context
// (the current function, nearby imports) without either blowing up
// latency on a large file or costing meaningfully more than a single
// short chat turn, since this fires on every pause in typing.
const (
	editorCompletePrefixCap = 4000
	editorCompleteSuffixCap = 2000
)

type editorCompleteRequest struct {
	Prefix   string `json:"prefix"`
	Suffix   string `json:"suffix"`
	Language string `json:"language"`
}

// EditorComplete returns a single inline code-completion suggestion —
// the editor's "ghost text" as you type (see EditorView.jsx's
// autocompletion wiring). Deliberately simple and fast rather than
// running through the full chat pipeline: no conversation history, no
// RAG retrieval, no tool-calling loop, no streaming — just one
// non-streaming completion from a LOCAL model (the pinned
// store.EditorSettings.CompletionModel if one's set, else
// h.LLM.ChatModel() — see the completionModel lookup below), always
// independent of whatever cloud/local model is selected in the chat
// Model picker, since this fires on a debounce timer while the user is
// actively typing and a cloud model would mean a real API call — and
// real cost/latency/rate-limit exposure — on every pause. Uses
// Router.Chat (non-streaming) rather than StreamChat since a single
// short completion has nothing worth streaming token-by-token.
func (h *Handler) EditorComplete(w http.ResponseWriter, r *http.Request) {
	if !h.Files.Enabled() {
		http.Error(w, "file access is not enabled", http.StatusForbidden)
		return
	}
	var req editorCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid completion request", http.StatusBadRequest)
		return
	}

	prefix := req.Prefix
	if len(prefix) > editorCompletePrefixCap {
		prefix = prefix[len(prefix)-editorCompletePrefixCap:]
	}
	suffix := req.Suffix
	if len(suffix) > editorCompleteSuffixCap {
		suffix = suffix[:editorCompleteSuffixCap]
	}

	ctx, cancel := context.WithTimeout(r.Context(), editorCompleteTimeout)
	defer cancel()

	// A pinned completion model (Settings → Model → "Completion model",
	// store.EditorSettings.CompletionModel) always wins over the backend's
	// default chat model — this is the whole point of the setting: a fast
	// non-reasoning model for snappy ghost text, independent of whichever
	// model chat is currently using. Always a bare local model name (the
	// picker only ever offers local models — see EditorSettingsPanel.jsx),
	// never a "provider:"-prefixed cloud model, since ghost text firing on
	// every pause in typing must never become a cloud API call (see this
	// handler's doc comment above).
	completionModel := h.LLM.ChatModel()
	if editorSettings, err := store.GetEditorSettings(h.DB); err == nil && editorSettings.CompletionModel != "" {
		completionModel = editorSettings.CompletionModel
	}

	prompt := buildCompletionPrompt(prefix, suffix, req.Language)
	resp, err := h.LLM.Chat(ctx, completionModel, []llm.Message{
		{Role: "system", Content: completionSystemPrompt},
		{Role: "user", Content: prompt},
	}, nil, "")
	if err != nil {
		// A completion failing (model unreachable, timed out, etc.) is not
		// something the editor should surface as an error banner — typing
		// just continues without a suggestion, same as any IDE's
		// autocomplete silently having nothing to offer. Empty completion,
		// 200 OK, not an error status.
		writeJSON(w, map[string]any{"completion": ""})
		return
	}
	writeJSON(w, map[string]any{"completion": cleanCompletion(resp.Content)})
}

// completionSystemPrompt keeps the model from wrapping its answer in
// markdown code fences or adding commentary — both would need stripping
// before the raw text could be inserted into the editor, and a model
// that ignores this instruction is exactly what cleanCompletion's fence
// stripping is a backstop for.
const completionSystemPrompt = "You are a code completion engine. Given code before and after the cursor, output ONLY the text that should be inserted at the cursor to continue it naturally. No explanation, no markdown code fences, no repeating the given code. Stop after a few lines or at the end of the current statement/block — do not write more than one function or a large chunk of unrelated code."

func buildCompletionPrompt(prefix, suffix, language string) string {
	lang := language
	if lang == "" {
		lang = "code"
	}
	if suffix == "" {
		return fmt.Sprintf("Language: %s\n\nCode before the cursor:\n%s\n\nContinue from the cursor:", lang, prefix)
	}
	return fmt.Sprintf("Language: %s\n\nCode before the cursor:\n%s\n\nCode after the cursor:\n%s\n\nText to insert at the cursor:", lang, prefix, suffix)
}

// cleanCompletion strips markdown code fences/inline-code backticks a
// model adds despite completionSystemPrompt's instruction not to — cheap
// insurance since local models vary in how strictly they follow a system
// prompt (observed live: qwen2.5-coder:7b wraps a short completion in
// single backticks, e.g. "`a + b`", even though the prompt says not to
// add markdown).
func cleanCompletion(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if nl := strings.IndexByte(s, '\n'); nl != -1 {
			s = s[nl+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		return strings.TrimSpace(s)
	}
	if strings.HasPrefix(s, "`") && strings.HasSuffix(s, "`") && len(s) >= 2 {
		s = strings.TrimSuffix(strings.TrimPrefix(s, "`"), "`")
	}
	return s
}

type editorDeleteRequest struct {
	Path string `json:"path"`
}

// EditorDeleteFile removes a single file from disk. No confirmation
// step server-side — the editor's own delete button is the
// confirmation, same as EditorSaveFile treats a human clicking Save as
// the approval step.
func (h *Handler) EditorDeleteFile(w http.ResponseWriter, r *http.Request) {
	if !h.Files.WritesEnabled() {
		http.Error(w, "file writes are not enabled", http.StatusForbidden)
		return
	}
	var req editorDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if err := h.Files.Delete(req.Path); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// EditorFolderFileCount reports how many files live under a given
// directory in the sandbox, for the "delete this folder and its N
// files?" confirmation copy the frontend shows before calling
// EditorDeleteFolder — see ConfirmDeleteModal usage in EditorView.jsx.
// Read-only; doesn't require write access, unlike the delete itself.
func (h *Handler) EditorFolderFileCount(w http.ResponseWriter, r *http.Request) {
	if !h.Files.Enabled() {
		http.Error(w, "file access is not enabled", http.StatusForbidden)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	count, err := h.Files.CountFilesUnder(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"path": path, "file_count": count})
}

// EditorDeleteFolder recursively removes a folder and everything in it.
// The frontend is expected to have already shown a confirmation (see
// EditorFolderFileCount) before calling this — same "the UI's own
// confirmation step is the approval" convention as EditorDeleteFile.
func (h *Handler) EditorDeleteFolder(w http.ResponseWriter, r *http.Request) {
	if !h.Files.WritesEnabled() {
		http.Error(w, "file writes are not enabled", http.StatusForbidden)
		return
	}
	var req editorDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if err := h.Files.DeleteFolder(req.Path); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type editorRenameRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// EditorRenameFile moves a file within the sandbox — covers both a
// same-directory rename and a move to a different folder, since both
// are just a path change to os.Rename. Fails if the destination already
// exists rather than silently overwriting it.
func (h *Handler) EditorRenameFile(w http.ResponseWriter, r *http.Request) {
	if !h.Files.WritesEnabled() {
		http.Error(w, "file writes are not enabled", http.StatusForbidden)
		return
	}
	var req editorRenameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.From == "" || req.To == "" {
		http.Error(w, "from and to are required", http.StatusBadRequest)
		return
	}
	if err := h.Files.Rename(req.From, req.To); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"path": req.To, "renamed": true})
}

// EditorSearch searches file contents across the project via `git grep`.
func (h *Handler) EditorSearch(w http.ResponseWriter, r *http.Request) {
	root, ok := h.editorRoot(w)
	if !ok {
		return
	}
	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, []gitrepo.SearchMatch{})
		return
	}
	matches, err := gitrepo.Search(r.Context(), root, query)
	if err != nil {
		h.writeGitError(w, err)
		return
	}
	if matches == nil {
		matches = []gitrepo.SearchMatch{}
	}
	writeJSON(w, matches)
}

// EditorGitStatus returns the working tree's current git status.
func (h *Handler) EditorGitStatus(w http.ResponseWriter, r *http.Request) {
	root, ok := h.editorRoot(w)
	if !ok {
		return
	}
	statuses, err := gitrepo.Status(r.Context(), root)
	if err != nil {
		h.writeGitError(w, err)
		return
	}
	if statuses == nil {
		statuses = []gitrepo.FileStatus{}
	}
	writeJSON(w, statuses)
}

// EditorGitWatch streams a "changed" SSE event every time something in
// the sandboxed root's .git directory changes — a commit, branch switch,
// staging, or any other update to HEAD/refs/the index, from any source
// (this app's own git actions, a `git` command typed into the Terminal
// panel, or an external tool touching the same repo). The frontend's git
// panel subscribes to this instead of polling on a timer, matching how
// desktop IDEs like VS Code pick up out-of-band git changes: a
// filesystem watcher (see gitrepo.Watch) rather than a fixed-interval
// refetch, so there's no cost while nothing is happening and no polling
// lag when something does. The event carries no payload — like the rest
// of this app's SSE endpoints, the client already knows how to refetch
// (see EditorGitStatus/EditorGitBranches); this only tells it when to.
func (h *Handler) EditorGitWatch(w http.ResponseWriter, r *http.Request) {
	root, ok := h.editorRoot(w)
	if !ok {
		return
	}
	ctx := r.Context()
	if !gitrepo.IsRepo(ctx, root) {
		h.writeGitError(w, gitrepo.ErrNotARepo)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	changes, stop, err := gitrepo.Watch(root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer stop()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-changes:
			if !ok {
				return
			}
			fmt.Fprint(w, "event: changed\ndata: {}\n\n")
			flusher.Flush()
		}
	}
}

// EditorGitDiff returns the diff for one file — worktree-vs-index by
// default, or index-vs-HEAD if ?staged=1 is set.
func (h *Handler) EditorGitDiff(w http.ResponseWriter, r *http.Request) {
	root, ok := h.editorRoot(w)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	staged := r.URL.Query().Get("staged") == "1"
	diff, err := gitrepo.Diff(r.Context(), root, path, staged)
	if err != nil {
		h.writeGitError(w, err)
		return
	}
	writeJSON(w, map[string]string{"diff": diff})
}

type editorGitPathsRequest struct {
	Paths []string `json:"paths"`
}

// EditorGitStage stages one or more paths.
func (h *Handler) EditorGitStage(w http.ResponseWriter, r *http.Request) {
	root, ok := h.editorRoot(w)
	if !ok {
		return
	}
	var req editorGitPathsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Paths) == 0 {
		http.Error(w, "paths is required", http.StatusBadRequest)
		return
	}
	if err := gitrepo.Stage(r.Context(), root, req.Paths); err != nil {
		h.writeGitError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// EditorGitUnstage unstages one or more paths.
func (h *Handler) EditorGitUnstage(w http.ResponseWriter, r *http.Request) {
	root, ok := h.editorRoot(w)
	if !ok {
		return
	}
	var req editorGitPathsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Paths) == 0 {
		http.Error(w, "paths is required", http.StatusBadRequest)
		return
	}
	if err := gitrepo.Unstage(r.Context(), root, req.Paths); err != nil {
		h.writeGitError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type editorGitCommitRequest struct {
	Message string `json:"message"`
}

// EditorGitCommit commits whatever is currently staged. Never stages
// anything itself — see gitrepo.Commit. Committing and pushing are
// separate, explicit actions in the UI (see EditorGitPush) — this
// endpoint never pushes.
func (h *Handler) EditorGitCommit(w http.ResponseWriter, r *http.Request) {
	root, ok := h.editorRoot(w)
	if !ok {
		return
	}
	var req editorGitCommitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := gitrepo.Commit(r.Context(), root, req.Message); err != nil {
		h.writeGitError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// EditorGitPush pushes the current branch to its configured upstream —
// a plain `git push`, never a force-push. If the remote has diverged
// (or there's no upstream configured), this fails and the git error is
// returned as-is; the caller decides what to do next rather than this
// endpoint resolving it automatically. A separate, explicit action from
// EditorGitCommit — the UI only ever calls this from its own "Push"
// button, never automatically after a commit.
func (h *Handler) EditorGitPush(w http.ResponseWriter, r *http.Request) {
	root, ok := h.editorRoot(w)
	if !ok {
		return
	}
	output, err := gitrepo.Push(r.Context(), root)
	if err != nil {
		h.writeGitError(w, err)
		return
	}
	writeJSON(w, map[string]string{"output": output})
}

// EditorGitBranches lists local branches, marking which is current.
func (h *Handler) EditorGitBranches(w http.ResponseWriter, r *http.Request) {
	root, ok := h.editorRoot(w)
	if !ok {
		return
	}
	branches, err := gitrepo.Branches(r.Context(), root)
	if err != nil {
		h.writeGitError(w, err)
		return
	}
	if branches == nil {
		branches = []gitrepo.Branch{}
	}
	writeJSON(w, branches)
}

type editorGitBranchRequest struct {
	Name string `json:"name"`
}

// EditorGitSwitchBranch checks out an existing local branch. If the
// working tree has uncommitted changes that would be overwritten by the
// target branch, this fails and git's own error is returned as-is — no
// auto-stash, nothing discarded. See gitrepo.SwitchBranch.
func (h *Handler) EditorGitSwitchBranch(w http.ResponseWriter, r *http.Request) {
	root, ok := h.editorRoot(w)
	if !ok {
		return
	}
	var req editorGitBranchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := gitrepo.SwitchBranch(r.Context(), root, req.Name); err != nil {
		h.writeGitError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// EditorGitCreateBranch creates a new branch from HEAD and switches to
// it. Fails if a branch with that name already exists.
func (h *Handler) EditorGitCreateBranch(w http.ResponseWriter, r *http.Request) {
	root, ok := h.editorRoot(w)
	if !ok {
		return
	}
	var req editorGitBranchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := gitrepo.CreateBranch(r.Context(), root, req.Name); err != nil {
		h.writeGitError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// editorRoot resolves the sandbox root for editor/git endpoints,
// writing an error response and returning ok=false if file access isn't
// enabled. All editor and git-panel operations share the same root the
// model's file tools use (see internal/files) — one project folder
// setting for the whole app.
func (h *Handler) editorRoot(w http.ResponseWriter) (root string, ok bool) {
	if !h.Files.Enabled() {
		http.Error(w, "file access is not enabled", http.StatusForbidden)
		return "", false
	}
	return h.Files.GetRoot(), true
}

func (h *Handler) writeGitError(w http.ResponseWriter, err error) {
	if errors.Is(err, gitrepo.ErrNotARepo) {
		http.Error(w, "the configured project folder is not a git repository", http.StatusConflict)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
