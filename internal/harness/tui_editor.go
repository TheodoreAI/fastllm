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
	head       []string
	hasHead    bool
	marks      []byte
	marksStale bool
	prompt     editorPrompt
	notice     string
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
		head, err := gitrepo.ShowHEAD(ctx, m.editorRoot(), rel)
		cancel()
		if err == nil {
			s.hasHead = true
			if head != "" {
				headText, _, _ := splitFileContent(head)
				s.head = strings.Split(headText, "\n")
			}
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

// editorGeometry lays out the editor: a title row, the text, a status row.
func (m *teaModel) editorGeometry() (bodyHeight, gutterWidth, codeWidth int) {
	width, height := max(m.width, 20), max(m.height, 5)
	if m.source.active {
		_, width, height = m.sourceGeometry()
	}
	bodyHeight = height - 2
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
	s.freeScroll = false
	if m.editorMoveKey(key) {
		return nil
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
		}
		s.marksStale = true
	case "ctrl+y", "ctrl+shift+z":
		if !s.redoEdit() {
			s.notice = "Nothing to redo."
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
		do = func() {
			s.moveVertical(-bodyHeight)
			s.top = max(s.top-bodyHeight, 0)
		}
	case "pgdown":
		do = func() {
			s.moveVertical(bodyHeight)
			s.top = min(s.top+bodyHeight, max(len(s.lines)-bodyHeight, 0))
		}
	case "ctrl+home":
		do = func() { s.moveTo(0, 0, false) }
	case "ctrl+end":
		do = func() { s.moveTo(len(s.lines)-1, len([]rune(s.lines[len(s.lines)-1])), false) }
	default:
		return false
	}
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
	if s == nil || s.prompt != editorPromptNone {
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
	if s := m.editor; s.prompt == editorPromptNone {
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
	_, gutterWidth, _ := m.editorGeometry()
	row := min(max(s.top+y-1, 0), len(s.lines)-1)
	return editorPos{row, runeColAt(s.lines[row], s.left+max(x-gutterWidth, 0))}
}

// handleEditorMouse scrolls the view with the wheel, places the cursor on
// click (Shift+click extends the selection) and selects by dragging.
func (m *teaModel) handleEditorMouse(msg tea.MouseMsg) {
	s := m.editor
	if s.prompt != editorPromptNone {
		return
	}
	mouse := msg.Mouse()
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
		s.top = min(max(s.top+step, 0), max(len(s.lines)-bodyHeight, 0))
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft || mouse.Y < 1 || mouse.Y > bodyHeight {
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
	width := max(m.width, 20)
	if m.source.active {
		_, width, _ = m.sourceGeometry()
	}
	bodyHeight, gutterWidth, codeWidth := m.editorGeometry()
	if s.freeScroll {
		s.top = min(max(s.top, 0), max(len(s.lines)-1, 0))
	} else {
		s.scrollIntoView(bodyHeight, codeWidth)
	}
	if s.marksStale {
		if s.hasHead {
			s.marks = computeLineMarks(s.head, s.lines)
		}
		s.marksStale = false
	}
	selStart, selEnd, selected := s.selection()

	rows := make([]string, 0, bodyHeight+2)

	title := ColorCyan(StyleBold(" ✎ " + sourceLabel(s.rel)))
	if s.lang != "" {
		title += " " + styleHeaderPill.Render(strings.ToUpper(s.lang))
	}
	if s.dirty {
		title += ColorYellow("  ● modified")
	}
	rows = append(rows, PadRight(clampToWidth(title, width), width))

	numWidth := gutterWidth - 5
	for i := range bodyHeight {
		row := s.top + i
		if row >= len(s.lines) {
			rows = append(rows, PadRight(ColorBorder(strings.Repeat(" ", gutterWidth-2)+SymVLine), width))
			continue
		}
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
		if row == s.row {
			number = ColorBrightWhite(number)
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
		rows = append(rows, mark+" "+number+" "+ColorBorder(SymVLine)+" "+code)
	}

	rows = append(rows, m.renderEditorStatus(width))
	return strings.Join(rows, "\n")
}

func (m *teaModel) renderEditorStatus(width int) string {
	s := m.editor
	switch s.prompt {
	case editorPromptClose:
		return clampToWidth(ColorYellow(StyleBold(" Unsaved changes in "+sourceLabel(s.rel)+"."))+
			styleMuted.Render("  s: Save and close  •  d: Discard  •  Esc: Keep editing"), width)
	case editorPromptConflict:
		return clampToWidth(ColorRed(StyleBold(" "+sourceLabel(s.rel)+" changed on disk since it was opened."))+
			styleMuted.Render("  o: Overwrite  •  r: Reload from disk  •  Esc: Cancel"), width)
	}
	left := s.notice
	if left == "" {
		left = styleMuted.Render("^S Save  ^Z/^Y Undo/Redo  ^C/^X/^V Copy/Cut/Paste  ^A All  ^D Diff vs HEAD  Esc Close")
	}
	endings := "LF"
	if s.crlf {
		endings = "CRLF"
	}
	position := fmt.Sprintf("Ln %d, Col %d", s.row+1, s.displayCol()+1)
	if text := s.selectedText(); text != "" {
		position += fmt.Sprintf(" (%d selected)", len([]rune(text)))
	}
	right := styleMuted.Render(fmt.Sprintf("%s  %s ", position, endings))
	left = clampToWidth(" "+left, max(width-VisualLen(right)-1, 0))
	return PadRight(left, width-VisualLen(right)) + right
}

// computeLineMarks marks each line of cur against old: '+' added, '~'
// changed, '-' where old lines were removed just above, 0 unchanged. It
// matches lines by longest common subsequence after trimming the common
// head and tail; a middle too large for that is marked changed as a block.
func computeLineMarks(old, cur []string) []byte {
	marks := make([]byte, len(cur))
	prefix := 0
	for prefix < len(old) && prefix < len(cur) && old[prefix] == cur[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(old)-prefix && suffix < len(cur)-prefix && old[len(old)-1-suffix] == cur[len(cur)-1-suffix] {
		suffix++
	}
	a, b := old[prefix:len(old)-suffix], cur[prefix:len(cur)-suffix]

	markGap := func(oldGap, curStart, curEnd int) {
		for j := curStart; j < curEnd; j++ {
			if oldGap > 0 {
				marks[prefix+j] = '~'
			} else {
				marks[prefix+j] = '+'
			}
		}
		if curStart == curEnd && oldGap > 0 && len(marks) > 0 {
			marks[min(prefix+curStart, len(marks)-1)] = '-'
		}
	}

	const maxCells = 1 << 20
	if len(a)*len(b) > maxCells {
		markGap(len(a), 0, len(b))
		return marks
	}
	// lcs[i][j] is the LCS length of a[i:] and b[j:].
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
			markGap(i-gapI, gapJ, j)
			i, j = i+1, j+1
			gapI, gapJ = i, j
		case lcs[(i+1)*width+j] >= lcs[i*width+j+1]:
			i++
		default:
			j++
		}
	}
	markGap(len(a)-gapI, gapJ, len(b))
	return marks
}
