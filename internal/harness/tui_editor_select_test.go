package harness

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestFileEditorSelectionReplaceAndDelete(t *testing.T) {
	e := newFileEditor("alpha\nbeta\ngamma", "")
	e.moveTo(0, 2, false)
	e.startSelection()
	e.moveTo(2, 1, false)
	if got := e.selectedText(); got != "pha\nbeta\ng" {
		t.Fatalf("selectedText = %q", got)
	}
	e.insert("X")
	if e.text() != "alXamma" || e.row != 0 || e.col != 3 {
		t.Fatalf("typing over the selection gave %q at %d:%d", e.text(), e.row, e.col)
	}
	if !e.undoEdit() || e.text() != "alpha\nbeta\ngamma" {
		t.Fatalf("one undo should restore the replaced selection, got %q", e.text())
	}

	// A selection made backwards deletes the same way.
	e.moveTo(1, 4, false)
	e.startSelection()
	e.moveTo(1, 0, false)
	e.backspace()
	if e.text() != "alpha\n\ngamma" {
		t.Fatalf("backspace over a selection gave %q", e.text())
	}
}

func TestFileEditorIndentAndOutdentSelectedLines(t *testing.T) {
	e := newFileEditor("a\nb\n\nc", "")
	e.moveTo(0, 1, false)
	e.startSelection()
	e.moveTo(3, 0, false) // ends at column 0: line 4 is not included
	e.indentLines("\t", false)
	if e.text() != "\ta\n\tb\n\nc" {
		t.Fatalf("indent = %q", e.text())
	}
	if e.anchor.col != 2 {
		t.Errorf("anchor should move with its line's indent, col %d", e.anchor.col)
	}
	e.indentLines("\t", true)
	if e.text() != "a\nb\n\nc" {
		t.Fatalf("outdent = %q", e.text())
	}

	s := newFileEditor("        x", "")
	s.indentLines("    ", true)
	if s.text() != "    x" {
		t.Fatalf("outdent spaces = %q", s.text())
	}
}

func newEditorOn(t *testing.T, content string) *teaModel {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newEditorTestModel(t, dir)
	if err := m.openEditor("f.txt", false); err != nil {
		t.Fatal(err)
	}
	return m
}

// stubEditorClipboard replaces the system clipboard for one test.
func stubEditorClipboard(t *testing.T) *string {
	t.Helper()
	var board string
	oldWrite, oldRead := writeClipboardText, readClipboardText
	writeClipboardText = func(s string) error { board = s; return nil }
	readClipboardText = func() (string, error) { return board, nil }
	t.Cleanup(func() { writeClipboardText, readClipboardText = oldWrite, oldRead })
	return &board
}

// runCmd executes a command and feeds its messages back into the model, the
// way the Bubble Tea runtime would.
func runCmd(t *testing.T, m *teaModel, cmd tea.Cmd) *teaModel {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			m = runCmd(t, m, c)
		}
	case nil:
	default:
		updated, next := m.Update(msg)
		m = runCmd(t, updated.(*teaModel), next)
	}
	return m
}

func editorKey(t *testing.T, m *teaModel, msg tea.KeyPressMsg) *teaModel {
	t.Helper()
	updated, cmd := m.Update(msg)
	return runCmd(t, updated.(*teaModel), cmd)
}

func TestEditorShiftArrowsSelectAndClipboardKeysCopyCutPaste(t *testing.T) {
	board := stubEditorClipboard(t)
	m := newEditorOn(t, "hello world\nsecond\n")

	m = editorKey(t, m, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift | tea.ModCtrl})
	if got := m.editor.selectedText(); got != "hello " {
		t.Fatalf("ctrl+shift+right selected %q", got)
	}
	m = editorKey(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if *board != "hello " {
		t.Fatalf("clipboard = %q after copy", *board)
	}
	m = editorKey(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if m.editor.anchor != nil {
		t.Fatal("a move without Shift should drop the selection")
	}
	m = editorKey(t, m, tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if m.editor.lines[0] != "hello worldhello " {
		t.Fatalf("paste gave %q", m.editor.lines[0])
	}

	// Cut with nothing selected takes the whole line; a line paste goes in
	// above the cursor's line, leaving the cursor where it was in the text.
	// (The buffer holds the file without its final newline.)
	m = editorKey(t, m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if m.editor.text() != "second" || *board != "hello worldhello \n" {
		t.Fatalf("line cut left %q, clipboard %q", m.editor.text(), *board)
	}
	m = editorKey(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
	m = editorKey(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m = editorKey(t, m, tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if m.editor.text() != "hello worldhello \nsecond" || m.editor.row != 1 || m.editor.col != 1 {
		t.Fatalf("line paste gave %q, cursor %d:%d", m.editor.text(), m.editor.row, m.editor.col)
	}

	m = editorKey(t, m, tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	m = editorKey(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.editor.text() != "" {
		t.Fatalf("select all + backspace left %q", m.editor.text())
	}
}

func TestEditorEscClearsSelectionBeforeClosing(t *testing.T) {
	m := newEditorOn(t, "abc\n")
	m = editorKey(t, m, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	m = editorKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.editor == nil || m.editor.anchor != nil {
		t.Fatal("first Esc should only clear the selection")
	}
	m = editorKey(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.editor != nil {
		t.Fatal("second Esc should close the unmodified file")
	}
}

func TestEditorWheelScrollsWithoutMovingTheCursorAndDragSelects(t *testing.T) {
	var content string
	for i := range 100 {
		content += "line " + string(rune('a'+i%26)) + "\n"
	}
	m := newEditorOn(t, content)
	m.render()

	for range 5 {
		updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		m = updated.(*teaModel)
	}
	m.render()
	if m.editor.row != 0 || m.editor.top != 15 {
		t.Fatalf("after scrolling, cursor row %d top %d; want 0 and 15", m.editor.row, m.editor.top)
	}

	_, gutter, _ := m.editorGeometry()
	updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: gutter + 2, Y: 1})
	m = updated.(*teaModel)
	updated, _ = m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: gutter + 4, Y: 3})
	m = updated.(*teaModel)
	updated, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: gutter + 4, Y: 3})
	m = updated.(*teaModel)
	start, end, ok := m.editor.selection()
	if !ok || start != (editorPos{15, 2}) || end != (editorPos{17, 4}) {
		t.Fatalf("drag selected %v..%v (ok %v), want 15:2..17:4", start, end, ok)
	}
	m.render()
	if m.editor.top != 15 {
		t.Errorf("clicking in view should not scroll it, top %d", m.editor.top)
	}

	m = editorKey(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.editor.anchor != nil || m.editor.row != 18 {
		t.Errorf("a key after the drag should move from the drag's end, row %d", m.editor.row)
	}
}
