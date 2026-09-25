package files

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxMatchLineChars is the most of one matching line a search result shows. A
// minified or generated file is a single line of hundreds of kilobytes; shown
// whole, one match can fill a model's context window by itself.
const MaxMatchLineChars = 300

// ClipMatch trims line and, when it is longer than MaxMatchLineChars, keeps a
// window around the first match of re, marking what it cut and how long the
// line was.
func ClipMatch(line string, re *regexp.Regexp) string {
	line = strings.TrimSpace(line)
	if len(line) <= MaxMatchLineChars {
		return line
	}
	start := 0
	if loc := re.FindStringIndex(line); loc != nil {
		// Show a little of what leads up to the match, most of the window after.
		start = loc[0] - MaxMatchLineChars/4
	}
	if start < 0 {
		start = 0
	}
	end := start + MaxMatchLineChars
	if end > len(line) {
		end = len(line)
		start = end - MaxMatchLineChars
	}
	start, end = runeStart(line, start), runeStart(line, end)

	var b strings.Builder
	if start > 0 {
		b.WriteString("…")
	}
	b.WriteString(line[start:end])
	if end < len(line) {
		b.WriteString("…")
	}
	fmt.Fprintf(&b, " [line is %d characters]", len(line))
	return b.String()
}

func runeStart(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}
