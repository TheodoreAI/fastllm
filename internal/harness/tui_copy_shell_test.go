package harness

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
)

func TestFormatTUITerminalUsesHighContrastTextColor(t *testing.T) {
	out := formatTUITerminal(TerminalBoxOptions{
		Command:  "git pull",
		Output:   "Updating 123..456\nFast-forward",
		Width:    80,
		ExitCode: 0,
	})
	// Output lines must use ColorBrightWhite (roleValue) rather than muted ColorGray (roleMuted)
	expectedWhite := ColorBrightWhite("Updating 123..456")
	if !strings.Contains(out, expectedWhite) {
		t.Fatalf("expected output line to be formatted with ColorWhite, got:\n%s", out)
	}
}

func TestCopyShellCommandOutput(t *testing.T) {
	ta := textarea.New()
	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(10))
	m := &teaModel{input: ta, viewport: vp, ready: true, width: 80}

	// 1. Simulate a finished shell command
	output := "Restored session: Wed Oct 7\nFast-forward"
	updated, _ := m.Update(teaShellDoneMsg{
		Command:      "git pull",
		Output:       output,
		Duration:     100 * time.Millisecond,
		ShellCommand: true,
	})
	m = updated.(*teaModel)

	if m.lastShellOutput != output {
		t.Fatalf("expected lastShellOutput to be %q, got %q", output, m.lastShellOutput)
	}

	// 2. /copy shell copies the shell output
	_ = m.handleAgentSubmit("/copy shell")
	if !strings.Contains(m.statusNotice, "shell output") {
		t.Fatalf("expected status notice for shell output, got: %q", m.statusNotice)
	}

	// 3. In shell mode, /copy without arguments defaults to copying the shell output
	m.mode = modeShell
	_ = m.handleAgentSubmit("/copy")
	if !strings.Contains(m.statusNotice, "shell output") {
		t.Fatalf("expected status notice for shell output in shell mode, got: %q", m.statusNotice)
	}
}
