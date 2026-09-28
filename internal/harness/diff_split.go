package harness

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The side-by-side view puts the old version of a file on the left and the
// new one on the right, one row per line, so a change reads across. It is
// built from the same parsed unified diff as the single-column view: context
// lines appear on both sides, and within each run of deletions followed by
// additions the two are paired line by line, the shorter side padded with
// filler rows.

// SplitRow is one row of a side-by-side diff. Old or New is nil where that
// side has no line (a filler row). A row with Hunk set separates two hunks.
type SplitRow struct {
	Old, New *ParsedDiffLine
	Hunk     string
}

// BuildSplitRows pairs a parsed file diff into side-by-side rows. The rows
// point into file.Lines.
func BuildSplitRows(file ParsedDiffFile) []SplitRow {
	var rows []SplitRow
	lines := file.Lines
	seenHunk := false
	for i := 0; i < len(lines); {
		switch lines[i].Kind {
		case DiffLineHunk:
			// The first hunk needs no separator; later ones mark skipped lines.
			if seenHunk {
				rows = append(rows, SplitRow{Hunk: lines[i].Raw})
			}
			seenHunk = true
			i++
		case DiffLineContext:
			rows = append(rows, SplitRow{Old: &lines[i], New: &lines[i]})
			i++
		case DiffLineDeleted, DiffLineAdded:
			var dels, adds []*ParsedDiffLine
			for i < len(lines) && lines[i].Kind == DiffLineDeleted {
				dels = append(dels, &lines[i])
				i++
			}
			for i < len(lines) && lines[i].Kind == DiffLineAdded {
				adds = append(adds, &lines[i])
				i++
			}
			for p := 0; p < max(len(dels), len(adds)); p++ {
				var row SplitRow
				if p < len(dels) {
					row.Old = dels[p]
				}
				if p < len(adds) {
					row.New = adds[p]
				}
				rows = append(rows, row)
			}
		default:
			i++
		}
	}
	return rows
}

// splitCode is how a line's text is shown in a column: tabs as four spaces
// (a fixed width both sides agree on) and no carriage return.
func splitCode(s string) string {
	return strings.ReplaceAll(strings.TrimRight(s, "\r"), "\t", "    ")
}

// RenderSplitDiff renders a parsed file diff as side-by-side rows exactly
// width cells wide: old version left, new version right. Each side is lexed
// as a whole, so block comments and multi-line strings highlight correctly
// when the diff carries the full file (gitrepo.FullFileContext).
func RenderSplitDiff(file ParsedDiffFile, lang string, width int) []string {
	rows := BuildSplitRows(file)
	gutterWidth := max(2, len(fmt.Sprint(file.MaxLine)))
	leftWidth := (width - 1) / 2
	rightWidth := width - 1 - leftWidth

	oldTokens := lexSide(rows, lang, func(r SplitRow) *ParsedDiffLine { return r.Old })
	newTokens := lexSide(rows, lang, func(r SplitRow) *ParsedDiffLine { return r.New })

	sep := ColorBorder(SymVLine)
	out := make([]string, 0, len(rows))
	for i, row := range rows {
		if row.Hunk != "" {
			label := " " + strings.TrimSpace(row.Hunk) + " "
			out = append(out, ColorCyan(ansi.Truncate(PadRight("┄┄"+label, width), width, "")))
			continue
		}
		left := renderSplitCell(row.Old, false, oldTokens[i], lang, gutterWidth, leftWidth)
		right := renderSplitCell(row.New, true, newTokens[i], lang, gutterWidth, rightWidth)
		out = append(out, left+sep+right)
	}
	return out
}

// lexSide lexes one side's lines in order and returns the tokens for each
// row (nil where the side has no line).
func lexSide(rows []SplitRow, lang string, side func(SplitRow) *ParsedDiffLine) [][]Token {
	var texts []string
	var at []int
	for i, row := range rows {
		if l := side(row); l != nil {
			texts = append(texts, splitCode(l.Content))
			at = append(at, i)
		}
	}
	lexed := LexLines(texts, lang)
	out := make([][]Token, len(rows))
	for j, i := range at {
		out[i] = lexed[j]
	}
	return out
}

// renderSplitCell renders one side of a row, padded to width. isNew says
// which side this is, which decides whether a changed line is an addition
// or a deletion.
func renderSplitCell(line *ParsedDiffLine, isNew bool, tokens []Token, lang string, gutterWidth, width int) string {
	if line == nil {
		return ColorBorder(strings.Repeat("╱", max(width, 0)))
	}
	colors := ColorsEnabled()
	lineNo := line.OldLineNo
	if isNew {
		lineNo = line.NewLineNo
	}
	number := fmt.Sprintf("%*d", gutterWidth, lineNo)

	var marker, bg, code string
	switch line.Kind {
	case DiffLineAdded:
		marker, bg = ColorGreen("+"), diffBg.lineAdd
		number = ColorGreen(number)
	case DiffLineDeleted:
		marker, bg = ColorRed("-"), diffBg.lineDel
		number = ColorRed(number)
	default:
		marker = " "
		number = ColorGray(number)
	}
	if !colors {
		bg = ""
	}
	if len(line.WordSpans) > 0 {
		spans := make([]WordSpan, len(line.WordSpans))
		for i, s := range line.WordSpans {
			spans[i] = WordSpan{Text: splitCode(s.Text), Changed: s.Changed}
		}
		code = renderWordSpans(spans, lang, line.Kind == DiffLineAdded)
	} else {
		code = HighlightTokens(tokens, bg)
	}

	prefix := number + " " + marker + " "
	codeWidth := max(width-gutterWidth-3, 0)
	code = ansi.Truncate(code, codeWidth, "…")
	pad := strings.Repeat(" ", max(codeWidth-ansi.StringWidth(code), 0))
	if bg == "" {
		return prefix + code + pad
	}
	// The tint runs to the column edge, past the end of the text.
	return prefix + bg + code + bg + pad + ansiReset
}

// SplitHeader is the title row over a side-by-side diff, width cells wide.
func SplitHeader(left, right string, width int) string {
	leftWidth := (width - 1) / 2
	rightWidth := width - 1 - leftWidth
	cell := func(s string, w int) string {
		return PadRight(ansi.Truncate(" "+s, w, "…"), w)
	}
	return StyleBold(cell(left, leftWidth)) + ColorBorder(SymVLine) + StyleBold(cell(right, rightWidth))
}
