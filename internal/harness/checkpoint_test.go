package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointManagerGit(t *testing.T) {
	tempDir := t.TempDir()

	// Initialize git repo
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tempDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v, out: %s", strings.Join(args, " "), err, string(out))
		}
	}

	runGit("init")
	runGit("config", "user.email", "test@fastllm.dev")
	runGit("config", "user.name", "FastLLM Test")

	testFile := filepath.Join(tempDir, "hello.txt")
	if err := os.WriteFile(testFile, []byte("version 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "hello.txt")
	runGit("commit", "-m", "initial commit")

	cm := NewCheckpointManager(tempDir)
	if !cm.IsGitRepo() {
		t.Fatal("expected IsGitRepo to be true")
	}

	// Create checkpoint
	ctx := context.Background()
	cp, err := cm.CreateCheckpoint(ctx, "before changes")
	if err != nil {
		t.Fatalf("CreateCheckpoint failed: %v", err)
	}

	// Modify file and add new file
	if err := os.WriteFile(testFile, []byte("version 2 modified\n"), 0644); err != nil {
		t.Fatal(err)
	}
	newFile := filepath.Join(tempDir, "untracked.txt")
	if err := os.WriteFile(newFile, []byte("temp\n"), 0644); err != nil {
		t.Fatal(err)
	}

	diff, err := cm.Diff(ctx)
	if err != nil {
		t.Fatalf("Diff failed: %v", err)
	}
	if !strings.Contains(diff, "version 2 modified") && !strings.Contains(diff, "hello.txt") {
		t.Errorf("expected diff to show changes, got: %s", diff)
	}

	// Rollback
	if err := cm.Rollback(ctx, cp.ID); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "version 1" {
		t.Errorf("file was not restored! got: %q, want 'version 1'", string(data))
	}

	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Errorf("untracked file was not cleaned up!")
	}
}
