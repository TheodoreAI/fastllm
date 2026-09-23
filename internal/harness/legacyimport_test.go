package harness

import (
	"errors"
	"strings"
	"testing"
	"time"

	"fastllm/internal/llm"
)

type fakeLegacySource struct {
	index    []LegacyConversation
	bodies   map[int64][]llm.Message
	loadErr  error
	loadCall int
}

func (f *fakeLegacySource) ListConversations() ([]LegacyConversation, error) {
	return f.index, nil
}

func (f *fakeLegacySource) LoadMessages(id int64) ([]llm.Message, error) {
	f.loadCall++
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.bodies[id], nil
}

func testRuntime() InteractiveRuntime {
	return InteractiveRuntime{MaxTurns: 20, CommandTimeout: time.Minute, AllowCommands: true, PermissionMode: PermissionAgent}
}

func newFakeSource() *fakeLegacySource {
	created := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	return &fakeLegacySource{
		index: []LegacyConversation{
			{ID: 1, Title: "Kept conversation", CreatedAt: created, UpdatedAt: created.Add(time.Hour)},
			{ID: 2, Title: "Empty conversation", CreatedAt: created, UpdatedAt: created},
		},
		bodies: map[int64][]llm.Message{
			1: {
				{Role: "user", Content: "what is the plan"},
				{Role: "system", Content: "internal rendering row"},
				{Role: "assistant", Content: "here is the plan"},
				{Role: "assistant", Content: "   "},
			},
			2: {{Role: "system", Content: "only a system row"}},
		},
	}
}

func TestImportLegacyConversationsRecoversHistory(t *testing.T) {
	store := &SessionStore{Dir: t.TempDir()}
	src := newFakeSource()

	report, err := ImportLegacyConversations(src, store, "/work", "test-model", testRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Imported) != 1 || report.Empty != 1 || report.Skipped != 0 {
		t.Fatalf("report = %+v", report)
	}

	loaded, err := store.Load(report.Imported[0])
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Title != "Kept conversation" || !loaded.CustomTitle {
		t.Fatalf("title not preserved: %+v", loaded)
	}
	if loaded.ImportedFrom != "sqlite:conversation/1" {
		t.Fatalf("origin tag = %q", loaded.ImportedFrom)
	}
	// Only user/assistant turns with real content survive: replaying the
	// rendering-only rows would corrupt the recovered context.
	if len(loaded.Messages) != 2 {
		t.Fatalf("messages = %+v", loaded.Messages)
	}
	if loaded.Messages[0].Role != "user" || loaded.Messages[1].Content != "here is the plan" {
		t.Fatalf("unexpected messages: %+v", loaded.Messages)
	}
	if loaded.WorkingDir != "/work" || loaded.Model != "test-model" {
		t.Fatalf("runtime context lost: %+v", loaded)
	}
}

func TestImportedConversationDoesNotHijackNextLaunch(t *testing.T) {
	store := &SessionStore{Dir: t.TempDir()}
	if _, err := ImportLegacyConversations(newFakeSource(), store, "/work", "test-model", testRuntime()); err != nil {
		t.Fatal(err)
	}
	// initializeSession auto-resumes the latest session only when it is unclosed.
	// Imports are history, so they must come in closed.
	latest, err := store.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if latest.ClosedAt == nil {
		t.Fatal("imported session is unclosed; it would hijack the next TUI launch")
	}
}

func TestImportLegacyConversationsIsIdempotent(t *testing.T) {
	store := &SessionStore{Dir: t.TempDir()}
	src := newFakeSource()

	first, err := ImportLegacyConversations(src, store, "/work", "test-model", testRuntime())
	if err != nil {
		t.Fatal(err)
	}
	second, err := ImportLegacyConversations(src, store, "/work", "test-model", testRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Imported) != 0 || second.Skipped != 1 {
		t.Fatalf("second run duplicated work: %+v", second)
	}

	sessions, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != len(first.Imported) {
		t.Fatalf("store holds %d sessions after re-import; want %d", len(sessions), len(first.Imported))
	}
}

func TestImportLegacyConversationsReportsLoadFailure(t *testing.T) {
	store := &SessionStore{Dir: t.TempDir()}
	src := newFakeSource()
	src.loadErr = errors.New("disk gone")

	_, err := ImportLegacyConversations(src, store, "/work", "test-model", testRuntime())
	if err == nil || !strings.Contains(err.Error(), "disk gone") {
		t.Fatalf("load failure was swallowed: %v", err)
	}
}

func TestImportLegacyConversationsRequiresSourceAndStore(t *testing.T) {
	if _, err := ImportLegacyConversations(nil, &SessionStore{Dir: t.TempDir()}, "/w", "m", testRuntime()); err == nil {
		t.Fatal("expected an error for a nil source")
	}
	if _, err := ImportLegacyConversations(newFakeSource(), nil, "/w", "m", testRuntime()); err == nil {
		t.Fatal("expected an error for a nil store")
	}
}

// installFakeOpener points the TUI commands at an in-memory legacy store.
func installFakeOpener(t *testing.T, src LegacyConversationSource) {
	t.Helper()
	previous := OpenLegacyConversations
	closed := false
	OpenLegacyConversations = func() (LegacyConversationSource, func() error, error) {
		return src, func() error { closed = true; return nil }, nil
	}
	t.Cleanup(func() {
		OpenLegacyConversations = previous
		if !closed {
			t.Error("legacy source was opened but never closed")
		}
	})
}

func TestTeaConversationsCommandListsLegacyDatabase(t *testing.T) {
	src := newFakeSource()
	installFakeOpener(t, src)
	m := &teaModel{workingDir: t.TempDir(), sessionStore: &SessionStore{Dir: t.TempDir()}}

	handled, _ := m.handleSessionSlash("/conversations", []string{"/conversations"}, "/conversations")
	if !handled {
		t.Fatal("/conversations was not handled")
	}
	plain := StripANSI(m.historyText.String())
	if !strings.Contains(plain, "Kept conversation") || !strings.Contains(plain, "Empty conversation") {
		t.Fatalf("conversation list not rendered: %q", plain)
	}
}

func TestTeaImportCommandImportsSingleConversation(t *testing.T) {
	src := newFakeSource()
	installFakeOpener(t, src)
	store := &SessionStore{Dir: t.TempDir()}
	m := &teaModel{workingDir: t.TempDir(), modelName: "test-model", sessionStore: store}

	m.handleSessionSlash("/import 1", []string{"/import", "1"}, "/import")

	sessions, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	// Conversation 2 must stay behind: /import <id> targets exactly one.
	if len(sessions) != 1 || sessions[0].ImportedFrom != "sqlite:conversation/1" {
		t.Fatalf("sessions = %+v", sessions)
	}
}

func TestTeaImportCommandRejectsBadArguments(t *testing.T) {
	m := &teaModel{workingDir: t.TempDir(), sessionStore: &SessionStore{Dir: t.TempDir()}}

	// A malformed id must not reach the database at all.
	OpenLegacyConversations = func() (LegacyConversationSource, func() error, error) {
		t.Error("a malformed id should be rejected before opening the store")
		return nil, nil, nil
	}
	t.Cleanup(func() { OpenLegacyConversations = nil })

	m.handleSessionSlash("/import zero", []string{"/import", "zero"}, "/import")
	m.handleSessionSlash("/import", []string{"/import"}, "/import")

	plain := StripANSI(m.historyText.String())
	if !strings.Contains(plain, "Not a conversation id") || !strings.Contains(plain, "Usage: /import") {
		t.Fatalf("bad arguments not reported: %q", plain)
	}
}

func TestTeaConversationsCommandWithoutLegacySupport(t *testing.T) {
	previous := OpenLegacyConversations
	OpenLegacyConversations = nil
	t.Cleanup(func() { OpenLegacyConversations = previous })

	m := &teaModel{workingDir: t.TempDir()}
	m.handleSessionSlash("/conversations", []string{"/conversations"}, "/conversations")

	if !strings.Contains(StripANSI(m.historyText.String()), "not built into this binary") {
		t.Fatalf("missing legacy support was not reported: %q", m.historyText.String())
	}
}
