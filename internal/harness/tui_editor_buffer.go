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
	dirty     bool
	undo      []editorSnap
	redo      []editorSnap
	lastEdit  editKind

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

// insert types s at the cursor; s may hold newlines (a paste).
func (e *fileEditor) insert(s string) {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	if s == "" {
		return
	}
	kind := editType
	if strings.Contains(s, "\n") || len([]rune(s)) > 1 {
		kind = editOther
	}
	e.checkpoint(kind)
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
	if e.col == 0 && e.row == 0 {
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

// renderEditorLine draws tokens from display column left, width columns
// wide, with the cursor (a display column, or -1) in reverse video.
func renderEditorLine(tokens []Token, left, width, cursor int) string {
	var sb strings.Builder
	colors := ColorsEnabled()
	col, drawn := 0, 0
	cursorDrawn, full := false, false
	for _, tok := range tokens {
		if full {
			break
		}
		color := ""
		if colors && tok.Type != TokenPlain {
			color = TokenColorCode(tok.Type)
		}
		open := false
		for _, r := range tok.Value {
			cells := runeCells(r, col)
			start := col
			col += cells
			if col <= left || drawn >= width {
				continue
			}
			// A wide rune or tab straddling the left edge shows as spaces.
			glyph := string(r)
			if unicode.IsControl(r) && r != '\t' {
				// Never pass the file's control bytes (escapes) to the terminal.
				glyph = "·"
			}
			if r == '\t' || start < left {
				glyph = strings.Repeat(" ", col-max(start, left))
			}
			if drawn+ansi.StringWidth(glyph) > width {
				full = true
				break
			}
			if start <= cursor && cursor < col {
				if open {
					sb.WriteString(ansiReset)
					open = false
				}
				sb.WriteString(ansiInverse + glyph + ansiReset)
				cursorDrawn = true
			} else {
				if color != "" && !open {
					sb.WriteString(color)
					open = true
				}
				sb.WriteString(glyph)
			}
			drawn += ansi.StringWidth(glyph)
		}
		if open {
			sb.WriteString(ansiReset)
		}
	}
	if cursor >= 0 && !cursorDrawn && cursor >= left && cursor-left < width {
		sb.WriteString(strings.Repeat(" ", max(cursor-left-drawn, 0)))
		drawn = max(drawn, cursor-left)
		sb.WriteString(ansiInverse + " " + ansiReset)
		drawn++
	}
	if drawn < width {
		sb.WriteString(strings.Repeat(" ", width-drawn))
	}
	return sb.String()
}
