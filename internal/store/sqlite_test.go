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

func TestConversationLifecycle(t *testing.T) {
	db := openTestDB(t)

	convID, err := CreateConversation(db, defaultWorkspaceID, "Test Thread")
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	exists, err := ConversationExists(db, defaultWorkspaceID, convID)
	if err != nil || !exists {
		t.Fatalf("ConversationExists = %v, err: %v", exists, err)
	}

	msgID, err := SaveMessage(db, defaultWorkspaceID, convID, "user", "Hello", nil)
	if err != nil || msgID == 0 {
		t.Fatalf("SaveMessage failed: %v", err)
	}

	msgs, err := LoadMessages(db, defaultWorkspaceID, convID)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("LoadMessages got %d messages, err: %v", len(msgs), err)
	}
	if msgs[0].Content != "Hello" {
		t.Errorf("expected 'Hello', got %q", msgs[0].Content)
	}

	if err := UpdateConversationTitle(db, defaultWorkspaceID, convID, "Renamed Thread"); err != nil {
		t.Fatalf("UpdateConversationTitle failed: %v", err)
	}

	convs, err := ListConversations(db, defaultWorkspaceID)
	if err != nil || len(convs) != 1 || convs[0].Title != "Renamed Thread" {
		t.Fatalf("ListConversations failed: %v", err)
	}

	if err := DeleteConversation(db, defaultWorkspaceID, convID); err != nil {
		t.Fatalf("DeleteConversation failed: %v", err)
	}

	exists, _ = ConversationExists(db, defaultWorkspaceID, convID)
	if exists {
		t.Fatal("expected conversation to be deleted")
	}
}

func TestSaveFileAccessSettingsValidation(t *testing.T) {
	db := openTestDB(t)

	err := SaveFileAccessSettings(db, FileAccessSettings{Root: "/x", ReadEnabled: false, WriteEnabled: true})
	if err == nil {
		t.Fatal("expected error for write_enabled without read_enabled")
	}

	err = SaveFileAccessSettings(db, FileAccessSettings{Root: "/x", ReadEnabled: true, WriteEnabled: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	settings, err := GetFileAccessSettings(db)
	if err != nil || !settings.WriteEnabled || settings.Root != "/x" {
		t.Fatalf("GetFileAccessSettings failed: %+v, err: %v", settings, err)
	}
}

func TestCloudProviderSettings(t *testing.T) {
	db := openTestDB(t)

	s := CloudProviderSettings{
		AnthropicAPIKey: "sk-ant-test",
		SelfHostedBaseURL:      "http://localhost:8010/v1",
	}
	if err := SaveCloudProviderSettings(db, s); err != nil {
		t.Fatalf("SaveCloudProviderSettings failed: %v", err)
	}

	loaded, err := GetCloudProviderSettings(db)
	if err != nil {
		t.Fatalf("GetCloudProviderSettings failed: %v", err)
	}
	if loaded.AnthropicAPIKey != "sk-ant-test" || loaded.SelfHostedBaseURL != "http://localhost:8010/v1" {
		t.Errorf("loaded settings mismatch: %+v", loaded)
	}
}
