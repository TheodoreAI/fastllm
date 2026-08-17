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
