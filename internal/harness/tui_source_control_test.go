package harness

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"fastllm/internal/gitrepo"
)

func sourceFixture(t *testing.T) (string, *teaModel) {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "core.autocrlf", "false")
	git("config", "core.eol", "lf")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "initial")
	m := newEditorTestModel(t, root)
	m.Update(m.openSourceControl("")())
	return root, m
}

func selectSource(t *testing.T, m *teaModel, group, path string) {
	t.Helper()
	for i, e := range m.source.entries {
		if e.group == group && e.path == path {
			m.source.cursor = i
			return
		}
	}
	t.Fatalf("missing %s %s: %+v", group, path, m.source.entries)
}

func TestSourcePartialStagingAndAsyncComparisons(t *testing.T) {
	root, m := sourceFixture(t)
	ctx := context.Background()
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("staged\n"), 0o644)
	gitrepo.Stage(ctx, root, []string{"file.txt"})
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("unstaged\n"), 0o644)
	m.Update(m.refreshSourceCmd()())
	selectSource(t, m, "Staged Changes", "file.txt")
	m.Update(m.loadSourceSelection()())
	if !strings.Contains(m.source.text, "+staged") || strings.Contains(m.source.text, "+unstaged") {
		t.Fatal(m.source.text)
	}
	selectSource(t, m, "Changes", "file.txt")
	m.Update(m.loadSourceSelection()())
	if !strings.Contains(m.source.text, "-staged") || !strings.Contains(m.source.text, "+unstaged") {
		t.Fatal(m.source.text)
	}
	key := m.source.selected().key()
	m.Update(m.refreshSourceCmd()())
	if m.source.selected().key() != key {
		t.Fatal("refresh lost comparison selection")
	}
}

func TestSourceConfirmationCancelAndDiscardPreservesIndex(t *testing.T) {
	root, m := sourceFixture(t)
	ctx := context.Background()
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("staged\n"), 0o644)
	gitrepo.Stage(ctx, root, []string{"file.txt"})
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("unstaged\n"), 0o644)
	m.Update(m.refreshSourceCmd()())
	selectSource(t, m, "Changes", "file.txt")
	m.sourceDiscard(false)
	if m.source.dialog != "Discard" || len(m.source.confirmPaths) != 1 {
		t.Fatal("missing confirmation")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	data, _ := os.ReadFile(filepath.Join(root, "file.txt"))
	if string(data) != "unstaged\n" {
		t.Fatal("cancel changed file")
	}
	m.sourceDiscard(false)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("confirmation did not produce mutation")
	}
	m.Update(cmd())
	data, _ = os.ReadFile(filepath.Join(root, "file.txt"))
	if string(data) != "staged\n" {
		t.Fatal("discard changed index")
	}
	staged, _ := gitrepo.ReviewDiff(ctx, root, "file.txt", true)
	if !strings.Contains(staged, "+staged") {
		t.Fatal("index lost")
	}
	m.source.status.Files = append(m.source.status.Files, gitrepo.RepoFileStatus{Path: "conflict.txt", Conflict: true})
	m.source.rebuild()
	selectSource(t, m, "Changes", "")
	m.sourceDiscard(true)
	if m.source.dialog != "" || !strings.Contains(m.source.notice, "conflicts") {
		t.Fatal("bulk discard enabled during conflict")
	}
}

func TestSourceMutationBlockingAndCommitDraft(t *testing.T) {
	root, m := sourceFixture(t)
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("changed\n"), 0o644)
	gitrepo.Stage(context.Background(), root, []string{"file.txt"})
	m.Update(m.refreshSourceCmd()())
	m.source.message = "draft"
	for _, active := range []string{"agent", "shell", "git"} {
		m.isExecuting = active == "agent"
		m.shellExecuting = active == "shell"
		m.source.busy = active == "git"
		if cmd := m.mutateSource("Commit", nil, "draft"); cmd != nil {
			t.Fatalf("mutation allowed during %s", active)
		}
	}
	m.isExecuting = false
	m.shellExecuting = false
	m.source.busy = false
	if cmd := m.mutateSource("Commit", nil, "  "); cmd != nil {
		t.Fatal("blank commit accepted")
	}
	m.source.busy = true
	m.Update(sourceMutationMsg{root: root, action: "Commit", message: "draft", err: errors.New("hook failed")})
	if m.source.message != "draft" || !strings.Contains(m.source.notice, "hook failed") {
		t.Fatal("failed commit lost draft/error")
	}
	cmd := m.mutateSource("Commit", nil, "draft")
	if cmd == nil {
		t.Fatal(m.source.notice)
	}
	if second := m.mutateSource("Stage", []string{"file.txt"}, ""); second != nil {
		t.Fatal("concurrent mutation allowed")
	}
	m.Update(cmd())
	if m.source.message != "" {
		t.Fatal("successful commit did not clear draft")
	}
}

func TestSourceStaleResultsAndChatPreservation(t *testing.T) {
	root, m := sourceFixture(t)
	m.input.SetValue("composer draft")
	m.source.message = "commit draft"
	m.appendHistory("conversation\n")
	history := m.historyText.String()
	mode := m.mode
	m.source.selectionID = 5
	m.source.selectedKey = "current"
	m.source.text = "keep"
	m.Update(sourceDiffMsg{root: root, key: "old", id: 4, text: "stale"})
	if m.source.text != "keep" {
		t.Fatal("stale selection applied")
	}
	m.Update(sourceStatusMsg{workspace: "other", root: "other", id: m.source.refreshID, status: gitrepo.RepoStatus{IsRepo: true}})
	if m.source.root != root {
		t.Fatal("stale workspace applied")
	}
	m.source.historyID = 7
	m.Update(sourceHistoryMsg{root: root, id: 6, commits: []gitrepo.CommitInfo{{ID: strings.Repeat("a", 40)}}})
	if len(m.source.history) != 0 {
		t.Fatal("stale history applied")
	}
	m.leaveSource()
	if m.input.Value() != "composer draft" || m.source.message != "commit draft" || m.historyText.String() != history || m.mode != mode {
		t.Fatal("workspace switch lost chat/drafts")
	}
}

func TestSourceLayoutsMouseAndUnsavedEditor(t *testing.T) {
	root, m := sourceFixture(t)
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("changed\n"), 0o644)
	m.Update(m.refreshSourceCmd()())
	selectSource(t, m, "Changes", "file.txt")
	for _, size := range [][2]int{{80, 24}, {120, 30}, {160, 45}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, pane := range []int{0, 1, 2} {
			m.source.pane = pane
			assertNewLookFrame(t, m)
		}
	}
	m.source.pane = 1
	m.openSourceEditor()
	if m.editor == nil {
		t.Fatal(m.source.notice)
	}
	m.editor.insert("unsaved")
	m.render()
	m.Update(tea.MouseClickMsg{X: 1, Y: 0, Button: tea.MouseLeft})
	if m.editor.prompt != editorPromptClose {
		t.Fatal("Chat did not prompt for unsaved buffer")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.editor == nil || !m.source.active || m.sourceExitEditor {
		t.Fatal("cancel did not retain editor")
	}
	m.isExecuting = true
	if cmd := m.saveEditor(false); cmd != nil || !m.editor.dirty {
		t.Fatal("save allowed during agent")
	}
	m.isExecuting = false
	m.sourceExitEditor = true
	m.editor.prompt = editorPromptClose
	m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if m.source.active || m.editor != nil {
		t.Fatal("discard did not return to chat")
	}
}

func TestSourceNestedEditorAndBranchFailure(t *testing.T) {
	root, m := sourceFixture(t)
	nested := filepath.Join(root, "nested")
	os.Mkdir(nested, 0o755)
	m.workingDir = nested
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("dirty\n"), 0o644)
	m.Update(m.openSourceControl("file.txt")())
	if m.source.root != root {
		t.Fatal("wrong root")
	}
	m.openSourceEditor()
	if m.editor == nil || m.editor.root != root || m.workingDir != nested {
		t.Fatal("editor changed agent workspace")
	}
	m.editor.insert("user ")
	m.saveEditor(false)
	data, _ := os.ReadFile(filepath.Join(root, "file.txt"))
	if !strings.Contains(string(data), "user ") {
		t.Fatal("repository-root save failed")
	}
	m.closeEditor()
	m.source.message = "draft"
	cmd := m.mutateSource("Switch branch", nil, "missing-branch")
	m.Update(cmd())
	if m.source.message != "draft" || !strings.Contains(m.source.notice, "failed") {
		t.Fatal("branch failure lost draft/error")
	}
}

func TestSourceEmptyStateAndCommandPalette(t *testing.T) {
	m := newEditorTestModel(t, t.TempDir())
	m.Update(m.handleAgentSubmit("/git")())
	m.source.pane = 1
	frame := StripANSI(m.render())
	if !strings.Contains(frame, "Not a Git repository") {
		t.Fatal(frame)
	}
	if len(commandActions("/git")) == 0 {
		t.Fatal("missing palette action")
	}
	m.leaveSource()
	m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
	if !m.source.active || cmd == nil {
		t.Fatal("leader shortcut failed")
	}
}

func TestSourceHistoryRenderingAndMouseStageFromMessage(t *testing.T) {
	root, m := sourceFixture(t)
	m.Update(m.sourceHistoryCmd(0)())
	if len(m.source.history) != 1 {
		t.Fatal("history did not load")
	}
	selectSource(t, m, "History", "initial")
	m.Update(m.loadSourceSelection()())
	m.source.pane = 1
	frame := StripANSI(m.render())
	if !strings.Contains(m.source.text, "+initial") || !strings.Contains(frame, "file.txt") || !strings.Contains(frame, "Test") {
		t.Fatal("history metadata/diff not rendered")
	}
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("changed\n"), 0o644)
	m.Update(m.refreshSourceCmd()())
	selectSource(t, m, "Changes", "file.txt")
	m.source.pane = 2
	m.source.message = "keep"
	m.render()
	_, cmd := m.Update(tea.MouseClickMsg{X: 2, Y: m.height - 1, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("Stage click did not stage from message pane")
	}
	m.Update(cmd())
	if m.source.message != "keep" {
		t.Fatal("Stage click typed into draft")
	}
	status, err := gitrepo.Status(context.Background(), root)
	if err != nil || len(status) != 1 || status[0].Staged != "M" {
		t.Fatalf("Stage click: %+v %v", status, err)
	}
}

func TestSourceLabelsRevealUnusualPaths(t *testing.T) {
	label := sourceLabel("tab\tline\nfile\x1b[31m")
	if strings.ContainsAny(label, "\n\t\x1b") || !strings.Contains(label, "⟨LF⟩") || !strings.Contains(label, "⟨TAB⟩") {
		t.Fatalf("unsafe label %q", label)
	}
}
