package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestFileEditorTypingNewlineAndUndo(t *testing.T) {
	e := newFileEditor("func f() {\n\treturn\n}", "go")
	e.moveTo(1, len("\treturn"), false)
	for _, r := range " nil" {
		e.insert(string(r))
	}
	if e.lines[1] != "\treturn nil" {
		t.Fatalf("typed line = %q", e.lines[1])
	}
	e.newline()
	if e.lines[2] != "\t" || e.row != 2 || e.col != 1 {
		t.Fatalf("newline should carry the indent: lines=%q row=%d col=%d", e.lines, e.row, e.col)
	}
	e.backspace()
	e.backspace()
	if e.text() != "func f() {\n\treturn nil\n}" {
		t.Fatalf("after backspaces = %q", e.text())
	}

	// The typed run undoes as one step, after the deletes and the newline.
	for e.undoEdit() {
	}
	if e.text() != "func f() {\n\treturn\n}" {
		t.Fatalf("after undoing everything = %q", e.text())
	}
	if !e.redoEdit() || e.lines[1] != "\treturn nil" {
		t.Fatalf("redo should restore the typed run, got %q", e.lines[1])
	}
}

func TestFileEditorPasteAndDeleteForwardJoinLines(t *testing.T) {
	e := newFileEditor("ab", "")
	e.moveTo(0, 1, false)
	e.insert("1\r\n2\n3")
	if e.text() != "a1\n2\n3b" || e.row != 2 || e.col != 1 {
		t.Fatalf("paste = %q at %d:%d", e.text(), e.row, e.col)
	}
	e.moveTo(0, 2, false)
	e.deleteForward()
	if e.text() != "a12\n3b" {
		t.Fatalf("delete at end of line = %q", e.text())
	}
}

func TestFileEditorVerticalMoveKeepsDisplayColumnAcrossTabs(t *testing.T) {
	e := newFileEditor("\tx = 1\nabcdefgh\n\ty", "")
	e.moveTo(0, 2, false) // after "\tx": display column 5
	e.moveVertical(1)
	if e.col != 5 {
		t.Fatalf("col on the untabbed line = %d, want 5", e.col)
	}
	e.moveVertical(1)
	if e.col != 2 {
		t.Fatalf("col clamps to the short line's end, got %d", e.col)
	}
}

func TestRenderEditorLineIsExactlyWidthAndHidesControlBytes(t *testing.T) {
	for _, line := range []string{"", "\tif x {", "日本語のテキスト", "evil\x1b[2Jtext", strings.Repeat("y", 200)} {
		tokens := LexLine(line, "go")
		for _, left := range []int{0, 1, 3} {
			for _, marks := range []editorLineMarks{
				{cursor: 2, selStart: 0, selEnd: 0},
				{cursor: -1, selStart: 1, selEnd: 999}, // selection through the line break
				{cursor: 30, selStart: 0, selEnd: 5},
			} {
				out := renderEditorLine(tokens, left, 20, marks)
				if got := ansi.StringWidth(out); got != 20 {
					t.Errorf("line %q left %d marks %+v: width %d, want 20", line, left, marks, got)
				}
			}
			out := renderEditorLine(tokens, left, 20, noLineMarks)
			if strings.Contains(out, "\x1b[2J") {
				t.Errorf("control sequence from the file reached the terminal: %q", out)
			}
		}
	}
}

func TestComputeLineMarks(t *testing.T) {
	old := []string{"a", "b", "c", "d", "e"}
	cur := []string{"a", "B", "c", "new", "d"}
	got := string(computeLineMarks(old, cur))
	want := "\x00~\x00+-"
	if got != want {
		t.Errorf("marks = %q, want %q", got, want)
	}
	if got := computeLineMarks(nil, []string{"x", "y"}); string(got) != "++" {
		t.Errorf("new file marks = %q", got)
	}
	if got := computeLineMarks(old, old); strings.Trim(string(got), "\x00") != "" {
		t.Errorf("unchanged file marks = %q", got)
	}
}

func newEditorTestModel(t *testing.T, dir string) *teaModel {
	t.Helper()
	ta := textarea.New()
	return &teaModel{
		runner:        NewRunner(&mockLLM{}, dir, "test-model"),
		workingDir:    dir,
		checkpointMgr: NewCheckpointManager(dir),
		modelName:     "test-model",
		input:         ta,
		viewport:      viewport.New(viewport.WithWidth(140), viewport.WithHeight(20)),
		width:         140,
		height:        30,
		ready:         true,
	}
}

func press(t *testing.T, m *teaModel, msgs ...tea.Msg) *teaModel {
	t.Helper()
	for _, msg := range msgs {
		updated, _ := m.Update(msg)
		m = updated.(*teaModel)
	}
	return m
}

func typed(s string) []tea.Msg {
	var msgs []tea.Msg
	for _, r := range s {
		msgs = append(msgs, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return msgs
}

func TestEditorSavesThroughTheKeyboardAndKeepsCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("one\r\ntwo\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newEditorTestModel(t, dir)
	m.input.SetValue("/edit notes.txt")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.editor == nil {
		t.Fatalf("/edit did not open the editor (status %q)", m.statusNotice)
	}
	m = press(t, m, typed("zero ")...)
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	data, _ := os.ReadFile(path)
	if string(data) != "zero one\r\ntwo\r\n" {
		t.Fatalf("saved %q", data)
	}
	if m.editor.dirty {
		t.Fatal("buffer still dirty after save")
	}
	if frame := m.render(); !strings.Contains(StripANSI(frame), "zero one") {
		t.Errorf("editor frame missing the text:\n%s", StripANSI(frame))
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.editor != nil {
		t.Fatal("Esc on a saved buffer should close the editor")
	}
}

func TestEditorAsksBeforeDiscardingAndBeforeOverwritingOtherChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newEditorTestModel(t, dir)
	if err := m.openEditor("a.go", false); err != nil {
		t.Fatal(err)
	}
	m = press(t, m, typed("x")...)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.editor == nil || m.editor.prompt != editorPromptClose {
		t.Fatal("Esc with unsaved changes should ask first")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	// Someone else (the agent, a command) writes the file meanwhile.
	if err := os.WriteFile(path, []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.editor.prompt != editorPromptConflict {
		t.Fatal("save over an outside change should ask first")
	}
	if data, _ := os.ReadFile(path); string(data) != "package b\n" {
		t.Fatalf("the outside change was overwritten without asking: %q", data)
	}
	m = press(t, m, typed("r")...)
	if m.editor.text() != "package b" || m.editor.dirty {
		t.Fatalf("reload gave %q (dirty %v)", m.editor.text(), m.editor.dirty)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.editor != nil {
		t.Fatal("Esc after reload should close the editor")
	}
}

func TestEditorRefusesPathsOutsideTheWorkspaceAndGitInternals(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newEditorTestModel(t, dir)
	for _, p := range []string{"../outside.txt", ".git/config", filepath.Join(t.TempDir(), "x.txt")} {
		if err := m.openEditor(p, false); err == nil {
			t.Errorf("openEditor(%q) should be refused", p)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "bin"), []byte("a\x00b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.openEditor("bin", false); err == nil {
		t.Error("a binary file should be refused")
	}
}

func TestEditorDiffRoundTripInGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	git("config", "core.autocrlf", "false")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "init")

	m := newEditorTestModel(t, dir)
	if err := m.openEditor("main.go", false); err != nil {
		t.Fatal(err)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown}, tea.KeyPressMsg{Code: tea.KeyDown})
	m = press(t, m, typed("// hi")...)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m.render()
	// "// hi" then Enter at the start of line 3 inserts a line above it.
	if len(m.editor.marks) < 3 || m.editor.marks[2] != '+' {
		t.Errorf("inserted line should be marked added, marks %q", m.editor.marks)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if m.editor != nil || !m.diffModal {
		t.Fatalf("Ctrl+D should open the diff modal (notice %q)", m.editor.notice)
	}
	frame := StripANSI(m.render())
	for _, want := range []string{"HEAD", "Working tree", "func main() {}", "// hi"} {
		if !strings.Contains(frame, want) {
			t.Errorf("side-by-side diff missing %q:\n%s", want, frame)
		}
	}
	m = press(t, m, typed("v")...)
	if frame := StripANSI(m.render()); m.diffShownSplit || strings.Contains(frame, SymVLine+" Working tree") {
		t.Errorf("v should switch to the single-column view:\n%s", frame)
	}
	m = press(t, m, typed("e")...)
	if m.editor == nil || !m.editor.returnToDiff {
		t.Fatal("e in the diff modal should open the editor")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.diffModal {
		t.Error("closing an editor opened from the diff modal should return to it")
	}
}
