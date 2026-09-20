package harness

import (
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

func TestPromptHistoryNavigation(t *testing.T) {
	ta := textarea.New()
	ta.Focus()

	m := &teaModel{
		input:         ta,
		promptHistory: []string{"first prompt", "second prompt", "third prompt"},
		historyIdx:    -1,
		ready:         true,
	}

	// 1. User types an unfinished draft
	m.input.SetValue("my unfinished draft")

	// 2. Press Up: should recall "third prompt" (the latest in history)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*teaModel)

	if m.historyIdx != 2 {
		t.Fatalf("expected historyIdx 2, got %d", m.historyIdx)
	}
	if m.input.Value() != "third prompt" {
		t.Fatalf("expected 'third prompt', got %q", m.input.Value())
	}
	if m.historyDraft != "my unfinished draft" {
		t.Fatalf("expected historyDraft 'my unfinished draft', got %q", m.historyDraft)
	}

	// 3. Press Up again: should recall "second prompt"
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*teaModel)

	if m.historyIdx != 1 {
		t.Fatalf("expected historyIdx 1, got %d", m.historyIdx)
	}
	if m.input.Value() != "second prompt" {
		t.Fatalf("expected 'second prompt', got %q", m.input.Value())
	}

	// 4. Press Up again: should recall "first prompt"
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*teaModel)

	if m.historyIdx != 0 {
		t.Fatalf("expected historyIdx 0, got %d", m.historyIdx)
	}
	if m.input.Value() != "first prompt" {
		t.Fatalf("expected 'first prompt', got %q", m.input.Value())
	}

	// 5. Press Up at oldest item: should stay at 0
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*teaModel)

	if m.historyIdx != 0 {
		t.Fatalf("expected historyIdx 0, got %d", m.historyIdx)
	}
	if m.input.Value() != "first prompt" {
		t.Fatalf("expected 'first prompt', got %q", m.input.Value())
	}

	// 6. Press Down: should move forward to "second prompt"
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*teaModel)

	if m.historyIdx != 1 {
		t.Fatalf("expected historyIdx 1, got %d", m.historyIdx)
	}
	if m.input.Value() != "second prompt" {
		t.Fatalf("expected 'second prompt', got %q", m.input.Value())
	}

	// 7. Press Down again: should move forward to "third prompt"
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*teaModel)

	if m.historyIdx != 2 {
		t.Fatalf("expected historyIdx 2, got %d", m.historyIdx)
	}
	if m.input.Value() != "third prompt" {
		t.Fatalf("expected 'third prompt', got %q", m.input.Value())
	}

	// 8. Press Down past the latest item: should restore "my unfinished draft"
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*teaModel)

	if m.historyIdx != -1 {
		t.Fatalf("expected historyIdx -1, got %d", m.historyIdx)
	}
	if m.input.Value() != "my unfinished draft" {
		t.Fatalf("expected restored draft 'my unfinished draft', got %q", m.input.Value())
	}

	// 9. Press Up to browse, then Esc: should cancel and restore draft
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*teaModel)
	if m.historyIdx != 2 {
		t.Fatalf("expected historyIdx 2, got %d", m.historyIdx)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*teaModel)
	if m.historyIdx != -1 {
		t.Fatalf("expected historyIdx -1 after Esc, got %d", m.historyIdx)
	}
	if m.input.Value() != "my unfinished draft" {
		t.Fatalf("expected restored draft 'my unfinished draft' after Esc, got %q", m.input.Value())
	}
}

func TestAddPromptHistory(t *testing.T) {
	m := &teaModel{
		promptHistory: make([]string, 0),
		historyIdx:    -1,
	}

	m.addPromptHistory("cmd 1")
	if len(m.promptHistory) != 1 || m.promptHistory[0] != "cmd 1" {
		t.Fatalf("expected [cmd 1], got %v", m.promptHistory)
	}

	// Consecutive duplicate should not be appended
	m.addPromptHistory("cmd 1")
	if len(m.promptHistory) != 1 {
		t.Fatalf("expected no duplicate, got %v", m.promptHistory)
	}

	m.addPromptHistory("cmd 2")
	if len(m.promptHistory) != 2 || m.promptHistory[1] != "cmd 2" {
		t.Fatalf("expected [cmd 1, cmd 2], got %v", m.promptHistory)
	}
}
