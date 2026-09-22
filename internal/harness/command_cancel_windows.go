//go:build windows

package harness

import (
	"fmt"
	"os"
	"os/exec"
)

func configureCommandTreeCancellation(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		cancel := exec.Command("taskkill", "/PID", fmt.Sprintf("%d", cmd.Process.Pid), "/T", "/F")
		if err := cancel.Run(); err != nil {
			if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				return os.ErrProcessDone
			}
			if killErr := cmd.Process.Kill(); killErr != nil {
				return killErr
			}
		}
		return nil
	}
}
