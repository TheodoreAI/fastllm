package harness

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func clickEditorAction(t *testing.T, m *teaModel, action string) {
	t.Helper()
	frame := strings.Split(ansi.Strip(m.render()), "\n")
	for y := range m.height {
		for x := range m.width {
			if m.hits.at(x, y) == "editor:"+action {
				if y >= len(frame) || strings.TrimSpace(ansi.Cut(frame[y], x, x+3)) == "" {
					t.Fatalf("action %s has no visible label at %d,%d\n%s", action, x, y, strings.Join(frame, "\n"))
				}
				m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
				return
			}
		}
	}
	t.Fatalf("missing action %s", action)
}

func TestEditorChromeActionsAndDialogs(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 30}, {160, 45}} {
		m := newEditorOn(t, "original\n")
		m.width, m.height = size[0], size[1]
		m.editor.rel = strings.Repeat("long-directory/", 15) + "file.go"
		m.editor.insert("draft")
		clickEditorAction(t, m, "help")
		if !m.editor.help {
			t.Fatal("Help did not open")
		}
		before := m.editor.text()
		m.handleEditorPaste("must not paste into help")
		if m.editor.text() != before {
			t.Fatal("help allowed paste")
		}
		clickEditorAction(t, m, "cancel")
		clickEditorAction(t, m, "close")
		if m.editor.prompt != editorPromptClose {
			t.Fatal("Close did not ask about unsaved edits")
		}
		frame := m.render()
		for _, label := range []string{"Save and continue", "Discard edits", "Keep editing", "file.go"} {
			if !strings.Contains(ansi.Strip(frame), label) {
				t.Fatalf("missing %s at %v", label, size)
			}
		}
		m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: 0})
		if m.editor.prompt != editorPromptClose {
			t.Fatal("outside click bypassed confirmation")
		}
		clickEditorAction(t, m, "cancel")
		if m.editor.text() != before {
			t.Fatal("cancel lost draft")
		}
		m.editor.prompt = editorPromptConflict
		frame = m.render()
		if !strings.Contains(ansi.Strip(frame), "Reload disk and discard") {
			t.Fatal("conflict choices clipped")
		}
		clickEditorAction(t, m, "cancel")
		clickEditorAction(t, m, "close")
		clickEditorAction(t, m, "discard")
		if m.editor != nil {
			t.Fatal("Discard did not close")
		}
	}
}

func TestSourceEditorIndentFocusAndMouseActions(t *testing.T) {
	_, m := sourceFixture(t)
	if err := m.openEditor("file.txt", false); err != nil {
		t.Fatal(err)
	}
	m.source.pane = 1
	m.editor.indent = "  "
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !strings.HasPrefix(m.editor.text(), "  ") || m.source.pane != 1 {
		t.Fatal("Tab did not indent editor")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if strings.HasPrefix(m.editor.text(), "  ") {
		t.Fatal("Shift+Tab did not outdent")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	if m.source.pane != 2 || m.editorFocused() {
		t.Fatal("F6 did not move focus")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyF6, Mod: tea.ModShift})
	if m.source.pane != 1 || !m.editorFocused() {
		t.Fatal("Shift+F6 did not restore focus")
	}
	clickEditorAction(t, m, "save")
	if m.editor.dirty {
		t.Fatal("mouse Save did not save")
	}
	clickEditorAction(t, m, "help")
	clickEditorAction(t, m, "cancel")
	clickEditorAction(t, m, "diff")
	if m.editor != nil {
		t.Fatal("mouse Diff did not return to diff")
	}
}
