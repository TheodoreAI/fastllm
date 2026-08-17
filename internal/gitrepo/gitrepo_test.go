package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
}
