//go:build !windows

package gitrepo

import (
	"context"
	"os/exec"
)

// gitCommand is a plain exec.CommandContext off Windows — the console
// window flash this hides is a Windows console-subsystem-process
// behavior with no equivalent to suppress elsewhere.
func gitCommand(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "git", args...)
}
