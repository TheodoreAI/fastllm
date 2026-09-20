package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"fastllm/internal/config"
)

// ANSI color and style escape codes
const (
	ansiReset        = "\033[0m"
	ansiBold         = "\033[1m"
	ansiDim          = "\033[2m"
	ansiItalic       = "\033[3m"
	ansiInverse      = "[7m"
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
	SymPrompt   = "❯"
	SymCheck    = "✓"
	SymCross    = "✗"
	SymDot      = "·"
	SymBullet   = "●"
	SymBranch   = "◈"
	SymCornerTL = "╭"
	SymCornerTR = "╮"
	SymCornerBL = "╰"
	SymCornerBR = "╯"
	SymHLine    = "─"
	SymVLine    = "│"
	SymTeeL     = "├"
	SymTeeR     = "┤"
	SymArrowR   = "→"
)

var ansiRegexp = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
var (
	markdownBoldRegexp = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	markdownCodeRegexp = regexp.MustCompile("`([^`]+)`")
	markdownLinkRegexp = regexp.MustCompile(`\[([^]]+)\]\(([^)]+)\)`)
)

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

func StyleBold(s string) string      { return applyStyle(ansiBold, s) }
func StyleDim(s string) string       { return applyStyle(ansiDim, s) }
func StyleItalic(s string) string    { return applyStyle(ansiItalic, s) }
func StyleUnderline(s string) string { return applyStyle(ansiUnderline, s) }
func ColorRed(s string) string       { return applyStyle(ansiBrightRed, s) }
func ColorGreen(s string) string     { return applyStyle(ansiBrightGreen, s) }
func ColorYellow(s string) string    { return applyStyle(ansiBrightYellow, s) }
func ColorBlue(s string) string      { return applyStyle(ansiBrightBlue, s) }
func ColorMagenta(s string) string   { return applyStyle(ansiBrightPurple, s) }
func ColorCyan(s string) string      { return applyStyle(ansiBrightCyan, s) }

// ColorInverse swaps foreground and background, which is how the inline
// chooser marks the highlighted option without relying on a colour that may
// not contrast against the user's terminal theme.
func ColorInverse(s string) string     { return applyStyle(ansiInverse, s) }
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

// FormatShellPrompt generates the prompt displayed when Shell Mode is active.
func FormatShellPrompt(dir string) string {
	brand := ColorYellow(StyleBold("fastllm"))
	base := filepath.Base(dir)
	if base == "" || base == "." {
		base = dir
	}
	badge := ColorGray("[") + ColorYellow("shell: "+base) + ColorGray("]")
	chevron := ColorYellow(StyleBold(SymPrompt))
	return fmt.Sprintf("\n%s %s %s ", brand, badge, chevron)
}

// ClearScreen clears the terminal display and scrollback buffer.
func ClearScreen() {
	if ColorsEnabled() {
		fmt.Print("\033[2J\033[H\033[3J")
	}
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
	minWidth := VisualLen(title) + 8
	for _, line := range lines {
		if v := VisualLen(line) + 6; v > minWidth {
			minWidth = v
		}
	}
	if width < minWidth {
		width = minWidth
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
		FormatKV("build", ColorGray(BuildIdentity()), 12),
	}
	if exe := runningExecutable(); exe != "" {
		lines = append(lines, FormatKV("binary", ColorGray(exe), 12))
	}
	lines = append(lines,
		"",
		ColorGray("Type ")+ColorCyan("/help")+ColorGray(" for slash commands or enter your prompt below."),
	)

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
	case "web_search", "web_fetch":
		badge = ColorBlue("[" + toolName + "]")
	case "finish_task":
		badge = ColorGreen("[" + toolName + "]")
	default:
		badge = ColorWhite("[" + toolName + "]")
	}

	summary := ColorWhite(argsSummary)
	return fmt.Sprintf("\n  %s %s %s %s", ColorGray(SymCornerTL+SymHLine), badge, ColorGray("pending"), summary)
}

func FormatToolState(toolName, state string, elapsed time.Duration) string {
	detail := state
	if elapsed > 0 {
		detail = fmt.Sprintf("%s in %s", state, elapsed.Round(time.Millisecond))
	}
	return fmt.Sprintf("  %s  %s %s", ColorGray(SymVLine), ColorCyan(toolName), ColorGray(detail))
}

// FormatToolResult renders the outcome of a tool execution with structured indentation.
func FormatToolResult(toolName, result string, maxPreviewLines int) string {
	if maxPreviewLines <= 0 {
		maxPreviewLines = 3
	}

	trimmed := strings.TrimSpace(result)
	lines := strings.Split(trimmed, "\n")

	var b strings.Builder
	isErr := strings.HasPrefix(trimmed, "Error") || strings.HasPrefix(trimmed, "error") || strings.HasPrefix(trimmed, "Permission denied")

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
		if toolName == "patch_file" || toolName == "edit_file" {
			line = HighlightDiff(line)
		} else {
			line = ColorGray(line)
		}
		b.WriteString(fmt.Sprintf("  %s  %s\n", ColorGray(SymVLine), line))
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

// FormatMarkdown renders Markdown structures with a default width of 76 characters.
func FormatMarkdown(markdown string) string {
	return FormatMarkdownWidth(markdown, 76)
}

// FormatMarkdownWidth renders Markdown structures wrapped cleanly to the specified terminal width.
func FormatMarkdownWidth(markdown string, width int) string {
	if width <= 0 {
		width = 76
	}
	lines := strings.Split(strings.TrimSpace(markdown), "\n")
	var out []string
	inCodeBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			if inCodeBlock {
				language := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
				label := "code"
				if language != "" {
					label += ": " + language
				}
				out = append(out, FormatDivider(label, width))
			} else {
				out = append(out, FormatDivider("", width))
			}
			continue
		}
		if inCodeBlock {
			out = append(out, "  "+ColorBrightWhite(line))
			continue
		}
		if isMarkdownRule(trimmed) {
			out = append(out, FormatDivider("", width))
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "### "):
			out = append(out, ColorCyan(StyleBold(strings.TrimSpace(trimmed[4:]))))
		case strings.HasPrefix(trimmed, "## "):
			out = append(out, "\n"+ColorCyan(StyleBold(strings.TrimSpace(trimmed[3:]))))
		case strings.HasPrefix(trimmed, "# "):
			out = append(out, "\n"+ColorBrightWhite(StyleBold(strings.TrimSpace(trimmed[2:]))))
		case strings.HasPrefix(trimmed, "> "):
			quoteText := renderInlineMarkdown(strings.TrimSpace(trimmed[2:]))
			quoteLines := wrapLine(quoteText, width-4)
			for _, ql := range quoteLines {
				out = append(out, ColorGray(SymVLine+" ")+ql)
			}
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			itemText := renderInlineMarkdown(strings.TrimSpace(trimmed[2:]))
			itemLines := wrapLine(itemText, width-4)
			for i, il := range itemLines {
				if i == 0 {
					out = append(out, "  "+ColorCyan(SymBullet)+" "+il)
				} else {
					out = append(out, "    "+il)
				}
			}
		default:
			rendered := renderInlineMarkdown(line)
			if strings.TrimSpace(rendered) == "" {
				out = append(out, "")
			} else {
				out = append(out, wrapLine(rendered, width)...)
			}
		}
	}
	return strings.Join(out, "\n")
}

// wrapLine breaks a line of text into multiple lines wrapped at the specified visual width.
func wrapLine(text string, width int) []string {
	if width <= 0 || VisualLen(text) <= width {
		return []string{text}
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}

	var lines []string
	var current strings.Builder
	currentLen := 0

	for _, word := range words {
		wLen := VisualLen(word)
		if currentLen == 0 {
			current.WriteString(word)
			currentLen = wLen
		} else if currentLen+1+wLen <= width {
			current.WriteString(" ")
			current.WriteString(word)
			currentLen += 1 + wLen
		} else {
			lines = append(lines, current.String())
			current.Reset()
			current.WriteString(word)
			currentLen = wLen
		}
	}
	if currentLen > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

func isMarkdownRule(line string) bool {
	compact := strings.ReplaceAll(line, " ", "")
	if len(compact) < 3 {
		return false
	}
	for _, marker := range []byte{'-', '*', '_'} {
		if strings.Trim(compact, string(marker)) == "" {
			return true
		}
	}
	return false
}

func renderInlineMarkdown(line string) string {
	line = markdownLinkRegexp.ReplaceAllStringFunc(line, func(match string) string {
		parts := markdownLinkRegexp.FindStringSubmatch(match)
		return StyleUnderline(parts[1]) + ColorGray(" ("+parts[2]+")")
	})
	line = markdownBoldRegexp.ReplaceAllStringFunc(line, func(match string) string {
		parts := markdownBoldRegexp.FindStringSubmatch(match)
		return StyleBold(parts[1])
	})
	return markdownCodeRegexp.ReplaceAllStringFunc(line, func(match string) string {
		parts := markdownCodeRegexp.FindStringSubmatch(match)
		return ColorYellow(parts[1])
	})
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
		{"/sessions", "List saved interactive sessions"},
		{"/resume <id>", "Resume a saved session"},
		{"/session [id]", "Show session details"},
		{"/rename <title>", "Rename the active session"},
		{"/delete-session <id>", "Delete an inactive saved session"},
		{"/new", "Save the current session and start a new one"},
		{"/c, /clear", "Clear conversation context and declutter UI screen"},
		{"/cls", "Clear terminal screen without resetting context"},
		{"/copy, /yank", "Copy last response to OS clipboard (/copy all for full log)"},
		{"/dir <path>", "Switch active working directory and reload workspace rules"},
		{"/exit, /quit", "Exit the interactive session"},
	})

	renderSection("Runtime Settings", []cmdEntry{
		{"/set", "Show current session runtime settings"},
		{"/set turns <1-100>", "Set maximum agent tool turns per prompt"},
		{"/set timeout <sec>", "Set command timeout (1-3600 seconds)"},
		{"/set think <level>", "Set off, low, medium, or high reasoning"},
		{"/set commands <on|off>", "Enable or disable command/process tools"},
		{"/set permissions <mode>", "Set ask, read-only, or auto tool permissions"},
		{"/set output <mode>", "Set compact or expanded tool results"},
	})

	renderSection("Shell & Execution", []cmdEntry{
		{"/shell, /sh", "Toggle interactive Shell Mode (run host terminal commands)"},
		{"/sh <cmd>", "Execute a shell command in working dir (e.g. /sh ls -la)"},
		{"!<cmd>, $ <cmd>", "Execute command immediately (e.g. !git status, !go test)"},
	})

	renderSection("Models & Endpoints", []cmdEntry{
		{"/models, /model", "List configured model endpoints"},
		{"/model <name>", "Switch active model (e.g. /model llama3.1)"},
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

	renderSection("Web Tools", []cmdEntry{
		{"/search <query>", "Search the public web (DuckDuckGo / Brave)"},
		{"/fetch <url>", "Fetch and read web page content converted to Markdown"},
	})

	return b.String()
}

func FormatRuntimeCard(maxTurns int, timeout time.Duration, thinkLevel string, allowCommands bool, permissionMode PermissionMode, sessionID string) string {
	think := thinkLevel
	if think == "" {
		think = "off"
	}
	commands := "off"
	if allowCommands {
		commands = "on"
	}
	lines := []string{
		"",
		FormatKV("session", sessionID, 12),
		FormatKV("max turns", fmt.Sprintf("%d", maxTurns), 12),
		FormatKV("timeout", timeout.String(), 12),
		FormatKV("thinking", think, 12),
		FormatKV("commands", commands, 12),
		FormatKV("permissions", string(permissionMode), 12),
		"",
	}
	return FormatCard("Runtime Settings", lines, 74)
}

func FormatPermissionPrompt(toolName, summary string) string {
	runes := []rune(summary)
	if len(runes) > 52 {
		summary = string(runes[:49]) + "..."
	}
	lines := []string{
		"",
		FormatKV("tool", toolName, 10),
		FormatKV("request", summary, 10),
		"",
	}
	return FormatCard("Permission Required", lines, 74)
}

func FormatSessionsTable(sessions []InteractiveSession, activeID string) string {
	if len(sessions) == 0 {
		return ColorGray("  No saved sessions.")
	}
	lines := []string{"", ColorGray("  UPDATED           ID                         TITLE")}
	lines = append(lines, ColorGray("  "+strings.Repeat(SymHLine, 70)))
	for _, session := range sessions {
		marker := " "
		if session.ID == activeID {
			marker = SymBullet
		}
		title := session.Title
		if VisualLen(title) > 28 {
			title = string([]rune(title)[:25]) + "..."
		}
		line := fmt.Sprintf("  %s %-16s %-26s %s", marker, session.UpdatedAt.Local().Format("2006-01-02 15:04"), session.ID, title)
		lines = append(lines, line)
	}
	lines = append(lines, "")
	return FormatCard("Saved Sessions", lines, 86)
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
		FormatKV("observations", fmt.Sprintf("%d archived %s %d packed %s %d reduced", sm.ObservationEfficiency.Archived, SymDot, sm.ObservationEfficiency.Packed, SymDot, sm.ObservationEfficiency.Reduced), 12),
		FormatKV("context saved", fmt.Sprintf("%d bytes", sm.ObservationEfficiency.ProjectedBytesSaved), 12),
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

// formatCharCount renders a character count compactly. Context sizes run to six
// digits, which are hard to compare at a glance mid-session.
func formatCharCount(chars int) string {
	switch {
	case chars >= 1_000_000:
		return fmt.Sprintf("%.1fM chars", float64(chars)/1_000_000)
	case chars >= 1_000:
		return fmt.Sprintf("%.0fk chars", float64(chars)/1_000)
	default:
		return fmt.Sprintf("%d chars", chars)
	}
}
