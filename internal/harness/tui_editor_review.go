package harness

import (
	"fmt"
	"slices"
	"strings"
)

type editorChange struct {
	oldStart, oldEnd int
	start, end       int
}

type editorReviewRow struct {
	line, oldLine int // -1 for a deletion header; oldLine >= 0 for read-only text
	change        int // -1 for a live line
}

// editorChanges bounds the quadratic work after trimming equal ends. Large
// replacements still get line highlighting, even when precise matching is costly.
func editorChanges(old, cur []string) []editorChange {
	prefix, suffix := 0, 0
	for prefix < len(old) && prefix < len(cur) && old[prefix] == cur[prefix] {
		prefix++
	}
	for suffix < len(old)-prefix && suffix < len(cur)-prefix && old[len(old)-1-suffix] == cur[len(cur)-1-suffix] {
		suffix++
	}
	a, b := old[prefix:len(old)-suffix], cur[prefix:len(cur)-suffix]
	var changes []editorChange
	gap := func(i, endI, j, endJ int) {
		if i != endI || j != endJ {
			changes = append(changes, editorChange{prefix + i, prefix + endI, prefix + j, prefix + endJ})
		}
	}
	const maxCells = 1 << 20
	if len(a) == 0 || len(b) == 0 || len(a) > maxCells/max(1, len(b)) {
		gap(0, len(a), 0, len(b))
		return changes
	}
	width := len(b) + 1
	lcs := make([]int32, (len(a)+1)*width)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i*width+j] = lcs[(i+1)*width+j+1] + 1
			} else {
				lcs[i*width+j] = max(lcs[(i+1)*width+j], lcs[i*width+j+1])
			}
		}
	}
	i, j, gapI, gapJ := 0, 0, 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			gap(gapI, i, gapJ, j)
			i, j = i+1, j+1
			gapI, gapJ = i, j
		case lcs[(i+1)*width+j] >= lcs[i*width+j+1]:
			i++
		default:
			j++
		}
	}
	gap(gapI, len(a), gapJ, len(b))
	return changes
}

func computeLineMarks(old, cur []string) []byte {
	return editorMarks(editorChanges(old, cur), len(cur))
}

func editorMarks(changes []editorChange, count int) []byte {
	marks := make([]byte, count)
	for _, c := range changes {
		mark := byte('+')
		if c.oldEnd > c.oldStart {
			mark = '~'
		}
		for row := c.start; row < c.end; row++ {
			marks[row] = mark
		}
		if c.start == c.end && c.oldStart < c.oldEnd && len(marks) > 0 {
			marks[min(c.start, len(marks)-1)] = '-'
		}
	}
	return marks
}

func editorChangedColumns(spans []WordSpan) []editorColumnRange {
	var ranges []editorColumnRange
	col := 0
	for _, span := range spans {
		start := col
		for _, r := range span.Text {
			col += runeCells(r, col)
		}
		if span.Changed {
			ranges = append(ranges, editorColumnRange{start, col})
		}
	}
	return ranges
}

func (s *editorSession) ensureReview() {
	if !s.marksStale && s.reviewRows != nil {
		return
	}
	previous := s.changes
	s.changes = nil
	s.marks = nil
	s.changedWords = make(map[int][]editorColumnRange)
	s.deletedWords = make(map[int][]editorColumnRange)
	if s.hasHead {
		if s.headTokens == nil {
			s.headTokens = newFileEditor(strings.Join(s.head, "\n"), s.lang)
		}
		s.changes = editorChanges(s.head, s.lines)
		s.marks = editorMarks(s.changes, len(s.lines))
		for _, c := range s.changes {
			for i := 0; i < min(c.oldEnd-c.oldStart, c.end-c.start); i++ {
				old, cur := s.head[c.oldStart+i], s.lines[c.start+i]
				// Bound DiffWords' token LCS for long generated/minified lines.
				if len(old) > 4096 || len(cur) > 4096 || len(tokenizeWords(old))*len(tokenizeWords(cur)) > 1<<18 {
					continue
				}
				before, after := DiffWords(old, cur)
				s.changedWords[c.start+i] = editorChangedColumns(after)
				s.deletedWords[c.oldStart+i] = editorChangedColumns(before)
			}
		}
	}
	if !slices.Equal(previous, s.changes) {
		s.expandedChanges = nil
	}
	s.marksStale = false
	s.rebuildReviewRows()
}

func (s *editorSession) rebuildReviewRows() {
	anchor, offset := -1, 0
	if s.freeScroll && len(s.reviewRows) > 0 && len(s.liveRows) > 0 {
		anchor = s.reviewLineAt(s.top)
		if anchor < len(s.liveRows) {
			offset = s.top - s.liveRows[anchor]
		}
	}
	s.reviewRows = nil
	s.liveRows = make([]int, len(s.lines))
	change := 0
	for row := 0; row <= len(s.lines); row++ {
		if change < len(s.changes) && s.changes[change].start == row {
			c := s.changes[change]
			if c.oldStart < c.oldEnd {
				s.reviewRows = append(s.reviewRows, editorReviewRow{-1, -1, change})
				if s.expandedChanges[change] {
					for old := c.oldStart; old < c.oldEnd; old++ {
						s.reviewRows = append(s.reviewRows, editorReviewRow{-1, old, change})
					}
				}
			}
			change++
		}
		if row < len(s.lines) {
			s.liveRows[row] = len(s.reviewRows)
			s.reviewRows = append(s.reviewRows, editorReviewRow{row, -1, -1})
		}
	}
	if anchor >= 0 && anchor < len(s.liveRows) {
		s.top = max(0, s.liveRows[anchor]+offset)
	}
}

func (s *editorSession) toggleDeletion(change int) {
	if s.expandedChanges == nil {
		s.expandedChanges = make(map[int]bool)
	}
	s.expandedChanges[change] = !s.expandedChanges[change]
	s.rebuildReviewRows()
	for i, row := range s.reviewRows {
		if row.change == change && row.oldLine < 0 {
			s.top = i
			break
		}
	}
	s.freeScroll = true
	s.dragging = false
}

func (s *editorSession) page(height, direction int) {
	s.ensureReview()
	offset := min(max(s.liveRows[s.row]-s.top, 0), height-1)
	wanted := min(max(s.top+direction*height+offset, 0), len(s.reviewRows)-1)
	s.top += direction * height
	s.clampView(height)
	row := s.reviewRows[wanted]
	if row.line >= 0 {
		s.moveTo(row.line, s.col, true)
	}
	// Paging can review read-only rows without pulling the cursor back into view.
	s.freeScroll = true
}

func (s *editorSession) toggleNearestDeletion() {
	s.ensureReview()
	nearest, distance := -1, len(s.lines)+1
	for i, c := range s.changes {
		delta := max(c.start-s.row, s.row-c.start)
		if c.oldStart < c.oldEnd && delta < distance {
			nearest, distance = i, delta
		}
	}
	if nearest >= 0 {
		s.toggleDeletion(nearest)
	}
}

func (s *editorSession) clampView(height int) {
	s.top = min(max(s.top, 0), max(len(s.reviewRows)-max(height, 1), 0))
}

func (s *editorSession) followCursor(height, width int) {
	visual := s.liveRows[s.row]
	if visual < s.top {
		s.top = visual
	} else if visual >= s.top+height {
		s.top = visual - height + 1
	}
	s.clampView(height)
	dc := s.displayCol()
	if dc < s.left {
		s.left = dc
	} else if dc >= s.left+width {
		s.left = dc - width + 1
	}
}

func (s *editorSession) reviewLineAt(visual int) int {
	visual = min(max(visual, 0), len(s.reviewRows)-1)
	for i := visual; i < len(s.reviewRows); i++ {
		if s.reviewRows[i].line >= 0 {
			return s.reviewRows[i].line
		}
	}
	return len(s.lines) - 1
}

func (s *editorSession) deletionLabel(row editorReviewRow) string {
	c := s.changes[row.change]
	verb, arrow := "deleted", "▸"
	if c.end > c.start {
		verb = "previous"
	}
	if s.expandedChanges[row.change] {
		arrow = "▾"
	}
	return fmt.Sprintf("%s %d %s lines · read-only · Alt+D toggle", arrow, c.oldEnd-c.oldStart, verb)
}

func (s *editorSession) baselineLabel() string {
	if s.baselineError != "" {
		return "Changes unavailable: " + sanitizeUntrusted(s.baselineError)
	}
	if s.hasHead {
		return "Compared with HEAD · + added · ~ changed · ▸ previous/deleted"
	}
	if s.baselineLoading {
		return "Loading HEAD…"
	}
	return ""
}

func editorHeadLines(text string) []string {
	if text == "" {
		return nil
	}
	text, _, _ = splitFileContent(text)
	return strings.Split(text, "\n")
}
