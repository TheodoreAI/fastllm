package harness

import (
	"fastllm/internal/gitrepo"
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

func TestSessionChangesUpdateFromGit(t *testing.T) {
	var c sessionChanges
	status := gitrepo.RepoStatus{
		IsRepo:   true,
		Branch:   "main",
		Upstream: "origin/main",
		Ahead:    1,
		Files: []gitrepo.RepoFileStatus{
			{Path: "internal/harness/tui_tea.go", Staged: "M", Added: 10, Removed: 2},
			{Path: "internal/harness/tui_style.go", Unstaged: "M", Added: 5, Removed: 1},
			{Path: "newfile.txt", Unstaged: "?", Added: 1, Removed: 0},
		},
	}
	c.UpdateFromGit(status)

	if len(c.files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(c.files))
	}
	if !c.files[0].Staged || c.files[0].Status != "M" {
		t.Errorf("expected file 0 to be Staged=true, got %+v", c.files[0])
	}
	if c.files[1].Staged {
		t.Errorf("expected file 1 to be Staged=false, got %+v", c.files[1])
	}

	rendered := StripANSI(c.Render(changesColumnWidth, 20))
	if !strings.Contains(rendered, "main") {
		t.Errorf("expected rendered output to contain branch 'main':\n%s", rendered)
	}
	if !strings.Contains(rendered, "↑1") {
		t.Errorf("expected rendered output to contain ahead indicator '↑1':\n%s", rendered)
	}

	// Verify exact line sizes
	for _, line := range strings.Split(c.Render(changesColumnWidth, 15), "\n") {
		if w := VisualLen(StripANSI(line)); w != changesColumnWidth {
			t.Fatalf("line width = %d, want %d: %q", w, changesColumnWidth, line)
		}
	}
}

func TestSessionChangesCleanAndSynced(t *testing.T) {
	var c sessionChanges
	// 1. Clean tree with 2 unpushed commits
	c.UpdateFromGit(gitrepo.RepoStatus{
		IsRepo:       true,
		Branch:       "feature/tui",
		Upstream:     "origin/feature/tui",
		Ahead:        2,
		LatestCommit: "abc1234 feat: terminal box",
	})

	rendered := StripANSI(c.Render(changesColumnWidth, 12))
	if !strings.Contains(rendered, "Working tree clean") {
		t.Errorf("expected 'Working tree clean', got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "2 unpushed") {
		t.Errorf("expected '2 unpushed', got:\n%s", rendered)
	}

	// 2. Synced with origin (after git push)
	c.UpdateFromGit(gitrepo.RepoStatus{
		IsRepo:   true,
		Branch:   "feature/tui",
		Upstream: "origin/feature/tui",
		Ahead:    0,
		Behind:   0,
	})
	syncedRendered := StripANSI(c.Render(changesColumnWidth, 12))
	if !strings.Contains(syncedRendered, "Working tree clean") {
		t.Errorf("expected 'Working tree clean', got:\n%s", syncedRendered)
	}
	if !strings.Contains(syncedRendered, "Synced") {
		t.Errorf("expected 'Synced', got:\n%s", syncedRendered)
	}
}

