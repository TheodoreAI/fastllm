package harness

import (
	"context"
	"fastllm/internal/execution"
	"fmt"
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
	scope       *execution.Scope
	workingDir  string
	checkpoints []Checkpoint
}

// NewCheckpointManager creates a new CheckpointManager for the specified directory.
func NewCheckpointManager(workingDir string, scopes ...*execution.Scope) *CheckpointManager {
	var scope *execution.Scope
	if len(scopes) > 0 {
		scope = scopes[0]
	}
	return &CheckpointManager{
		scope:       scope,
		workingDir:  workingDir,
		checkpoints: make([]Checkpoint, 0),
	}
}

// IsGitRepo checks whether the working directory is inside a Git repository.
func (cm *CheckpointManager) git(ctx context.Context, args ...string) (string, error) {
	if cm.scope != nil {
		ctx = execution.WithScope(ctx, cm.scope)
	}
	result, err := execution.RunLocal(ctx, cm.workingDir, execution.LocalPolicy(), execution.Command{Executable: "git", Args: args})
	if err != nil {
		return "", err
	}
	if result.Truncated {
		return "", fmt.Errorf("git output exceeded capture limit")
	}
	return result.Output, result.Err()
}
func (cm *CheckpointManager) IsGitRepo() bool {
	out, err := cm.git(context.Background(), "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// CreateCheckpoint creates a new snapshot point of the repository state.
func (cm *CheckpointManager) CreateCheckpoint(ctx context.Context, label string) (*Checkpoint, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if !cm.IsGitRepo() {
		return nil, fmt.Errorf("directory %q is not a git repository", cm.workingDir)
	}

	// 1. Get current HEAD commit hash
	headOut, err := cm.git(ctx, "rev-parse", "HEAD")
	headCommit := strings.TrimSpace(string(headOut))
	if err != nil {
		headCommit = "initial"
	}

	// 2. Create a stash commit without touching working tree
	// git stash create creates a commit in objects/ representing dirty state
	stashOut, stashErr := cm.git(ctx, "stash", "create")
	if stashErr != nil && headCommit != "initial" {
		return nil, stashErr
	}
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
		if out, err := cm.git(ctx, "reset", "--hard", target.Commit); err != nil {
			return fmt.Errorf("git reset --hard failed: %s: %w", string(out), err)
		}
	} else {
		_, _ = cm.git(ctx, "checkout", "--", ".")
	}

	// Clean untracked files and directories
	if _, err := cm.git(ctx, "clean", "-fd"); err != nil {
		return err
	}

	// If there was a stash at checkpoint time, re-apply it
	if target.Stash != "" {
		if out, err := cm.git(ctx, "stash", "apply", target.Stash); err != nil {
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

	diff, err := cm.git(ctx, "diff", "HEAD")
	if err != nil {
		return "", err
	}
	statusOut, err := cm.git(ctx, "status", "-s")
	if err != nil {
		return "", err
	}

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
