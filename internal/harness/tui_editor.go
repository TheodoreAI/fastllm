package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fastllm/internal/files"
	"fastllm/internal/gitrepo"

	tea "charm.land/bubbletea/v2"
)

// The in-TUI editor opens a workspace file full-screen with syntax
// highlighting, and marks in the gutter the lines that differ from HEAD.
// Ctrl+D shows the file's before/after in the diff modal.
//
// The user, not the model, drives it, so it does not pass through the
// reference monitor (monitor.go). It does write through the same files
// layer the model's write tools use, so it has the same limits: inside the
// workspace, never into .git. Its saves are not journaled: /undo reverts
// the model's changes only (journal.go), and treats a file the user saved
// since as theirs.

// maxEditorFileBytes is the largest file the editor opens.
const maxEditorFileBytes = 2 << 20

type editorPrompt int

const (
	editorPromptNone editorPrompt = iota
	editorPromptClose
	editorPromptConflict
)

// editorSession is an open editor: the buffer plus the file it came from.
type editorSession struct {
	*fileEditor
	path string // absolute
	root string // fixed user-driven file context, independent of agent workspace
	rel  string // relative to the workspace, slash-separated
	// crlf and finalNL are the file's line endings, kept on save.
	crlf, finalNL bool
	// indent is what Tab inserts: a tab if the file indents with tabs.
	indent string
	// exists and diskHash describe the file as last read or saved, to spot
	// another writer (the agent, a command) changing it meanwhile.
	exists   bool
	diskHash [32]byte
	// head is the file at HEAD, for the gutter marks; hasHead is false
	// outside a git repository.
	head                       []string
	hasHead                    bool
	marks                      []byte
	marksStale                 bool
	changes                    []editorChange
	reviewRows                 []editorReviewRow
	liveRows                   []int
	changedWords, deletedWords map[int][]editorColumnRange
	expandedChanges            map[int]bool
	baselineID                 uint64
	baselineLoading            bool
	baselineError              string
	headTokens                 *fileEditor
	prompt                     editorPrompt
	help                       bool
	notice                     string
	// clipboard is the editor's last copy, for Ctrl+V when the system
	// clipboard is out of reach; clipboardLine marks a whole-line copy.
	clipboard     string
	clipboardLine bool
	// dragging is set while the left button, pressed in the text, is held.
	dragging bool
	// returnToDiff reopens the diff modal on close, when the editor was
	// opened from it.
	returnToDiff bool
}

// splitFileContent turns file content into buffer text: LF endings and
// no final newline, reporting what it removed.
func splitFileContent(content string) (text string, crlf, finalNL bool) {
	crlf = strings.Contains(content, "\r\n")
	if crlf {
		content = strings.ReplaceAll(content, "\r\n", "\n")
	}
	finalNL = strings.HasSuffix(content, "\n")
	return strings.TrimSuffix(content, "\n"), crlf, finalNL
}

// fileContent is splitFileContent in reverse.
func (s *editorSession) fileContent() string {
	content := s.text()
	if s.finalNL {
		content += "\n"
	}
	if s.crlf {
		content = strings.ReplaceAll(content, "\n", "\r\n")
	}
	return content
}

func detectIndent(text string) string {
	for _, line := range strings.SplitN(text, "\n", 200) {
		if strings.HasPrefix(line, "\t") {
			return "\t"
		}
	}
	return "    "
}

// editorFiles is the files layer the editor writes through.
func (m *teaModel) editorRoot() string {
	if m.source.active && m.source.root != "" {
		return m.source.root
	}
	return m.workingDir
}
func (m *teaModel) editorFiles() *files.Reader { return files.New(m.editorRoot(), true) }

// openEditor opens requested (relative to the workspace, or absolute
// inside it) in the editor. A path that does not exist yet opens empty and
// is created on save.
func (m *teaModel) openEditor(requested string, returnToDiff bool) error {
	if !m.source.active {
		requested = strings.TrimSpace(requested)
	}
	if m.editor != nil && m.editor.dirty {
		return errors.New("save or discard the unsaved buffer first")
	}
	if requested == "" {
		return errors.New("no file named")
	}
	path, err := m.editorFiles().ResolveForWrite(requested)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(m.editorRoot(), path)
	if err != nil {
		rel = path
	}
	rel = filepath.ToSlash(rel)

	var data []byte
	exists := false
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", rel)
		}
		if info.Size() > maxEditorFileBytes {
			return fmt.Errorf("%s is %d KB; the editor opens files up to %d KB", rel, info.Size()>>10, maxEditorFileBytes>>10)
		}
		if data, err = os.ReadFile(path); err != nil {
			return err
		}
		if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			return fmt.Errorf("%s looks like a binary file", rel)
		}
		exists = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	text, crlf, finalNL := splitFileContent(string(data))
	if !exists {
		finalNL = true
	}
	s := &editorSession{
		fileEditor:   newFileEditor(text, DetectLanguage(path)),
		path:         path,
		root:         m.editorRoot(),
		rel:          rel,
		crlf:         crlf,
		finalNL:      finalNL,
		indent:       detectIndent(text),
		exists:       exists,
		diskHash:     sha256.Sum256(data),
		marksStale:   true,
		returnToDiff: returnToDiff,
	}
	if !m.source.active && m.checkpointMgr != nil && m.checkpointMgr.IsGitRepo() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		head, err := gitrepo.HeadContents(ctx, m.editorRoot(), rel)
		cancel()
		if err == nil {
			s.hasHead = true
			s.head = editorHeadLines(head)
		} else {
			s.baselineError = err.Error()
		}
	}
	if !exists {
		s.notice = "New file: it is created when you save."
	}
	m.editor = s
	m.input.Blur()
	return nil
}

// closeEditor closes the editor without saving; callers check dirty first.
func (m *teaModel) closeEditor() {
	s := m.editor
	m.editor = nil
	if m.source.active {
		if m.sourceExitEditor {
			m.sourceExitEditor = false
			m.leaveSource()
		}
		return
	}
	if s != nil && s.returnToDiff {
		m.syncGitStatus()
		if idx := m.changedFileIndex(s.rel); idx >= 0 {
			m.openDiffModal(idx)
			return
		}
	}
	if !m.modalOpen() {
		m.input.Focus()
	}
}

// syncGitStatus refreshes the changes list now, for a view that needs it
// current before the watcher's refresh arrives.
func (m *teaModel) syncGitStatus() {
	if m.checkpointMgr == nil || !m.checkpointMgr.IsGitRepo() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if status, err := gitrepo.GetRepoStatus(ctx, m.workingDir); err == nil {
		m.changes.UpdateFromGit(status)
	}
}

// changedFileIndex finds rel in the changes list, or -1.
func (m *teaModel) changedFileIndex(rel string) int {
	for i, f := range m.changes.files {
		if f.Path == rel || strings.HasSuffix(f.Path, "/"+rel) {
			return i
		}
	}
	return -1
}

// saveEditor writes the buffer to disk. Unless force is set, it first
// checks that nothing else changed the file since it was read, and asks
// what to do if something did.
func (m *teaModel) saveEditor(force bool) tea.Cmd {
	s := m.editor
	if reason := m.sourceBusyReason(); reason != "" {
		s.notice = reason
		return nil
	}
	if !force {
		current, err := os.ReadFile(s.path)
		existsNow := err == nil
		if existsNow != s.exists || (existsNow && sha256.Sum256(current) != s.diskHash) {
			s.prompt = editorPromptConflict
			return nil
		}
	}
	content := s.fileContent()
	root := s.root
	if root == "" {
		root = m.editorRoot()
	}
	if err := files.New(root, true).Write(s.rel, content); err != nil {
		s.notice = ColorRed("Save failed: " + err.Error())
		return nil
	}
	s.exists = true
	s.diskHash = sha256.Sum256([]byte(content))
	s.dirty = false
	s.marksStale = true
	s.prompt = editorPromptNone
	s.notice = ColorGreen("✓ Saved " + sourceLabel(s.rel))
	return tea.Batch(m.refreshGitStatusCmd(), m.refreshSourceCmd())
}

// reloadEditor replaces the buffer with the file on disk, keeping the
// cursor's line where it can.
func (m *teaModel) reloadEditor() {
	s := m.editor
	data, err := os.ReadFile(s.path)
	s.exists = err == nil
	row, top := s.row, s.top
	text, crlf, finalNL := splitFileContent(string(data))
	s.setContent(text)
	s.crlf, s.finalNL = crlf, finalNL || !s.exists
	s.diskHash = sha256.Sum256(data)
	s.moveTo(row, 0, false)
	s.top = top
	s.marksStale = true
	s.prompt = editorPromptNone
	s.notice = "Reloaded " + s.rel + " from disk."
}

// editorShowDiff opens the diff modal on the editor's file.
func (m *teaModel) editorShowDiff() {
	if m.source.active {
		if m.editor.dirty {
			m.editor.prompt = editorPromptClose
		} else {
			m.closeEditor()
		}
		return
	}
	s := m.editor
	if s.dirty {
		s.notice = ColorYellow("Save with Ctrl+S first: the diff compares the file on disk.")
		return
	}
	m.syncGitStatus()
	idx := m.changedFileIndex(s.rel)
	if idx < 0 {
		s.notice = "No changes from HEAD in " + s.rel + "."
		return
	}
	m.editor = nil
	m.openDiffModal(idx)
}

// editorGeometry lays out a title, text, status, and action row.
func (m *teaModel) editorGeometry() (bodyHeight, gutterWidth, codeWidth int) {
	width, height := m.frameWidth(), max(m.height, 3)
	if m.source.active {
		_, width, height = m.sourceGeometry()
	}
	bodyHeight = max(height-3, 1)
	numWidth := 3
	if m.editor != nil {
		numWidth = max(numWidth, len(fmt.Sprint(len(m.editor.lines))))
	}
	gutterWidth = numWidth + 5 // mark, space, number, space, bar, space
	return bodyHeight, gutterWidth, max(width-gutterWidth, 1)
}

func (m *teaModel) handleEditorKey(msg tea.KeyPressMsg) tea.Cmd {
	s := m.editor
	key := msg.String()
	if s.help {
		if key == "f1" || key == "esc" || key == "ctrl+c" {
			s.help = false
		}
		return nil
	}
	switch s.prompt {
	case editorPromptClose:
		switch key {
		case "s", "S":
			cmd := m.saveEditor(false)
			if !s.dirty {
				m.closeEditor()
			}
			return cmd
		case "d", "D", "n", "N":
			m.closeEditor()
		case "esc", "ctrl+c":
			s.prompt = editorPromptNone
			m.sourceExitEditor = false
			m.source.pendingAction = ""
		}
		return nil
	case editorPromptConflict:
		switch key {
		case "o", "O":
			return m.saveEditor(true)
		case "r", "R":
			m.reloadEditor()
		case "esc", "ctrl+c", "c", "C":
			s.prompt = editorPromptNone
		}
		return nil
	}

	s.notice = ""
	if key == "f1" {
		s.help = true
		s.dragging = false
		return nil
	}
	if key == "alt+d" {
		s.toggleNearestDeletion()
		return nil
	}
	if m.editorMoveKey(key) {
		return nil
	}
	// Save, copy, ignored keys, and redraws preserve independent wheel scrolling.
	switch key {
	case "ctrl+a", "ctrl+x", "enter", "backspace", "delete", "tab", "shift+tab":
		s.freeScroll = false
	}
	switch key {
	case "ctrl+s":
		return m.saveEditor(false)
	case "esc", "ctrl+q":
		switch {
		case key == "esc" && s.anchor != nil:
			s.clearSelection()
		case s.dirty:
			s.prompt = editorPromptClose
		default:
			m.closeEditor()
		}
	case "ctrl+z":
		if !s.undoEdit() {
			s.notice = "Nothing to undo."
		} else {
			s.freeScroll = false
		}
		s.marksStale = true
	case "ctrl+y", "ctrl+shift+z":
		if !s.redoEdit() {
			s.notice = "Nothing to redo."
		} else {
			s.freeScroll = false
		}
		s.marksStale = true
	case "ctrl+d":
		m.editorShowDiff()
	case "ctrl+a":
		s.selectAll()
	case "ctrl+c":
		return m.editorCopy(false)
	case "ctrl+x":
		return m.editorCopy(true)
	case "ctrl+v":
		return m.editorPasteFromClipboard()
	case "enter":
		s.deleteSelection()
		s.newline()
		s.marksStale = true
	case "backspace":
		s.backspace()
		s.marksStale = true
	case "delete":
		s.deleteForward()
		s.marksStale = true
	case "tab":
		if start, end, ok := s.selection(); ok && start.row != end.row {
			s.indentLines(s.indent, false)
		} else {
			s.insert(s.indent)
		}
		s.marksStale = true
	case "shift+tab":
		s.indentLines(s.indent, true)
		s.marksStale = true
	default:
		if msg.Text != "" && !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) {
			s.freeScroll = false
			s.insert(msg.Text)
			s.marksStale = true
		}
	}
	return nil
}

// editorMoveKey moves the cursor for a navigation key, reporting whether
// key was one. With Shift the move extends the selection; without, it
// drops it (Left and Right first collapse it to its start or end).
func (m *teaModel) editorMoveKey(key string) bool {
	s := m.editor
	shifted := strings.Contains(key, "shift+")
	move := strings.Replace(key, "shift+", "", 1)
	bodyHeight, _, _ := m.editorGeometry()
	var do func()
	switch move {
	case "up":
		do = func() { s.moveVertical(-1) }
	case "down":
		do = func() { s.moveVertical(1) }
	case "left":
		do = s.moveLeft
	case "right":
		do = s.moveRight
	case "ctrl+left", "alt+left":
		do = func() { s.moveWord(-1) }
	case "ctrl+right", "alt+right":
		do = func() { s.moveWord(1) }
	case "home":
		do = s.home
	case "end":
		do = s.end
	case "pgup":
		do = func() { s.page(bodyHeight, -1) }
	case "pgdown":
		do = func() { s.page(bodyHeight, 1) }
	case "ctrl+home":
		do = func() { s.moveTo(0, 0, false) }
	case "ctrl+end":
		do = func() { s.moveTo(len(s.lines)-1, len([]rune(s.lines[len(s.lines)-1])), false) }
	default:
		return false
	}
	s.freeScroll = false
	s.dragging = false
	if shifted {
		s.startSelection()
		do()
		return true
	}
	if start, end, ok := s.selection(); ok && (move == "left" || move == "right") {
		to := start
		if move == "right" {
			to = end
		}
		s.clearSelection()
		s.moveTo(to.row, to.col, false)
		return true
	}
	s.clearSelection()
	do()
	return true
}

// editorClipboardMsg carries the system clipboard's text to the editor.
type editorClipboardMsg struct {
	text string
	err  error
}

// editorCopy copies the selection, or the cursor's whole line when nothing
// is selected, and with cut removes it.
func (m *teaModel) editorCopy(cut bool) tea.Cmd {
	s := m.editor
	text, whole := s.selectedText(), false
	if text == "" {
		text, whole = s.lines[s.row]+"\n", true
	}
	s.clipboard, s.clipboardLine = text, whole
	lines := strings.Count(strings.TrimSuffix(text, "\n"), "\n") + 1
	verb := "Copied"
	if cut {
		verb = "Cut"
		if whole {
			s.deleteLine()
		} else {
			s.deleteSelection()
		}
		s.marksStale = true
	}
	switch {
	case whole:
		s.notice = verb + " the line."
	case lines > 1:
		s.notice = fmt.Sprintf("%s %d lines.", verb, lines)
	default:
		s.notice = fmt.Sprintf("%s %d characters.", verb, len([]rune(text)))
	}
	// The system clipboard, and OSC 52 for a terminal on another machine.
	return tea.Batch(tea.SetClipboard(text), func() tea.Msg {
		_ = writeClipboardText(text)
		return nil
	})
}

// editorPasteFromClipboard reads the system clipboard for Ctrl+V. Most
// terminals paste on Ctrl+V themselves, which arrives as a paste instead.
func (m *teaModel) editorPasteFromClipboard() tea.Cmd {
	return func() tea.Msg {
		text, err := readClipboardText()
		return editorClipboardMsg{text: text, err: err}
	}
}

// handleEditorClipboard pastes what editorPasteFromClipboard read, falling
// back to the editor's own last copy when the system clipboard is out of
// reach. A whole line copied with nothing selected pastes above the
// cursor's line, as it does in most editors.
func (m *teaModel) handleEditorClipboard(msg editorClipboardMsg) {
	s := m.editor
	if s == nil || s.prompt != editorPromptNone || s.help {
		return
	}
	text := msg.text
	if msg.err != nil || text == "" {
		text = s.clipboard
	}
	if text == "" {
		s.notice = "The clipboard is empty."
		return
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	s.freeScroll = false
	if _, _, selected := s.selection(); !selected && s.clipboardLine && text == s.clipboard {
		row, col := s.row, s.col
		s.moveTo(row, 0, false)
		s.insert(text)
		s.moveTo(row+strings.Count(text, "\n"), col, false)
	} else {
		s.insert(text)
	}
	s.marksStale = true
}

// handleEditorPaste inserts pasted text at the cursor.
func (m *teaModel) handleEditorPaste(content string) {
	if s := m.editor; s.prompt == editorPromptNone && !s.help {
		s.freeScroll = false
		s.insert(content)
		s.marksStale = true
	}
}

// editorPosAt is the buffer position under screen cell x, y, clamped to
// the text; a drag above or below the text reaches the line past the view,
// which scrolls it.
func (m *teaModel) editorPosAt(x, y int) editorPos {
	s := m.editor
	s.ensureReview()
	_, gutterWidth, _ := m.editorGeometry()
	row := s.reviewLineAt(s.top + y - 1)
	return editorPos{row, runeColAt(s.lines[row], s.left+max(x-gutterWidth, 0))}
}

// handleEditorMouse scrolls the view with the wheel, places the cursor on
// click (Shift+click extends the selection) and selects by dragging.
func (m *teaModel) handleEditorMouse(msg tea.MouseMsg) {
	s := m.editor
	if s.prompt != editorPromptNone || s.help {
		return
	}
	mouse := msg.Mouse()
	s.ensureReview()
	bodyHeight, _, _ := m.editorGeometry()
	switch msg.(type) {
	case tea.MouseWheelMsg:
		// The view scrolls on its own; the cursor stays where it was.
		step := 0
		switch mouse.Button {
		case tea.MouseWheelUp:
			step = -3
		case tea.MouseWheelDown:
			step = 3
		}
		s.freeScroll = true
		s.dragging = false
		s.top += step
		s.clampView(bodyHeight)
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft || mouse.Y < 1 || mouse.Y > bodyHeight {
			return
		}
		visual := s.top + mouse.Y - 1
		if visual >= len(s.reviewRows) {
			return
		}
		if row := s.reviewRows[visual]; row.line < 0 {
			if row.oldLine < 0 {
				s.toggleDeletion(row.change)
			}
			return
		}
		pos := m.editorPosAt(mouse.X, mouse.Y)
		if mouse.Mod.Contains(tea.ModShift) {
			s.startSelection()
		} else {
			s.anchor = &pos
		}
		s.moveTo(pos.row, pos.col, false)
		s.dragging = true
		s.freeScroll = false
	case tea.MouseMotionMsg:
		if mouse.Button == tea.MouseNone {
			s.dragging = false
		}
		if !s.dragging || mouse.Button != tea.MouseLeft {
			return
		}
		pos := m.editorPosAt(mouse.X, mouse.Y)
		s.moveTo(pos.row, pos.col, false)
		s.freeScroll = false
	case tea.MouseReleaseMsg:
		s.dragging = false
		if s.anchor != nil && *s.anchor == s.cursor() {
			s.clearSelection()
		}
	}
}

// renderEditor draws the editor over the whole screen.
func (m *teaModel) renderEditor() string {
	s := m.editor
	width := m.frameWidth()
	if m.source.active {
		_, width, _ = m.sourceGeometry()
	}
	bodyHeight, gutterWidth, codeWidth := m.editorGeometry()
	s.ensureReview()
	if s.freeScroll {
		s.clampView(bodyHeight)
	} else {
		s.followCursor(bodyHeight, codeWidth)
	}
	selStart, selEnd, selected := s.selection()

	rows := make([]string, 0, bodyHeight+3)
	rows = append(rows, m.renderEditorHeader(width))

	numWidth := gutterWidth - 5
	for i := range bodyHeight {
		visual := s.top + i
		if visual >= len(s.reviewRows) {
			rows = append(rows, PadRight(ColorBorder(strings.Repeat(" ", gutterWidth-2)+SymVLine), width))
			continue
		}
		review := s.reviewRows[visual]
		if review.line < 0 {
			if review.oldLine < 0 {
				rows = append(rows, PadRight(clampToWidth(ColorRed(s.deletionLabel(review)), width), width))
			} else {
				marks := noLineMarks
				marks.background, marks.wordBackground = diffBg.lineDel, diffBg.wordDel
				marks.changed = s.deletedWords[review.oldLine]
				code := renderEditorLine(s.headTokens.tokensAt(review.oldLine), s.left, codeWidth, marks)
				gutter := ColorRed(fmt.Sprintf("- %*d │ ", numWidth, review.oldLine+1))
				rows = append(rows, gutter+code)
			}
			continue
		}
		row := review.line
		mark := " "
		if row < len(s.marks) {
			switch s.marks[row] {
			case '+':
				mark = ColorGreen("+")
			case '~':
				mark = ColorYellow("~")
			case '-':
				mark = ColorRed("▾")
			}
		}
		number := fmt.Sprintf("%*d", numWidth, row+1)
		lineMarks := noLineMarks
		if row < len(s.marks) && (s.marks[row] == '+' || s.marks[row] == '~') {
			lineMarks.background, lineMarks.wordBackground = diffBg.lineAdd, diffBg.wordAdd
			lineMarks.changed = s.changedWords[row]
		}
		if row == s.row && m.editorFocused() {
			number = ColorCyan(StyleBold(number))
			lineMarks.cursor = s.displayCol()
		} else {
			number = ColorGray(number)
		}
		if selected && row >= selStart.row && row <= selEnd.row {
			runes := []rune(s.lines[row])
			if row == selStart.row {
				lineMarks.selStart = displayWidth(runes[:selStart.col])
			}
			if row == selEnd.row {
				lineMarks.selEnd = displayWidth(runes[:selEnd.col])
			} else {
				lineMarks.selEnd = displayWidth(runes) + 1 // the line break
			}
		}
		code := renderEditorLine(s.tokensAt(row), s.left, codeWidth, lineMarks)
		line := mark + " " + number + " " + ColorBorder(SymVLine) + " " + code
		if row == s.row && m.editorFocused() && lineMarks.background == "" {
			line = editorSurface(line, composerBackground())
		}
		rows = append(rows, line)
	}

	rows = append(rows, m.renderEditorStatus(width))
	rows = append(rows, m.renderEditorActions(width, bodyHeight+2))
	return strings.Join(rows, "\n")
}

func (m *teaModel) renderEditorStatus(width int) string {
	s := m.editor
	left := s.notice
	if left == "" {
		left = s.baselineLabel()
		if s.hasHead && s.baselineError == "" {
			added, removed := 0, 0
			for _, c := range s.changes {
				added += c.end - c.start
				removed += c.oldEnd - c.oldStart
			}
			left = fmt.Sprintf("Compared with HEAD  +%d / -%d", added, removed)
		}
	}
	if reason := m.sourceBusyReason(); reason != "" {
		left = ColorYellow("Save unavailable: " + reason)
	}
	endings := "LF"
	if s.crlf {
		endings = "CRLF"
	}
	position := fmt.Sprintf("Ln %d, Col %d", s.row+1, s.displayCol()+1)
	if text := s.selectedText(); text != "" {
		position += fmt.Sprintf(" (%d selected)", len([]rune(text)))
	}
	right := fmt.Sprintf("%s  %s", position, endings)
	if width >= 75 && s.lang != "" {
		right += "  " + strings.ToUpper(s.lang)
	}
	return editorSurface(editorSplit(" "+left, right+" ", width), currentTheme.CardBg)
}
