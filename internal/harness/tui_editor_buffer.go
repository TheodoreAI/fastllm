package harness

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// fileEditor is the text buffer behind the in-TUI editor (tui_editor.go):
// the file's lines, the cursor, scrolling and undo. It knows nothing about
// the TUI or the disk, so its editing is tested on its own.

// editorTabWidth is the display width of a tab stop.
const editorTabWidth = 4

// maxEditorUndo bounds the undo history.
const maxEditorUndo = 500

type editorSnap struct {
	lines    []string
	row, col int
}

// editKind groups consecutive edits into one undo step: a run of typed
// characters undoes together, as in most editors.
type editKind int

const (
	editNone editKind = iota
	editType
	editDelete
	editOther
)

type fileEditor struct {
	lines []string
	// row and col are the cursor's line and rune offset; wantCol is the
	// display column vertical moves try to keep.
	row, col, wantCol int
	// top is the first line shown and left the first display column.
	top, left int
	// anchor is the fixed end of the selection, the cursor its moving end;
	// nil, or equal to the cursor, means nothing is selected.
	anchor *editorPos
	// freeScroll lets the view scroll away from the cursor (the mouse
	// wheel); any key brings the cursor back into view.
	freeScroll bool
	dirty      bool
	undo       []editorSnap
	redo       []editorSnap
	lastEdit   editKind

	// lang picks the lexer; states[i] is the lexer state line i starts in,
	// valid for i < statesValid.
	lang        string
	states      []lexState
	statesValid int
}

func newFileEditor(content, lang string) *fileEditor {
	e := &fileEditor{lang: lang}
	e.setContent(content)
	return e
}

// setContent replaces the buffer with content (LF line endings, no final
// newline handling: a trailing "\n" gives a last empty line).
func (e *fileEditor) setContent(content string) {
	e.lines = strings.Split(content, "\n")
	e.row, e.col, e.wantCol, e.top, e.left = 0, 0, 0, 0, 0
	e.anchor = nil
	e.dirty = false
	e.undo, e.redo = nil, nil
	e.lastEdit = editNone
	e.invalidateFrom(0)
}

// text is the buffer joined with "\n".
func (e *fileEditor) text() string { return strings.Join(e.lines, "\n") }

func (e *fileEditor) invalidateFrom(row int) {
	e.statesValid = min(e.statesValid, max(row, 0))
}

// stateAt is the lexer state line row starts in, computed incrementally.
func (e *fileEditor) stateAt(row int) lexState {
	if len(e.states) < len(e.lines)+1 {
		grown := make([]lexState, len(e.lines)+1)
		copy(grown, e.states[:e.statesValid])
		e.states = grown
	}
	if e.statesValid == 0 {
		e.states[0] = lexState{}
		e.statesValid = 1
	}
	for e.statesValid <= row {
		i := e.statesValid - 1
		_, e.states[i+1] = lexLineState(e.lines[i], e.lang, e.states[i])
		e.statesValid++
	}
	return e.states[row]
}

// tokensAt lexes line row in the state the lines above leave it in.
func (e *fileEditor) tokensAt(row int) []Token {
	tokens, _ := lexLineState(e.lines[row], e.lang, e.stateAt(row))
	return tokens
}

// checkpoint records the buffer for undo before an edit of kind k. Edits
// of the same kind in a row share one checkpoint.
func (e *fileEditor) checkpoint(k editKind) {
	if k != e.lastEdit || k == editOther {
		e.undo = append(e.undo, editorSnap{lines: append([]string(nil), e.lines...), row: e.row, col: e.col})
		if len(e.undo) > maxEditorUndo {
			e.undo = e.undo[len(e.undo)-maxEditorUndo:]
		}
	}
	e.redo = nil
	e.lastEdit = k
	e.dirty = true
}

// breakUndoRun ends the current run of typing, so the next edit starts a
// new undo step (called on cursor moves).
func (e *fileEditor) breakUndoRun() { e.lastEdit = editNone }

func (e *fileEditor) restore(from, to *[]editorSnap) bool {
	if len(*from) == 0 {
		return false
	}
	snap := (*from)[len(*from)-1]
	*from = (*from)[:len(*from)-1]
	*to = append(*to, editorSnap{lines: append([]string(nil), e.lines...), row: e.row, col: e.col})
	e.lines, e.row, e.col = snap.lines, snap.row, snap.col
	e.anchor = nil
	e.clampCursor()
	e.wantCol = e.displayCol()
	e.lastEdit = editNone
	e.dirty = true
	e.invalidateFrom(0)
	return true
}

func (e *fileEditor) undoEdit() bool { return e.restore(&e.undo, &e.redo) }
func (e *fileEditor) redoEdit() bool { return e.restore(&e.redo, &e.undo) }

func (e *fileEditor) clampCursor() {
	e.row = min(max(e.row, 0), len(e.lines)-1)
	e.col = min(max(e.col, 0), len([]rune(e.lines[e.row])))
}

// editorPos is a place in the buffer: a line and a rune offset in it.
type editorPos struct{ row, col int }

func (p editorPos) before(q editorPos) bool {
	return p.row < q.row || p.row == q.row && p.col < q.col
}

func (e *fileEditor) cursor() editorPos { return editorPos{e.row, e.col} }

// selection is the selected range, start before end, if there is one.
func (e *fileEditor) selection() (start, end editorPos, ok bool) {
	if e.anchor == nil || *e.anchor == e.cursor() {
		return editorPos{}, editorPos{}, false
	}
	start, end = *e.anchor, e.cursor()
	if end.before(start) {
		start, end = end, start
	}
	return start, end, true
}

// startSelection anchors a selection at the cursor unless one is under way.
func (e *fileEditor) startSelection() {
	if e.anchor == nil {
		p := e.cursor()
		e.anchor = &p
	}
}

func (e *fileEditor) clearSelection() { e.anchor = nil }

func (e *fileEditor) selectAll() {
	e.anchor = &editorPos{0, 0}
	e.moveTo(len(e.lines)-1, len([]rune(e.lines[len(e.lines)-1])), false)
}

// selectedText is the text of the selection, or "".
func (e *fileEditor) selectedText() string {
	start, end, ok := e.selection()
	if !ok {
		return ""
	}
	first := []rune(e.lines[start.row])
	if start.row == end.row {
		return string(first[start.col:end.col])
	}
	parts := []string{string(first[start.col:])}
	parts = append(parts, e.lines[start.row+1:end.row]...)
	parts = append(parts, string([]rune(e.lines[end.row])[:end.col]))
	return strings.Join(parts, "\n")
}

// removeSelection deletes the selected text, leaving the cursor where it
// began. The caller has taken the undo checkpoint.
func (e *fileEditor) removeSelection() {
	start, end, ok := e.selection()
	e.anchor = nil
	if !ok {
		return
	}
	head := string([]rune(e.lines[start.row])[:start.col])
	tail := string([]rune(e.lines[end.row])[end.col:])
	e.lines = append(e.lines[:start.row+1], e.lines[end.row+1:]...)
	e.lines[start.row] = head + tail
	e.row, e.col = start.row, start.col
	e.invalidateFrom(start.row)
	e.wantCol = e.displayCol()
}

// deleteSelection deletes the selection as one undo step; false when there
// is none.
func (e *fileEditor) deleteSelection() bool {
	if _, _, ok := e.selection(); !ok {
		e.anchor = nil
		return false
	}
	e.checkpoint(editOther)
	e.removeSelection()
	return true
}

// deleteLine removes the cursor's line (a cut with nothing selected).
func (e *fileEditor) deleteLine() {
	e.checkpoint(editOther)
	e.anchor = nil
	if len(e.lines) == 1 {
		e.lines[0] = ""
	} else {
		e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
	}
	e.row = min(e.row, len(e.lines)-1)
	e.col = runeColAt(e.lines[e.row], e.wantCol)
	e.invalidateFrom(e.row)
}

// indentLines indents (or outdents) every line the selection touches, or
// the cursor's line, by unit.
func (e *fileEditor) indentLines(unit string, outdent bool) {
	first, last := e.row, e.row
	if start, end, ok := e.selection(); ok {
		first, last = start.row, end.row
		// A selection ending at column 0 does not take in that line.
		if end.col == 0 && end.row > start.row {
			last--
		}
	}
	e.checkpoint(editOther)
	shift := map[int]int{}
	for row := first; row <= last; row++ {
		line := e.lines[row]
		if !outdent {
			if line == "" && first != last {
				continue
			}
			e.lines[row] = unit + line
			shift[row] = len([]rune(unit))
			continue
		}
		cut := 0
		switch {
		case strings.HasPrefix(line, unit):
			cut = len(unit)
		case strings.HasPrefix(line, "\t"):
			cut = 1
		default:
			for cut < len(line) && cut < editorTabWidth && line[cut] == ' ' {
				cut++
			}
		}
		e.lines[row] = line[cut:]
		shift[row] = -cut
	}
	e.col = max(e.col+shift[e.row], 0)
	if e.anchor != nil {
		e.anchor.col = max(e.anchor.col+shift[e.anchor.row], 0)
	}
	e.clampCursor()
	e.wantCol = e.displayCol()
	e.invalidateFrom(first)
}

// insert types s at the cursor, replacing the selection; s may hold
// newlines (a paste).
func (e *fileEditor) insert(s string) {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	_, _, replacing := e.selection()
	if s == "" && !replacing {
		return
	}
	kind := editType
	if strings.Contains(s, "\n") || len([]rune(s)) > 1 || replacing {
		kind = editOther
	}
	e.checkpoint(kind)
	e.removeSelection()
	if s == "" {
		return
	}
	line := []rune(e.lines[e.row])
	head, tail := string(line[:e.col]), string(line[e.col:])
	parts := strings.Split(s, "\n")
	if len(parts) == 1 {
		e.lines[e.row] = head + s + tail
		e.col += len([]rune(s))
	} else {
		added := make([]string, len(parts))
		added[0] = head + parts[0]
		copy(added[1:], parts[1:])
		last := len(parts) - 1
		added[last] = parts[last] + tail
		e.lines = append(e.lines[:e.row], append(added, e.lines[e.row+1:]...)...)
		e.invalidateFrom(e.row)
		e.row += last
		e.col = len([]rune(parts[last]))
	}
	e.invalidateFrom(e.row)
	e.wantCol = e.displayCol()
}

// newline splits the line at the cursor, carrying its indentation over.
func (e *fileEditor) newline() {
	line := e.lines[e.row]
	indent := line[:len(line)-len(strings.TrimLeftFunc(line, unicode.IsSpace))]
	if len([]rune(indent)) > e.col {
		indent = string([]rune(indent)[:e.col])
	}
	e.insert("\n" + indent)
}

// backspace deletes the rune before the cursor, joining lines at column 0.
func (e *fileEditor) backspace() {
	if e.deleteSelection() || e.col == 0 && e.row == 0 {
		return
	}
	e.checkpoint(editDelete)
	if e.col == 0 {
		prev := []rune(e.lines[e.row-1])
		e.lines[e.row-1] = string(prev) + e.lines[e.row]
		e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
		e.row--
		e.col = len(prev)
	} else {
		line := []rune(e.lines[e.row])
		e.lines[e.row] = string(line[:e.col-1]) + string(line[e.col:])
		e.col--
	}
	e.invalidateFrom(e.row)
	e.wantCol = e.displayCol()
}

// deleteForward deletes the rune under the cursor, joining lines at the end.
func (e *fileEditor) deleteForward() {
	if e.deleteSelection() {
		return
	}
	line := []rune(e.lines[e.row])
	if e.col == len(line) {
		if e.row == len(e.lines)-1 {
			return
		}
		e.checkpoint(editDelete)
		e.lines[e.row] = string(line) + e.lines[e.row+1]
		e.lines = append(e.lines[:e.row+1], e.lines[e.row+2:]...)
	} else {
		e.checkpoint(editDelete)
		e.lines[e.row] = string(line[:e.col]) + string(line[e.col+1:])
	}
	e.invalidateFrom(e.row)
}

// moveTo puts the cursor at row, col (clamped); keepCol keeps wantCol, for
// vertical moves.
func (e *fileEditor) moveTo(row, col int, keepCol bool) {
	e.row, e.col = row, col
	e.clampCursor()
	if !keepCol {
		e.wantCol = e.displayCol()
	}
	e.breakUndoRun()
}

// moveVertical moves the cursor n lines, keeping its display column.
func (e *fileEditor) moveVertical(n int) {
	row := min(max(e.row+n, 0), len(e.lines)-1)
	e.moveTo(row, runeColAt(e.lines[row], e.wantCol), true)
}

func (e *fileEditor) moveLeft() {
	if e.col > 0 {
		e.moveTo(e.row, e.col-1, false)
	} else if e.row > 0 {
		e.moveTo(e.row-1, len([]rune(e.lines[e.row-1])), false)
	}
}

func (e *fileEditor) moveRight() {
	if e.col < len([]rune(e.lines[e.row])) {
		e.moveTo(e.row, e.col+1, false)
	} else if e.row < len(e.lines)-1 {
		e.moveTo(e.row+1, 0, false)
	}
}

// moveWord jumps to the start of the next (dir > 0) or previous word.
func (e *fileEditor) moveWord(dir int) {
	line := []rune(e.lines[e.row])
	col := e.col
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
	if dir > 0 {
		if col >= len(line) {
			e.moveRight()
			return
		}
		for col < len(line) && isWord(line[col]) {
			col++
		}
		for col < len(line) && !isWord(line[col]) {
			col++
		}
	} else {
		if col == 0 {
			e.moveLeft()
			return
		}
		for col > 0 && !isWord(line[col-1]) {
			col--
		}
		for col > 0 && isWord(line[col-1]) {
			col--
		}
	}
	e.moveTo(e.row, col, false)
}

// home goes to the first non-blank character, or column 0 if already there.
func (e *fileEditor) home() {
	line := e.lines[e.row]
	first := len([]rune(line)) - len([]rune(strings.TrimLeftFunc(line, unicode.IsSpace)))
	if e.col == first {
		first = 0
	}
	e.moveTo(e.row, first, false)
}

func (e *fileEditor) end() { e.moveTo(e.row, len([]rune(e.lines[e.row])), false) }

// displayCol is the cursor's column on screen, with tabs expanded.
func (e *fileEditor) displayCol() int {
	return displayWidth([]rune(e.lines[e.row])[:e.col])
}

// scrollIntoView adjusts top and left so the cursor is inside a view of
// height lines by width columns.
func (e *fileEditor) scrollIntoView(height, width int) {
	height, width = max(height, 1), max(width, 1)
	if e.row < e.top {
		e.top = e.row
	} else if e.row >= e.top+height {
		e.top = e.row - height + 1
	}
	e.top = min(max(e.top, 0), max(len(e.lines)-1, 0))
	dc := e.displayCol()
	if dc < e.left {
		e.left = dc
	} else if dc >= e.left+width {
		e.left = dc - width + 1
	}
}

// displayWidth is the screen width of runes laid out from column 0.
func displayWidth(runes []rune) int {
	w := 0
	for _, r := range runes {
		w += runeCells(r, w)
	}
	return w
}

// runeCells is how many columns r takes when drawn at column at.
func runeCells(r rune, at int) int {
	if r == '\t' {
		return editorTabWidth - at%editorTabWidth
	}
	return max(ansi.StringWidth(string(r)), 1)
}

// runeColAt is the rune offset in line nearest to display column want.
func runeColAt(line string, want int) int {
	w := 0
	for i, r := range []rune(line) {
		cells := runeCells(r, w)
		if w+cells > want {
			return i
		}
		w += cells
	}
	return len([]rune(line))
}

// editorLineMarks are what renderEditorLine highlights on a line, in
// display columns: the cursor (-1 for none) and the selected columns
// [selStart, selEnd). selEnd past the end of the text selects the line
// break, shown as one highlighted cell.
type editorLineMarks struct {
	cursor, selStart, selEnd int
}

var noLineMarks = editorLineMarks{cursor: -1}

// defaultSelectionBg is the selection background in the terminal's own
// palette (bright black), for themes that print with ANSI16 colours;
// ApplyTheme sets selectionBg from the theme otherwise.
const defaultSelectionBg = "\033[100m"

var selectionBg = defaultSelectionBg

// renderEditorLine draws tokens from display column left, exactly width
// columns wide, with the cursor in reverse video and the selection on
// selectionBg.
func renderEditorLine(tokens []Token, left, width int, marks editorLineMarks) string {
	var sb strings.Builder
	colors := ColorsEnabled()
	selBg := ""
	if colors {
		selBg = selectionBg
	}
	col, drawn := 0, 0
	cursorDrawn, full := false, false
	// open is the colour sequence in force, so runs of one colour share it.
	open := ""
	setStyle := func(style string) {
		if style == open {
			return
		}
		if open != "" {
			sb.WriteString(ansiReset)
		}
		sb.WriteString(style)
		open = style
	}
	for _, tok := range tokens {
		if full {
			break
		}
		color := ""
		if colors && tok.Type != TokenPlain {
			color = TokenColorCode(tok.Type)
		}
		for _, r := range tok.Value {
			cells := runeCells(r, col)
			start := col
			col += cells
			if col <= left {
				continue
			}
			glyph := string(r)
			if unicode.IsControl(r) && r != '\t' {
				// Never pass the file's control bytes (escapes) to the terminal.
				glyph = "·"
			}
			// A tab, or a wide rune straddling the left edge, shows as spaces.
			if r == '\t' || start < left {
				glyph = strings.Repeat(" ", col-max(start, left))
			}
			if drawn+ansi.StringWidth(glyph) > width {
				full = true
				break
			}
			switch {
			case start <= marks.cursor && marks.cursor < col:
				setStyle(ansiInverse)
				cursorDrawn = true
			case start >= marks.selStart && start < marks.selEnd && selBg != "":
				setStyle(selBg + color)
			default:
				setStyle(color)
			}
			sb.WriteString(glyph)
			drawn += ansi.StringWidth(glyph)
		}
	}
	setStyle("")
	// Past the text: the cursor at the end of the line, or the selected
	// line break.
	eol := col
	if eol >= left && eol-left < width && drawn <= eol-left {
		style := ""
		switch {
		case marks.cursor == eol && !cursorDrawn:
			style = ansiInverse
		case marks.selEnd > eol && marks.selStart <= eol && selBg != "":
			style = selBg
		}
		if style != "" {
			sb.WriteString(strings.Repeat(" ", eol-left-drawn))
			sb.WriteString(style + " " + ansiReset)
			drawn = eol - left + 1
		}
	}
	if drawn < width {
		sb.WriteString(strings.Repeat(" ", width-drawn))
	}
	return sb.String()
}
