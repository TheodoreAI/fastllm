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

// The hue-named helpers below draw from the active theme (theme.go): ColorGray
// is its muted role, ColorCyan its accent, and so on.
func StyleBold(s string) string      { return applyStyle(ansiBold, s) }
func StyleDim(s string) string       { return applyStyle(ansiDim, s) }
func StyleItalic(s string) string    { return applyStyle(ansiItalic, s) }
func StyleUnderline(s string) string { return applyStyle(ansiUnderline, s) }
func ColorRed(s string) string       { return applyStyle(themeSeqs[roleError], s) }
func ColorGreen(s string) string     { return applyStyle(themeSeqs[roleOk], s) }
func ColorYellow(s string) string    { return applyStyle(themeSeqs[roleWarn], s) }
func ColorBlue(s string) string      { return applyStyle(themeSeqs[roleAccent2], s) }
func ColorMagenta(s string) string   { return applyStyle(themeSeqs[rolePurple], s) }
func ColorCyan(s string) string      { return applyStyle(themeSeqs[roleAccent], s) }

// ColorInverse swaps foreground and background, which is how the inline
// chooser marks the highlighted option without relying on a colour that may
// not contrast against the user's terminal theme.
func ColorInverse(s string) string     { return applyStyle(ansiInverse, s) }
func ColorGray(s string) string        { return applyStyle(themeSeqs[roleMuted], s) }
func ColorWhite(s string) string       { return applyStyle(themeSeqs[roleText], s) }
func ColorBrightWhite(s string) string { return applyStyle(themeSeqs[roleValue], s) }

// ColorBorder colours card frames and dividers, dimmer than label text.
func ColorBorder(s string) string { return applyStyle(themeSeqs[roleFrame], s) }

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
		return ColorBorder(strings.Repeat(SymHLine, width))
	}
	prefix := ColorBorder(SymHLine + SymHLine + " ")
	suffix := " "
	labelFormatted := ColorGray(label)
	rem := width - 4 - VisualLen(label)
	if rem < 2 {
		rem = 2
	}
	return prefix + labelFormatted + ColorBorder(suffix+strings.Repeat(SymHLine, rem))
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
	b.WriteString(ColorBorder(SymCornerTL + SymHLine + " "))
	b.WriteString(ColorBrightWhite(StyleBold(title)))
	b.WriteString(" ")
	titleVisLen := VisualLen(title)
	topDashes := width - titleVisLen - 5
	if topDashes < 2 {
		topDashes = 2
	}
	b.WriteString(ColorBorder(strings.Repeat(SymHLine, topDashes) + SymCornerTR + "\n"))

	// Content lines
	contentWidth := width - 6
	for _, line := range lines {
		vLen := VisualLen(line)
		padding := contentWidth - vLen
		if padding < 0 {
			padding = 0
		}
		b.WriteString(ColorBorder(SymVLine) + "  ")
		b.WriteString(line)
		b.WriteString(strings.Repeat(" ", padding))
		b.WriteString("  " + ColorBorder(SymVLine) + "\n")
	}

	// Bottom border
	b.WriteString(ColorBorder(SymCornerBL + strings.Repeat(SymHLine, width-2) + SymCornerBR))
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
	case "read_file", "list_files", "search_files", "glob_files":
		badge = ColorCyan("[" + toolName + "]")
	case "write_file", "edit_file", "patch_file":
		badge = ColorYellow("[" + toolName + "]")
	case "run_command", "process_status", "kill_process", "spawn_agent", "agent_status", "send_agent_message", "cancel_agent":
		badge = ColorMagenta("[" + toolName + "]")
	case "web_search", "web_fetch":
		badge = ColorBlue("[" + toolName + "]")
	case "finish_task":
		badge = ColorGreen("[" + toolName + "]")
	default:
		badge = ColorWhite("[" + toolName + "]")
	}

	summary := ColorWhite(sanitizeUntrusted(argsSummary))
	return fmt.Sprintf("\n  %s %s %s %s", ColorBorder(SymCornerTL+SymHLine), badge, ColorGray("pending"), summary)
}

func FormatToolState(toolName, state string, elapsed time.Duration) string {
	detail := state
	if elapsed > 0 {
		detail = fmt.Sprintf("%s in %s", state, elapsed.Round(time.Millisecond))
	}
	return fmt.Sprintf("  %s  %s %s", ColorBorder(SymVLine), ColorCyan(toolName), ColorGray(detail))
}

// FormatToolResult renders the outcome of a tool execution with structured indentation.
func FormatToolResult(toolName, result string, maxPreviewLines int) string {
	result = sanitizeUntrusted(result)
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
		b.WriteString(fmt.Sprintf("  %s  %s\n", ColorBorder(SymVLine), line))
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

	b.WriteString(fmt.Sprintf("  %s %s", ColorBorder(SymCornerBL+SymHLine), statusText))
	return b.String()
}

// TerminalBoxOptions configures the rendering of a command execution inside a terminal box.
type TerminalBoxOptions struct {
	Command     string
	Output      string
	Width       int
	ExitCode    int
	Duration    time.Duration
	IsError     bool
	Canceled    bool
	MaxLines    int
	AgentCalled bool
}

// FormatTerminalBox renders a command execution inside a styled terminal box frame.
func FormatTerminalBox(opts TerminalBoxOptions) string {
	opts.Command = sanitizeUntrusted(opts.Command)
	opts.Output = sanitizeOutput(opts.Output)
	width := opts.Width
	if width <= 0 {
		width = 76
	}
	contentWidth := width - 4
	if contentWidth < 10 {
		contentWidth = 10
		width = contentWidth + 4
	}

	var b strings.Builder

	// Title
	title := "terminal"
	titleColor := ColorYellow
	if opts.AgentCalled {
		title = "terminal " + SymDot + " agent"
		titleColor = ColorCyan
	}

	// 1. Top border: ╭─ terminal ─────────────────────────╮
	b.WriteString(ColorBorder(SymCornerTL + SymHLine + " "))
	b.WriteString(titleColor(StyleBold(title)))
	b.WriteString(" ")
	titleVisLen := VisualLen(title)
	topDashes := width - titleVisLen - 5
	if topDashes < 2 {
		topDashes = 2
	}
	b.WriteString(ColorBorder(strings.Repeat(SymHLine, topDashes) + SymCornerTR + "\n"))

	// 2. Command Prompt line: │ ❯ command                 │
	cmdText := strings.TrimSpace(opts.Command)
	prompt := ColorGreen(StyleBold(SymPrompt)) + " " + ColorBrightWhite(cmdText)
	b.WriteString(formatTerminalBoxLine(prompt, contentWidth) + "\n")

	// 3. Divider: ├─────────────────────────────────────────┤
	b.WriteString(ColorBorder(SymTeeL + strings.Repeat(SymHLine, width-2) + SymTeeR + "\n"))

	// 4. Output lines
	output := strings.TrimRight(opts.Output, "\r\n")
	var rawLines []string
	if output != "" {
		rawLines = strings.Split(output, "\n")
	}

	showLines := rawLines
	truncatedCount := 0
	if opts.MaxLines > 0 && len(rawLines) > opts.MaxLines {
		showLines = rawLines[:opts.MaxLines]
		truncatedCount = len(rawLines) - opts.MaxLines
	}

	if len(showLines) == 0 {
		if opts.Canceled {
			b.WriteString(formatTerminalBoxLine(ColorGray("[Command canceled]"), contentWidth) + "\n")
		} else if opts.IsError {
			b.WriteString(formatTerminalBoxLine(ColorRed("[Command failed]"), contentWidth) + "\n")
		} else {
			b.WriteString(formatTerminalBoxLine(ColorGray("(no output)"), contentWidth) + "\n")
		}
	} else {
		for _, line := range showLines {
			line = strings.TrimRight(line, "\r")
			b.WriteString(formatTerminalBoxLine(ColorGray(line), contentWidth) + "\n")
		}
		if truncatedCount > 0 {
			hint := fmt.Sprintf("... %d more lines ...", truncatedCount)
			b.WriteString(formatTerminalBoxLine(ColorGray(hint), contentWidth) + "\n")
		}
	}

	// 5. Footer: ╰─ ✓ exit 0 · 120ms ───────────────────────╯
	var statusSym, statusMsg string
	if opts.Canceled {
		statusSym = ColorYellow("⊘")
		statusMsg = ColorYellow("canceled")
	} else if opts.IsError || opts.ExitCode != 0 {
		statusSym = ColorRed(SymCross)
		if opts.ExitCode != 0 {
			statusMsg = ColorRed(fmt.Sprintf("exit %d", opts.ExitCode))
		} else {
			statusMsg = ColorRed("error")
		}
	} else {
		statusSym = ColorGreen(SymCheck)
		statusMsg = ColorGreen("exit 0")
	}

	durStr := ""
	if opts.Duration > 0 {
		durStr = fmt.Sprintf(" · %.1fs", opts.Duration.Seconds())
		if opts.Duration < time.Second {
			durStr = fmt.Sprintf(" · %dms", opts.Duration.Milliseconds())
		}
	}

	footerContent := fmt.Sprintf("%s %s%s", statusSym, statusMsg, ColorGray(durStr))
	footerVisLen := VisualLen(footerContent)
	bottomDashes := width - footerVisLen - 5
	if bottomDashes < 2 {
		bottomDashes = 2
	}
	b.WriteString(ColorBorder(SymCornerBL+SymHLine+" ") + footerContent + " " + ColorBorder(strings.Repeat(SymHLine, bottomDashes)+SymCornerBR))

	return b.String()
}

func formatTerminalBoxLine(content string, contentWidth int) string {
	clamped := clampToWidth(content, contentWidth)
	vLen := VisualLen(clamped)
	padding := contentWidth - vLen
	if padding < 0 {
		padding = 0
	}
	return ColorBorder(SymVLine) + " " + clamped + strings.Repeat(" ", padding) + " " + ColorBorder(SymVLine)
}

// FormatMarkdown renders Markdown structures with a default width of 76 characters.
func FormatMarkdown(markdown string) string {
	return FormatMarkdownWidth(markdown, 76)
}

// FormatMarkdownWidth renders Markdown structures wrapped cleanly to the specified terminal width.
func FormatMarkdownWidth(markdown string, width int) string {
	markdown = sanitizeUntrusted(markdown)
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
	costStr := FormatCost(tm.EstimatedCost, tm.CostKnown)

	parts := []string{
		fmt.Sprintf("%.1fs", tm.Duration.Seconds()),
		fmt.Sprintf("%.1f tok/s", tm.TokensPerSecond),
		fmt.Sprintf("%d in / %d out", tm.PromptTokens, tm.CompletionTokens),
		costStr,
	}

	label := strings.Join(parts, " "+ColorGray(SymDot)+" ")
	return "\n" + FormatDivider(label, 70)
}

// HighlightDiff applies ANSI syntax coloring and VS Code-style formatting to git diff text.
func HighlightDiff(diffText string) string {
	return HighlightDiffFile(diffText, "")
}

// HighlightDiffFile applies VS Code-style syntax coloring, gutter formatting, and intra-line
// word-diff highlighting to git diff text for a specified file path.
func HighlightDiffFile(diffText, filePath string) string {
	if strings.TrimSpace(diffText) == "" {
		return ""
	}
	opts := DefaultDiffOptions()
	opts.FilePath = filePath
	res := FormatVSCodeDiff(diffText, opts)
	if res == "" {
		return diffText
	}
	return res
}

// FormatHelp renders the styled slash command reference.
func FormatHelp() string {
	var b strings.Builder
	b.WriteString("\n" + ColorBrightWhite(StyleBold("fastllm Slash Commands")) + "\n\n")
	for _, section := range commandSections {
		b.WriteString(ColorCyan(StyleBold(SymBullet+" "+section.Title)) + "\n")
		for _, row := range section.Rows {
			cmdStyled := ColorBrightWhite(PadRight("  "+row.Usage, 26))
			descStyled := ColorGray(row.Desc)
			b.WriteString(cmdStyled + descStyled + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func FormatRuntimeCard(settings InteractiveRuntime, sessionID string) string {
	think := settings.ThinkLevel
	if think == "" {
		think = "off"
	}
	commands := "off"
	if settings.AllowCommands {
		commands = "on"
	}
	lines := []string{
		"",
		FormatKV("session", sessionID, 12),
		FormatKV("max turns", fmt.Sprintf("%d", settings.MaxTurns), 12),
		FormatKV("timeout", settings.CommandTimeout.String(), 12),
		FormatKV("thinking", think, 12),
		FormatKV("commands", commands, 12),
		FormatKV("sandbox", sandboxLabel(settings.Sandbox), 12),
		FormatKV("permissions", colorForMode(settings.PermissionMode)(settings.PermissionMode.Label()), 12),
		FormatKV("budget", FormatBudget(settings.Budget), 12),
		"",
	}
	return FormatCard("Runtime Settings", lines, 74)
}

func FormatPermissionPrompt(toolName, summary string) string {
	// The request is shown whole, wrapped rather than truncated: an approval is
	// only informed if every part of what it permits is visible.
	// Anything that would not print as itself is spelled out instead (I7).
	summary, hidden := revealHidden(summary)
	lines := []string{"", FormatKV("tool", toolName, 10)}
	for i, chunk := range wrapRunes(summary, 58) {
		if i == 0 {
			lines = append(lines, FormatKV("request", chunk, 10))
		} else {
			lines = append(lines, FormatKV("", chunk, 10))
		}
	}
	if hidden {
		lines = append(lines, "", ColorYellow("! This request contains hidden or control characters, shown"),
			ColorYellow("  as ⟨…⟩ above. Approving runs the raw text, not what a"),
			ColorYellow("  terminal would display. Deny unless you expected them."))
	}
	lines = append(lines, "")
	return FormatCard("Permission Required", lines, 74)
}

// wrapRunes splits s into rows of at most width runes, breaking on existing
// newlines as well.
func wrapRunes(s string, width int) []string {
	var rows []string
	for _, line := range strings.Split(s, "\n") {
		runes := []rune(line)
		if len(runes) == 0 {
			rows = append(rows, "")
			continue
		}
		for len(runes) > width {
			rows = append(rows, string(runes[:width]))
			runes = runes[width:]
		}
		rows = append(rows, string(runes))
	}
	return rows
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
	costStr := FormatCost(sm.TotalCost, sm.CostComplete)

	procsCount := 0
	if pm != nil {
		procsCount = pm.ActiveCount()
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

// FormatLegacyConversationsTable renders the conversations still held in the
// legacy web database, marking the ones already pulled into the session store.
func FormatLegacyConversationsTable(conversations []LegacyConversation, imported map[string]bool) string {
	if len(conversations) == 0 {
		return ColorGray("  No conversations in the legacy database.")
	}
	lines := []string{"", ColorGray("      ID      UPDATED           TITLE")}
	lines = append(lines, ColorGray("  "+strings.Repeat(SymHLine, 70)))
	for _, conv := range conversations {
		marker := " "
		if imported[legacyOrigin(conv.ID)] {
			marker = SymBullet
		}
		title := conv.Title
		if strings.TrimSpace(title) == "" {
			title = "(untitled)"
		}
		if VisualLen(title) > 34 {
			title = string([]rune(title)[:31]) + "..."
		}
		updated := "-"
		if !conv.UpdatedAt.IsZero() {
			updated = conv.UpdatedAt.Local().Format("2006-01-02 15:04")
		}
		lines = append(lines, fmt.Sprintf("  %s %-7d %-16s %s", marker, conv.ID, updated, title))
	}
	lines = append(lines, "")
	lines = append(lines, ColorGray("  "+SymBullet+" already imported   ·   /import <id>   ·   /import all"))
	lines = append(lines, "")
	return FormatCard("Legacy Web Conversations", lines, 86)
}

// FormatPermissionKeyLegend renders the key hints for the Bubble Tea TUI's
// permission prompt. It is separate from FormatPermissionPrompt because the
// legacy REPL draws its own inline menu via Choose right after that card;
// putting the legend inside the shared card would double it up there. Deny is
// listed last but bound to enter/esc, so a reflexive keypress is never the
// destructive one.
// FormatPermissionKeyLegend names exactly what [a] grants for the session.
func FormatPermissionKeyLegend(sessionScope string) string {
	return "  " + ColorYellow("Allow?") + "  " +
		ColorGreen("[y]") + " once   " +
		ColorRed("[n]") + " deny   " +
		ColorGray("(enter or esc denies)") + "\n  " +
		ColorGreen("[a]") + " yes, and allow " + sanitizeUntrusted(sessionScope) + " this session"
}

// FormatUntrackedAsDiff formats new or untracked file content as a unified diff with additions.
func FormatUntrackedAsDiff(path, content string) string {
	var b strings.Builder
	cleanPath := filepath.ToSlash(path)
	b.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", cleanPath, cleanPath))
	b.WriteString("new file mode 100644\n")
	b.WriteString("--- /dev/null\n")
	b.WriteString(fmt.Sprintf("+++ b/%s\n", cleanPath))

	trimmed := strings.TrimRight(content, "\r\n")
	if trimmed == "" {
		b.WriteString("@@ -0,0 +0,0 @@\n")
		return b.String()
	}
	lines := strings.Split(trimmed, "\n")
	b.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", len(lines)))
	for _, l := range lines {
		b.WriteString("+" + strings.TrimRight(l, "\r") + "\n")
	}
	return b.String()
}
