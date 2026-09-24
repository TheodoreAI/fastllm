package harness

import (
	"context"
	"fastllm/internal/execution"
	"fastllm/internal/gitrepo"
	"fmt"
	"strings"
)

// CheckpointManager answers git questions about a working directory. It
// used to snapshot and roll back the whole tree with git stash and reset
// --hard; undo is now the write journal (journal.go), which reverts only what
// the model changed and works outside git too.
type CheckpointManager struct {
	scope      *execution.Scope
	workingDir string
}

// NewCheckpointManager creates a new CheckpointManager for the specified directory.
func NewCheckpointManager(workingDir string, scopes ...*execution.Scope) *CheckpointManager {
	var scope *execution.Scope
	if len(scopes) > 0 {
		scope = scopes[0]
	}
	return &CheckpointManager{scope: scope, workingDir: workingDir}
}

// IsGitRepo checks whether the working directory is inside a Git repository.
func (cm *CheckpointManager) git(ctx context.Context, args ...string) (string, error) {
	if cm.scope != nil {
		ctx = execution.WithScope(ctx, cm.scope)
	}
	result, err := execution.RunLocal(ctx, cm.workingDir, execution.LocalPolicy(), execution.Command{Executable: "git", Args: gitrepo.HardenedArgs(args...)})
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
