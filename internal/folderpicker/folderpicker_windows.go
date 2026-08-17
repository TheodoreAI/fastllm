//go:build windows

package folderpicker

import (
	"os/exec"
	"syscall"
)

// hideConsoleWindow suppresses the console window Go would otherwise flash
// briefly for the powershell.exe host process — the FolderBrowserDialog
// itself is a GUI window and is unaffected by this, it's specifically the
// PowerShell console host window we don't want appearing at all.
func hideConsoleWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
