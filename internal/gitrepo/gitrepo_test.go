package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newTestRepo creates a throwaway git repo on disk with one committed
// file and a configured user identity so `git commit` works without
// relying on the machine's global git config.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")

	mustWrite(t, filepath.Join(dir, "committed.txt"), "hello\n")
	run("add", "committed.txt")
	run("commit", "-q", "-m", "initial")

	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIsRepo(t *testing.T) {
	dir := newTestRepo(t)
	if !IsRepo(context.Background(), dir) {
		t.Fatal("expected IsRepo to be true for a real git repo")
	}
	if IsRepo(context.Background(), t.TempDir()) {
		t.Fatal("expected IsRepo to be false for a plain directory")
	}
}

func TestListFilesIncludesUntrackedButNotIgnored(t *testing.T) {
	dir := newTestRepo(t)
	mustWrite(t, filepath.Join(dir, "untracked.txt"), "new\n")
	mustWrite(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n")
	mustWrite(t, filepath.Join(dir, "ignored.txt"), "skip me\n")

	files, err := ListFiles(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, f := range files {
		got[f] = true
	}
	if !got["committed.txt"] {
		t.Error("expected committed.txt in the list")
	}
	if !got["untracked.txt"] {
		t.Error("expected untracked.txt in the list")
	}
	if got["ignored.txt"] {
		t.Error("expected ignored.txt to be excluded")
	}
}

func TestStatusReportsModifiedStagedAndUntracked(t *testing.T) {
	dir := newTestRepo(t)
	mustWrite(t, filepath.Join(dir, "committed.txt"), "changed\n")
	mustWrite(t, filepath.Join(dir, "new.txt"), "new\n")

	statuses, err := Status(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	byPath := map[string]FileStatus{}
	for _, s := range statuses {
		byPath[s.Path] = s
	}

	modified, ok := byPath["committed.txt"]
	if !ok || modified.Unstaged != "M" || modified.Staged != "" {
		t.Errorf("expected committed.txt unstaged=M staged=empty, got %+v", modified)
	}
	untracked, ok := byPath["new.txt"]
	if !ok || untracked.Unstaged != "?" {
		t.Errorf("expected new.txt unstaged=?, got %+v", untracked)
	}
}

func TestGetRepoStatus(t *testing.T) {
	dir := newTestRepo(t)
	mustWrite(t, filepath.Join(dir, "committed.txt"), "changed\nline2\n")
	mustWrite(t, filepath.Join(dir, "new.txt"), "new file content\n")

	status, err := GetRepoStatus(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !status.IsRepo {
		t.Fatal("expected IsRepo to be true")
	}
	if status.Branch == "" {
		t.Error("expected Branch to be set")
	}
	if len(status.Files) != 2 {
		t.Fatalf("expected 2 changed files, got %d", len(status.Files))
	}

	// Test stage
	if err := Stage(context.Background(), dir, []string{"committed.txt"}); err != nil {
		t.Fatal(err)
	}
	stagedStatus, err := GetRepoStatus(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	var stagedFound bool
	for _, f := range stagedStatus.Files {
		if f.Path == "committed.txt" && f.Staged != "" {
			stagedFound = true
		}
	}
	if !stagedFound {
		t.Error("expected committed.txt to have Staged set after Stage()")
	}
}

func TestStageAndUnstage(t *testing.T) {
	dir := newTestRepo(t)
	mustWrite(t, filepath.Join(dir, "committed.txt"), "changed\n")

	if err := Stage(context.Background(), dir, []string{"committed.txt"}); err != nil {
		t.Fatal(err)
	}
	statuses, _ := Status(context.Background(), dir)
	if len(statuses) != 1 || statuses[0].Staged != "M" {
		t.Fatalf("expected staged=M after Stage, got %+v", statuses)
	}

	if err := Unstage(context.Background(), dir, []string{"committed.txt"}); err != nil {
		t.Fatal(err)
	}
	statuses, _ = Status(context.Background(), dir)
	if len(statuses) != 1 || statuses[0].Staged != "" || statuses[0].Unstaged != "M" {
		t.Fatalf("expected staged=empty unstaged=M after Unstage, got %+v", statuses)
	}
}

func TestCommitOnlyIncludesStagedChanges(t *testing.T) {
	dir := newTestRepo(t)
	mustWrite(t, filepath.Join(dir, "committed.txt"), "staged change\n")
	mustWrite(t, filepath.Join(dir, "untouched.txt"), "should stay unstaged\n")

	if err := Stage(context.Background(), dir, []string{"committed.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := Commit(context.Background(), dir, "test commit"); err != nil {
		t.Fatal(err)
	}

	statuses, err := Status(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	// Only untouched.txt (never staged) should remain in status —
	// committed.txt's staged change is now part of history.
	if len(statuses) != 1 || statuses[0].Path != "untouched.txt" {
		t.Fatalf("expected only untouched.txt left in status, got %+v", statuses)
	}
}

func TestCommitRejectsEmptyMessage(t *testing.T) {
	dir := newTestRepo(t)
	if err := Commit(context.Background(), dir, "   "); err == nil {
		t.Fatal("expected an error for a blank commit message")
	}
}

func TestDiffShowsChange(t *testing.T) {
	dir := newTestRepo(t)
	mustWrite(t, filepath.Join(dir, "committed.txt"), "changed content\n")

	diff, err := Diff(context.Background(), dir, "committed.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	if diff == "" {
		t.Fatal("expected a non-empty diff for a modified file")
	}
}

func TestDiffHEADIncludesStagedAndUnstagedWithFullContext(t *testing.T) {
	dir := newTestRepo(t)
	mustWrite(t, filepath.Join(dir, "committed.txt"), "a\nb\nc\nd\ne\nf\ng\nh\n")
	cmd := exec.Command("git", "commit", "-qam", "more lines")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	mustWrite(t, filepath.Join(dir, "committed.txt"), "A\nb\nc\nd\ne\nf\ng\nh\n")
	if err := Stage(context.Background(), dir, []string{"committed.txt"}); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "committed.txt"), "A\nb\nc\nd\ne\nf\ng\nH\n")

	diff, err := DiffHEAD(context.Background(), dir, "committed.txt", FullFileContext)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-a", "+A", "-h", "+H", " d"} {
		if !strings.Contains(strings.ReplaceAll(diff, "\r", ""), "\n"+want+"\n") {
			t.Errorf("diff missing line %q:\n%s", want, diff)
		}
	}
}

func TestShowHEAD(t *testing.T) {
	dir := newTestRepo(t)
	mustWrite(t, filepath.Join(dir, "committed.txt"), "changed\n")
	mustWrite(t, filepath.Join(dir, "new.txt"), "new\n")

	got, err := ShowHEAD(context.Background(), dir, "committed.txt")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(got, "\r", "") != "hello\n" {
		t.Errorf("ShowHEAD committed.txt = %q, want the committed contents", got)
	}
	got, err = ShowHEAD(context.Background(), dir, "new.txt")
	if err != nil || got != "" {
		t.Errorf("ShowHEAD on an untracked file = %q, %v; want empty, nil", got, err)
	}
}

func TestSearchFindsMatchInTrackedAndUntrackedFiles(t *testing.T) {
	dir := newTestRepo(t)
	mustWrite(t, filepath.Join(dir, "untracked.go"), "package main\n\nfunc findme() {}\n")

	matches, err := Search(context.Background(), dir, "findme")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Path != "untracked.go" {
		t.Fatalf("expected one match in untracked.go, got %+v", matches)
	}
}

func TestSearchReturnsEmptyForNoMatches(t *testing.T) {
	dir := newTestRepo(t)
	matches, err := Search(context.Background(), dir, "definitely_not_present_anywhere_xyz")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no matches, got %+v", matches)
	}
}

func TestSearchTreatsQueryAsLiteralNotRegex(t *testing.T) {
	dir := newTestRepo(t)
	mustWrite(t, filepath.Join(dir, "regex.txt"), "a.b.c literal dots\n")

	// "." would match any character as a regex — as a literal (-F) it
	// should only match an actual "." character, confirming the search
	// doesn't accidentally do regex matching on unescaped user input.
	matches, err := Search(context.Background(), dir, "a.b.c")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one literal match, got %+v", matches)
	}
}

// newTestRepoWithRemote creates a throwaway "remote" as a bare repo,
// plus a clone of it configured with an upstream — the standard way to
// exercise real `git push` behavior in a test without network access.
func newTestRepoWithRemote(t *testing.T) (clone string, remote string) {
	t.Helper()
	remote = t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	clone = newTestRepo(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = clone
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("remote", "add", "origin", remote)
	run("push", "-q", "--set-upstream", "origin", "HEAD")

	return clone, remote
}

func TestPushSucceedsWithUpstreamConfigured(t *testing.T) {
	clone, _ := newTestRepoWithRemote(t)

	mustWrite(t, filepath.Join(clone, "committed.txt"), "changed\n")
	if err := Stage(context.Background(), clone, []string{"committed.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := Commit(context.Background(), clone, "second commit"); err != nil {
		t.Fatal(err)
	}

	if _, err := Push(context.Background(), clone); err != nil {
		t.Fatalf("expected push to succeed, got: %v", err)
	}
}

func TestPushFailsWithoutUpstream(t *testing.T) {
	dir := newTestRepo(t) // no remote configured at all
	_, err := Push(context.Background(), dir)
	if err == nil {
		t.Fatal("expected push to fail with no remote/upstream configured")
	}
}

func TestPushFailsWhenRemoteHasDivergedNoForce(t *testing.T) {
	clone, remote := newTestRepoWithRemote(t)

	// Simulate someone else pushing to the remote: clone it again to a
	// second working copy, commit there, and push that — so the first
	// clone's local HEAD is now behind the remote's.
	otherClone := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", remote, otherClone).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	runIn := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
		}
	}
	runIn(otherClone, "config", "user.email", "other@example.com")
	runIn(otherClone, "config", "user.name", "Other")
	mustWrite(t, filepath.Join(otherClone, "committed.txt"), "someone else's change\n")
	runIn(otherClone, "add", "committed.txt")
	runIn(otherClone, "commit", "-q", "-m", "diverging commit")
	runIn(otherClone, "push", "-q")

	// Now make an unrelated local commit in the original clone and try
	// to push — the remote has moved on, so this must fail rather than
	// force-overwrite what the other clone pushed.
	mustWrite(t, filepath.Join(clone, "another.txt"), "local change\n")
	if err := Stage(context.Background(), clone, []string{"another.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := Commit(context.Background(), clone, "local diverging commit"); err != nil {
		t.Fatal(err)
	}

	if _, err := Push(context.Background(), clone); err == nil {
		t.Fatal("expected push to fail when the remote has diverged (no force-push)")
	}
}

func TestBranchesListsAndMarksCurrent(t *testing.T) {
	dir := newTestRepo(t)
	initialBranch := currentBranchName(t, dir)

	if out, err := exec.Command("git", "-C", dir, "branch", "feature").CombinedOutput(); err != nil {
		t.Fatalf("git branch feature: %v\n%s", err, out)
	}

	branches, err := Branches(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]Branch{}
	for _, b := range branches {
		byName[b.Name] = b
	}
	if !byName[initialBranch].Current {
		t.Errorf("expected %q to be marked current, got %+v", initialBranch, byName[initialBranch])
	}
	if byName["feature"].Current {
		t.Errorf("expected feature to not be current, got %+v", byName["feature"])
	}
}

func currentBranchName(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCreateBranchThenSwitchBack(t *testing.T) {
	dir := newTestRepo(t)
	initialBranch := currentBranchName(t, dir)

	if err := CreateBranch(context.Background(), dir, "feature"); err != nil {
		t.Fatal(err)
	}
	if got := currentBranchName(t, dir); got != "feature" {
		t.Fatalf("expected to be on feature after CreateBranch, got %q", got)
	}

	if err := SwitchBranch(context.Background(), dir, initialBranch); err != nil {
		t.Fatal(err)
	}
	if got := currentBranchName(t, dir); got != initialBranch {
		t.Fatalf("expected to be back on %q after SwitchBranch, got %q", initialBranch, got)
	}
}

func TestCreateBranchFailsIfAlreadyExists(t *testing.T) {
	dir := newTestRepo(t)
	initialBranch := currentBranchName(t, dir)

	if err := CreateBranch(context.Background(), dir, "feature"); err != nil {
		t.Fatal(err)
	}
	// Switch back to the original branch, then try to create "feature"
	// again — it already exists, so this must fail rather than silently
	// switching to it (that's what SwitchBranch is for).
	if err := SwitchBranch(context.Background(), dir, initialBranch); err != nil {
		t.Fatal(err)
	}
	if err := CreateBranch(context.Background(), dir, "feature"); err == nil {
		t.Fatal("expected CreateBranch to fail: branch already exists")
	}
}

func TestSwitchBranchFailsOnConflictingUncommittedChanges(t *testing.T) {
	dir := newTestRepo(t)
	initialBranch := currentBranchName(t, dir)

	if err := CreateBranch(context.Background(), dir, "feature"); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "committed.txt"), "feature version\n")
	if err := Stage(context.Background(), dir, []string{"committed.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := Commit(context.Background(), dir, "feature change"); err != nil {
		t.Fatal(err)
	}

	if err := SwitchBranch(context.Background(), dir, initialBranch); err != nil {
		t.Fatal(err)
	}
	// An uncommitted change to the same file, different content than
	// either branch has committed — switching to feature would have to
	// overwrite it, so git must refuse.
	mustWrite(t, filepath.Join(dir, "committed.txt"), "conflicting uncommitted change\n")

	if err := SwitchBranch(context.Background(), dir, "feature"); err == nil {
		t.Fatal("expected SwitchBranch to fail rather than overwrite uncommitted conflicting changes")
	}

	// The uncommitted change must survive untouched — SwitchBranch failing
	// must not have discarded it.
	got, err := os.ReadFile(filepath.Join(dir, "committed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "conflicting uncommitted change\n" {
		t.Fatalf("uncommitted change was lost after failed SwitchBranch, got: %q", got)
	}
}

func TestSwitchBranchCarriesNonConflictingChanges(t *testing.T) {
	dir := newTestRepo(t)
	initialBranch := currentBranchName(t, dir)

	if err := CreateBranch(context.Background(), dir, "feature"); err != nil {
		t.Fatal(err)
	}
	if err := SwitchBranch(context.Background(), dir, initialBranch); err != nil {
		t.Fatal(err)
	}

	// An uncommitted new file, unrelated to anything either branch
	// tracks — switching branches should carry it along rather than
	// requiring it to be committed first.
	mustWrite(t, filepath.Join(dir, "untracked.txt"), "carry me over\n")

	if err := SwitchBranch(context.Background(), dir, "feature"); err != nil {
		t.Fatalf("expected switch to succeed and carry the non-conflicting change, got: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "untracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "carry me over\n" {
		t.Fatalf("uncommitted non-conflicting file was not carried over, got: %q", got)
	}
}

func TestNonRepoOperationsReturnErrNotARepo(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	if _, err := ListFiles(ctx, dir); err != ErrNotARepo {
		t.Errorf("ListFiles: expected ErrNotARepo, got %v", err)
	}
	if _, err := Status(ctx, dir); err != ErrNotARepo {
		t.Errorf("Status: expected ErrNotARepo, got %v", err)
	}
	if err := Stage(ctx, dir, []string{"x"}); err != ErrNotARepo {
		t.Errorf("Stage: expected ErrNotARepo, got %v", err)
	}
	if err := Commit(ctx, dir, "msg"); err != ErrNotARepo {
		t.Errorf("Commit: expected ErrNotARepo, got %v", err)
	}
	if _, err := Push(ctx, dir); err != ErrNotARepo {
		t.Errorf("Push: expected ErrNotARepo, got %v", err)
	}
	if _, err := Branches(ctx, dir); err != ErrNotARepo {
		t.Errorf("Branches: expected ErrNotARepo, got %v", err)
	}
	if err := SwitchBranch(ctx, dir, "main"); err != ErrNotARepo {
		t.Errorf("SwitchBranch: expected ErrNotARepo, got %v", err)
	}
	if err := CreateBranch(ctx, dir, "feature"); err != ErrNotARepo {
		t.Errorf("CreateBranch: expected ErrNotARepo, got %v", err)
	}
}

func TestRemoveFileChanges(t *testing.T) {
	ctx := context.Background()
	dir := newTestRepo(t)

	// 1. Tracked unstaged modification
	committedPath := filepath.Join(dir, "committed.txt")
	mustWrite(t, committedPath, "modified unstaged\n")
	if err := RemoveFileChanges(ctx, dir, "committed.txt", false); err != nil {
		t.Fatalf("RemoveFileChanges unstaged failed: %v", err)
	}
	content, _ := os.ReadFile(committedPath)
	if string(content) != "hello\n" {
		t.Fatalf("expected 'hello\\n', got %q", string(content))
	}

	// 2. Tracked staged modification
	mustWrite(t, committedPath, "modified staged\n")
	if err := Stage(ctx, dir, []string{"committed.txt"}); err != nil {
		t.Fatalf("Stage failed: %v", err)
	}
	if err := RemoveFileChanges(ctx, dir, "committed.txt", false); err != nil {
		t.Fatalf("RemoveFileChanges staged failed: %v", err)
	}
	content, _ = os.ReadFile(committedPath)
	if string(content) != "hello\n" {
		t.Fatalf("expected 'hello\\n', got %q", string(content))
	}

	// 3. Untracked file
	untrackedPath := filepath.Join(dir, "extra.txt")
	mustWrite(t, untrackedPath, "untracked\n")
	if err := RemoveFileChanges(ctx, dir, "extra.txt", true); err != nil {
		t.Fatalf("RemoveFileChanges untracked failed: %v", err)
	}
	if _, err := os.Stat(untrackedPath); !os.IsNotExist(err) {
		t.Fatal("expected extra.txt to be removed")
	}

	// 4. Deleted file
	if err := os.Remove(committedPath); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFileChanges(ctx, dir, "committed.txt", false); err != nil {
		t.Fatalf("RemoveFileChanges deleted failed: %v", err)
	}
	content, err := os.ReadFile(committedPath)
	if err != nil || string(content) != "hello\n" {
		t.Fatalf("expected restored committed.txt, got err=%v content=%q", err, string(content))
	}
}

func TestDiscardAll(t *testing.T) {
	ctx := context.Background()
	dir := newTestRepo(t)

	committedPath := filepath.Join(dir, "committed.txt")
	mustWrite(t, committedPath, "modified\n")
	_ = Stage(ctx, dir, []string{"committed.txt"})

	secondPath := filepath.Join(dir, "second.txt")
	mustWrite(t, secondPath, "second\n")
	_ = Stage(ctx, dir, []string{"second.txt"})

	if err := DiscardAll(ctx, dir); err != nil {
		t.Fatalf("DiscardAll failed: %v", err)
	}

	content, _ := os.ReadFile(committedPath)
	if string(content) != "hello\n" {
		t.Fatalf("expected committed.txt restored to 'hello\\n', got %q", string(content))
	}
	status, err := GetRepoStatus(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Files) != 0 {
		t.Fatalf("expected clean working tree, got files: %+v", status.Files)
	}
}

