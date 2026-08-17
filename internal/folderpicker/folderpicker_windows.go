//go:build windows

package folderpicker

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"syscall"
)

// dialogScript shows a native folder-browser dialog and prints the chosen
// absolute path to stdout, or nothing if the user cancels. Runs via
// powershell.exe rather than a cgo/native binding so this package adds no
// build-time dependency beyond what Windows already ships with.
const dialogScript = `
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = "Select the folder fastllm should be allowed to read and write"
$dialog.ShowNewFolderButton = $true
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
    Write-Output $dialog.SelectedPath
}
`

// Choose blocks until the user picks a folder or cancels the dialog. The
// dialog appears on the same machine/desktop session the server process
// is running under — this only makes sense for a locally-run server like
// fastllm, never a remotely-hosted one.
func Choose(ctx context.Context) (string, error) {
	// -NonInteractive would suppress the dialog itself (ShowDialog resolves
	// immediately as if cancelled, presumably because WinForms' message
	// pump doesn't get a chance to run) — confirmed by testing directly,
	// so despite this being a non-interactive-from-the-terminal use case,
	// the flag must be omitted for the GUI dialog to actually appear.
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", dialogScript)
	// Suppresses the console window Go would otherwise flash briefly for
	// the powershell.exe host process — the FolderBrowserDialog itself is
	// a GUI window and is unaffected by this, it's specifically the
	// PowerShell console host window we don't want appearing at all.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", err
	}

	path := strings.TrimSpace(stdout.String())
	if path == "" {
		return "", ErrCancelled
	}
	return path, nil
}
