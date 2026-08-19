package gitrepo

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounceWindow coalesces the burst of filesystem events a single git
// operation (or a multi-file write from the chat model's file tool)
// produces into one "changed" notification, rather than firing once per
// underlying write.
const debounceWindow = 300 * time.Millisecond

// Watch notifies the returned channel whenever the working tree at root
// changes — an edit saved from the editor, a file written by the chat
// model's write tool, a `git` command typed into the Terminal panel, an
// external editor, a new commit, a branch switch, staging, etc. It's a
// best-effort convenience for the editor's git panel to refresh itself
// live (see internal/chat's git-watch SSE endpoint) instead of polling;
// callers should still treat gitrepo.Status/gitrepo.Branches as the
// source of truth, since a change notification only means "something
// happened," not what. Returns a stop function that closes the
// underlying watcher and the returned channel; call it when the caller
// (e.g. an SSE connection) goes away.
//
// Which directories get watched is derived from ListFiles — the same
// `git ls-files --cached --others --exclude-standard` call the file tree
// and search use — rather than a hardcoded skip-list, so this works
// correctly for whatever language/toolchain the project uses (Python's
// .venv, Rust's target/, Java's build artifacts, etc.) purely from
// following the project's own .gitignore, with no per-language list here
// to fall out of date. Plus .git/HEAD, .git/refs/ recursively, and
// .git/index specifically, so branch switches and commits from a
// separate `git` process are caught even though they don't always touch
// a file under the working tree itself. A directory that holds no
// tracked-or-unignored file (an empty dir, or one containing only
// ignored files) won't get its own watch at setup time — nothing
// meaningful could happen in it yet — but the event loop below adds a
// directory to the watch the moment fsnotify reports it being created
// (fsnotify has no recursive-watch primitive on Windows, so this manual
// catch-up is what stands in for one), so a file written into it
// afterward — e.g. ApproveWrite's os.MkdirAll landing a model-proposed
// file in a path that didn't exist when this watch started — is still
// caught without waiting for an unrelated git action to force a refresh.
func Watch(ctx context.Context, root string) (changes <-chan struct{}, stop func(), err error) {
	gitDir := filepath.Join(root, ".git")

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, err
	}

	// Best-effort: if this fails (e.g. root isn't a repo after all, a
	// caller-side race), we still fall back to watching root itself below
	// so the watcher isn't completely blind.
	files, _ := ListFiles(ctx, root)
	dirs := map[string]bool{root: true}
	for _, f := range files {
		dir := filepath.Dir(filepath.Join(root, filepath.FromSlash(f)))
		for dir != root && dir != filepath.Dir(dir) {
			if dirs[dir] {
				break
			}
			dirs[dir] = true
			dir = filepath.Dir(dir)
		}
	}
	for dir := range dirs {
		// Best-effort: a directory that no longer exists just means
		// nothing to watch there.
		_ = w.Add(dir)
	}

	// root itself is always watched (above) so top-level file/directory
	// creation is caught, but that means an ignored top-level directory
	// (node_modules, .venv, target, whatever) still generates an event
	// when its own mtime changes — a non-recursive watch on root sees
	// its immediate children change, even though nothing inside that
	// child directory is itself watched. ignoredTopLevel records which
	// of root's direct children were left out of dirs so the event loop
	// below can drop those specifically, instead of every ignored write
	// producing a needless (if harmless) refresh.
	ignoredTopLevel := map[string]bool{}
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if full := filepath.Join(root, e.Name()); !dirs[full] {
				ignoredTopLevel[full] = true
			}
		}
	}

	watchTargets := []string{
		gitDir,
		filepath.Join(gitDir, "refs"),
		filepath.Join(gitDir, "refs", "heads"),
		filepath.Join(gitDir, "refs", "tags"),
	}
	for _, dir := range watchTargets {
		// Best-effort: refs/tags (etc.) may not exist in a fresh repo with
		// no tags yet — a missing directory just means nothing to watch
		// there, not a failure of the whole watcher.
		_ = w.Add(dir)
	}

	out := make(chan struct{}, 1)
	done := make(chan struct{})

	go func() {
		var debounce *time.Timer
		defer func() {
			if debounce != nil {
				debounce.Stop()
			}
			close(out)
		}()
		for {
			select {
			case <-done:
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if isUnderAny(ev.Name, ignoredTopLevel) {
					continue
				}
				// A directory created after the watcher started (e.g. by
				// ApproveWrite's os.MkdirAll for a model-proposed file in a
				// path that didn't exist yet) is invisible to fsnotify
				// until something explicitly watches it — otherwise a
				// SECOND write landing inside that same new directory
				// later produces no event at all, even though the mkdir
				// itself did. fsnotify only ever reports the single
				// directory it directly saw appear — for a nested
				// os.MkdirAll("a/b/c") where none of a/b/c existed before,
				// that's just "a", with "b" and "c" already sitting inside
				// it by the time this event is handled, so a plain w.Add on
				// ev.Name alone would still miss writes under b/c. Walking
				// the new directory and adding every subdirectory found
				// inside it (there may already be several, all created in
				// the same MkdirAll call) closes that gap in one pass;
				// walkDir itself is silent about future creates one level
				// further down, but those are caught the same way the next
				// time this branch fires for them.
				if ev.Op&fsnotify.Create != 0 {
					if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
						_ = w.Add(ev.Name)
						_ = filepath.WalkDir(ev.Name, func(path string, d fs.DirEntry, err error) error {
							if err != nil || !d.IsDir() || path == ev.Name {
								return nil
							}
							_ = w.Add(path)
							return nil
						})
					}
				}
				if debounce == nil {
					debounce = time.AfterFunc(debounceWindow, func() {
						select {
						case out <- struct{}{}:
						default: // a notification is already pending — coalesce
						}
					})
				} else {
					debounce.Reset(debounceWindow)
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
				// fsnotify surfaces watcher-internal errors (e.g. a watched
				// path removed out from under it) on this channel — nothing
				// actionable for a best-effort live-refresh signal, so it's
				// dropped rather than propagated; the SSE connection stays
				// up and a subsequent poll-on-reconnect (if the frontend
				// ever adds one) would still catch up.
			}
		}
	}()

	stop = func() {
		close(done)
		w.Close()
	}
	return out, stop, nil
}

// isUnderAny reports whether path is equal to, or nested inside, any
// directory in dirs.
func isUnderAny(path string, dirs map[string]bool) bool {
	for dir := range dirs {
		if path == dir || strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
