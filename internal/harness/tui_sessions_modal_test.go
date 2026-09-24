package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fastllm/internal/llm"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// sessionsModalFixture builds a model in dirHere with an active session and
// three saved ones: two elsewhere (one newer than everything) and one here.
func sessionsModalFixture(t *testing.T) (*teaModel, string, string) {
	t.Helper()
	store := &SessionStore{Dir: t.TempDir()}
	dirHere, dirElse := t.TempDir(), t.TempDir()
	m := &teaModel{workingDir: dirHere, sessionStore: store, input: textarea.New()}

	save := func(id, title, dir string, updated time.Time) {
		t.Helper()
		s := &InteractiveSession{
			ID: id, Title: title, CustomTitle: true, WorkingDir: dir,
			Messages: []llm.Message{{Role: "user", Content: title}},
		}
		if err := store.Save(s); err != nil {
			t.Fatal(err)
		}
		// Save stamps UpdatedAt with now; rewrite it to control ordering.
		loaded, _ := store.Load(id)
		loaded.UpdatedAt = updated
		data := mustMarshalSession(t, loaded)
		if err := os.WriteFile(filepath.Join(store.Dir, id+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	save("active", "Active work", dirHere, now.Add(-2*time.Hour))
	save("here-old", "Refactor auth cache", dirHere, now.Add(-72*time.Hour))
	save("else-new", "Webapp auth docs", dirElse, now)
	save("else-old", "Unrelated notes", dirElse, now.Add(-96*time.Hour))

	active, err := store.Load("active")
	if err != nil {
		t.Fatal(err)
	}
	m.activeSession = active
	return m, dirHere, dirElse
}

func mustMarshalSession(t *testing.T, s *InteractiveSession) []byte {
	t.Helper()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func visibleIDs(m *teaModel) []string {
	var ids []string
	for _, idx := range m.sessionsModal.visible {
		ids = append(ids, m.sessionsModal.all[idx].ID)
	}
	return ids
}

func typeKeys(m *teaModel, text string) {
	for _, r := range text {
		m.handleSessionsModalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func key(m *teaModel, k tea.KeyType) tea.Cmd {
	return m.handleSessionsModalKey(tea.KeyMsg{Type: k})
}

func TestSessionsModalOrdersCurrentDirectoryFirst(t *testing.T) {
	m, _, _ := sessionsModalFixture(t)
	m.openSessionsModal()
	if m.sessionsModal == nil {
		t.Fatal("menu did not open")
	}
	got := strings.Join(visibleIDs(m), ",")
	if got != "active,here-old,else-new,else-old" {
		t.Fatalf("order = %s", got)
	}
	if hl := m.sessionsModal.highlighted(); hl == nil || hl.ID != "active" {
		t.Fatalf("cursor should start on the active session, got %+v", hl)
	}
}

func TestSessionsModalFilterAndEscape(t *testing.T) {
	m, _, dirElse := sessionsModalFixture(t)
	m.openSessionsModal()

	typeKeys(m, "AUTH")
	if got := strings.Join(visibleIDs(m), ","); got != "here-old,else-new" {
		t.Fatalf("title filter = %s", got)
	}
	key(m, tea.KeyEsc)
	if m.sessionsModal == nil || m.sessionsModal.filter != "" || len(m.sessionsModal.visible) != 4 {
		t.Fatal("first Esc should clear the filter and keep the menu open")
	}

	typeKeys(m, "else-old")
	if got := strings.Join(visibleIDs(m), ","); got != "else-old" {
		t.Fatalf("id filter = %s", got)
	}
	key(m, tea.KeyEsc)
	typeKeys(m, filepath.Base(dirElse))
	if got := strings.Join(visibleIDs(m), ","); got != "else-new,else-old" {
		t.Fatalf("directory filter = %s", got)
	}
	key(m, tea.KeyEsc)
	key(m, tea.KeyEsc)
	if m.sessionsModal != nil {
		t.Fatal("second Esc should close the menu")
	}
}

func TestSessionsModalTabLimitsToCurrentDirectory(t *testing.T) {
	m, _, _ := sessionsModalFixture(t)
	m.openSessionsModal()
	key(m, tea.KeyTab)
	if got := strings.Join(visibleIDs(m), ","); got != "active,here-old" {
		t.Fatalf("here-only = %s", got)
	}
	key(m, tea.KeyTab)
	if len(m.sessionsModal.visible) != 4 {
		t.Fatal("Tab should toggle back to all directories")
	}
}

func TestSessionsModalEnterResumesHighlighted(t *testing.T) {
	m, _, _ := sessionsModalFixture(t)
	m.openSessionsModal()
	key(m, tea.KeyDown) // here-old
	key(m, tea.KeyEnter)
	if m.sessionsModal != nil {
		t.Fatal("menu should close after resuming")
	}
	if m.activeSessionID() != "here-old" {
		t.Fatalf("active = %s", m.activeSessionID())
	}
	if len(m.sessionMessages) != 1 || m.sessionMessages[0].Content != "Refactor auth cache" {
		t.Fatalf("messages not loaded: %+v", m.sessionMessages)
	}
	if prev, _ := m.sessionStore.Load("active"); prev.ClosedAt == nil {
		t.Fatal("previous session should be closed on switch")
	}
}

func TestSessionsModalDeleteConfirmAndGuard(t *testing.T) {
	m, _, _ := sessionsModalFixture(t)
	m.openSessionsModal()

	key(m, tea.KeyCtrlD) // cursor is on the active session
	if m.sessionsModal.confirmDelete {
		t.Fatal("deleting the active session must be refused")
	}
	if _, err := m.sessionStore.Load("active"); err != nil {
		t.Fatal("active session file was removed")
	}

	key(m, tea.KeyDown) // here-old
	key(m, tea.KeyCtrlD)
	typeKeys(m, "n")
	if _, err := m.sessionStore.Load("here-old"); err != nil {
		t.Fatal("n should cancel the delete")
	}
	if m.sessionsModal.filter != "" {
		t.Fatal("the confirm answer must not leak into the filter")
	}

	key(m, tea.KeyCtrlD)
	typeKeys(m, "y")
	if _, err := m.sessionStore.Load("here-old"); err == nil {
		t.Fatal("y should delete the session")
	}
	if got := strings.Join(visibleIDs(m), ","); got != "active,else-new,else-old" {
		t.Fatalf("list after delete = %s", got)
	}
}

func TestSessionsModalRenameActiveSurvivesAutosave(t *testing.T) {
	m, _, _ := sessionsModalFixture(t)
	m.activeSession.CustomTitle = false
	m.sessionMessages = m.activeSession.Messages
	m.openSessionsModal()

	key(m, tea.KeyCtrlR)
	for range []rune(m.sessionsModal.renameBuf) {
		key(m, tea.KeyBackspace)
	}
	typeKeys(m, "Renamed")
	key(m, tea.KeyEnter)
	if m.sessionsModal.renaming {
		t.Fatal("Enter should finish renaming")
	}
	if err := m.saveSession(); err != nil {
		t.Fatal(err)
	}
	saved, _ := m.sessionStore.Load("active")
	if saved.Title != "Renamed" || !saved.CustomTitle {
		t.Fatalf("title after autosave = %q (custom=%v)", saved.Title, saved.CustomTitle)
	}
}

func TestSessionsModalRenameInactive(t *testing.T) {
	m, _, _ := sessionsModalFixture(t)
	m.openSessionsModal()
	key(m, tea.KeyEnd) // else-old
	key(m, tea.KeyCtrlR)
	typeKeys(m, " v2")
	key(m, tea.KeyEnter)
	saved, _ := m.sessionStore.Load("else-old")
	if saved.Title != "Unrelated notes v2" {
		t.Fatalf("title = %q", saved.Title)
	}
}

func TestSessionsModalRefusesWhileExecuting(t *testing.T) {
	m, _, _ := sessionsModalFixture(t)
	m.isExecuting = true
	m.openSessionsModal()
	if m.sessionsModal != nil {
		t.Fatal("menu must not open during a run")
	}
}

func TestSessionsCommandsOpenMenuOrPrintTable(t *testing.T) {
	m, _, _ := sessionsModalFixture(t)
	m.handleSessionSlash("/resume", []string{"/resume"}, "/resume")
	if m.sessionsModal == nil {
		t.Fatal("bare /resume should open the menu")
	}
	m.closeSessionsModal()

	m.handleSessionSlash("/sessions", []string{"/sessions"}, "/sessions")
	if m.sessionsModal == nil {
		t.Fatal("/sessions should open the menu")
	}
	m.closeSessionsModal()

	m.handleSessionSlash("/sessions list", []string{"/sessions", "list"}, "/sessions")
	if m.sessionsModal != nil {
		t.Fatal("/sessions list should not open the menu")
	}
	if !strings.Contains(StripANSI(m.historyText.String()), "Saved Sessions") {
		t.Fatal("/sessions list should print the table")
	}
}

func TestSessionsModalRenders(t *testing.T) {
	m, _, _ := sessionsModalFixture(t)
	m.width, m.height = 100, 40
	m.openSessionsModal()
	out := StripANSI(m.renderSessionsModal())
	for _, want := range []string{"SESSIONS · 4 saved", "Active work", "(here)", "Webapp auth docs", "Del delete"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}

func TestSessionsModalDeleteAndF2Keys(t *testing.T) {
	m, _, _ := sessionsModalFixture(t)
	m.openSessionsModal()
	key(m, tea.KeyDown) // here-old
	key(m, tea.KeyDelete)
	if !m.sessionsModal.confirmDelete {
		t.Fatal("Delete should ask for confirmation")
	}
	key(m, tea.KeyEnter)
	if _, err := m.sessionStore.Load("here-old"); err == nil {
		t.Fatal("confirmed delete should remove the session")
	}

	key(m, tea.KeyF2)
	if !m.sessionsModal.renaming {
		t.Fatal("F2 should start renaming")
	}
}

func TestSessionsModalIgnoresControlRunes(t *testing.T) {
	m, _, _ := sessionsModalFixture(t)
	m.openSessionsModal()
	m.handleSessionsModalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0}})
	m.handleSessionsModalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{4}})
	if m.sessionsModal.filter != "" {
		t.Fatalf("control runes leaked into the filter: %q", m.sessionsModal.filter)
	}
}
