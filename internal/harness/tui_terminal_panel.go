package harness

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// The legacy REPL keeps its terminal cards; the full-screen transcript uses
// a flat output panel with the same output limit and explicit exit status.
func formatTUITerminal(opts TerminalBoxOptions) string {
	width := max(1, opts.Width)
	inner := max(1, width-4)
	lines := []string{ColorCyan("$ ") + StyleBold(ColorBrightWhite(sanitizeUntrusted(opts.Command)))}
	output := strings.TrimRight(sanitizeOutput(opts.Output), "\r\n")
	if output != "" {
		rows := strings.Split(output, "\n")
		limit := opts.MaxLines
		if limit <= 0 {
			limit = 15
		}
		for _, row := range rows[:min(limit, len(rows))] {
			lines = append(lines, ColorBrightWhite(clampToWidth(row, inner)))
		}
		if len(rows) > limit {
			lines = append(lines, ColorGray(fmt.Sprintf("… %d more lines · /set output expanded", len(rows)-limit)))
		}
	}
	status := fmt.Sprintf("exit %d", opts.ExitCode)
	if opts.Duration > 0 {
		status += fmt.Sprintf(" · %.1fs", opts.Duration.Seconds())
	}
	if opts.Canceled {
		status = "canceled"
	}
	if opts.IsError || opts.ExitCode != 0 {
		lines = append(lines, ColorRed(status))
	} else {
		lines = append(lines, ColorGreen(status))
	}
	return fillSurface(lipgloss.NewStyle().Background(tuiColorCardBg).Padding(1, 2).Width(width).Render(strings.Join(lines, "\n")), currentTheme.CardBg)
}
