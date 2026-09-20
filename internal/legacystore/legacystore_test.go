package legacystore

import (
	"path/filepath"
	"testing"

	"fastllm/internal/harness"
	"fastllm/internal/store"
)

// seedLegacyDB builds a database shaped like the one the web frontend wrote.
func seedLegacyDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fastllm.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	kept, err := store.CreateConversation(db, WorkspaceID, "Planning the migration")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveMessage(db, WorkspaceID, kept, "user", "how do we migrate", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveMessage(db, WorkspaceID, kept, "assistant", "import the conversations first", nil); err != nil {
		t.Fatal(err)
	}

	if _, err := store.CreateConversation(db, WorkspaceID, "Never used"); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSourceReadsRealLegacyDatabase(t *testing.T) {
	src, err := Open(seedLegacyDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	index, err := src.ListConversations()
	if err != nil {
		t.Fatal(err)
	}
	if len(index) != 2 {
		t.Fatalf("conversations = %d; want 2", len(index))
	}

	var kept harness.LegacyConversation
	for _, conv := range index {
		if conv.Title == "Planning the migration" {
			kept = conv
		}
	}
	if kept.ID == 0 {
		t.Fatalf("seeded conversation missing from index: %+v", index)
	}
	// A timestamp that fails to parse would silently become the zero time and
	// give every imported session the same bogus date.
	if kept.UpdatedAt.IsZero() {
		t.Fatalf("updated_at did not parse: %+v", kept)
	}

	messages, err := src.LoadMessages(kept.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Content != "how do we migrate" {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestImportFromRealDatabaseEndToEnd(t *testing.T) {
	src, err := Open(seedLegacyDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	sessions := &harness.SessionStore{Dir: t.TempDir()}
	report, err := harness.ImportLegacyConversations(src, sessions, t.TempDir(), "test-model", harness.InteractiveRuntime{MaxTurns: 20})
	if err != nil {
		t.Fatal(err)
	}
	// One real conversation imports; the message-less one is ignored.
	if len(report.Imported) != 1 || report.Empty != 1 {
		t.Fatalf("report = %+v", report)
	}

	loaded, err := sessions.Load(report.Imported[0])
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Title != "Planning the migration" {
		t.Fatalf("title = %q", loaded.Title)
	}
	if len(loaded.Messages) != 2 || loaded.Messages[1].Content != "import the conversations first" {
		t.Fatalf("messages = %+v", loaded.Messages)
	}
}

func TestInstallerReportsMissingDatabase(t *testing.T) {
	t.Setenv("FASTLLM_DB", filepath.Join(t.TempDir(), "absent.db"))
	if _, _, err := Installer()(); err == nil {
		t.Fatal("expected an error for a missing database, not a freshly created empty one")
	}
}
