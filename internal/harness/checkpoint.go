package harness

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Checkpoint captures the state of a repository at a specific point in time.
type Checkpoint struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Timestamp time.Time `json:"timestamp"`
	Commit    string    `json:"commit"`
	Stash     string    `json:"stash,omitempty"`
}

// CheckpointManager manages git snapshots and rollbacks for a working directory.
type CheckpointManager struct {
	mu          sync.Mutex
	workingDir  string
	checkpoints []Checkpoint
}

// NewCheckpointManager creates a new CheckpointManager for the specified directory.
func NewCheckpointManager(workingDir string) *CheckpointManager {
	return &CheckpointManager{
		workingDir:  workingDir,
		checkpoints: make([]Checkpoint, 0),
	}
}

// IsGitRepo checks whether the working directory is inside a Git repository.
func (cm *CheckpointManager) IsGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = cm.workingDir
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// CreateCheckpoint creates a new snapshot point of the repository state.
func (cm *CheckpointManager) CreateCheckpoint(ctx context.Context, label string) (*Checkpoint, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if !cm.IsGitRepo() {
		return nil, fmt.Errorf("directory %q is not a git repository", cm.workingDir)
	}

	// 1. Get current HEAD commit hash
	cmdHead := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmdHead.Dir = cm.workingDir
	headOut, err := cmdHead.Output()
	headCommit := strings.TrimSpace(string(headOut))
	if err != nil {
		headCommit = "initial"
	}

	// 2. Create a stash commit without touching working tree
	// git stash create creates a commit in objects/ representing dirty state
	cmdStash := exec.CommandContext(ctx, "git", "stash", "create")
	cmdStash.Dir = cm.workingDir
	stashOut, _ := cmdStash.Output()
	stashHash := strings.TrimSpace(string(stashOut))

	id := fmt.Sprintf("cp-%d", time.Now().UnixNano())
	cp := Checkpoint{
		ID:        id,
		Label:     label,
		Timestamp: time.Now(),
		Commit:    headCommit,
		Stash:     stashHash,
	}

	cm.checkpoints = append(cm.checkpoints, cp)
	return &cp, nil
}

// Rollback restores the working directory to the specified checkpoint (or latest if id is empty).
func (cm *CheckpointManager) Rollback(ctx context.Context, id string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if !cm.IsGitRepo() {
		return fmt.Errorf("directory %q is not a git repository", cm.workingDir)
	}

	if len(cm.checkpoints) == 0 {
		return fmt.Errorf("no checkpoints available to rollback to")
	}

	var target *Checkpoint
	if id == "" {
		// Pick the most recent checkpoint
		target = &cm.checkpoints[len(cm.checkpoints)-1]
	} else {
		for i := range cm.checkpoints {
			if cm.checkpoints[i].ID == id {
				target = &cm.checkpoints[i]
				break
			}
		}
	}

	if target == nil {
		return fmt.Errorf("checkpoint %q not found", id)
	}

	// Reset tracked files to HEAD
	if target.Commit != "" && target.Commit != "initial" {
		resetCmd := exec.CommandContext(ctx, "git", "reset", "--hard", target.Commit)
		resetCmd.Dir = cm.workingDir
		if out, err := resetCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git reset --hard failed: %s: %w", string(out), err)
		}
	} else {
		checkoutCmd := exec.CommandContext(ctx, "git", "checkout", "--", ".")
		checkoutCmd.Dir = cm.workingDir
		_ = checkoutCmd.Run()
	}

	// Clean untracked files and directories
	cleanCmd := exec.CommandContext(ctx, "git", "clean", "-fd")
	cleanCmd.Dir = cm.workingDir
	_ = cleanCmd.Run()

	// If there was a stash at checkpoint time, re-apply it
	if target.Stash != "" {
		applyCmd := exec.CommandContext(ctx, "git", "stash", "apply", target.Stash)
		applyCmd.Dir = cm.workingDir
		if out, err := applyCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git stash apply %s failed: %s: %w", target.Stash, string(out), err)
		}
	}

	return nil
}

// Diff returns the git diff between HEAD/checkpoint and current working directory.
func (cm *CheckpointManager) Diff(ctx context.Context) (string, error) {
	if !cm.IsGitRepo() {
		return "", fmt.Errorf("directory %q is not a git repository", cm.workingDir)
	}

	cmd := exec.CommandContext(ctx, "git", "diff", "HEAD")
	cmd.Dir = cm.workingDir
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	_ = cmd.Run()

	diff := outBuf.String()

	// Also check untracked files
	statusCmd := exec.CommandContext(ctx, "git", "status", "-s")
	statusCmd.Dir = cm.workingDir
	statusOut, _ := statusCmd.Output()

	if len(statusOut) > 0 {
		return fmt.Sprintf("=== Working Tree Status ===\n%s\n=== Diff ===\n%s", string(statusOut), diff), nil
	}

	if strings.TrimSpace(diff) == "" {
		return "No changes detected (working tree clean).", nil
	}

	return diff, nil
}

// ListCheckpoints returns all stored checkpoints.
func (cm *CheckpointManager) ListCheckpoints() []Checkpoint {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	res := make([]Checkpoint, len(cm.checkpoints))
	copy(res, cm.checkpoints)
	return res
}
