package harness

import (
	"fastllm/internal/gitrepo"
	"fmt"
	"os/exec"
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

func TestSessionChangesFileAtRow(t *testing.T) {
	var c sessionChanges
	status := gitrepo.RepoStatus{
		IsRepo:   true,
		Branch:   "main",
		Upstream: "origin/main",
		Files: []gitrepo.RepoFileStatus{
			{Path: "file1.go", Unstaged: "M", Added: 5, Removed: 1},
			{Path: "file2.go", Staged: "M", Added: 10, Removed: 2},
			{Path: "file3.go", Unstaged: "?", Added: 20, Removed: 0},
		},
	}
	c.UpdateFromGit(status)

	hdr := c.HeaderRows()
	if hdr < 2 {
		t.Fatalf("expected header rows >= 2, got %d", hdr)
	}

	// Click in header row should not resolve to a file
	for r := 0; r < hdr; r++ {
		_, _, ok := c.FileAtRow(r)
		if ok {
			t.Fatalf("row %d in header unexpectedly resolved to file", r)
		}
	}

	// First file row
	f1, idx1, ok1 := c.FileAtRow(hdr)
	if !ok1 || idx1 != 0 || f1.Path != "file1.go" {
		t.Fatalf("expected file 0 file1.go, got ok=%v idx=%d file=%+v", ok1, idx1, f1)
	}

	// Second file row
	f2, idx2, ok2 := c.FileAtRow(hdr + 1)
	if !ok2 || idx2 != 1 || f2.Path != "file2.go" {
		t.Fatalf("expected file 1 file2.go, got ok=%v idx=%d file=%+v", ok2, idx2, f2)
	}

	// Third file row
	f3, idx3, ok3 := c.FileAtRow(hdr + 2)
	if !ok3 || idx3 != 2 || f3.Path != "file3.go" {
		t.Fatalf("expected file 2 file3.go, got ok=%v idx=%d file=%+v", ok3, idx3, f3)
	}

	// Out of bounds row
	_, _, ok4 := c.FileAtRow(hdr + 3)
	if ok4 {
		t.Fatal("expected out of bounds row to return ok=false")
	}
}

func TestDiffModalNavigationAndHotkeys(t *testing.T) {
	tmp := t.TempDir()
	ta := textarea.New()
	ta.ShowLineNumbers = false
	m := &teaModel{
		runner:        NewRunner(&mockLLM{}, tmp, "test-model"),
		workingDir:    tmp,
		checkpointMgr: NewCheckpointManager(tmp),
		modelName:     "test-model",
		input:         ta,
		viewport:      viewport.New(120, 20),
		width:         120,
		height:        30,
	}

	// Populate files
	m.changes.files = []fileChange{
		{Path: "alpha.go", Added: 5, Removed: 2, Status: "M"},
		{Path: "beta.go", Added: 10, Removed: 0, Status: "M"},
	}

	// 1. Alt+c opens diff modal
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}, Alt: true})
	m = updated.(*teaModel)
	if !m.diffModal {
		t.Fatal("expected diffModal to be open after Alt+c")
	}
	if m.diffCursor != 0 {
		t.Fatalf("expected diffCursor=0, got %d", m.diffCursor)
	}

	// 2. Next file with 'n'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(*teaModel)
	if m.diffCursor != 1 {
		t.Fatalf("expected diffCursor=1, got %d", m.diffCursor)
	}

	// 3. Prev file with 'p'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(*teaModel)
	if m.diffCursor != 0 {
		t.Fatalf("expected diffCursor=0 after 'p', got %d", m.diffCursor)
	}

	// 4. Close with Esc
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*teaModel)
	if m.diffModal {
		t.Fatal("expected diffModal to be closed after Esc")
	}

	// 5. Ctrl+O opens diff modal
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = updated.(*teaModel)
	if !m.diffModal {
		t.Fatal("expected diffModal to be open after Ctrl+O")
	}
	if m.diffCursor != 0 {
		t.Fatalf("expected diffCursor=0 after Ctrl+O, got %d", m.diffCursor)
	}

	// Close with 'q'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = updated.(*teaModel)
	if m.diffModal {
		t.Fatal("expected diffModal to be closed after 'q'")
	}
}

func TestMouseClickChangesColumnOpensModal(t *testing.T) {
	tmp := t.TempDir()
	ta := textarea.New()
	ta.ShowLineNumbers = false
	m := &teaModel{
		runner:        NewRunner(&mockLLM{}, tmp, "test-model"),
		workingDir:    tmp,
		checkpointMgr: NewCheckpointManager(tmp),
		modelName:     "test-model",
		input:         ta,
		viewport:      viewport.New(120, 20),
		width:         120,
		height:        30,
	}

	m.changes.files = []fileChange{
		{Path: "main.go", Added: 3, Removed: 1, Status: "M"},
		{Path: "util.go", Added: 8, Removed: 4, Status: "M"},
	}

	// Terminal width = 120 (>= changesColumnMinFrame so Changes column is shown).
	// Changes column is at X in [120 - 32, 120) = [88, 120).
	// Header rows in Changes column is m.changes.HeaderRows()
	// Row Y in terminal: header is row 0, divider is row 1, viewport + changes column begins at row 2.
	clickX := 100
	clickY := 2 + m.changes.HeaderRows() // First file row

	mouseMsg := tea.MouseMsg{
		X:      clickX,
		Y:      clickY,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	}

	updated, _ := m.Update(mouseMsg)
	m = updated.(*teaModel)
	if !m.diffModal {
		t.Fatal("expected mouse click on changes column to open diff modal")
	}
	if m.diffCursor != 0 {
		t.Fatalf("expected diffCursor=0, got %d", m.diffCursor)
	}
	if m.diffPath != "main.go" {
		t.Fatalf("expected diffPath='main.go', got %q", m.diffPath)
	}
}

func TestSessionChangesRemoveFile(t *testing.T) {
	var c sessionChanges
	c.files = []fileChange{
		{Path: "a.go"},
		{Path: "b.go"},
		{Path: "c.go"},
	}
	c.cursor = 2
	c.RemoveFile("b.go")
	if len(c.files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(c.files))
	}
	if c.files[0].Path != "a.go" || c.files[1].Path != "c.go" {
		t.Fatalf("unexpected files: %+v", c.files)
	}
	if c.cursor != 1 {
		t.Fatalf("expected cursor adjusted to 1, got %d", c.cursor)
	}
}

func TestDiffModalDiscardWorkflow(t *testing.T) {
	tmp := t.TempDir()
	ta := textarea.New()
	ta.ShowLineNumbers = false
	m := &teaModel{
		runner:        NewRunner(&mockLLM{}, tmp, "test-model"),
		workingDir:    tmp,
		checkpointMgr: NewCheckpointManager(tmp),
		modelName:     "test-model",
		input:         ta,
		viewport:      viewport.New(120, 20),
		width:         120,
		height:        30,
	}

	m.changes.files = []fileChange{
		{Path: "file1.txt", Added: 2, Removed: 0, Status: "M"},
		{Path: "file2.txt", Added: 5, Removed: 1, Status: "M"},
	}

	// 1. Open diff modal on file 0
	m.openDiffModal(0)
	if !m.diffModal || m.diffCursor != 0 {
		t.Fatalf("expected modal open at cursor 0")
	}

	// 2. Press 'x' to trigger discard confirmation
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(*teaModel)
	if !m.diffConfirmDiscard {
		t.Fatal("expected diffConfirmDiscard to be true after pressing 'x'")
	}

	// 3. Press 'n' to cancel confirmation
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(*teaModel)
	if m.diffConfirmDiscard {
		t.Fatal("expected diffConfirmDiscard to be false after pressing 'n'")
	}
	if len(m.changes.files) != 2 {
		t.Fatalf("expected file count to remain 2, got %d", len(m.changes.files))
	}

	// 4. Press 'x', then 'y' to confirm discard
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(*teaModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(*teaModel)

	if m.diffConfirmDiscard {
		t.Fatal("expected diffConfirmDiscard to be reset")
	}
	if len(m.changes.files) != 1 {
		t.Fatalf("expected 1 file remaining, got %d", len(m.changes.files))
	}
	if m.changes.files[0].Path != "file2.txt" {
		t.Fatalf("expected remaining file to be file2.txt, got %s", m.changes.files[0].Path)
	}
	if m.diffPath != "file2.txt" {
		t.Fatalf("expected modal to show file2.txt, got %s", m.diffPath)
	}

	// 5. Discard last remaining file: modal should close
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(*teaModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(*teaModel)

	if m.diffModal {
		t.Fatal("expected diffModal to close after discarding last file")
	}
	if len(m.changes.files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(m.changes.files))
	}
	if !strings.Contains(m.statusNotice, "Working tree clean") {
		t.Fatalf("expected status notice 'Working tree clean', got %q", m.statusNotice)
	}
}

func TestDiscardSlashCommand(t *testing.T) {
	tmp := t.TempDir()
	cmdInit := exec.Command("git", "init", "-q")
	cmdInit.Dir = tmp
	_ = cmdInit.Run()

	ta := textarea.New()
	ta.ShowLineNumbers = false
	m := &teaModel{
		runner:        NewRunner(&mockLLM{}, tmp, "test-model"),
		workingDir:    tmp,
		checkpointMgr: NewCheckpointManager(tmp),
		modelName:     "test-model",
		input:         ta,
		viewport:      viewport.New(120, 20),
		width:         120,
		height:        30,
	}

	// 1. /discard with no args prints usage
	cmd := m.handleAgentSubmit("/discard")
	if cmd != nil {
		t.Fatal("expected nil cmd for usage message")
	}
	history := m.historyText.String()
	if !strings.Contains(history, "Usage: /discard") {
		t.Fatalf("expected usage message in history, got %q", history)
	}

	// 2. /discard on non-repo reports not a git repository
	nonRepoDir := t.TempDir()
	ta2 := textarea.New()
	mNonRepo := &teaModel{
		runner:        NewRunner(&mockLLM{}, nonRepoDir, "test-model"),
		workingDir:    nonRepoDir,
		checkpointMgr: NewCheckpointManager(nonRepoDir),
		modelName:     "test-model",
		input:         ta2,
		viewport:      viewport.New(120, 20),
		width:         120,
		height:        30,
	}
	mNonRepo.handleAgentSubmit("/discard somefile.go")
	if !strings.Contains(mNonRepo.historyText.String(), "Not a git repository") {
		t.Fatalf("expected 'Not a git repository' message")
	}
}




