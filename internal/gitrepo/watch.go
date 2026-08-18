package gitrepo

import (
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounceWindow coalesces the burst of filesystem events a single git
// operation produces (a commit touches .git/index, .git/HEAD, and a ref
// file all at once; a checkout touches even more) into one "changed"
// notification, rather than firing once per underlying write.
const debounceWindow = 300 * time.Millisecond

// Watch notifies the returned channel whenever the git repository at root
// changes — new commits, branch switches, staging, etc. It's a
// best-effort convenience for the editor's git panel to refresh itself
// live (see internal/chat's git-watch SSE endpoint) instead of polling;
// callers should still treat gitrepo.Status/gitrepo.Branches as the
// source of truth, since a change notification only means "something
// happened," not what. Returns a stop function that closes the
// underlying watcher and the returned channel; call it when the caller
// (e.g. an SSE connection) goes away.
//
// Watches .git/HEAD (branch switches, commits on the current branch),
// .git/refs/ recursively (branch/tag creation, updates from any source —
// this app's own UI, a separate `git` process in the Terminal panel, or
// any other tool touching the same repo), and .git/index (staging).
// Doesn't watch the working tree itself — that's a much larger and
// noisier surface (every file save would fire it) and file saves already
// trigger a refresh through the editor's own save path.
func Watch(root string) (changes <-chan struct{}, stop func(), err error) {
	gitDir := filepath.Join(root, ".git")

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, err
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
			case _, ok := <-w.Events:
				if !ok {
					return
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
