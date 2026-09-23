// Package gitrepo shells out to the git CLI to back the text editor's
// git panel (status/diff/stage/commit/push) and its file tree and
// search (git ls-files / git grep respect .gitignore for free). Every
// command runs with root as its working directory and a fixed set of
// flags — nothing here ever force-pushes or touches history, and
// commits only ever include what's already staged (never `git commit
// -a`), matching the tool's "stage, then commit" UI flow. Push is a
// plain `git push` to the current branch's upstream: if the remote has
// diverged, it fails and surfaces git's own error rather than silently
// resolving the conflict any way (e.g. no auto-force, no auto-pull).
package gitrepo

import (
	"context"
	"errors"
	"fastllm/internal/execution"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrNotARepo means root isn't inside a git working tree.
var ErrNotARepo = errors.New("gitrepo: not a git repository")

func run(ctx context.Context, root string, args ...string) (string, error) {
	result, err := execution.RunLocal(ctx, root, execution.LocalPolicy(), execution.Command{Executable: "git", Args: args})
	if err != nil {
		return "", err
	}
	if err = result.Err(); err != nil {
		msg := strings.TrimSpace(result.Stderr)
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	if result.Truncated {
		return "", errors.New("git output exceeded capture limit")
	}
	return result.Stdout + result.Stderr, nil
}

// IsRepo reports whether root is inside a git working tree.
func IsRepo(ctx context.Context, root string) bool {
	out, err := run(ctx, root, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// ListFiles returns every tracked and untracked-but-not-ignored file
// path (relative to root), the same set git itself considers "part of
// the project" — used to build the editor's file tree and to scope
// search, so build artifacts, node_modules, .git internals etc. are
// excluded for free instead of needing their own ignore-list here.
func ListFiles(ctx context.Context, root string) ([]string, error) {
	if !IsRepo(ctx, root) {
		return nil, ErrNotARepo
	}
	out, err := run(ctx, root, "ls-files", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// FileStatus is one entry from `git status`.
type FileStatus struct {
	Path     string `json:"path"`
	Staged   string `json:"staged"`   // index status: "M", "A", "D", "R", "" = none
	Unstaged string `json:"unstaged"` // worktree status: "M", "D", "?" (untracked), "" = none
}

// Status returns the working tree's current status, parsed from
// `git status --porcelain=v2`.
func Status(ctx context.Context, root string) ([]FileStatus, error) {
	if !IsRepo(ctx, root) {
		return nil, ErrNotARepo
	}
	out, err := run(ctx, root, "status", "--porcelain=v2", "--untracked-files=all")
	if err != nil {
		return nil, err
	}

	var results []FileStatus
	for _, line := range splitLines(out) {
		if line == "" {
			continue
		}
		switch line[0] {
		case '1', '2': // ordinary / renamed-or-copied changed entry
			fields := strings.SplitN(line, " ", 9)
			if len(fields) < 9 {
				continue
			}
			xy := fields[1]
			path := fields[8]
			// Renamed entries carry "path\told_path" in field 8 for
			// type '2' — the editor only needs the current path.
			if idx := strings.IndexByte(path, '\t'); idx != -1 {
				path = path[:idx]
			}
			results = append(results, FileStatus{
				Path:     path,
				Staged:   statusChar(xy[0]),
				Unstaged: statusChar(xy[1]),
			})
		case '?': // untracked
			path := strings.TrimPrefix(line, "? ")
			results = append(results, FileStatus{Path: path, Unstaged: "?"})
		}
	}
	return results, nil
}

func statusChar(c byte) string {
	if c == '.' {
		return ""
	}
	return string(c)
}

// RepoStatus holds complete repository state for the TUI / UI.
type RepoStatus struct {
	IsRepo       bool             `json:"is_repo"`
	Branch       string           `json:"branch"`
	Upstream     string           `json:"upstream"`
	Ahead        int              `json:"ahead"`
	Behind       int              `json:"behind"`
	Files        []RepoFileStatus `json:"files"`
	LatestCommit string           `json:"latest_commit"`
}

// RepoFileStatus is one changed file in the repository.
type RepoFileStatus struct {
	Path     string `json:"path"`
	Staged   string `json:"staged"`   // "M", "A", "D", "R", ""
	Unstaged string `json:"unstaged"` // "M", "D", "?", ""
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
}

// GetRepoStatus returns comprehensive repository status including branch,
// upstream ahead/behind counts, staged and unstaged file lists, and diff stats.
func GetRepoStatus(ctx context.Context, root string) (RepoStatus, error) {
	if !IsRepo(ctx, root) {
		return RepoStatus{IsRepo: false}, ErrNotARepo
	}
	out, err := run(ctx, root, "status", "--porcelain=v2", "--branch", "--untracked-files=all")
	if err != nil {
		return RepoStatus{IsRepo: false}, err
	}

	status := RepoStatus{IsRepo: true}
	fileMap := make(map[string]*RepoFileStatus)
	var fileList []string

	for _, line := range splitLines(out) {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# ") {
			header := strings.TrimPrefix(line, "# ")
			parts := strings.SplitN(header, " ", 2)
			if len(parts) == 2 {
				key, val := parts[0], parts[1]
				switch key {
				case "branch.head":
					status.Branch = val
				case "branch.upstream":
					status.Upstream = val
				case "branch.ab":
					abParts := strings.Fields(val)
					for _, ab := range abParts {
						if strings.HasPrefix(ab, "+") {
							if n, err := strconv.Atoi(ab[1:]); err == nil {
								status.Ahead = n
							}
						} else if strings.HasPrefix(ab, "-") {
							if n, err := strconv.Atoi(ab[1:]); err == nil {
								status.Behind = n
							}
						}
					}
				}
			}
			continue
		}

		switch line[0] {
		case '1', '2':
			fields := strings.SplitN(line, " ", 9)
			if len(fields) < 9 {
				continue
			}
			xy := fields[1]
			path := fields[8]
			if idx := strings.IndexByte(path, '\t'); idx != -1 {
				path = path[:idx]
			}
			f := &RepoFileStatus{
				Path:     path,
				Staged:   statusChar(xy[0]),
				Unstaged: statusChar(xy[1]),
			}
			fileMap[path] = f
			fileList = append(fileList, path)
		case '?':
			path := strings.TrimPrefix(line, "? ")
			f := &RepoFileStatus{
				Path:     path,
				Unstaged: "?",
			}
			fileMap[path] = f
			fileList = append(fileList, path)
		}
	}

	// Diff numstats for unstaged changes
	if numstatOut, err := run(ctx, root, "diff", "--numstat"); err == nil {
		parseNumstat(numstatOut, fileMap, false)
	}

	// Diff numstats for staged changes
	if numstatOut, err := run(ctx, root, "diff", "--cached", "--numstat"); err == nil {
		parseNumstat(numstatOut, fileMap, true)
	}

	// For untracked files without diff stats, count lines directly
	for _, f := range fileMap {
		if f.Unstaged == "?" && f.Added == 0 {
			fullPath := filepath.Join(root, filepath.FromSlash(f.Path))
			if info, err := os.Stat(fullPath); err == nil && !info.IsDir() && info.Size() < 500000 {
				if data, err := os.ReadFile(fullPath); err == nil {
					f.Added = countFileLines(string(data))
				}
			}
		}
	}

	for _, path := range fileList {
		if f, ok := fileMap[path]; ok {
			status.Files = append(status.Files, *f)
		}
	}

	if logOut, err := run(ctx, root, "log", "-1", "--format=%h %s"); err == nil {
		status.LatestCommit = strings.TrimSpace(logOut)
	}

	return status, nil
}

func parseNumstat(output string, fileMap map[string]*RepoFileStatus, staged bool) {
	for _, line := range splitLines(output) {
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		path := parts[2]
		f, ok := fileMap[path]
		if !ok {
			f = &RepoFileStatus{Path: path}
			if staged {
				f.Staged = "M"
			} else {
				f.Unstaged = "M"
			}
			fileMap[path] = f
		}
		added, _ := strconv.Atoi(parts[0])
		removed, _ := strconv.Atoi(parts[1])
		f.Added += added
		f.Removed += removed
	}
}

func countFileLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

// Diff returns the diff for one path. staged selects `git diff --staged`
// (index vs HEAD) instead of the default (worktree vs index).
func Diff(ctx context.Context, root, path string, staged bool) (string, error) {
	if !IsRepo(ctx, root) {
		return "", ErrNotARepo
	}
	args := []string{"diff", "--no-color"}
	if staged {
		args = append(args, "--staged")
	}
	args = append(args, "--", path)
	return run(ctx, root, args...)
}

// Stage runs `git add` for the given paths.
func Stage(ctx context.Context, root string, paths []string) error {
	if !IsRepo(ctx, root) {
		return ErrNotARepo
	}
	if len(paths) == 0 {
		return nil
	}
	_, err := run(ctx, root, append([]string{"add", "--"}, paths...)...)
	return err
}

// Unstage runs `git restore --staged` for the given paths.
func Unstage(ctx context.Context, root string, paths []string) error {
	if !IsRepo(ctx, root) {
		return ErrNotARepo
	}
	if len(paths) == 0 {
		return nil
	}
	_, err := run(ctx, root, append([]string{"restore", "--staged", "--"}, paths...)...)
	return err
}

// Discard runs `git restore` (worktree only, no `--staged`) for the
// given paths — throws away unstaged edits to already-tracked files,
// resetting them back to whatever's in the index (or HEAD, if nothing's
// staged for that path). This is the one destructive, no-undo action in
// the git panel: unlike Stage/Unstage/Commit, there's no git command
// that could recover the discarded edit afterward, so the caller (see
// EditorGitDiscard's doc comment) is expected to have already gotten a
// confirmation from the user before calling this. Does not touch
// untracked files — `git restore` has nothing to restore an untracked
// file *to*, since git has never seen its content; deleting an untracked
// file entirely is a different operation (see internal/files.Delete),
// which the frontend routes to instead for that status.
func Discard(ctx context.Context, root string, paths []string) error {
	if !IsRepo(ctx, root) {
		return ErrNotARepo
	}
	if len(paths) == 0 {
		return nil
	}
	_, err := run(ctx, root, append([]string{"restore", "--"}, paths...)...)
	return err
}

// Commit creates a commit from whatever is currently staged. Never
// stages anything itself (no `-a`) — the caller (the editor's git
// panel) is expected to have already staged what it wants committed, so
// a commit only ever contains changes the user explicitly reviewed and
// staged.
func Commit(ctx context.Context, root, message string) error {
	if !IsRepo(ctx, root) {
		return ErrNotARepo
	}
	if strings.TrimSpace(message) == "" {
		return errors.New("gitrepo: commit message is required")
	}
	_, err := run(ctx, root, "commit", "-m", message)
	return err
}

// Branch is one local branch.
type Branch struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

// Branches lists local branches (not remote-tracking refs), marking
// which one is currently checked out.
func Branches(ctx context.Context, root string) ([]Branch, error) {
	if !IsRepo(ctx, root) {
		return nil, ErrNotARepo
	}
	out, err := run(ctx, root, "branch", "--format=%(refname:short)%00%(HEAD)")
	if err != nil {
		return nil, err
	}
	var branches []Branch
	for _, line := range splitLines(out) {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		branches = append(branches, Branch{Name: parts[0], Current: parts[1] == "*"})
	}
	return branches, nil
}

// SwitchBranch checks out an existing local branch. Like the rest of
// this package, never does anything beyond the plain git command: if
// the working tree has uncommitted changes that would be overwritten by
// the target branch's version of the same files, `git switch` itself
// refuses and this returns that error as-is — no auto-stash, no
// discarding. Changes that don't conflict are carried over onto the new
// branch, same as running `git switch` by hand.
func SwitchBranch(ctx context.Context, root, name string) error {
	if !IsRepo(ctx, root) {
		return ErrNotARepo
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("gitrepo: branch name is required")
	}
	_, err := run(ctx, root, "switch", name)
	return err
}

// CreateBranch creates a new branch starting from the current HEAD and
// switches to it (`git switch -c`). Fails if a branch with that name
// already exists — callers should use SwitchBranch for that case
// instead of silently switching.
func CreateBranch(ctx context.Context, root, name string) error {
	if !IsRepo(ctx, root) {
		return ErrNotARepo
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("gitrepo: branch name is required")
	}
	_, err := run(ctx, root, "switch", "-c", name)
	return err
}

// Push runs a plain `git push` (current branch to its configured
// upstream). No force flag, ever — if the remote has commits this
// branch doesn't (someone else pushed, or there's no upstream
// configured yet), this fails and returns git's own error message
// as-is rather than resolving it automatically. The caller decides
// what to do next (pull/rebase, set an upstream, etc.) — this package
// never guesses.
func Push(ctx context.Context, root string) (string, error) {
	if !IsRepo(ctx, root) {
		return "", ErrNotARepo
	}
	out, err := run(ctx, root, "push")
	if err != nil && strings.Contains(err.Error(), "has no upstream branch") {
		// Wrap rather than replace: %w keeps errors.Is(err, ErrNoUpstream)
		// working for the caller that wants to offer the "set upstream and
		// push" fix, while %s keeps git's own full message (which names
		// the exact branch and suggests the exact command) intact for
		// anyone just displaying the error as-is.
		return out, fmt.Errorf("%s: %w", err.Error(), ErrNoUpstream)
	}
	return out, err
}

// ErrNoUpstream means a plain Push failed specifically because the
// current branch has never been pushed before and has no configured
// upstream — the one Push failure mode with a single unambiguous fix
// (set the upstream to origin/<branch>, the same remote/name a bare
// `git push -u origin HEAD` would use), unlike a diverged-history
// failure, which could mean several different things depending on why
// the remote has commits this branch doesn't. Detected by matching
// git's own message rather than parsing exit codes, since git doesn't
// give this case a distinct one.
var ErrNoUpstream = errors.New("gitrepo: current branch has no upstream")

// PushSetUpstream runs `git push -u origin HEAD` — pushes the currently
// checked-out branch and records it as tracking origin/<that branch>, so
// a plain Push works from then on. HEAD (not a separately-looked-up
// branch name) is what's pushed, so there's no window between reading
// "what's the current branch" and actually pushing where a concurrent
// checkout could push the wrong one. Only ever called from the editor's
// "Set upstream & push" button, shown specifically when Push fails with
// ErrNoUpstream — never automatically, same "caller decides, this
// package never guesses" rule Push itself follows.
func PushSetUpstream(ctx context.Context, root string) (string, error) {
	if !IsRepo(ctx, root) {
		return "", ErrNotARepo
	}
	return run(ctx, root, "push", "-u", "origin", "HEAD")
}

// SearchMatch is one line matched by Search.
type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// MaxSearchMatches caps how many results Search returns — a search
// across a large tree with a common query could otherwise return an
// unbounded amount of text.
const MaxSearchMatches = 500

// Search runs `git grep` for query across every tracked/untracked
// (non-ignored) file in root, returning at most MaxSearchMatches lines.
func Search(ctx context.Context, root, query string) ([]SearchMatch, error) {
	if !IsRepo(ctx, root) {
		return nil, ErrNotARepo
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	// --untracked includes files not yet added to the index (but still
	// respects .gitignore, same as ListFiles); -I skips binary files;
	// -n includes line numbers; -F treats query as a literal string,
	// not a regex, so search input can't be used to inject grep/regex
	// syntax the user didn't intend.
	result, err := execution.RunLocal(ctx, root, execution.LocalPolicy(), execution.Command{Executable: "git", Args: []string{"grep", "-n", "-I", "-F", "--untracked", "-e", query}})
	if err != nil {
		return nil, err
	}
	if result.Reason == "exit" && result.ExitCode == 1 {
		return nil, nil
	}
	if err = result.Err(); err != nil {
		return nil, fmt.Errorf("git grep: %w: %s", err, result.Stderr)
	}
	if result.Truncated {
		return nil, errors.New("git search output exceeded capture limit")
	}
	out := result.Stdout

	lines := splitLines(out)
	matches := make([]SearchMatch, 0, min(len(lines), MaxSearchMatches))
	for _, line := range lines {
		if len(matches) >= MaxSearchMatches {
			break
		}
		// Format: path:lineno:text
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		lineNo, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		matches = append(matches, SearchMatch{Path: parts[0], Line: lineNo, Text: parts[2]})
	}
	return matches, nil
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
