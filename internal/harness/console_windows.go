//go:build windows

package harness

import (
	"os"

	"golang.org/x/sys/windows"
)

// initConsole initializes UTF-8 code page and virtual terminal processing (ANSI escape sequences)
// on Windows consoles so modern TUI styling, box-drawing, and colors render cleanly.
func initConsole() {
	_ = windows.SetConsoleOutputCP(65001)

	stdout := windows.Handle(os.Stdout.Fd())
	var outMode uint32
	if err := windows.GetConsoleMode(stdout, &outMode); err == nil {
		_ = windows.SetConsoleMode(stdout, outMode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}

	stderr := windows.Handle(os.Stderr.Fd())
	var errMode uint32
	if err := windows.GetConsoleMode(stderr, &errMode); err == nil {
		_ = windows.SetConsoleMode(stderr, errMode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
}
