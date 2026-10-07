package harness

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// DiffLineKind denotes whether a diff line is a header, hunk marker, addition, deletion, or context.
type DiffLineKind int

const (
	DiffLineHeader DiffLineKind = iota
	DiffLineHunk
	DiffLineContext
	DiffLineAdded
	DiffLineDeleted
)

// WordSpan represents a segment of a line with intra-line change status.
type WordSpan struct {
	Text    string
	Changed bool // true if this span is part of the intra-line modification
}

// ParsedDiffLine represents one line in a structured diff.
type ParsedDiffLine struct {
	Kind      DiffLineKind
	OldLineNo int // > 0 if present
	NewLineNo int // > 0 if present
	Prefix    string
	Content   string     // text without the +/-/space prefix
	Raw       string     // original raw line
	HunkInfo  string     // function / section context from @@ ... @@ section
	WordSpans []WordSpan // intra-line word diff spans (empty if not applicable)
}

// ParsedDiffFile represents a file diff with its header information and lines.
type ParsedDiffFile struct {
	OldPath string
	NewPath string
	Lines   []ParsedDiffLine
	MaxLine int // highest line number encountered, for gutter width calculation
}

var hunkRegex = regexp.MustCompile(`^@@\s+-(\d+)(?:,(\d+))?\s+\+(\d+)(?:,(\d+))?\s+@@(?:[ \t]*(.*))?$`)

// ParseDiffForDisplay parses unified diff text into one or more ParsedDiffFile objects.
func ParseDiffForDisplay(diffText string) []ParsedDiffFile {
	lines := strings.Split(diffText, "\n")
	var files []ParsedDiffFile
	var currentFile *ParsedDiffFile

	oldLine := 0
	newLine := 0

	for _, rawLine := range lines {
		// Detect file headers
		if strings.HasPrefix(rawLine, "diff --git ") {
			if currentFile != nil {
				files = append(files, *currentFile)
			}
			parts := strings.Fields(rawLine)
			oldP, newP := "", ""
			if len(parts) >= 4 {
				oldP = strings.TrimPrefix(parts[2], "a/")
				newP = strings.TrimPrefix(parts[3], "b/")
			}
			currentFile = &ParsedDiffFile{OldPath: oldP, NewPath: newP}
			currentFile.Lines = append(currentFile.Lines, ParsedDiffLine{
				Kind: DiffLineHeader,
				Raw:  rawLine,
			})
			continue
		}

		if currentFile == nil {
			currentFile = &ParsedDiffFile{}
		}

		if strings.HasPrefix(rawLine, "--- ") {
			path := strings.TrimPrefix(rawLine, "--- ")
			if idx := strings.Index(path, "\t"); idx != -1 {
				path = path[:idx]
			}
			if decoded, err := strconv.Unquote(path); err == nil {
				path = decoded
			}
			currentFile.OldPath = strings.TrimPrefix(path, "a/")
			currentFile.Lines = append(currentFile.Lines, ParsedDiffLine{
				Kind: DiffLineHeader,
				Raw:  rawLine,
			})
			continue
		}

		if strings.HasPrefix(rawLine, "+++ ") {
			path := strings.TrimPrefix(rawLine, "+++ ")
			if idx := strings.Index(path, "\t"); idx != -1 {
				path = path[:idx]
			}
			if decoded, err := strconv.Unquote(path); err == nil {
				path = decoded
			}
			currentFile.NewPath = strings.TrimPrefix(path, "b/")
			currentFile.Lines = append(currentFile.Lines, ParsedDiffLine{
				Kind: DiffLineHeader,
				Raw:  rawLine,
			})
			continue
		}

		if strings.HasPrefix(rawLine, "index ") || strings.HasPrefix(rawLine, "new file mode") ||
			strings.HasPrefix(rawLine, "deleted file mode") || strings.HasPrefix(rawLine, "similarity index") {
			currentFile.Lines = append(currentFile.Lines, ParsedDiffLine{
				Kind: DiffLineHeader,
				Raw:  rawLine,
			})
			continue
		}

		// Hunk Header
		if strings.HasPrefix(rawLine, "@@") {
			matches := hunkRegex.FindStringSubmatch(rawLine)
			hunkContext := ""
			if len(matches) > 0 {
				oldStart, _ := strconv.Atoi(matches[1])
				newStart, _ := strconv.Atoi(matches[3])
				oldLine = oldStart
				newLine = newStart
				if len(matches) > 5 {
					hunkContext = strings.TrimSpace(matches[5])
				}
			}

			currentFile.Lines = append(currentFile.Lines, ParsedDiffLine{
				Kind:     DiffLineHunk,
				Raw:      rawLine,
				HunkInfo: hunkContext,
			})
			continue
		}

		// Diff content lines
		if strings.HasPrefix(rawLine, "+") {
			content := rawLine[1:]
			pLine := ParsedDiffLine{
				Kind:      DiffLineAdded,
				NewLineNo: newLine,
				Prefix:    "+",
				Content:   content,
				Raw:       rawLine,
			}
			if newLine > currentFile.MaxLine {
				currentFile.MaxLine = newLine
			}
			newLine++
			currentFile.Lines = append(currentFile.Lines, pLine)
		} else if strings.HasPrefix(rawLine, "-") {
			content := rawLine[1:]
			pLine := ParsedDiffLine{
				Kind:      DiffLineDeleted,
				OldLineNo: oldLine,
				Prefix:    "-",
				Content:   content,
				Raw:       rawLine,
			}
			if oldLine > currentFile.MaxLine {
				currentFile.MaxLine = oldLine
			}
			oldLine++
			currentFile.Lines = append(currentFile.Lines, pLine)
		} else if strings.HasPrefix(rawLine, " ") || (len(rawLine) > 0 && !strings.HasPrefix(rawLine, "\\")) {
			content := rawLine
			if strings.HasPrefix(rawLine, " ") {
				content = rawLine[1:]
			}
			pLine := ParsedDiffLine{
				Kind:      DiffLineContext,
				OldLineNo: oldLine,
				NewLineNo: newLine,
				Prefix:    " ",
				Content:   content,
				Raw:       rawLine,
			}
			if oldLine > currentFile.MaxLine {
				currentFile.MaxLine = oldLine
			}
			if newLine > currentFile.MaxLine {
				currentFile.MaxLine = newLine
			}
			oldLine++
			newLine++
			currentFile.Lines = append(currentFile.Lines, pLine)
		} else if strings.HasPrefix(rawLine, "\\") {
			// e.g. "\ No newline at end of file"
			currentFile.Lines = append(currentFile.Lines, ParsedDiffLine{
				Kind: DiffLineHeader,
				Raw:  rawLine,
			})
		}
	}

	if currentFile != nil && len(currentFile.Lines) > 0 {
		files = append(files, *currentFile)
	}

	// Compute intra-line word diffs for each file
	for fIdx := range files {
		computeWordDiffs(&files[fIdx])
	}

	return files
}

// computeWordDiffs matches paired deletion and addition lines to compute intra-line word diffs.
func computeWordDiffs(file *ParsedDiffFile) {
	n := len(file.Lines)
	i := 0
	for i < n {
		if file.Lines[i].Kind != DiffLineDeleted {
			i++
			continue
		}

		// Collect contiguous deleted lines
		delStart := i
		for i < n && file.Lines[i].Kind == DiffLineDeleted {
			i++
		}
		delEnd := i

		// Collect contiguous added lines immediately following
		addStart := i
		for i < n && file.Lines[i].Kind == DiffLineAdded {
			i++
		}
		addEnd := i

		delCount := delEnd - delStart
		addCount := addEnd - addStart

		if delCount > 0 && addCount > 0 {
			// Pair up 1:1 up to min(delCount, addCount)
			pairCount := delCount
			if addCount < pairCount {
				pairCount = addCount
			}
			for p := 0; p < pairCount; p++ {
				dLine := &file.Lines[delStart+p]
				aLine := &file.Lines[addStart+p]

				dSpans, aSpans := DiffWords(dLine.Content, aLine.Content)
				dLine.WordSpans = dSpans
				aLine.WordSpans = aSpans
			}
		}
	}
}

// tokenizeWords splits a code line into word tokens, punctuation tokens, and whitespace tokens.
func tokenizeWords(s string) []string {
	if s == "" {
		return nil
	}
	var tokens []string
	runes := []rune(s)
	n := len(runes)
	i := 0

	for i < n {
		r := runes[i]
		if unicode.IsSpace(r) {
			start := i
			for i < n && unicode.IsSpace(runes[i]) {
				i++
			}
			tokens = append(tokens, string(runes[start:i]))
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			start := i
			for i < n && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_') {
				i++
			}
			tokens = append(tokens, string(runes[start:i]))
			continue
		}
		// Punctuation / symbols: group contiguous identical symbols or single punctuation
		tokens = append(tokens, string(r))
		i++
	}
	return tokens
}

// DiffWords computes word-level diff between an old line and a new line.
// It returns WordSpans for oldLine (with deletions marked Changed: true)
// and WordSpans for newLine (with additions marked Changed: true).
func DiffWords(oldLine, newLine string) ([]WordSpan, []WordSpan) {
	oldTokens := tokenizeWords(oldLine)
	newTokens := tokenizeWords(newLine)

	if len(oldTokens) == 0 && len(newTokens) == 0 {
		return nil, nil
	}
	if len(oldTokens) == 0 {
		return nil, []WordSpan{{Text: newLine, Changed: true}}
	}
	if len(newTokens) == 0 {
		return []WordSpan{{Text: oldLine, Changed: true}}, nil
	}

	// Compute LCS (Longest Common Subsequence) on tokens
	m := len(oldTokens)
	n := len(newTokens)

	// Standard dynamic programming table
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if oldTokens[i-1] == newTokens[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// Backtrack to build diff alignment
	type diffOp struct {
		oldIdx int
		newIdx int
		match  bool
	}
	var ops []diffOp
	i, j := m, n
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && oldTokens[i-1] == newTokens[j-1] {
			ops = append(ops, diffOp{oldIdx: i - 1, newIdx: j - 1, match: true})
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			ops = append(ops, diffOp{oldIdx: -1, newIdx: j - 1, match: false})
			j--
		} else {
			ops = append(ops, diffOp{oldIdx: i - 1, newIdx: -1, match: false})
			i--
		}
	}

	// Reverse ops to forward order
	for k := 0; k < len(ops)/2; k++ {
		ops[k], ops[len(ops)-1-k] = ops[len(ops)-1-k], ops[k]
	}

	// Build oldSpans and newSpans
	var oldSpans []WordSpan
	var newSpans []WordSpan

	for _, op := range ops {
		if op.oldIdx >= 0 {
			tok := oldTokens[op.oldIdx]
			changed := !op.match
			oldSpans = appendSpan(oldSpans, tok, changed)
		}
		if op.newIdx >= 0 {
			tok := newTokens[op.newIdx]
			changed := !op.match
			newSpans = appendSpan(newSpans, tok, changed)
		}
	}

	return oldSpans, newSpans
}

func appendSpan(spans []WordSpan, text string, changed bool) []WordSpan {
	if len(spans) > 0 && spans[len(spans)-1].Changed == changed {
		spans[len(spans)-1].Text += text
		return spans
	}
	return append(spans, WordSpan{Text: text, Changed: changed})
}
