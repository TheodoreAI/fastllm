// Package screenshot captures fastllm's own native window as a PNG —
// used only by cmd/desktop (the Wails app), since a plain browser tab
// (cmd/server) has no OS window handle to capture. Real per-platform
// capture is only implemented for Windows today (see
// screenshot_windows.go); other platforms get the ErrUnsupported stub in
// screenshot_other.go, mirroring internal/terminal's same
// windows/other split.
package screenshot

import "errors"

// ErrUnsupported is returned on platforms without a capture implementation.
var ErrUnsupported = errors.New("screenshot capture is not supported on this platform")

// CaptureWindow finds the window with the given title and returns its
// client-area content PNG-encoded.
func CaptureWindow(title string) ([]byte, error) {
	return captureWindow(title)
}
