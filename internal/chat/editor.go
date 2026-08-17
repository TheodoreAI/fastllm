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
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"

	"fastllm/internal/gitrepo"
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
	writeJSON(w, map[string]any{"path": req.Path, "saved": true})
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
