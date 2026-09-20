package harness

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
)

// spinnerFrames is a braille cycle: every frame is one column wide in a
// monospace terminal, so the line never reflows as it animates.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const spinnerInterval = 90 * time.Millisecond

// Spinner shows that work is in progress on a single rewritten line, along with
// elapsed time so a long wait is visibly still moving rather than hung.
//
// It writes only to an interactive terminal. When output is redirected — a pipe,
// a file, the -json mode — Start and Stop are no-ops, so machine-readable output
// is never polluted with carriage returns and escape codes.
type Spinner struct {
	label string

	mu      sync.Mutex
	done    chan struct{}
	stopped bool
	active  bool
}

// NewSpinner creates a spinner with a fixed label, e.g. "Thinking".
func NewSpinner(label string) *Spinner {
	return &Spinner{label: label}
}

func spinnerSupported() bool {
	if os.Getenv("FASTLLM_NO_SPINNER") != "" || os.Getenv("NO_COLOR") != "" {
		return false
	}
	// FASTLLM_FORCE_SPINNER exists so the animation can be exercised with
	// output captured to a pipe, where the TTY check would otherwise skip it.
	if os.Getenv("FASTLLM_FORCE_SPINNER") != "" {
		return true
	}
	fd := os.Stdout.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// Start begins animating. Calling Start on an already-running spinner is a no-op.
func (s *Spinner) Start() {
	if s == nil || !spinnerSupported() {
		return
	}
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return
	}
	s.active = true
	s.stopped = false
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()

	started := time.Now()
	go func() {
		ticker := time.NewTicker(spinnerInterval)
		defer ticker.Stop()
		frame := 0
		for {
			select {
			case <-ticker.C:
				elapsed := time.Since(started).Round(time.Second)
				fmt.Printf("\r\033[2K  %s %s %s",
					ColorCyan(spinnerFrames[frame%len(spinnerFrames)]),
					ColorGray(s.label+"…"),
					ColorGray(elapsed.String()))
				frame++
			case <-done:
				return
			}
		}
	}()
}

// Stop halts the animation and clears the line, leaving the cursor at column 0
// so whatever prints next starts on a clean line.
func (s *Spinner) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.active || s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.active = false
	close(s.done)
	s.mu.Unlock()

	fmt.Print("\r\033[2K")
}

// withSpinner runs fn while a spinner labelled label is displayed, and always
// clears it afterwards — including when fn panics.
func withSpinner(label string, fn func()) {
	spinner := NewSpinner(label)
	spinner.Start()
	defer spinner.Stop()
	fn()
}
