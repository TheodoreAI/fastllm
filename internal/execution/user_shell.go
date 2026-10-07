package execution

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// UserShellCommand loads startup definitions only for a direct human action.
// It never changes the shell used by model scopes or direct executable launches.
func UserShellCommand(shell, text string) (Command, error) {
	shell = UserShellName(shell)
	path, err := exec.LookPath(shell)
	if err != nil {
		return Command{}, fmt.Errorf("user shell %q: %w (set user_shell in fastllm config)", shell, err)
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
	var args []string
	switch name {
	case "bash", "zsh":
		args = []string{"-lic", text}
	case "fish":
		args = []string{"-l", "-i", "-c", text}
	case "powershell", "pwsh":
		args = []string{"-NonInteractive", "-Command", text}
	case "sh", "dash", "ksh":
		args = []string{"-lc", text}
	default:
		return Command{}, fmt.Errorf("unsupported user shell %q; configure bash, zsh, fish, sh, or PowerShell", shell)
	}
	return Command{Executable: path, Args: args}, nil
}

// UserShellName is also used by the UI to display the selected shell.
func UserShellName(shell string) string {
	if shell == "" {
		if runtime.GOOS == "windows" {
			shell = "powershell.exe"
		} else {
			shell = os.Getenv("SHELL")
			if shell == "" {
				shell = "/bin/sh"
			}
		}
	}
	return shell
}
