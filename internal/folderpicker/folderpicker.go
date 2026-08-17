// Package folderpicker shows a native OS folder-selection dialog on the
// machine running the fastllm server, for the "Choose folder" button in
// Settings → File access. A browser's own <input type=file webkitdirectory>
// never exposes an absolute filesystem path (only a folder-name hint, for
// browser sandboxing reasons that apply even to a locally-run app like this
// one) — so getting a real, usable path requires asking the OS directly on
// the server side instead of the browser.
package folderpicker

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// ErrCancelled is returned when the user closes the dialog without
// choosing a folder.
var ErrCancelled = errors.New("folder selection was cancelled")

// ErrUnsupported is returned on platforms without an implementation.
var ErrUnsupported = errors.New("folder picker is not supported on this platform")

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
	if runtime.GOOS != "windows" {
		return "", ErrUnsupported
	}

	// -NonInteractive would suppress the dialog itself (ShowDialog resolves
	// immediately as if cancelled, presumably because WinForms' message
	// pump doesn't get a chance to run) — confirmed by testing directly,
	// so despite this being a non-interactive-from-the-terminal use case,
	// the flag must be omitted for the GUI dialog to actually appear.
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", dialogScript)
	hideConsoleWindow(cmd)
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
