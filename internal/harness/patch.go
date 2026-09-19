package harness

import (
	"bufio"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Hunk represents a single unified diff hunk.
type Hunk struct {
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Lines    []string // lines starting with ' ', '+', '-'
}

// DiffFile represents the parsed changes for a single file.
type DiffFile struct {
	OldPath string
	NewPath string
	Hunks   []Hunk
}

var hunkHeaderRegex = regexp.MustCompile(`^@@\s+-(\d+)(?:,(\d+))?\s+\+(\d+)(?:,(\d+))?\s+@@`)

// ParseUnifiedDiff parses a unified diff text into a slice of DiffFile.
func ParseUnifiedDiff(diffText string) ([]DiffFile, error) {
	scanner := bufio.NewScanner(strings.NewReader(diffText))
	var files []DiffFile
	var currentFile *DiffFile
	var currentHunk *Hunk

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "--- ") {
			if currentHunk != nil && currentFile != nil {
				currentFile.Hunks = append(currentFile.Hunks, *currentHunk)
				currentHunk = nil
			}
			if currentFile != nil && len(currentFile.Hunks) > 0 {
				files = append(files, *currentFile)
			}
			path := strings.TrimSpace(strings.TrimPrefix(line, "--- "))
			path = strings.TrimPrefix(path, "a/")
			currentFile = &DiffFile{OldPath: path}
			continue
		}

		if strings.HasPrefix(line, "+++ ") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			path = strings.TrimPrefix(path, "b/")
			if currentFile == nil {
				currentFile = &DiffFile{}
			}
			currentFile.NewPath = path
			continue
		}

		if strings.HasPrefix(line, "@@") {
			if currentHunk != nil && currentFile != nil {
				currentFile.Hunks = append(currentFile.Hunks, *currentHunk)
				currentHunk = nil
			}
			matches := hunkHeaderRegex.FindStringSubmatch(line)
			if len(matches) < 4 {
				// Malformed hunk header, try basic parsing
				currentHunk = &Hunk{OldStart: 1, OldCount: 0, NewStart: 1, NewCount: 0}
			} else {
				oldStart, _ := strconv.Atoi(matches[1])
				oldCount := 1
				if matches[2] != "" {
					oldCount, _ = strconv.Atoi(matches[2])
				}
				newStart, _ := strconv.Atoi(matches[3])
				newCount := 1
				if len(matches) > 4 && matches[4] != "" {
					newCount, _ = strconv.Atoi(matches[4])
				}
				currentHunk = &Hunk{
					OldStart: oldStart,
					OldCount: oldCount,
					NewStart: newStart,
					NewCount: newCount,
				}
			}
			if currentFile == nil {
				currentFile = &DiffFile{}
			}
			continue
		}

		if currentHunk != nil {
			if len(line) == 0 {
				// Empty line often represents an unchanged empty line (' ')
				currentHunk.Lines = append(currentHunk.Lines, " ")
			} else if line[0] == ' ' || line[0] == '+' || line[0] == '-' {
				currentHunk.Lines = append(currentHunk.Lines, line)
			} else if line == `\ No newline at end of file` {
				// ignore diff metadata
				continue
			} else {
				// LLM might emit context without leading space
				currentHunk.Lines = append(currentHunk.Lines, " "+line)
			}
		}
	}

	if currentHunk != nil && currentFile != nil {
		currentFile.Hunks = append(currentFile.Hunks, *currentHunk)
	}
	if currentFile != nil && (len(currentFile.Hunks) > 0 || currentFile.NewPath != "") {
		files = append(files, *currentFile)
	}

	return files, scanner.Err()
}

// ApplyPatch applies a series of hunks to original file content.
// It supports fuzzy line matching if exact line numbers or whitespace differ slightly.
func ApplyPatch(original string, hunks []Hunk) (string, error) {
	origLines := splitLines(original)

	for _, hunk := range hunks {
		newLines, err := applyHunk(origLines, hunk)
		if err != nil {
			return "", err
		}
		origLines = newLines
	}

	return strings.Join(origLines, "\n"), nil
}

func applyHunk(lines []string, hunk Hunk) ([]string, error) {
	// Build the expected old content and new replacement lines
	var expectedOld []string
	var replacement []string

	for _, hl := range hunk.Lines {
		if len(hl) == 0 {
			continue
		}
		prefix := hl[0]
		content := ""
		if len(hl) > 1 {
			content = hl[1:]
		}

		switch prefix {
		case ' ':
			expectedOld = append(expectedOld, content)
			replacement = append(replacement, content)
		case '-':
			expectedOld = append(expectedOld, content)
		case '+':
			replacement = append(replacement, content)
		}
	}

	if len(expectedOld) == 0 {
		// Pure insertion
		insertAt := hunk.NewStart - 1
		if insertAt < 0 {
			insertAt = 0
		}
		if insertAt > len(lines) {
			insertAt = len(lines)
		}
		result := make([]string, 0, len(lines)+len(replacement))
		result = append(result, lines[:insertAt]...)
		result = append(result, replacement...)
		result = append(result, lines[insertAt:]...)
		return result, nil
	}

	// 1. Try exact match at suggested line number (OldStart - 1)
	matchIdx := findMatchingBlock(lines, expectedOld, hunk.OldStart-1, false)
	if matchIdx == -1 {
		// 2. Try exact match anywhere in file
		matchIdx = findMatchingBlock(lines, expectedOld, -1, false)
	}
	if matchIdx == -1 {
		// 3. Try whitespace-trimmed match anywhere in file
		matchIdx = findMatchingBlock(lines, expectedOld, -1, true)
	}

	if matchIdx == -1 {
		return nil, fmt.Errorf("hunk failed: could not locate context lines:\n%s", strings.Join(expectedOld, "\n"))
	}

	// Splice replacement into lines
	result := make([]string, 0, len(lines)-len(expectedOld)+len(replacement))
	result = append(result, lines[:matchIdx]...)
	result = append(result, replacement...)
	result = append(result, lines[matchIdx+len(expectedOld):]...)
	return result, nil
}

func findMatchingBlock(lines []string, expected []string, preferredIdx int, trimSpace bool) int {
	if len(expected) == 0 {
		return 0
	}
	if len(expected) > len(lines) {
		return -1
	}

	checkMatch := func(start int) bool {
		if start < 0 || start+len(expected) > len(lines) {
			return false
		}
		for i := 0; i < len(expected); i++ {
			a := lines[start+i]
			b := expected[i]
			if trimSpace {
				if strings.TrimSpace(a) != strings.TrimSpace(b) {
					return false
				}
			} else {
				if a != b {
					return false
				}
			}
		}
		return true
	}

	if preferredIdx >= 0 && checkMatch(preferredIdx) {
		return preferredIdx
	}

	for i := 0; i <= len(lines)-len(expected); i++ {
		if checkMatch(i) {
			return i
		}
	}

	return -1
}

func splitLines(s string) []string {
	// Normalize \r\n to \n
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "\n")
}

// ResilientReplace attempts to replace target block in content, with fallbacks for whitespace.
func ResilientReplace(original, search, replace string) (string, error) {
	// 1. Direct exact match
	count := strings.Count(original, search)
	if count == 1 {
		return strings.Replace(original, search, replace, 1), nil
	}
	if count > 1 {
		return "", fmt.Errorf("search block matches %d times in file, must be unique", count)
	}

	// 2. Line-ending normalized match (\r\n vs \n)
	normOrig := strings.ReplaceAll(original, "\r\n", "\n")
	normSearch := strings.ReplaceAll(search, "\r\n", "\n")
	normReplace := strings.ReplaceAll(replace, "\r\n", "\n")

	count = strings.Count(normOrig, normSearch)
	if count == 1 {
		res := strings.Replace(normOrig, normSearch, normReplace, 1)
		if strings.Contains(original, "\r\n") {
			res = strings.ReplaceAll(res, "\n", "\r\n")
		}
		return res, nil
	}
	if count > 1 {
		return "", fmt.Errorf("search block matches %d times in file, must be unique", count)
	}

	// 3. Line-based fuzzy whitespace match (trailing whitespace & indentation flexibility)
	origLines := splitLines(normOrig)
	searchLines := splitLines(normSearch)
	replaceLines := splitLines(normReplace)

	if len(searchLines) == 0 {
		return "", errors.New("search string is empty")
	}

	matches := make([]int, 0)
	for i := 0; i <= len(origLines)-len(searchLines); i++ {
		matched := true
		for j := 0; j < len(searchLines); j++ {
			if strings.TrimSpace(origLines[i+j]) != strings.TrimSpace(searchLines[j]) {
				matched = false
				break
			}
		}
		if matched {
			matches = append(matches, i)
		}
	}

	if len(matches) == 1 {
		idx := matches[0]
		// Preserve indentation of the first line if possible
		origIndent := getLeadingWhitespace(origLines[idx])
		searchIndent := getLeadingWhitespace(searchLines[0])

		adjustedReplace := make([]string, len(replaceLines))
		for i, rl := range replaceLines {
			if strings.HasPrefix(rl, searchIndent) {
				adjustedReplace[i] = origIndent + strings.TrimPrefix(rl, searchIndent)
			} else {
				adjustedReplace[i] = rl
			}
		}

		result := make([]string, 0, len(origLines)-len(searchLines)+len(adjustedReplace))
		result = append(result, origLines[:idx]...)
		result = append(result, adjustedReplace...)
		result = append(result, origLines[idx+len(searchLines):]...)

		out := strings.Join(result, "\n")
		if strings.Contains(original, "\r\n") {
			out = strings.ReplaceAll(out, "\n", "\r\n")
		}
		return out, nil
	}

	if len(matches) > 1 {
		return "", fmt.Errorf("fuzzy search matched %d locations in file, cannot unambiguously replace", len(matches))
	}

	return "", errors.New("search text not found in file (even with whitespace tolerance)")
}

func getLeadingWhitespace(s string) string {
	for i, r := range s {
		if r != ' ' && r != '\t' {
			return s[:i]
		}
	}
	return s
}
