package store

import (
	"database/sql"
	"testing"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSeedFileAccessSettingsFromEnvSeedsOnce(t *testing.T) {
	db := openTestDB(t)

	if err := SeedFileAccessSettingsFromEnv(db, "/some/root", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := GetFileAccessSettings(db)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := FileAccessSettings{Root: "/some/root", ReadEnabled: true, WriteEnabled: true}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// This is the regression test for the bug the review caught: a user
// explicitly disabling file access must survive a server restart, not be
// silently re-seeded from env vars because the resulting row looks
// identical to "never configured."
func TestSeedFileAccessSettingsFromEnvDoesNotOverrideExplicitDisable(t *testing.T) {
	db := openTestDB(t)

	if err := SeedFileAccessSettingsFromEnv(db, "/some/root", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// User explicitly turns file access off via the Settings UI.
	if err := SaveFileAccessSettings(db, FileAccessSettings{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Simulate a server restart: seeding runs again with the same env vars.
	if err := SeedFileAccessSettingsFromEnv(db, "/some/root", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := GetFileAccessSettings(db)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := FileAccessSettings{}
	if got != want {
		t.Fatalf("explicit disable did not survive reseed: got %+v, want %+v", got, want)
	}
}

func TestSaveFileAccessSettingsRejectsWriteWithoutRead(t *testing.T) {
	db := openTestDB(t)

	err := SaveFileAccessSettings(db, FileAccessSettings{Root: "/x", ReadEnabled: false, WriteEnabled: true})
	if err == nil {
		t.Fatal("expected error for write_enabled without read_enabled")
	}
}

func TestGetFileAccessSettingsCoercesWriteWithoutRead(t *testing.T) {
	db := openTestDB(t)

	// Bypass SaveFileAccessSettings's validation to simulate a
	// hand-edited or migrated row with an inconsistent combination.
	if err := SaveFileAccessSettings(db, FileAccessSettings{Root: "/x", ReadEnabled: true, WriteEnabled: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := db.Exec(`UPDATE settings SET value = ? WHERE key = ?`,
		`{"root":"/x","read_enabled":false,"write_enabled":true}`, settingFileAccess); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := GetFileAccessSettings(db)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.WriteEnabled {
		t.Fatal("expected write_enabled to be coerced to false when read_enabled is false")
	}
}

// Regression test for the gap where a proposed/approved file write's diff
// (the PendingWriteCard) vanished completely on reload — nothing about it
// was ever saved to the database, only the plain-text SaveMessage call.
// SaveAssistantMessage/LoadMessages round-tripping PendingWrites is what
// lets a reopened conversation show the same diff card again.
func TestSaveAssistantMessageRoundTripsPendingWrites(t *testing.T) {
	db := openTestDB(t)
	convID, err := CreateConversation(db, defaultWorkspaceID, "test")
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	writes := []PendingWriteRow{
		{ID: "w1", Path: "src/main.go", NewContent: "package main\n", ExistingContent: "", FileExists: false, Status: "pending"},
	}
	msgID, err := SaveAssistantMessage(db, defaultWorkspaceID, convID, "I've proposed a change.", writes)
	if err != nil {
		t.Fatalf("SaveAssistantMessage: %v", err)
	}
	if msgID == 0 {
		t.Fatal("expected a non-zero message id")
	}

	msgs, err := LoadMessages(db, defaultWorkspaceID, convID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	got := msgs[0].PendingWrites
	if len(got) != 1 {
		t.Fatalf("got %d pending writes, want 1", len(got))
	}
	if got[0] != writes[0] {
		t.Errorf("round-tripped write = %+v, want %+v", got[0], writes[0])
	}
}

// A message with no proposed writes must round-trip as none at all
// (nil/empty), not as an empty-but-present array — mirrors how Images
// already behaves (see SaveMessage's doc comment), so callers can use one
// "len(m.PendingWrites) > 0" check without needing a null-vs-empty check
// on top of it too.
func TestSaveAssistantMessageWithNoPendingWrites(t *testing.T) {
	db := openTestDB(t)
	convID, err := CreateConversation(db, defaultWorkspaceID, "test")
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if _, err := SaveAssistantMessage(db, defaultWorkspaceID, convID, "no writes here", nil); err != nil {
		t.Fatalf("SaveAssistantMessage: %v", err)
	}
	msgs, err := LoadMessages(db, defaultWorkspaceID, convID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs[0].PendingWrites) != 0 {
		t.Errorf("got %d pending writes, want 0", len(msgs[0].PendingWrites))
	}
}

// SetMessagePendingWrites is how ApproveWrite/RejectWrite patch a write's
// Status once a human resolves it — this confirms the patched status
// actually survives a reload rather than only living in the in-memory
// value the caller already has.
func TestSetMessagePendingWritesUpdatesStatus(t *testing.T) {
	db := openTestDB(t)
	convID, err := CreateConversation(db, defaultWorkspaceID, "test")
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	writes := []PendingWriteRow{{ID: "w1", Path: "a.txt", Status: "pending"}}
	msgID, err := SaveAssistantMessage(db, defaultWorkspaceID, convID, "proposed", writes)
	if err != nil {
		t.Fatalf("SaveAssistantMessage: %v", err)
	}

	writes[0].Status = "approved"
	if err := SetMessagePendingWrites(db, msgID, writes); err != nil {
		t.Fatalf("SetMessagePendingWrites: %v", err)
	}

	msgs, err := LoadMessages(db, defaultWorkspaceID, convID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if got := msgs[0].PendingWrites[0].Status; got != "approved" {
		t.Errorf("status = %q, want %q", got, "approved")
	}
}

// Regression test for the "abandoned" case: a write that was still
// "pending" when the app last closed can no longer actually be approved
// or rejected (Handler.writes, the in-memory map those act on, starts
// empty on every process start — see abandonOrphanedPendingWrites' doc
// comment). Open (called by openTestDB, same as a real startup) must flip
// it to "abandoned" so a reloaded PendingWriteCard doesn't show live
// Approve/Reject buttons for a write that would just 404.
func TestAbandonOrphanedPendingWritesOnStartup(t *testing.T) {
	db := openTestDB(t)
	convID, err := CreateConversation(db, defaultWorkspaceID, "test")
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	writes := []PendingWriteRow{
		{ID: "w1", Path: "a.txt", Status: "pending"},
		{ID: "w2", Path: "b.txt", Status: "approved"},
	}
	if _, err := SaveAssistantMessage(db, defaultWorkspaceID, convID, "proposed", writes); err != nil {
		t.Fatalf("SaveAssistantMessage: %v", err)
	}

	// Simulates the app restarting — Open runs abandonOrphanedPendingWrites
	// against the same underlying data again.
	if err := abandonOrphanedPendingWrites(db); err != nil {
		t.Fatalf("abandonOrphanedPendingWrites: %v", err)
	}

	msgs, err := LoadMessages(db, defaultWorkspaceID, convID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	got := msgs[0].PendingWrites
	if got[0].Status != "abandoned" {
		t.Errorf("w1 status = %q, want %q (was pending)", got[0].Status, "abandoned")
	}
	if got[1].Status != "approved" {
		t.Errorf("w2 status = %q, want %q (already resolved, should be untouched)", got[1].Status, "approved")
	}
}
