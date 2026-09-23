package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fastllm/internal/llm"
)

func TestSessionStoreRoundTrip(t *testing.T) {
	store := &SessionStore{Dir: t.TempDir()}
	session := store.New("/work", "test-model", InteractiveRuntime{MaxTurns: 12, CommandTimeout: 30 * time.Second, ThinkLevel: "high", AllowCommands: true, PermissionMode: PermissionAgent})
	session.Messages = []llm.Message{{Role: "user", Content: "Fix the parser"}, {Role: "assistant", Content: "Done"}}
	session.Title = sessionTitle(session.Messages)
	if err := store.Save(session); err != nil {
		t.Fatal(err)
	}
	session.Messages = append(session.Messages, llm.Message{Role: "user", Content: "Run tests"})
	if err := store.Save(session); err != nil {
		t.Fatalf("overwrite saved session: %v", err)
	}

	loaded, err := store.Load(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Title != "Fix the parser" || loaded.Runtime.MaxTurns != 12 || loaded.Runtime.PermissionMode != PermissionAgent || len(loaded.Messages) != 3 {
		t.Fatalf("unexpected loaded session: %+v", loaded)
	}

	listed, err := store.List()
	if err != nil || len(listed) != 1 || listed[0].ID != session.ID {
		t.Fatalf("unexpected session list: %+v, %v", listed, err)
	}
}

func TestSessionStoreLifecycleAndRetention(t *testing.T) {
	store := &SessionStore{Dir: t.TempDir()}
	now := time.Now().UTC()
	empty := store.New(".", "model", InteractiveRuntime{})
	empty.UpdatedAt = now.Add(-48 * time.Hour)
	if err := store.Save(empty); err != nil {
		t.Fatal(err)
	}
	// Save refreshes UpdatedAt, so age the serialized session explicitly through its clock-neutral field.
	empty.UpdatedAt = now.Add(-48 * time.Hour)
	data, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir, empty.ID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	kept := store.New(".", "model", InteractiveRuntime{})
	kept.Messages = []llm.Message{{Role: "user", Content: "hello"}}
	if err := store.Save(kept); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Rename(kept.ID, "named"); err != nil {
		t.Fatal(err)
	}
	removed, err := store.Prune(now, 100)
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	latest, err := store.Latest()
	if err != nil || latest.Title != "named" {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	if err := store.Delete(kept.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSessionStoreRejectsInvalidID(t *testing.T) {
	store := &SessionStore{Dir: t.TempDir()}
	if _, err := store.Load("../escape"); err == nil {
		t.Fatal("expected invalid session ID to fail")
	}
}
