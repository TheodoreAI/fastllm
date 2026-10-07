package harness

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSourceEditorScrollReachesEOFAndSurvivesRedraw(t *testing.T) {
	var lines []string
	for i := range 200 {
		lines = append(lines, fmt.Sprintf("line %03d", i+1))
	}
	for _, size := range [][2]int{{80, 24}, {120, 30}, {160, 45}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := newEditorOn(t, strings.Join(lines, "\n"))
			m.openSourceControl("") // initialize view without a Git query
			m.source.pane = 1
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.render()
			sidebar, _, _ := m.sourceGeometry()
			x := sidebar + 4
			if size[0] < 90 {
				x = 4
			}
			// A stale button-down must not revive a drag after wheel scrolling.
			m.editor.dragging = true
			for range 80 {
				m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: x, Y: 6})
				m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: x, Y: 6})
				m.render()
			}
			top := m.editor.top
			if top <= 38 || !strings.Contains(StripANSI(m.render()), "line 200") {
				t.Fatalf("cannot scroll past line 38 to EOF: top=%d", top)
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyF2})
			m.Update(tea.MouseMotionMsg{Button: tea.MouseNone, X: 0, Y: 0})
			m.Update(teaStatusClearMsg{})
			if m.editor.top != top || !strings.Contains(StripANSI(m.render()), "line 200") {
				t.Fatal("unrelated events moved the editor viewport")
			}
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1] - 2})
			if m.editor.top != top {
				t.Fatal("resize moved the top line")
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl})
			for range 199 {
				m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
				m.render()
			}
			if m.editor.row != 199 || !strings.Contains(StripANSI(m.render()), "line 200") {
				t.Fatal("arrow navigation cannot reach EOF")
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl})
			for range 30 {
				m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
				m.render()
			}
			if m.editor.row != 199 {
				t.Fatal("paging cannot reach EOF")
			}
			for _, row := range strings.Split(m.render(), "\n") {
				if ansi.StringWidth(row) > size[0] {
					t.Fatal("editor row exceeds terminal width")
				}
			}
		})
	}
}

func TestEditorInlineReviewWordHighlightsAndReadOnlyDeletions(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	m := newEditorOn(t, "package main\nvar value = 20\nnew line")
	s := m.editor
	s.hasHead = true
	s.head = []string{"package main", "var value = 10", "removed"}
	s.marksStale = true
	s.ensureReview()
	if string(s.marks) != "\x00~~" || len(s.changedWords[1]) == 0 {
		t.Fatalf("missing changes: %q %+v", s.marks, s.changedWords)
	}
	m.renderEditor()
	s.toggleNearestDeletion()
	frame := m.renderEditor()
	for _, want := range []string{"Compared with HEAD", "var value = 10", "removed", "read-only"} {
		if !strings.Contains(StripANSI(frame), want) {
			t.Fatalf("missing %q in %s", want, StripANSI(frame))
		}
	}
	if !strings.Contains(frame, diffBg.wordAdd) || !strings.Contains(frame, diffBg.lineDel) {
		t.Fatal("missing word/line backgrounds")
	}
	before := s.text()
	for i, row := range s.reviewRows {
		if row.oldLine >= 0 {
			m.handleEditorMouse(tea.MouseClickMsg{Button: tea.MouseLeft, X: 12, Y: i + 1})
			if s.text() != before || s.row != 0 || s.dragging {
				t.Fatal("deleted text became editable")
			}
			break
		}
	}
	// Deleting text at EOF still has a visible, expandable anchor.
	s.setContent("package main")
	s.marksStale = true
	s.ensureReview()
	last := s.reviewRows[len(s.reviewRows)-1]
	if last.change < 0 {
		t.Fatal("missing EOF deletion anchor")
	}
	// A selection wins over the changed-word background; syntax colors remain.
	marks := noLineMarks
	marks.background, marks.wordBackground = diffBg.lineAdd, diffBg.wordAdd
	marks.changed = []editorColumnRange{{0, 100}}
	marks.selStart, marks.selEnd = 0, 4
	for _, text := range []string{"\tvar 日本 = 20", "/* comment */", "\x1b[2J"} {
		out := renderEditorLine(LexLine(text, "go"), 1, 24, marks)
		if ansi.StringWidth(out) != 24 || strings.Contains(out, "\x1b[2J") {
			t.Fatal("invalid highlighted row geometry/control bytes")
		}
		if !strings.Contains(out, selectionBg) {
			t.Fatal("change highlight hid selection")
		}
	}
}

func TestSourceEditorBaselineRefreshKeepsBufferAndRejectsStaleResults(t *testing.T) {
	root, m := sourceFixture(t)
	selectSource(t, m, "Changes", "") // group header
	if err := m.openEditor("file.txt", false); err != nil {
		t.Fatal(err)
	}
	s := m.editor
	first := m.editorHeadCmd()
	second := m.editorHeadCmd()
	m.Update(second())
	m.Update(first())
	if !s.hasHead || strings.Join(s.head, "\n") != "initial" {
		t.Fatal("HEAD baseline missing")
	}
	s.insert("unsaved ")
	s.marksStale = true
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("new baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := exec.Command("git", "add", "file.txt")
	stage.Dir = root
	if out, err := stage.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	cmd := exec.Command("git", "commit", "-qm", "external")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	var apply func(tea.Cmd)
	apply = func(command tea.Cmd) {
		if command == nil {
			return
		}
		msg := command()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				apply(sub)
			}
			return
		}
		_, next := m.Update(msg)
		apply(next)
	}
	apply(m.refreshSourceCmd())
	if s.text() != "unsaved initial" || !s.dirty {
		t.Fatal("baseline refresh replaced live buffer")
	}
	if strings.Join(s.head, "\n") != "new baseline" {
		t.Fatal("external commit did not refresh HEAD")
	}
	id := s.baselineID
	m.Update(sourceEditorHeadMsg{editor: s, id: id - 1, head: "stale"})
	if strings.Join(s.head, "\n") != "new baseline" {
		t.Fatal("stale baseline applied")
	}
	m.Update(sourceEditorHeadMsg{editor: s, id: id, err: errors.New("query failed")})
	if s.hasHead || !strings.Contains(StripANSI(m.renderEditor()), "Changes unavailable") {
		t.Fatal("query failure treated as new file")
	}
	closed := s
	m.closeEditor()
	m.Update(sourceEditorHeadMsg{editor: closed, id: id, head: "late"})
	if m.editor != nil {
		t.Fatal("late baseline reopened closed editor")
	}
}

func TestEditorDeletionPreviewPagesWithoutCursorSnap(t *testing.T) {
	m := newEditorOn(t, "current")
	s := m.editor
	s.hasHead = true
	for i := range 100 {
		s.head = append(s.head, fmt.Sprintf("old %03d", i))
	}
	s.marksStale = true
	s.ensureReview()
	s.toggleNearestDeletion()
	if frame := StripANSI(m.renderEditor()); !strings.Contains(frame, "old 000") {
		t.Fatal("expansion hid the beginning of deleted text")
	}
	for range 3 {
		m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
		m.renderEditor()
	}
	if s.top < 38 {
		t.Fatal("paging deleted text snapped to the live cursor")
	}
	m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m.renderEditor()
	if s.top < 38 {
		t.Fatal("page up snapped to the live cursor")
	}
}

func TestEditorFrameLeavesTerminalWrapMargin(t *testing.T) {
	for _, source := range []bool{false, true} {
		for _, size := range [][2]int{{80, 24}, {90, 24}, {120, 30}, {160, 45}} {
			m := newEditorOn(t, strings.Repeat("a", 200)+"\n"+strings.Repeat("b\n", 200))
			if source {
				m.openSourceControl("")
				m.source.pane = 1
			}
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
			frame := m.render()
			if len(strings.Split(frame, "\n")) != size[1] {
				t.Fatal("editor exceeds terminal height")
			}
			for _, row := range strings.Split(frame, "\n") {
				if ansi.StringWidth(row) > m.frameWidth() {
					t.Fatalf("source=%v size=%v fills the terminal's last column", source, size)
				}
			}
		}
	}
}
