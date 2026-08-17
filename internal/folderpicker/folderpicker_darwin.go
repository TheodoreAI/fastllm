//go:build darwin

package folderpicker

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
)

// dialogScript shows a native "choose folder" dialog via AppleScript and
// prints the chosen path (converted from AppleScript's colon-delimited
// HFS form to a normal POSIX path) to stdout. Cancelling the dialog makes
// osascript exit non-zero rather than print anything, which Choose below
// treats as ErrCancelled rather than a real failure — see the stderr
// check there.
const dialogScript = `
set chosenFolder to choose folder with prompt "Select the folder fastllm should be allowed to read and write"
return POSIX path of chosenFolder
`

// Choose blocks until the user picks a folder or cancels the dialog. The
// dialog appears on the same machine/desktop session the server process
// is running under — this only makes sense for a locally-run server like
// fastllm, never a remotely-hosted one.
func Choose(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "osascript", "-e", dialogScript)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// osascript reports the user cancelling ("choose folder" was
		// cancelled) as exit code 1 with "User canceled." on stderr,
		// indistinguishable from a real script error except by message —
		// treat that specific case as a cancellation, anything else as a
		// genuine failure the caller should surface.
		if strings.Contains(stderr.String(), "User canceled") {
			return "", ErrCancelled
		}
		return "", err
	}

	path := strings.TrimSpace(stdout.String())
	if path == "" {
		return "", ErrCancelled
	}
	return path, nil
}
