package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Undo moved to the write journal (journal_test.go); what remains is git
// inspection.
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

	ctx := context.Background()

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
}
