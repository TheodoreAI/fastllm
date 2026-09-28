package harness

import (
	"fmt"
	"path/filepath"
	"strings"
)

// DiffRenderOptions configures the VS Code diff formatter.
type DiffRenderOptions struct {
	FilePath    string // optional override for language detection
	ShowGutter  bool   // show line numbers and gutter (default true)
	Compact     bool   // omit file headers if true
	ForceColor  bool   // force color even in non-TTY (for testing)
}

// DefaultDiffOptions returns the standard VS Code diff render options.
func DefaultDiffOptions() DiffRenderOptions {
	return DiffRenderOptions{
		ShowGutter: true,
		Compact:    false,
	}
}

// FormatVSCodeDiff formats unified diff text into a VS Code-style syntax-highlighted diff.
func FormatVSCodeDiff(diffText string, opts DiffRenderOptions) string {
	diffText = strings.TrimRight(diffText, "\r\n")
	if strings.TrimSpace(diffText) == "" {
		return ""
	}

	files := ParseDiffForDisplay(diffText)
	if len(files) == 0 {
		return diffText
	}

	var sb strings.Builder
	for i, file := range files {
		if i > 0 {
			sb.WriteString("\n")
		}

		targetPath := opts.FilePath
		if targetPath == "" {
			if file.NewPath != "" && file.NewPath != "/dev/null" {
				targetPath = file.NewPath
			} else if file.OldPath != "" {
				targetPath = file.OldPath
			}
		}

		lang := DetectLanguage(targetPath)
		renderFileDiff(&sb, file, targetPath, lang, opts)
	}

	return sb.String()
}

func renderFileDiff(sb *strings.Builder, file ParsedDiffFile, filePath, lang string, opts DiffRenderOptions) {
	showGutter := opts.ShowGutter && file.MaxLine > 0
	gutterWidth := 2
	if file.MaxLine > 0 {
		width := len(fmt.Sprintf("%d", file.MaxLine))
		if width > gutterWidth {
			gutterWidth = width
		}
	}
	if gutterWidth > 6 {
		gutterWidth = 6
	}

	// Render file header if path is available and not in compact mode
	if !opts.Compact && filePath != "" {
		addCount, delCount := countFileDiffStats(file)
		langBadge := ""
		if lang != "" {
			langBadge = "[" + strings.ToUpper(lang) + "] "
		}
		statBadge := ""
		if addCount > 0 || delCount > 0 {
			statBadge = fmt.Sprintf(" %s %s",
				ColorGreen(fmt.Sprintf("+%d", addCount)),
				ColorRed(fmt.Sprintf("-%d", delCount)),
			)
		}
		header := fmt.Sprintf("─── %s%s%s ─",
			ColorCyan(StyleBold(langBadge+filepath.ToSlash(filePath))),
			statBadge,
			ColorBorder(""),
		)
		sb.WriteString(header + "\n")
	}

	for _, line := range file.Lines {
		switch line.Kind {
		case DiffLineHeader:
			// Raw git metadata lines (diff --git, index, ---, +++)
			// Only render if compact is false and no filePath was determined, or as subtle gray
			if opts.Compact || filePath != "" {
				// In clean VS Code view, omit raw index/---/+++ headers when filePath header is already shown
				continue
			}
			sb.WriteString(ColorGray(line.Raw) + "\n")

		case DiffLineHunk:
			renderHunkHeader(sb, line)

		case DiffLineContext:
			renderContextLine(sb, line, lang, gutterWidth, showGutter)

		case DiffLineAdded:
			renderAddedLine(sb, line, lang, gutterWidth, showGutter)

		case DiffLineDeleted:
			renderDeletedLine(sb, line, lang, gutterWidth, showGutter)
		}
	}
}

func countFileDiffStats(file ParsedDiffFile) (add, del int) {
	for _, l := range file.Lines {
		if l.Kind == DiffLineAdded {
			add++
		} else if l.Kind == DiffLineDeleted {
			del++
		}
	}
	return add, del
}

func renderHunkHeader(sb *strings.Builder, line ParsedDiffLine) {
	hunkText := line.Raw
	contextInfo := line.HunkInfo

	if !ColorsEnabled() {
		sb.WriteString(hunkText + "\n")
		return
	}

	pill := ColorCyan(hunkText)
	if contextInfo != "" {
		pill = ColorCyan(strings.TrimSpace(strings.Split(hunkText, "@@")[1]))
		pill = ColorCyan("@@" + pill + "@@ ") + ColorBrightWhite(StyleBold(contextInfo))
	}
	sb.WriteString("  " + pill + "\n")
}

func renderContextLine(sb *strings.Builder, line ParsedDiffLine, lang string, gutterWidth int, showGutter bool) {
	if showGutter {
		oldStr := fmt.Sprintf("%*d", gutterWidth, line.OldLineNo)
		newStr := fmt.Sprintf("%*d", gutterWidth, line.NewLineNo)
		sb.WriteString(ColorGray(oldStr))
		sb.WriteString(" ")
		sb.WriteString(ColorGray(newStr))
		sb.WriteString("   ")
		sb.WriteString(ColorBorder(SymVLine))
		sb.WriteString(" ")
	}

	highlighted := HighlightCodeLine(line.Content, lang)
	sb.WriteString(highlighted + "\n")
}

func renderAddedLine(sb *strings.Builder, line ParsedDiffLine, lang string, gutterWidth int, showGutter bool) {
	if showGutter {
		oldStr := strings.Repeat(" ", gutterWidth)
		newStr := fmt.Sprintf("%*d", gutterWidth, line.NewLineNo)
		sb.WriteString(oldStr)
		sb.WriteString(" ")
		sb.WriteString(ColorGreen(newStr))
		sb.WriteString(" ")
		sb.WriteString(ColorGreen("+"))
		sb.WriteString(" ")
		sb.WriteString(ColorBorder(SymVLine))
		sb.WriteString(" ")
	}

	// Render code content with intra-line word diffs + syntax highlighting
	if len(line.WordSpans) > 0 {
		sb.WriteString(renderWordSpans(line.WordSpans, lang, true) + "\n")
	} else {
		// No paired intra-line diff: full line addition
		code := HighlightCodeLineBg(line.Content, lang, diffBg.lineAdd)
		if !ColorsEnabled() {
			sb.WriteString("+" + line.Content + "\n")
		} else {
			sb.WriteString(styleAddedLine(code) + "\n")
		}
	}
}

func renderDeletedLine(sb *strings.Builder, line ParsedDiffLine, lang string, gutterWidth int, showGutter bool) {
	if showGutter {
		oldStr := fmt.Sprintf("%*d", gutterWidth, line.OldLineNo)
		newStr := strings.Repeat(" ", gutterWidth)
		sb.WriteString(ColorRed(oldStr))
		sb.WriteString(" ")
		sb.WriteString(newStr)
		sb.WriteString(" ")
		sb.WriteString(ColorRed("-"))
		sb.WriteString(" ")
		sb.WriteString(ColorBorder(SymVLine))
		sb.WriteString(" ")
	}

	// Render code content with intra-line word diffs + syntax highlighting
	if len(line.WordSpans) > 0 {
		sb.WriteString(renderWordSpans(line.WordSpans, lang, false) + "\n")
	} else {
		// No paired intra-line diff: full line deletion
		code := HighlightCodeLineBg(line.Content, lang, diffBg.lineDel)
		if !ColorsEnabled() {
			sb.WriteString("-" + line.Content + "\n")
		} else {
			sb.WriteString(styleDeletedLine(code) + "\n")
		}
	}
}

// renderWordSpans highlights words with syntax highlighting, applying a distinct badge/tint to changed words.
func renderWordSpans(spans []WordSpan, lang string, isAddition bool) string {
	if !ColorsEnabled() {
		var sb strings.Builder
		for _, s := range spans {
			sb.WriteString(s.Text)
		}
		return sb.String()
	}

	var sb strings.Builder
	for _, span := range spans {
		if span.Changed {
			if isAddition {
				// Changed word in added line: high-contrast green highlight
				sb.WriteString(styleAddedWord(HighlightCodeLineBg(span.Text, lang, diffBg.wordAdd)))
			} else {
				// Changed word in deleted line: high-contrast red highlight
				sb.WriteString(styleDeletedWord(HighlightCodeLineBg(span.Text, lang, diffBg.wordDel)))
			}
		} else {
			// Unchanged word in diff line: standard syntax highlight with subtle diff tint
			if isAddition {
				sb.WriteString(styleAddedLine(HighlightCodeLineBg(span.Text, lang, diffBg.lineAdd)))
			} else {
				sb.WriteString(styleDeletedLine(HighlightCodeLineBg(span.Text, lang, diffBg.lineDel)))
			}
		}
	}
	return sb.String()
}

// Background / Style Helpers for VS Code diff appearance

// diffBackgrounds are the 24-bit backgrounds behind changed lines and the
// changed words within them. ApplyTheme picks the set matching the theme.
type diffBackgrounds struct {
	lineAdd, lineDel, wordAdd, wordDel string
}

var (
	darkDiffBackgrounds = diffBackgrounds{
		lineAdd: "\033[48;2;20;50;30m",
		lineDel: "\033[48;2;55;25;25m",
		wordAdd: "\033[48;2;35;95;50m\033[1m",
		wordDel: "\033[48;2;110;35;35m\033[1m",
	}
	lightDiffBackgrounds = diffBackgrounds{
		lineAdd: "\033[48;2;218;251;225m",
		lineDel: "\033[48;2;255;235;233m",
		wordAdd: "\033[48;2;172;238;187m\033[1m",
		wordDel: "\033[48;2;255;206;203m\033[1m",
	}
	diffBg = darkDiffBackgrounds
)

func styleAddedLine(text string) string {
	if text == "" {
		return ""
	}
	// In TrueColor terminals, wrap with subtle dark green background tint
	// On terminals with restricted colors, preserve syntax text with a green tint or bold
	return diffBg.lineAdd + text + ansiReset
}

func styleDeletedLine(text string) string {
	if text == "" {
		return ""
	}
	// In TrueColor terminals, wrap with subtle dark red background tint
	return diffBg.lineDel + text + ansiReset
}

func styleAddedWord(text string) string {
	if text == "" {
		return ""
	}
	return diffBg.wordAdd + text + ansiReset
}

func styleDeletedWord(text string) string {
	if text == "" {
		return ""
	}
	return diffBg.wordDel + text + ansiReset
}
