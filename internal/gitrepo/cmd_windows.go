//go:build windows

package gitrepo

import (
	"context"
	"os/exec"
	"syscall"
)

// gitCommand builds a git invocation with its console window suppressed.
// git.exe is a console-subsystem executable like powershell.exe; spawned
// from fastllm's GUI process (which owns no console of its own) it would
// otherwise make Windows allocate — and briefly flash — a new console
// window for every single call. The editor's git panel fires several of
// these back to back (tree, status, branches) whenever file access is
// turned on, which is what produced the "multiple windows opening and
// closing" symptom. See internal/folderpicker/folderpicker_windows.go for
// the same fix applied to its own powershell.exe subprocess.
func gitCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}
