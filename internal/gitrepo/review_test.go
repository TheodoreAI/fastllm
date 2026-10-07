package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fixtureGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestReviewPartialStageDiscardAndCommit(t *testing.T) {
	root := newTestRepo(t)
	ctx := context.Background()
	mustWrite(t, filepath.Join(root, "committed.txt"), "staged\n")
	if err := Stage(ctx, root, []string{"committed.txt"}); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "committed.txt"), "unstaged\n")
	mustWrite(t, filepath.Join(root, "untracked name.txt"), "not committed\n")
	status, err := GetRepoStatus(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Files) != 2 || status.Files[0].Staged != "M" || status.Files[0].Unstaged != "M" {
		t.Fatalf("partial status: %+v", status)
	}
	staged, err := ReviewDiff(ctx, root, "committed.txt", true)
	if err != nil || !strings.Contains(staged, "+staged") || strings.Contains(staged, "+unstaged") {
		t.Fatalf("staged diff: %s %v", staged, err)
	}
	unstaged, err := ReviewDiff(ctx, root, "committed.txt", false)
	if err != nil || !strings.Contains(unstaged, "-staged") || !strings.Contains(unstaged, "+unstaged") {
		t.Fatalf("unstaged diff: %s %v", unstaged, err)
	}
	if err := Discard(ctx, root, []string{"committed.txt"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "committed.txt"))
	if string(data) != "staged\n" {
		t.Fatalf("discard did not preserve index: %q", data)
	}
	mustWrite(t, filepath.Join(root, "committed.txt"), "still unstaged\n")
	if err := Commit(ctx, root, "only index"); err != nil {
		t.Fatal(err)
	}
	if got := fixtureGit(t, root, "show", "HEAD:committed.txt"); got != "staged" {
		t.Fatalf("commit content %q", got)
	}
	if got := fixtureGit(t, root, "ls-tree", "--name-only", "HEAD"); strings.Contains(got, "untracked") {
		t.Fatal("committed untracked file")
	}
}

func TestReviewRenameAndUnusualFilenames(t *testing.T) {
	root := newTestRepo(t)
	ctx := context.Background()
	name := "new file ü.txt"
	fixtureGit(t, root, "mv", "committed.txt", name)
	mustWrite(t, filepath.Join(root, " spaced .txt"), "space\n")
	if runtime.GOOS != "windows" {
		mustWrite(t, filepath.Join(root, "tab\tnewline\n.txt"), "unusual\n")
	}
	status, err := Status(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range status {
		if f.Path == name {
			found = true
			if f.Staged != "R" || f.OriginalPath != "committed.txt" {
				t.Fatalf("rename: %+v", f)
			}
		}
	}
	if !found {
		t.Fatalf("rename missing: %+v", status)
	}
	if err := Commit(ctx, root, "rename"); err != nil {
		t.Fatal(err)
	}
	history, err := History(ctx, root, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := Detail(ctx, root, history[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Files) != 1 || detail.Files[0].Path != name || detail.Files[0].OriginalPath != "committed.txt" {
		t.Fatalf("rename detail: %+v", detail)
	}
}

func TestReviewConflictsAndFailedSwitch(t *testing.T) {
	root := newTestRepo(t)
	ctx := context.Background()
	original := fixtureGit(t, root, "branch", "--show-current")
	fixtureGit(t, root, "checkout", "-qb", "other")
	mustWrite(t, filepath.Join(root, "committed.txt"), "other\n")
	fixtureGit(t, root, "commit", "-am", "other")
	fixtureGit(t, root, "checkout", original)
	mustWrite(t, filepath.Join(root, "committed.txt"), "dirty\n")
	if err := SwitchBranch(ctx, root, "other"); err == nil {
		t.Fatal("switch should refuse overwriting changes")
	}
	data, _ := os.ReadFile(filepath.Join(root, "committed.txt"))
	if string(data) != "dirty\n" {
		t.Fatal("failed switch changed file")
	}
	fixtureGit(t, root, "commit", "-am", "ours")
	cmd := exec.Command("git", "merge", "other")
	cmd.Dir = root
	if err := cmd.Run(); err == nil {
		t.Fatal("expected conflict")
	}
	status, err := Status(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(status) != 1 || !status[0].Conflict || status[0].Staged != "U" || status[0].Unstaged != "U" {
		t.Fatalf("conflict: %+v", status)
	}
	diff, err := ConflictDiff(ctx, root, "committed.txt")
	if err != nil || !strings.Contains(diff, "<<<<<<<") || !strings.Contains(diff, "diff --git") {
		t.Fatalf("conflict diff: %s %v", diff, err)
	}
	complete, err := GetRepoStatus(ctx, root)
	if err != nil || !complete.Files[0].Conflict {
		t.Fatalf("repo conflict: %+v %v", complete, err)
	}
}

func TestReviewHistoryRootMergeAndPagination(t *testing.T) {
	root := newTestRepo(t)
	ctx := context.Background()
	original := fixtureGit(t, root, "branch", "--show-current")
	first := fixtureGit(t, root, "rev-parse", "HEAD")
	detail, err := Detail(ctx, root, first)
	if err != nil || detail.Parent != "" || len(detail.Files) != 1 {
		t.Fatalf("root detail %+v %v", detail, err)
	}
	diff, err := CommitDiff(ctx, root, detail, "committed.txt")
	if err != nil || !strings.Contains(diff, "+hello") {
		t.Fatalf("root diff %s %v", diff, err)
	}
	fixtureGit(t, root, "checkout", "-qb", "side")
	mustWrite(t, filepath.Join(root, "side.txt"), "side\n")
	fixtureGit(t, root, "add", ".")
	fixtureGit(t, root, "commit", "-qm", "side")
	fixtureGit(t, root, "checkout", original)
	fixtureGit(t, root, "commit", "--allow-empty", "-qm", "main")
	fixtureGit(t, root, "merge", "--no-ff", "side", "-m", "merge")
	merge := fixtureGit(t, root, "rev-parse", "HEAD")
	detail, err = Detail(ctx, root, merge)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Parent != fixtureGit(t, root, "rev-parse", "HEAD^1") || len(detail.Files) != 1 || detail.Files[0].Path != "side.txt" {
		t.Fatalf("merge detail %+v", detail)
	}
	diff, err = CommitDiff(ctx, root, detail, "side.txt")
	if err != nil || !strings.Contains(diff, "+side") {
		t.Fatalf("merge diff %s %v", diff, err)
	}
	for i := 0; i < 49; i++ {
		fixtureGit(t, root, "commit", "--allow-empty", "-qm", "pagination")
	}
	page, err := History(ctx, root, 0, 50)
	if err != nil || len(page) != 50 {
		t.Fatalf("page %d %v", len(page), err)
	}
	next, err := History(ctx, root, 50, 50)
	if err != nil || len(next) != 3 || next[0].ID == page[49].ID {
		t.Fatalf("next %d %v", len(next), err)
	}
	fixtureGit(t, root, "checkout", "--detach", first)
	detached, err := History(ctx, root, 0, 50)
	if err != nil || len(detached) != 1 {
		t.Fatalf("detached history: %+v %v", detached, err)
	}
}

func TestReviewEmptyNestedAndLinkedWorktree(t *testing.T) {
	ctx := context.Background()
	empty := t.TempDir()
	fixtureGit(t, empty, "init", "-q")
	fixtureGit(t, empty, "config", "core.autocrlf", "false")
	history, err := History(ctx, empty, 0, 50)
	if err != nil || len(history) != 0 {
		t.Fatalf("empty history %v %v", history, err)
	}
	mustWrite(t, filepath.Join(empty, "new.txt"), "new\n")
	if err := Stage(ctx, empty, []string{"new.txt"}); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(empty, "new.txt"), "unstaged new\n")
	if err := Unstage(ctx, empty, []string{"new.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(empty, "new.txt")); err != nil {
		t.Fatal("unstage deleted worktree")
	}
	data, _ := os.ReadFile(filepath.Join(empty, "new.txt"))
	if string(data) != "unstaged new\n" {
		t.Fatal("unborn unstage changed worktree content")
	}
	root := newTestRepo(t)
	nested := filepath.Join(root, "nested")
	os.Mkdir(nested, 0o755)
	discovered, err := Root(ctx, nested)
	if err != nil || !strings.EqualFold(discovered, root) {
		t.Fatalf("root %q %v", discovered, err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	fixtureGit(t, root, "worktree", "add", "-b", "linked", linked)
	ch, stop, err := Watch(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	mustWrite(t, filepath.Join(linked, "committed.txt"), "linked\n")
	fixtureGit(t, linked, "add", ".")
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("linked worktree watcher missed index change")
	}
	fixtureGit(t, linked, "commit", "-qm", "linked")
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("linked worktree watcher missed commit")
	}
	fixtureGit(t, linked, "commit", "--allow-empty", "-qm", "empty")
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("linked worktree watcher missed HEAD/ref-only change")
	}
	stop() // stop is idempotent, including a pending debounce.
}

func TestReviewCommitHookFailure(t *testing.T) {
	root := newTestRepo(t)
	ctx := context.Background()
	fixtureGit(t, root, "config", "core.hooksPath", filepath.Join(root, ".git", "hooks"))
	if err := os.WriteFile(filepath.Join(root, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\necho hook-rejected >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "committed.txt"), "staged\n")
	if err := Stage(ctx, root, []string{"committed.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := Commit(ctx, root, "hook"); err == nil || !strings.Contains(err.Error(), "hook-rejected") {
		t.Fatalf("hook error: %v", err)
	}
	status, err := Status(ctx, root)
	if err != nil || len(status) != 1 || status[0].Staged != "M" {
		t.Fatalf("hook failure changed index: %+v %v", status, err)
	}
}

func TestReviewLiteralPathspecs(t *testing.T) {
	root := newTestRepo(t)
	ctx := context.Background()
	for _, p := range []string{"[ab].txt", "a.txt", "b.txt"} {
		mustWrite(t, filepath.Join(root, p), "initial\n")
	}
	fixtureGit(t, root, "add", ".")
	fixtureGit(t, root, "commit", "-qm", "literal paths")
	for _, p := range []string{"[ab].txt", "a.txt", "b.txt"} {
		mustWrite(t, filepath.Join(root, p), "changed\n")
	}
	if err := Stage(ctx, root, []string{"[ab].txt"}); err != nil {
		t.Fatal(err)
	}
	status, err := Status(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range status {
		if f.Path == "[ab].txt" {
			if f.Staged != "M" {
				t.Fatal("literal path was not staged")
			}
		} else if f.Staged != "" {
			t.Fatalf("staged unintended path %+v", f)
		}
	}
	if err := Discard(ctx, root, []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := Unstage(ctx, root, []string{"[ab].txt"}); err != nil {
		t.Fatal(err)
	}
	if err := Discard(ctx, root, []string{"[ab].txt"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "b.txt"))
	if string(data) != "changed\n" {
		t.Fatal("discard affected a wildcard match")
	}
}

func TestReviewBinaryDeletionAndUntrackedDelete(t *testing.T) {
	root := newTestRepo(t)
	ctx := context.Background()
	mustWrite(t, filepath.Join(root, "binary.bin"), "a\x00b")
	fixtureGit(t, root, "add", ".")
	fixtureGit(t, root, "commit", "-qm", "binary")
	mustWrite(t, filepath.Join(root, "binary.bin"), "a\x00c")
	os.Remove(filepath.Join(root, "committed.txt"))
	diff, err := ReviewDiff(ctx, root, "binary.bin", false)
	if err != nil || !strings.Contains(diff, "Binary files") {
		t.Fatalf("binary %q %v", diff, err)
	}
	status, err := Status(ctx, root)
	if err != nil || len(status) != 2 {
		t.Fatalf("deletion status %+v %v", status, err)
	}
	if err := DeleteUntracked(ctx, root, []string{"binary.bin"}); err == nil {
		t.Fatal("deleted tracked file")
	}
	mustWrite(t, filepath.Join(root, "new file"), "delete\n")
	if err := DeleteUntracked(ctx, root, []string{"new file"}); err != nil {
		t.Fatal(err)
	}
}
