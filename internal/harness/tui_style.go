package harness

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"fastllm/internal/config"
)

// ANSI color and style escape codes
const (
	ansiReset        = "\033[0m"
	ansiBold         = "\033[1m"
	ansiDim          = "\033[2m"
	ansiItalic       = "\033[3m"
	ansiUnderline    = "\033[4m"
	ansiRed          = "\033[31m"
	ansiGreen        = "\033[32m"
	ansiYellow       = "\033[33m"
	ansiBlue         = "\033[34m"
	ansiMagenta      = "\033[35m"
	ansiCyan         = "\033[36m"
	ansiWhite        = "\033[37m"
	ansiGray         = "\033[90m"
	ansiBrightRed    = "\033[91m"
	ansiBrightGreen  = "\033[92m"
	ansiBrightYellow = "\033[93m"
	ansiBrightBlue   = "\033[94m"
	ansiBrightPurple = "\033[95m"
	ansiBrightCyan   = "\033[96m"
	ansiBrightWhite  = "\033[97m"
)

// Geometric Unicode symbols (strictly no emojis)
const (
	SymPrompt    = "❯"
	SymCheck     = "✓"
	SymCross     = "✗"
	SymDot       = "·"
	SymBullet    = "●"
	SymBranch    = "◈"
	SymCornerTL  = "╭"
	SymCornerTR  = "╮"
	SymCornerBL  = "╰"
	SymCornerBR  = "╯"
	SymHLine     = "─"
	SymVLine     = "│"
	SymTeeL      = "├"
	SymTeeR      = "┤"
	SymArrowR    = "→"
)

var ansiRegexp = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// ColorsEnabled checks if the terminal supports ANSI colors.
func ColorsEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if strings.ToLower(os.Getenv("TERM")) == "dumb" {
		return false
	}
	return true
}

func applyStyle(code, s string) string {
	if !ColorsEnabled() || s == "" {
		return s
	}
	return code + s + ansiReset
}

func StyleBold(s string) string        { return applyStyle(ansiBold, s) }
func StyleDim(s string) string         { return applyStyle(ansiDim, s) }
func StyleItalic(s string) string      { return applyStyle(ansiItalic, s) }
func ColorRed(s string) string         { return applyStyle(ansiBrightRed, s) }
func ColorGreen(s string) string       { return applyStyle(ansiBrightGreen, s) }
func ColorYellow(s string) string      { return applyStyle(ansiBrightYellow, s) }
func ColorBlue(s string) string        { return applyStyle(ansiBrightBlue, s) }
func ColorMagenta(s string) string     { return applyStyle(ansiBrightPurple, s) }
func ColorCyan(s string) string        { return applyStyle(ansiBrightCyan, s) }
func ColorGray(s string) string        { return applyStyle(ansiGray, s) }
func ColorWhite(s string) string       { return applyStyle(ansiWhite, s) }
func ColorBrightWhite(s string) string { return applyStyle(ansiBrightWhite, s) }

// StripANSI removes ANSI escape sequences to compute true visual length.
func StripANSI(s string) string {
	return ansiRegexp.ReplaceAllString(s, "")
}

// VisualLen returns the number of visible runes ignoring ANSI escape codes.
func VisualLen(s string) int {
	return utf8.RuneCountInString(StripANSI(s))
}

// PadRight pads s with spaces until its visual width equals width.
func PadRight(s string, width int) string {
	vLen := VisualLen(s)
	if vLen >= width {
		return s
	}
	return s + strings.Repeat(" ", width-vLen)
}

// FormatPrompt generates the interactive REPL prompt string.
func FormatPrompt(model string) string {
	brand := ColorCyan(StyleBold("fastllm"))
	chevron := ColorGreen(StyleBold(SymPrompt))
	if model != "" {
		badge := ColorGray("[") + ColorBrightWhite(model) + ColorGray("]")
		return fmt.Sprintf("\n%s %s %s ", brand, badge, chevron)
	}
	return fmt.Sprintf("\n%s %s ", brand, chevron)
}

// FormatDivider produces a horizontal rule with an optional label.
func FormatDivider(label string, width int) string {
	if width <= 0 {
		width = 68
	}
	if label == "" {
		return ColorGray(strings.Repeat(SymHLine, width))
	}
	prefix := ColorGray(SymHLine + SymHLine + " ")
	suffix := " "
	labelFormatted := ColorGray(label)
	rem := width - 4 - VisualLen(label)
	if rem < 2 {
		rem = 2
	}
	return prefix + labelFormatted + ColorGray(suffix+strings.Repeat(SymHLine, rem))
}

// FormatCard wraps an array of text lines in a modern rounded box.
func FormatCard(title string, lines []string, width int) string {
	if width <= 0 {
		width = 72
	}
	var b strings.Builder

	// Top border
	b.WriteString(ColorGray(SymCornerTL + SymHLine + " "))
	b.WriteString(ColorBrightWhite(StyleBold(title)))
	b.WriteString(" ")
	titleVisLen := VisualLen(title)
	topDashes := width - titleVisLen - 5
	if topDashes < 2 {
		topDashes = 2
	}
	b.WriteString(ColorGray(strings.Repeat(SymHLine, topDashes) + SymCornerTR + "\n"))

	// Content lines
	contentWidth := width - 6
	for _, line := range lines {
		vLen := VisualLen(line)
		padding := contentWidth - vLen
		if padding < 0 {
			padding = 0
		}
		b.WriteString(ColorGray(SymVLine) + "  ")
		b.WriteString(line)
		b.WriteString(strings.Repeat(" ", padding))
		b.WriteString("  " + ColorGray(SymVLine) + "\n")
	}

	// Bottom border
	b.WriteString(ColorGray(SymCornerBL + strings.Repeat(SymHLine, width-2) + SymCornerBR))
	return b.String()
}

// FormatKV formats a label-value pair with dimmed label and bright value.
func FormatKV(label, val string, labelWidth int) string {
	dimmed := ColorGray(PadRight(label, labelWidth))
	return dimmed + "  " + ColorBrightWhite(val)
}

// FormatWelcomeBanner creates the hero banner displayed upon starting the REPL.
func FormatWelcomeBanner(dir, model, configPath string, isGit bool, rulesCount int, allowCmds bool) string {
	gitStatus := ColorYellow("no")
	if isGit {
		gitStatus = ColorGreen("yes") + " " + ColorGray("("+SymBranch+" git)")
	}

	cmdStatus := ColorYellow("disabled")
	if allowCmds {
		cmdStatus = ColorGreen("enabled") + " " + ColorGray("(file ops + local commands)")
	}

	rulesStatus := ColorGray("none")
	if rulesCount > 0 {
		rulesStatus = ColorGreen(fmt.Sprintf("%d discovered", rulesCount)) + " " + ColorGray("(AGENTS.md, etc.)")
	}

	cfgStr := configPath
	if cfgStr == "" {
		cfgStr = "(default ~/.fastllm/config.json)"
	}

	lines := []string{
		"",
		FormatKV("directory", dir, 12),
		FormatKV("model", model, 12),
		FormatKV("config", cfgStr, 12),
		FormatKV("git repo", gitStatus, 12),
		FormatKV("rules", rulesStatus, 12),
		FormatKV("commands", cmdStatus, 12),
		"",
		ColorGray("Type ") + ColorCyan("/help") + ColorGray(" for slash commands or enter your prompt below."),
	}

	return FormatCard("fastllm "+SymDot+" autonomous agent repl", lines, 74)
}

// FormatToolCall renders a structured tool invocation card header.
func FormatToolCall(toolName, argsSummary string) string {
	var badge string
	switch toolName {
	case "read_file", "list_files", "search_files":
		badge = ColorCyan("[" + toolName + "]")
	case "write_file", "edit_file", "patch_file":
		badge = ColorYellow("[" + toolName + "]")
	case "run_command", "process_status", "kill_process":
		badge = ColorMagenta("[" + toolName + "]")
	case "finish_task":
		badge = ColorGreen("[" + toolName + "]")
	default:
		badge = ColorWhite("[" + toolName + "]")
	}

	summary := ColorWhite(argsSummary)
	return fmt.Sprintf("\n  %s %s %s", ColorGray(SymCornerTL+SymHLine), badge, summary)
}

// FormatToolResult renders the outcome of a tool execution with structured indentation.
func FormatToolResult(toolName, result string, maxPreviewLines int) string {
	if maxPreviewLines <= 0 {
		maxPreviewLines = 3
	}

	trimmed := strings.TrimSpace(result)
	lines := strings.Split(trimmed, "\n")

	var b strings.Builder
	isErr := strings.HasPrefix(trimmed, "Error") || strings.HasPrefix(trimmed, "error")

	// Print first few lines of output
	showCount := len(lines)
	if showCount > maxPreviewLines {
		showCount = maxPreviewLines
	}

	for i := 0; i < showCount; i++ {
		line := lines[i]
		if len(line) > 100 {
			line = line[:97] + "..."
		}
		b.WriteString(fmt.Sprintf("  %s  %s\n", ColorGray(SymVLine), ColorGray(line)))
	}

	// Bottom line with status indicator
	statusSym := ColorGreen(SymCheck)
	if isErr {
		statusSym = ColorRed(SymCross)
	}

	var statusText string
	if isErr {
		statusText = ColorRed("error returned")
	} else if len(lines) > maxPreviewLines {
		statusText = ColorGray(fmt.Sprintf("%s completed (%d lines output)", statusSym, len(lines)))
	} else {
		statusText = ColorGray(fmt.Sprintf("%s completed", statusSym))
	}

	b.WriteString(fmt.Sprintf("  %s %s", ColorGray(SymCornerBL+SymHLine), statusText))
	return b.String()
}

// FormatTurnSummary formats the metrics at the completion of a turn into a sleek divider.
func FormatTurnSummary(tm TurnMetrics) string {
	costStr := "$0.00"
	if tm.EstimatedCost > 0 {
		costStr = fmt.Sprintf("$%.4f", tm.EstimatedCost)
	}

	parts := []string{
		fmt.Sprintf("%.1fs", tm.Duration.Seconds()),
		fmt.Sprintf("%.1f tok/s", tm.TokensPerSecond),
		fmt.Sprintf("%d in / %d out", tm.PromptTokens, tm.CompletionTokens),
		costStr,
	}

	label := strings.Join(parts, " "+ColorGray(SymDot)+" ")
	return "\n" + FormatDivider(label, 70)
}

// HighlightDiff applies ANSI syntax coloring to git diff text.
func HighlightDiff(diffText string) string {
	lines := strings.Split(diffText, "\n")
	var out []string
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			out = append(out, ColorBrightWhite(StyleBold(line)))
		} else if strings.HasPrefix(line, "@@") {
			out = append(out, ColorCyan(line))
		} else if strings.HasPrefix(line, "+") {
			out = append(out, ColorGreen(line))
		} else if strings.HasPrefix(line, "-") {
			out = append(out, ColorRed(line))
		} else if strings.HasPrefix(line, "index ") {
			out = append(out, ColorGray(line))
		} else {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// FormatHelp renders the styled slash command reference.
func FormatHelp() string {
	var b strings.Builder
	b.WriteString("\n" + ColorBrightWhite(StyleBold("fastllm Slash Commands")) + "\n\n")

	type cmdEntry struct {
		cmd  string
		desc string
	}

	renderSection := func(title string, entries []cmdEntry) {
		b.WriteString(ColorCyan(StyleBold(SymBullet+" "+title)) + "\n")
		for _, e := range entries {
			cmdStyled := ColorBrightWhite(PadRight("  "+e.cmd, 26))
			descStyled := ColorGray(e.desc)
			b.WriteString(cmdStyled + descStyled + "\n")
		}
		b.WriteString("\n")
	}

	renderSection("Session & Control", []cmdEntry{
		{"/help", "Display this command reference"},
		{"/status", "Inspect session token usage, latency, cost, and jobs"},
		{"/clear", "Reset conversation context history"},
		{"/dir <path>", "Switch active working directory and reload workspace rules"},
		{"/exit, /quit", "Exit the interactive session"},
	})

	renderSection("Models & Endpoints", []cmdEntry{
		{"/models, /model", "List configured model endpoints"},
		{"/model <name>", "Switch active model (e.g. /model muse-glimmer)"},
		{"/models add <id> <url>", "Register a new inference endpoint in config.json"},
	})

	renderSection("Git & Checkpoints", []cmdEntry{
		{"/diff", "Syntax-highlighted git diff of uncommitted changes"},
		{"/undo", "Rollback working directory to pre-turn git checkpoint"},
		{"/rules", "Inspect discovered workspace instruction files"},
	})

	renderSection("Background Processes", []cmdEntry{
		{"/ps", "List active background processes"},
		{"/kill <id>", "Terminate a background process (e.g. /kill proc-1)"},
	})

	return b.String()
}

// FormatStatusCard renders a comprehensive session metrics and environment card.
func FormatStatusCard(dir, model string, rulesCount int, sm SessionMetrics, pm *ProcessManager) string {
	costStr := "$0.0000"
	if sm.TotalCost > 0 {
		costStr = fmt.Sprintf("$%.4f", sm.TotalCost)
	}

	procsCount := 0
	if pm != nil {
		procs := pm.List()
		for _, p := range procs {
			if !p.Exited {
				procsCount++
			}
		}
	}

	tokenSummary := fmt.Sprintf("%d total (%d prompt %s %d completion)",
		sm.TotalTokens, sm.TotalPromptTokens, SymDot, sm.TotalCompletionTokens)

	lines := []string{
		"",
		FormatKV("directory", dir, 12),
		FormatKV("model", model, 12),
		FormatKV("rules", fmt.Sprintf("%d loaded", rulesCount), 12),
		FormatKV("turns", fmt.Sprintf("%d", sm.TotalTurns), 12),
		FormatKV("tokens", tokenSummary, 12),
		FormatKV("duration", fmt.Sprintf("%.2fs", sm.TotalDuration.Seconds()), 12),
		FormatKV("cost", costStr, 12),
		FormatKV("processes", fmt.Sprintf("%d active", procsCount), 12),
		"",
	}

	return FormatCard("Session Status", lines, 74)
}

// FormatModelsTable renders a cleanly aligned table of configured models.
func FormatModelsTable(models []config.ModelEndpoint, activeModel, configPath string) string {
	var lines []string
	lines = append(lines, "")

	header := fmt.Sprintf("  %-2s %-18s %-24s %s", "", "ID", "NAME", "ENDPOINT")
	lines = append(lines, ColorGray(header))
	lines = append(lines, ColorGray("  "+strings.Repeat(SymHLine, 72)))

	for _, m := range models {
		marker := "  "
		isCurrent := strings.EqualFold(m.ID, activeModel)
		if isCurrent {
			marker = ColorGreen(SymBullet) + " "
		}
		desc := m.Name
		if desc == "" {
			desc = m.ID
		}
		if VisualLen(desc) > 22 {
			desc = desc[:19] + "..."
		}

		idText := PadRight(m.ID, 18)
		var idCol string
		if isCurrent {
			idCol = ColorBrightWhite(StyleBold(idText))
		} else {
			idCol = ColorCyan(idText)
		}

		descText := PadRight(desc, 24)
		descCol := ColorWhite(descText)
		urlCol := ColorGray(m.URL)

		row := fmt.Sprintf("  %s %s %s %s", marker, idCol, descCol, urlCol)
		lines = append(lines, row)
	}

	lines = append(lines, "")
	if configPath != "" {
		lines = append(lines, ColorGray("Config: ")+ColorWhite(configPath))
	}

	return FormatCard("Configured Model Endpoints", lines, 80)
}
