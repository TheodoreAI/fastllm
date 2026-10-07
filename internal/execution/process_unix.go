//go:build !windows

package execution

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureProcess(cmd *exec.Cmd) (func(), func() error, error) {
	// Captured commands must not inherit the UI's controlling terminal. An
	// interactive zsh otherwise suspends itself while acquiring the terminal
	// from this background process group. A new session also creates the
	// process group used below for cancellation.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	kill := func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		pgid := -cmd.Process.Pid

		// Attempt graceful termination first via SIGTERM to the process group.
		err := syscall.Kill(pgid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		if err != nil {
			// If sending SIGTERM fails, fall back directly to SIGKILL.
			_ = syscall.Kill(pgid, syscall.SIGKILL)
			return err
		}

		// Allow the process group a brief grace period to clean up and exit cleanly.
		// Poll every 25ms up to 1 second before escalating to SIGKILL.
		const gracePeriod = 1 * time.Second
		const pollInterval = 25 * time.Millisecond
		deadline := time.Now().Add(gracePeriod)

		for time.Now().Before(deadline) {
			time.Sleep(pollInterval)
			if err := syscall.Kill(pgid, 0); errors.Is(err, syscall.ESRCH) {
				return nil
			}
		}

		// Process did not exit within the grace period; escalate to SIGKILL.
		killErr := syscall.Kill(pgid, syscall.SIGKILL)
		if errors.Is(killErr, syscall.ESRCH) {
			return nil
		}
		return killErr
	}
	cmd.Cancel = kill
	return func() { _ = kill() }, func() error { return nil }, nil
}
