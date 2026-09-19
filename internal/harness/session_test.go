package harness

import (
	"testing"
	"time"

	"fastllm/internal/llm"
)

func TestSessionStoreRoundTrip(t *testing.T) {
	store := &SessionStore{Dir: t.TempDir()}
	session := store.New("/work", "test-model", InteractiveRuntime{MaxTurns: 12, CommandTimeout: 30 * time.Second, ThinkLevel: "high", AllowCommands: true, PermissionMode: PermissionAsk})
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
	if loaded.Title != "Fix the parser" || loaded.Runtime.MaxTurns != 12 || loaded.Runtime.PermissionMode != PermissionAsk || len(loaded.Messages) != 3 {
		t.Fatalf("unexpected loaded session: %+v", loaded)
	}

	listed, err := store.List()
	if err != nil || len(listed) != 1 || listed[0].ID != session.ID {
		t.Fatalf("unexpected session list: %+v, %v", listed, err)
	}
}

func TestSessionStoreRejectsInvalidID(t *testing.T) {
	store := &SessionStore{Dir: t.TempDir()}
	if _, err := store.Load("../escape"); err == nil {
		t.Fatal("expected invalid session ID to fail")
	}
}
