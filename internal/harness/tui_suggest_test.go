package harness

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// suggestModel is a TUI model driven through the real Update loop.
func suggestModel(t *testing.T) *teaModel {
	t.Helper()
	input := textarea.New()
	input.Focus()
	m := &teaModel{
		input: input, viewport: viewport.New(80, 20),
		width: 100, height: 40, ready: true,
		historyIdx: -1, workingDir: t.TempDir(),
		sessionStore: &SessionStore{Dir: t.TempDir()},
	}
	m.syncInputHeight()
	m.resizeViewport()
	return m
}

func send(m *teaModel, msg tea.KeyMsg) tea.Cmd {
	_, cmd := m.Update(msg)
	return cmd
}

func typeInto(m *teaModel, text string) {
	for _, r := range text {
		if r == ' ' {
			send(m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			continue
		}
		send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func suggestionInserts(m *teaModel) []string {
	var out []string
	for _, s := range m.suggest.items {
		out = append(out, s.Insert)
	}
	return out
}

func TestSuggestionsAppearAndNarrow(t *testing.T) {
	m := suggestModel(t)
	typeInto(m, "/")
	if len(m.suggest.items) != len(slashCommandList()) {
		t.Fatalf("a bare / should list every command, got %d", len(m.suggest.items))
	}
	typeInto(m, "sess")
	if got := suggestionInserts(m); len(got) == 0 || got[0] != "/sessions" {
		t.Fatalf("/sess = %v", got)
	}
	typeInto(m, "zzz")
	if len(m.suggest.items) != 0 {
		t.Fatal("no match should hide the list")
	}
}

func TestSuggestionsShrinkTheViewport(t *testing.T) {
	m := suggestModel(t)
	before := m.viewport.Height
	typeInto(m, "/")
	if m.viewport.Height != before-(maxSuggestionRows+2) {
		t.Fatalf("viewport %d -> %d, want a drop of %d", before, m.viewport.Height, maxSuggestionRows+2)
	}
	if lines := strings.Count(m.View(), "\n") + 1; lines > m.height {
		t.Fatalf("frame is %d rows, terminal is %d", lines, m.height)
	}
	send(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.viewport.Height != before {
		t.Fatal("hiding the list should give the rows back")
	}
}

func TestTabCompletesAndEnterIsSmart(t *testing.T) {
	m := suggestModel(t)
	typeInto(m, "/ren")
	send(m, tea.KeyMsg{Type: tea.KeyTab})
	if got := m.input.Value(); got != "/rename " {
		t.Fatalf("Tab gave %q", got)
	}
	if len(m.suggest.items) != 0 || !strings.Contains(m.suggest.usage, "/rename <title>") {
		t.Fatalf("free-text argument should show usage, not a list: %+v", m.suggest)
	}

	m = suggestModel(t)
	typeInto(m, "/ren")
	send(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.input.Value(); got != "/rename " {
		t.Fatalf("Enter on a command needing an argument should complete it, got %q", got)
	}

	m = suggestModel(t)
	typeInto(m, "/sess")
	send(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.sessionsModal == nil || m.input.Value() != "" {
		t.Fatal("Enter on /sessions should run it and open the menu")
	}
}

func TestDestructiveCommandsNeverRunFromTheList(t *testing.T) {
	m := suggestModel(t)
	typeInto(m, "/und")
	send(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.input.Value(); got != "/undo" {
		t.Fatalf("Enter should only fill in /undo, got %q", got)
	}
	if len(m.suggest.items) != 0 {
		t.Fatal("the list should hide so the next Enter is deliberate")
	}
}

func TestExactCommandRunsAsTyped(t *testing.T) {
	m := suggestModel(t)
	typeInto(m, "/sessions")
	send(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.sessionsModal == nil {
		t.Fatal("typing a full command then Enter should run it")
	}
}

func TestArgumentSuggestions(t *testing.T) {
	m := suggestModel(t)
	typeInto(m, "/set think ")
	if got := strings.Join(suggestionInserts(m), ","); got != "off,low,medium,high" {
		t.Fatalf("/set think = %s", got)
	}
	typeInto(m, "hi")
	if got := suggestionInserts(m); len(got) != 1 || got[0] != "high" {
		t.Fatalf("/set think hi = %v", got)
	}

	m = suggestModel(t)
	session := m.sessionStore.New(m.workingDir, "", InteractiveRuntime{})
	session.Title, session.CustomTitle = "Fix auth retry", true
	if err := m.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	typeInto(m, "/resume ")
	var found bool
	for _, s := range m.suggest.items {
		if s.Label == "Fix auth retry" && s.Insert == session.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("/resume should list sessions by title and insert the ID: %+v", m.suggest.items)
	}
	typeInto(m, "auth")
	send(m, tea.KeyMsg{Type: tea.KeyTab})
	if got := m.input.Value(); got != "/resume "+session.ID {
		t.Fatalf("Tab should insert the session ID, got %q", got)
	}
}

func TestEscDismissesUntilInputChanges(t *testing.T) {
	m := suggestModel(t)
	typeInto(m, "/se")
	send(m, tea.KeyMsg{Type: tea.KeyEsc})
	if len(m.suggest.items) != 0 {
		t.Fatal("Esc should hide the list")
	}
	send(m, tea.KeyMsg{Type: tea.KeyDown}) // must not move a hidden list
	typeInto(m, "s")
	if len(m.suggest.items) == 0 {
		t.Fatal("typing again should bring the list back")
	}
}

func TestArrowKeysMoveTheHighlightNotHistory(t *testing.T) {
	m := suggestModel(t)
	m.promptHistory = []string{"earlier prompt"}
	typeInto(m, "/s")
	send(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.suggest.cursor != 1 || m.input.Value() != "/s" {
		t.Fatalf("↓ should move the highlight (cursor %d, input %q)", m.suggest.cursor, m.input.Value())
	}
	send(m, tea.KeyMsg{Type: tea.KeyUp})
	send(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.suggest.cursor != 0 || m.input.Value() != "/s" {
		t.Fatal("↑ at the top should stay in the list, not recall history")
	}
}

func TestNoSuggestionsWhenNotApplicable(t *testing.T) {
	m := suggestModel(t)
	m.mode = modeShell
	typeInto(m, "/s")
	if len(m.suggest.items) != 0 {
		t.Fatal("no suggestions in Shell Mode")
	}

	m = suggestModel(t)
	typeInto(m, "hello /s")
	if len(m.suggest.items) != 0 {
		t.Fatal("only a leading slash starts suggestions")
	}

	m = suggestModel(t)
	m.pendingPermission = &teaPermissionRequestMsg{}
	m.input.SetValue("/s")
	m.refreshSuggestions()
	if len(m.suggest.items) != 0 {
		t.Fatal("no suggestions while a permission prompt is open")
	}
}

func TestTabTogglesShellModeWhenNoList(t *testing.T) {
	m := suggestModel(t)
	send(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.mode != modeShell {
		t.Fatal("Tab on an empty input should still toggle Shell Mode")
	}
}
