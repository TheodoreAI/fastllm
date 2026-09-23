package harness

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

func TestSessionChangesTalliesSuccessfulFileTools(t *testing.T) {
	dir := t.TempDir()
	var c sessionChanges

	if !c.Record(dir, "write_file", `{"path":"a.go","content":"one\ntwo\nthree\n"}`, "Successfully wrote 14 bytes to a.go.") {
		t.Fatal("write_file was not recorded")
	}
	if !c.Record(dir, "edit_file", `{"path":"b.go","search":"old","replace":"new\nlines"}`, "Successfully edited b.go.") {
		t.Fatal("edit_file was not recorded")
	}
	// The same file spelled absolutely folds into the existing entry and moves to the top.
	diff := "--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n-one\n+uno\n two\n"
	if !c.Record(dir, "patch_file", fmt.Sprintf(`{"path":%q,"diff":%q}`, filepath.Join(dir, "a.go"), diff), "Successfully patched a.go (1 hunks applied).") {
		t.Fatal("patch_file was not recorded")
	}

	// Failures and non-mutating tools are ignored.
	if c.Record(dir, "edit_file", `{"path":"c.go","search":"x","replace":"y"}`, "Error: file \"c.go\" does not exist.") {
		t.Fatal("failed edit was recorded")
	}
	if c.Record(dir, "read_file", `{"path":"a.go"}`, "Successfully read") {
		t.Fatal("read_file was recorded")
	}

	if len(c.files) != 2 {
		t.Fatalf("files = %+v, want 2 entries", c.files)
	}
	top := c.files[0]
	if top.Path != "a.go" || top.Added != 4 || top.Removed != 1 || top.Edits != 2 || !top.Written {
		t.Fatalf("a.go entry = %+v", top)
	}
	if b := c.files[1]; b.Path != "b.go" || b.Added != 2 || b.Removed != 1 || b.Written {
		t.Fatalf("b.go entry = %+v", b)
	}
	if files, added, removed := c.Totals(); files != 2 || added != 6 || removed != 2 {
		t.Fatalf("totals = %d files +%d -%d", files, added, removed)
	}
}

func TestSessionChangesRenderIsExactSize(t *testing.T) {
	var c sessionChanges
	for i := 0; i < 20; i++ {
		c.Record("", "write_file", fmt.Sprintf(`{"path":"internal/some/very/long/directory/file%d.go","content":"x"}`, i), "Successfully wrote 1 bytes.")
	}
	for _, height := range []int{1, 3, 5, 12, 40} {
		rows := strings.Split(c.Render(changesColumnWidth, height), "\n")
		if len(rows) != height {
			t.Fatalf("height=%d rendered %d rows", height, len(rows))
		}
		for i, row := range rows {
			if w := VisualLen(StripANSI(row)); w != changesColumnWidth {
				t.Fatalf("height=%d row %d is %d columns, want %d: %q", height, i, w, changesColumnWidth, StripANSI(row))
			}
		}
	}
	if out := StripANSI(c.Render(changesColumnWidth, 8)); !strings.Contains(out, "more") {
		t.Fatalf("overflowing list did not show a \"more\" line:\n%s", out)
	}
}

func TestChangesColumnKeepsFrameWithinTerminal(t *testing.T) {
	const height = 30
	tmp := t.TempDir()
	ta := textarea.New()
	ta.ShowLineNumbers = false
	m := &teaModel{
		runner: NewRunner(&mockLLM{}, tmp, "test-model"), workingDir: tmp,
		modelName: "test-model", input: ta, viewport: viewport.New(80, 10),
	}
	m.appendHistory(strings.Repeat("history line that is fairly long so it fills the conversation width\n", 40))
	m.changes.Record(tmp, "write_file", `{"path":"internal/harness/tui_changes.go","content":"a\nb"}`, "Successfully wrote 3 bytes.")

	for _, width := range []int{80, 99, 100, 101, 120, 200} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		m = updated.(*teaModel)
		view := m.View()
		rows := strings.Split(view, "\n")
		if len(rows) > height {
			t.Fatalf("width=%d frame is %d rows in a %d-row terminal", width, len(rows), height)
		}
		for i, row := range rows {
			if w := VisualLen(StripANSI(row)); w > width-1 {
				t.Fatalf("width=%d row %d is %d columns wide", width, i, w)
			}
		}
		shown := strings.Contains(StripANSI(view), "tui_changes.go")
		if shown != m.showChangesColumn() {
			t.Fatalf("width=%d changes column shown=%v, want %v", width, shown, m.showChangesColumn())
		}
	}
}
