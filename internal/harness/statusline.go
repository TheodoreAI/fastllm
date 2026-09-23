package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// lineIndent is the blank line and indent every status line carries, kept as a
// constant so the prompt below it always sits a fixed distance from the chrome.
const lineIndent = "\n  "

// StatusLine is the state shown on the persistent line above the prompt. It is a
// struct rather than a long parameter list so adding a field later does not
// churn every call site.
type StatusLine struct {
	Model          string
	PermissionMode PermissionMode
	ShellMode      bool
	ContextChars   int
	ContextBudget  int
	Turns          int
	TotalTokens    int
	Cost           float64
	WorkingDir     string
	Branch         string
}

// FormatStatusLine renders the always-visible session summary.
//
// It answers, without a command, the questions that otherwise require /status,
// /set and /model: which model, what it is allowed to do, how close the context
// is to compaction, and what the session has cost so far.
func FormatStatusLine(s StatusLine) string {
	var parts []string

	if s.ShellMode {
		parts = append(parts, ColorYellow("shell"))
	} else if s.Model != "" {
		parts = append(parts, ColorCyan(s.Model))
	}

	if strings.TrimSpace(string(s.PermissionMode)) != "" {
		// The mode decides what the agent may touch, so it is always coloured.
		parts = append(parts, colorForMode(s.PermissionMode)(strings.ToLower(s.PermissionMode.Label())))
	}

	if s.ContextBudget > 0 {
		pct := s.ContextChars * 100 / s.ContextBudget
		usage := fmt.Sprintf("ctx %s/%s (%d%%)",
			compactCount(s.ContextChars), compactCount(s.ContextBudget), pct)
		switch {
		case pct >= 90:
			parts = append(parts, ColorYellow(usage))
		default:
			parts = append(parts, ColorGray(usage))
		}
	}

	if s.Turns > 0 {
		parts = append(parts, ColorGray(fmt.Sprintf("%d turns", s.Turns)))
	}
	if s.TotalTokens > 0 {
		parts = append(parts, ColorGray(compactCount(s.TotalTokens)+" tok"))
	}
	if s.Cost > 0 {
		parts = append(parts, ColorGray(fmt.Sprintf("$%.2f", s.Cost)))
	}

	location := abbreviateHome(s.WorkingDir)
	short := location
	if index := strings.LastIndex(location, "/"); index >= 0 {
		short = location[index+1:]
	}
	if s.Branch != "" {
		location += " " + SymBranch + " " + s.Branch
		short += " " + SymBranch + " " + s.Branch
	}
	if location != "" {
		parts = append(parts, ColorGray(location))
	}

	if len(parts) == 0 {
		return ""
	}
	line := lineIndent + strings.Join(parts, ColorGray(" "+SymDot+" "))
	return fitToTerminal(line, location, short)
}

// compactCount renders a count as 1.2k / 3.4M so the line stays short enough to
// survive a narrow terminal without wrapping.
func compactCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func abbreviateHome(path string) string {
	if path == "" {
		return ""
	}
	slashed := filepath.ToSlash(path)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		slashedHome := filepath.ToSlash(home)
		if strings.HasPrefix(slashed, slashedHome) {
			return "~" + strings.TrimPrefix(slashed, slashedHome)
		}
	}
	return slashed
}

// currentBranch reads .git/HEAD directly rather than shelling out to git. This
// runs before every prompt, and a subprocess per prompt is a cost the status
// line should not impose.
func currentBranch(root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(data))
	if ref, ok := strings.CutPrefix(head, "ref: refs/heads/"); ok {
		return ref
	}
	if len(head) >= 7 && !strings.Contains(head, " ") {
		return head[:7] // detached HEAD
	}
	return ""
}

// visibleWidth is the rendered width of s, ignoring ANSI styling.
func visibleWidth(s string) int {
	width, inEscape := 0, false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape:
			if r == 'm' {
				inEscape = false
			}
		default:
			width++
		}
	}
	return width
}

// fitToTerminal shortens the location segment when the line would wrap. A
// wrapped status line costs two rows above every prompt, which defeats the
// point of a single glanceable line.
func fitToTerminal(line, location, shortLocation string) string {
	if location == shortLocation || location == "" {
		return line
	}
	width := terminalWidth()
	if width <= 0 || visibleWidth(line) <= width {
		return line
	}
	return strings.Replace(line, location, shortLocation, 1)
}

func terminalWidth() int {
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return width
	}
	return 0
}

// colorForMode is the one colour per mode shared by every surface that shows
// it: plan cyan, agent grey, edit green, full-access yellow.
func colorForMode(mode PermissionMode) func(string) string {
	switch NormalizeMode(mode) {
	case PermissionPlan:
		return ColorCyan
	case PermissionEdit:
		return ColorGreen
	case PermissionFull:
		return ColorYellow
	default:
		return ColorGray
	}
}
