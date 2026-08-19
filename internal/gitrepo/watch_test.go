package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Regression test for a gap where a write landing inside a directory
// created earlier in the same watch session went unnoticed: the mkdir
// itself was seen (it changes root's own listing), but nothing added
// that new directory to the fsnotify watch set, so a later write inside
// it produced no event. This is exactly what ApproveWrite hits when the
// chat model proposes a file under a path that doesn't exist yet —
// os.MkdirAll creates the directory as a side effect of Files.Write, and
// without this fix the editor's git panel/badge would silently miss the
// change until an unrelated git action forced a refresh.
func TestWatchCatchesWriteInsideDirectoryCreatedDuringSession(t *testing.T) {
	dir := newTestRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	changes, stop, err := Watch(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	// Let the watcher finish its initial fsnotify.Add calls before acting.
	time.Sleep(100 * time.Millisecond)

	newDir := filepath.Join(dir, "brandnewdir")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Drain the notification produced by the mkdir itself and let the
	// debounce window fully lapse, so the write below starts a fresh cycle
	// rather than riding along on the mkdir's own notification.
	select {
	case <-changes:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a notification for the mkdir itself")
	}
	time.Sleep(debounceWindow + 200*time.Millisecond)

	if err := os.WriteFile(filepath.Join(newDir, "new.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-changes:
	case <-time.After(2 * time.Second):
		t.Fatal("no change notification for a write inside a directory created earlier in the same session")
	}
}
