package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"fastllm/internal/gitrepo"
)

type sourceEntry struct {
	group, path, original, badge, commit string
}

func (e sourceEntry) key() string { return e.group + "\x00" + e.path + "\x00" + e.commit }

// Navigation state belongs to the workspace, never to agent/shell execution modes.
type sourceControl struct {
	active                            bool
	root                              string
	status                            gitrepo.RepoStatus
	entries                           []sourceEntry
	cursor, offset, pane              int // sidebar, detail, commit message
	filter, message, notice, text     string
	queryMode, dialog, dialogText     string
	confirmPaths                      []string
	branches                          []gitrepo.Branch
	branchCursor                      int
	history                           []gitrepo.CommitInfo
	detail                            gitrepo.CommitDetail
	hasMore, busy, loading, unified   bool
	refreshID, selectionID, historyID uint64
	selectedKey, requestedPath        string
	cancel                            context.CancelFunc
	view                              viewport.Model
	initialized                       bool
	watchError                        string
	pendingAction                     string
	pendingIndex                      int
	requestedDiscard                  string
	dialogOffset                      int
	branchID                          uint64
	pendingKey                        string
}

type sourceStatusMsg struct {
	workspace, root string
	id              uint64
	status          gitrepo.RepoStatus
	err             error
}
type sourceDiffMsg struct {
	root, key, text string
	id              uint64
	detail          gitrepo.CommitDetail
	err             error
}
type sourceHistoryMsg struct {
	root    string
	id      uint64
	offset  int
	commits []gitrepo.CommitInfo
	err     error
}
type sourceBranchesMsg struct {
	root     string
	id       uint64
	branches []gitrepo.Branch
	err      error
}
type sourceMutationMsg struct {
	root, action, message string
	err                   error
}

type sourceEditorHeadMsg struct {
	editor *editorSession
	head   string
	err    error
}

func (m *teaModel) openSourceControl(path string) tea.Cmd {
	s := &m.source
	if !s.initialized {
		s.view = viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))
		s.initialized = true
	}
	s.active = true
	m.input.Blur()
	if path != "" {
		s.requestedPath = path
	}
	return m.refreshSourceCmd()
}

func (m *teaModel) sourceBusyReason() string {
	if m.isExecuting || m.shellExecuting {
		return "Review only: agent or foreground shell is active. Mutations and saves disabled."
	}
	if m.runner != nil && m.runner.agents != nil {
		a := m.runner.agents.Summary()
		if a.Pending+a.Running > 0 {
			return "Review only: a child agent is active. Mutations and saves disabled."
		}
	}
	if m.source.busy {
		return "Git operation in progress; mutations and saves disabled. Esc cancels."
	}
	return ""
}

func (m *teaModel) refreshSourceCmd() tea.Cmd {
	s := &m.source
	s.refreshID++
	id := s.refreshID
	workspace := m.workingDir
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		root, err := gitrepo.Root(ctx, workspace)
		if err != nil {
			return sourceStatusMsg{workspace: workspace, id: id, err: err}
		}
		status, err := gitrepo.GetRepoStatus(ctx, root)
		return sourceStatusMsg{workspace: workspace, root: root, id: id, status: status, err: err}
	}
}

func (s *sourceControl) rebuild() {
	key := s.selectedKey
	s.entries = nil
	for _, group := range []string{"Conflicts", "Staged Changes", "Changes", "Untracked Files"} {
		s.entries = append(s.entries, sourceEntry{group: group})
		for _, f := range s.status.Files {
			match := false
			badge := f.Unstaged
			switch group {
			case "Conflicts":
				match = f.Conflict
				badge = f.Staged + f.Unstaged
			case "Staged Changes":
				match = !f.Conflict && f.Staged != ""
				badge = f.Staged
			case "Changes":
				match = !f.Conflict && f.Unstaged != "" && f.Unstaged != "?"
			case "Untracked Files":
				match = f.Unstaged == "?"
			}
			if match && strings.Contains(strings.ToLower(f.Path), strings.ToLower(s.filter)) {
				s.entries = append(s.entries, sourceEntry{group: group, path: f.Path, original: f.OriginalPath, badge: badge})
			}
		}
	}
	s.entries = append(s.entries, sourceEntry{group: "History"})
	for _, c := range s.history {
		if strings.Contains(strings.ToLower(c.Subject), strings.ToLower(s.filter)) {
			s.entries = append(s.entries, sourceEntry{group: "History", path: c.Subject, commit: c.ID, badge: c.ID[:7]})
		}
	}
	if s.hasMore {
		s.entries = append(s.entries, sourceEntry{group: "History", path: "Load 50 more", badge: "+"})
	}
	s.cursor = min(s.cursor, max(0, len(s.entries)-1))
	for i, e := range s.entries {
		if e.key() == key {
			s.cursor = i
			break
		}
	}
	if s.requestedPath != "" {
		for i, e := range s.entries {
			if e.path == s.requestedPath {
				s.cursor = i
				break
			}
		}
		s.requestedPath = ""
	}
}

func (s *sourceControl) selected() sourceEntry {
	if s.cursor < 0 || s.cursor >= len(s.entries) {
		return sourceEntry{}
	}
	return s.entries[s.cursor]
}

func (m *teaModel) loadSourceSelection() tea.Cmd {
	s := &m.source
	e := s.selected()
	s.selectedKey = e.key()
	s.selectionID++
	id := s.selectionID
	root := s.root
	s.detail = gitrepo.CommitDetail{}
	if e.path == "" {
		s.text = "Select a file to review its diff."
		s.loading = false
		return nil
	}
	if e.badge == "+" {
		return m.sourceHistoryCmd(len(s.history))
	}
	s.loading = true
	s.text = "Loading diff…"
	s.view.GotoTop()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		msg := sourceDiffMsg{root: root, key: e.key(), id: id}
		if e.commit != "" {
			msg.detail, msg.err = gitrepo.Detail(ctx, root, e.commit)
			if msg.err == nil {
				var b strings.Builder
				for _, f := range msg.detail.Files {
					d, err := gitrepo.CommitDiff(ctx, root, msg.detail, f.Path)
					if err != nil {
						msg.err = err
						break
					}
					b.WriteString(d)
				}
				msg.text = b.String()
			}
		} else if e.group == "Untracked Files" {
			var data []byte
			full := filepath.Join(root, filepath.FromSlash(e.path))
			info, err := os.Lstat(full)
			if err != nil {
				msg.err = err
			} else if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(full)
				data = []byte(target)
				msg.err = err
			} else if info.Size() > maxEditorFileBytes {
				msg.err = fmt.Errorf("file exceeds review limit")
			} else {
				data, msg.err = os.ReadFile(full)
			}
			if strings.ContainsRune(string(data), 0) {
				msg.text = "Binary untracked file: " + e.path
			} else {
				msg.text = FormatUntrackedAsDiff(e.path, string(data))
			}
		} else if e.group == "Conflicts" {
			msg.text, msg.err = gitrepo.ConflictDiff(ctx, root, e.path)
		} else {
			var original []string
			if e.original != "" {
				original = append(original, e.original)
			}
			msg.text, msg.err = gitrepo.ReviewDiff(ctx, root, e.path, e.group == "Staged Changes", original...)
		}
		return msg
	}
}

func (m *teaModel) sourceHistoryCmd(offset int) tea.Cmd {
	s := &m.source
	s.historyID++
	id, root := s.historyID, s.root
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		commits, err := gitrepo.History(ctx, root, offset, 50)
		return sourceHistoryMsg{root: root, id: id, offset: offset, commits: commits, err: err}
	}
}

func (m *teaModel) sourceBranchesCmd() tea.Cmd {
	if m.editor != nil && m.editor.dirty {
		m.source.pendingAction = "branch"
		m.editor.prompt = editorPromptClose
		return nil
	}
	root := m.source.root
	m.source.branchID++
	id := m.source.branchID
	m.source.dialog = "branches"
	m.source.dialogText = ""
	m.source.branches = nil
	m.source.branchCursor = 0
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		b, err := gitrepo.Branches(ctx, root)
		return sourceBranchesMsg{root: root, id: id, branches: b, err: err}
	}
}

func (m *teaModel) handleSourceResult(msg tea.Msg) (bool, tea.Cmd) {
	s := &m.source
	switch msg := msg.(type) {
	case sourceEditorHeadMsg:
		if msg.editor == m.editor && msg.err == nil {
			m.editor.hasHead = true
			if msg.head != "" {
				text, _, _ := splitFileContent(msg.head)
				m.editor.head = strings.Split(text, "\n")
			}
			m.editor.marksStale = true
		}
		return true, nil
	case sourceStatusMsg:
		if msg.workspace != m.workingDir || msg.id != s.refreshID {
			return true, nil
		}
		if msg.err != nil {
			s.notice = msg.err.Error()
			if msg.root == "" {
				s.root = ""
				s.status = gitrepo.RepoStatus{}
				s.entries = nil
			}
			return true, nil
		}
		changed := s.root != msg.root || s.status.Branch != msg.status.Branch || s.status.LatestCommit != msg.status.LatestCommit
		s.root = msg.root
		s.status = msg.status
		m.gitRoot = msg.root
		m.changes.UpdateFromGit(msg.status)
		s.rebuild()
		if s.requestedDiscard != "" {
			arg := s.requestedDiscard
			s.requestedDiscard = ""
			for i, e := range s.entries {
				if (e.group == "Changes" && (arg == "all" || arg == "." || e.path == arg)) || (e.group == "Untracked Files" && e.path == arg) {
					s.cursor = i
					s.pane = 0
					m.sourceDiscard(arg == "all" || arg == ".")
					return true, nil
				}
			}
			s.notice = "No unstaged changes match " + arg
			return true, nil
		}
		cmd := m.loadSourceSelection()
		if changed || len(s.history) == 0 {
			s.history = nil
			return true, tea.Batch(cmd, m.sourceHistoryCmd(0))
		}
		return true, cmd
	case sourceDiffMsg:
		if msg.root != s.root || msg.id != s.selectionID || msg.key != s.selectedKey {
			return true, nil
		}
		s.loading = false
		s.text = msg.text
		s.detail = msg.detail
		if msg.err != nil {
			s.text = "Diff error: " + msg.err.Error()
		}
		if s.text == "" {
			s.text = "No changes in this comparison."
		}
		return true, nil
	case sourceHistoryMsg:
		if msg.root != s.root || msg.id != s.historyID {
			return true, nil
		}
		if msg.err != nil {
			s.notice = msg.err.Error()
			return true, nil
		}
		if msg.offset == 0 {
			s.history = nil
		}
		s.history = append(s.history, msg.commits...)
		s.hasMore = len(msg.commits) == 50
		s.rebuild()
		return true, nil
	case sourceBranchesMsg:
		if msg.root != s.root || msg.id != s.branchID || s.dialog != "branches" {
			return true, nil
		}
		s.branches = msg.branches
		if msg.err != nil {
			s.notice = msg.err.Error()
		}
		return true, nil
	case sourceMutationMsg:
		if msg.root != s.root {
			return true, nil
		}
		s.busy = false
		s.cancel = nil
		if msg.err != nil {
			s.notice = msg.action + " failed: " + msg.err.Error()
			s.dialog = "operation error"
			s.dialogText = s.notice
			s.dialogOffset = 0
			return true, m.refreshSourceAfterFailure()
		}
		if msg.action == "Commit" && s.message == msg.message {
			s.message = ""
		}
		s.notice = msg.action + " completed."
		return true, tea.Batch(m.refreshSourceCmd(), m.refreshGitStatusCmd())
	}
	return false, nil
}

// Refresh after failures without erasing Git's error (including hook output).
func (m *teaModel) refreshSourceAfterFailure() tea.Cmd {
	return tea.Batch(m.refreshGitStatusCmd(), m.refreshSourceCmd())
}

func (m *teaModel) mutateSource(action string, paths []string, value string) tea.Cmd {
	s := &m.source
	if reason := m.sourceBusyReason(); reason != "" {
		s.notice = reason
		return nil
	}
	if s.root == "" {
		s.notice = "Not a Git repository."
		return nil
	}
	if m.editor != nil && m.editor.dirty {
		s.notice = "Save, discard, or cancel the unsaved editor buffer first."
		return nil
	}
	if (action == "Switch branch" || action == "Create branch") && m.editor != nil {
		m.closeEditor()
	}
	if action == "Commit" {
		if strings.TrimSpace(value) == "" {
			s.notice = "A commit message is required."
			return nil
		}
		staged := false
		for _, f := range s.status.Files {
			staged = staged || (!f.Conflict && f.Staged != "")
		}
		if !staged {
			s.notice = "Stage changes before committing."
			return nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.busy = true
	s.notice = action + " in progress…"
	root := s.root
	paths = append([]string(nil), paths...)
	return func() tea.Msg {
		defer cancel()
		var err error
		switch action {
		case "Stage":
			err = gitrepo.Stage(ctx, root, paths)
		case "Unstage":
			err = gitrepo.Unstage(ctx, root, paths)
		case "Discard":
			err = gitrepo.Discard(ctx, root, paths)
		case "Delete untracked":
			err = gitrepo.DeleteUntracked(ctx, root, paths)
		case "Commit":
			err = gitrepo.Commit(ctx, root, value)
		case "Switch branch":
			err = gitrepo.SwitchBranch(ctx, root, value)
		case "Create branch":
			err = gitrepo.CreateBranch(ctx, root, value)
		}
		return sourceMutationMsg{root: root, action: action, message: value, err: err}
	}
}

func (m *teaModel) sourcePaths(all bool) []string {
	s := &m.source
	e := s.selected()
	var paths []string
	if e.group == "History" {
		return nil
	}
	for _, entry := range s.entries {
		if entry.group == e.group && entry.path != "" && (all || e.path == "" || entry.path == e.path) {
			paths = append(paths, entry.path)
			if entry.original != "" && e.group == "Staged Changes" {
				paths = append(paths, entry.original)
			}
		}
	}
	return paths
}

func (m *teaModel) sourceDiscard(all bool) {
	s := &m.source
	e := s.selected()
	if reason := m.sourceBusyReason(); reason != "" {
		s.notice = reason
		return
	}
	if e.group == "Conflicts" || e.group == "Staged Changes" || e.group == "History" {
		s.notice = "Discard is available only for unstaged changes or untracked files."
		return
	}
	if all {
		for _, f := range s.status.Files {
			if f.Conflict {
				s.notice = "Bulk discard disabled while conflicts are unresolved."
				return
			}
		}
	}
	paths := m.sourcePaths(all)
	if len(paths) == 0 {
		return
	}
	s.confirmPaths = paths
	s.dialogOffset = 0
	s.dialog = "Discard"
	if e.group == "Untracked Files" {
		s.dialog = "Delete untracked"
	}
}

func (s *sourceControl) filteredBranches() []gitrepo.Branch {
	var result []gitrepo.Branch
	for _, b := range s.branches {
		if strings.Contains(strings.ToLower(b.Name), strings.ToLower(s.dialogText)) {
			result = append(result, b)
		}
	}
	return result
}

func trimLastRune(text string) string {
	r := []rune(text)
	if len(r) > 0 {
		r = r[:len(r)-1]
	}
	return string(r)
}

func sourceLabel(text string) string {
	visible, _ := revealHidden(text)
	return strings.NewReplacer("\n", "⟨LF⟩", "\t", "⟨TAB⟩").Replace(visible)
}

func (m *teaModel) handleSourceKey(msg tea.KeyPressMsg) tea.Cmd {
	s := &m.source
	key := msg.String()
	if s.dialog != "" {
		if s.dialog == "operation error" {
			if key == "esc" || key == "enter" || key == "ctrl+c" {
				s.dialog = ""
			} else if key == "down" || key == "pgdown" {
				s.dialogOffset++
			} else if key == "up" || key == "pgup" {
				s.dialogOffset = max(0, s.dialogOffset-1)
			}
			return nil
		}
		if key == "esc" || key == "ctrl+c" {
			s.dialog = ""
			s.confirmPaths = nil
			return nil
		}
		if s.dialog == "Discard" || s.dialog == "Delete untracked" {
			if key == "down" || key == "pgdown" {
				s.dialogOffset++
			}
			if key == "up" || key == "pgup" {
				s.dialogOffset = max(0, s.dialogOffset-1)
			}
			if key == "y" {
				action, paths := s.dialog, s.confirmPaths
				s.dialog = ""
				s.confirmPaths = nil
				return m.mutateSource(action, paths, "")
			}
			if key == "n" {
				s.dialog = ""
				s.confirmPaths = nil
			}
			return nil
		}
		switch key {
		case "ctrl+n":
			s.dialog = "create branch"
			s.dialogText = ""
		case "up":
			s.branchCursor = max(0, s.branchCursor-1)
		case "down":
			s.branchCursor++
		case "backspace":
			s.dialogText = trimLastRune(s.dialogText)
			s.branchCursor = 0
		case "enter":
			if s.dialog == "create branch" {
				name := s.dialogText
				s.dialog = ""
				return m.mutateSource("Create branch", nil, name)
			}
			branches := s.filteredBranches()
			if len(branches) > 0 {
				b := branches[min(s.branchCursor, len(branches)-1)]
				s.dialog = ""
				return m.mutateSource("Switch branch", nil, b.Name)
			}
		default:
			if !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) {
				s.dialogText += printableRunes([]rune(msg.Text))
				s.branchCursor = 0
			}
		}
		return nil
	}
	if s.queryMode != "" {
		switch key {
		case "esc", "enter":
			s.queryMode = ""
			return m.loadSourceSelection()
		case "backspace":
			s.filter = trimLastRune(s.filter)
			s.rebuild()
		default:
			if !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) {
				s.filter += printableRunes([]rune(msg.Text))
				s.rebuild()
			}
		}
		return nil
	}
	if key == "esc" {
		if s.busy && s.cancel != nil {
			s.cancel()
			return nil
		}
		if m.isExecuting || m.shellExecuting {
			m.cancelActiveOperation()
			return nil
		}
		s.pane = 0
		return nil
	}
	if handled, cmd := m.handleCommonKey(msg); handled {
		return cmd
	}
	if key == "ctrl+q" {
		if m.editor != nil {
			m.sourceExitEditor = true
			return m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEscape})
		}
		m.leaveSource()
		return nil
	}
	if key == "tab" {
		s.pane = (s.pane + 1) % 3
		return nil
	}
	if key == "shift+tab" {
		s.pane = (s.pane + 2) % 3
		return nil
	}
	if s.pane == 2 {
		if key == "ctrl+enter" {
			return m.mutateSource("Commit", nil, s.message)
		}
		if key == "backspace" {
			s.message = trimLastRune(s.message)
		} else if key == "enter" {
			s.message += "\n"
		} else if !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) {
			s.message += printableRunes([]rune(msg.Text))
		}
		return nil
	}
	switch key {
	case "q":
		if m.editor != nil {
			m.sourceExitEditor = true
			return m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEscape})
		}
		m.leaveSource()
	case "/":
		s.queryMode = "filter"
	case "r":
		return m.refreshSourceCmd()
	case "b":
		return m.sourceBranchesCmd()
	case "c":
		s.pane = 2
	case "ctrl+enter":
		return m.mutateSource("Commit", nil, s.message)
	case "s", "S":
		if s.selected().group != "History" && s.selected().group != "Staged Changes" {
			return m.mutateSource("Stage", m.sourcePaths(key == "S"), "")
		}
	case "u", "U":
		if s.selected().group == "Staged Changes" {
			return m.mutateSource("Unstage", m.sourcePaths(key == "U"), "")
		}
	case "x", "X":
		m.sourceDiscard(key == "X")
	case "e":
		return m.openSourceEditor()
	case "v":
		s.unified = !s.unified
	case "left":
		s.pane = 0
	case "right", "enter":
		s.pane = 1
		return m.loadSourceSelection()
	case "up", "down", "home", "end":
		if s.pane == 0 {
			if m.editor != nil && m.editor.dirty {
				m.editor.prompt = editorPromptClose
				return nil
			}
			if m.editor != nil {
				m.closeEditor()
			}
			switch key {
			case "up":
				s.cursor = max(0, s.cursor-1)
			case "down":
				s.cursor = min(len(s.entries)-1, s.cursor+1)
			case "home":
				s.cursor = 0
			case "end":
				s.cursor = max(0, len(s.entries)-1)
			}
			return m.loadSourceSelection()
		}
		s.view, _ = s.view.Update(msg)
	case "pgup":
		s.view.ScrollUp(10)
	case "pgdown":
		s.view.ScrollDown(10)
	}
	return nil
}

func (m *teaModel) leaveSource() {
	m.source.active = false
	m.input.Focus()
}

func (m *teaModel) openSourceEditor() tea.Cmd {
	s := &m.source
	e := s.selected()
	if e.path == "" || e.group == "History" {
		s.notice = "History is read-only."
		return nil
	}
	if e.badge == "D" {
		s.notice = "This file was deleted."
		return nil
	}
	if err := m.openEditor(e.path, false); err != nil {
		s.notice = "Cannot open file: " + err.Error()
		return nil
	}
	s.pane = 1
	editor, root, path := m.editor, s.root, e.path
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		head, err := gitrepo.ShowHEAD(ctx, root, path)
		return sourceEditorHeadMsg{editor, head, err}
	}
}

func (m *teaModel) sourceGeometry() (sidebar, detail, height int) {
	width := max(1, m.width)
	height = max(1, m.height-5)
	if width < 90 {
		return width, width, height
	}
	sidebar = min(36, width/3)
	return sidebar, width - sidebar - 1, height
}

func sourceRows(rows []string, width, height int) string {
	var out []string
	for i := 0; i < height; i++ {
		row := ""
		if i < len(rows) {
			row = rows[i]
		}
		out = append(out, PadRight(clampToWidth(row, width), width))
	}
	return strings.Join(out, "\n")
}

func (m *teaModel) renderSource() string {
	s := &m.source
	sidebar, detail, height := m.sourceGeometry()
	m.inputOrigin = nil
	count := len(s.status.Files)
	labels := []string{"Chat [q]", fmt.Sprintf("Source Control (%d)", count), "Back [←]", "Refresh [r]", "Branch [b]"}
	ids := []string{"source:chat", "", "source:back", "source:refresh", "source:branch"}
	x := 0
	for i, label := range labels {
		if ids[i] != "" {
			m.hits.add(ids[i], x, 0, VisualLen(label), 1)
		}
		x += VisualLen(label) + 2
	}
	nav := strings.Join(labels, "  ")
	rows := []string{styleDiffHdr.Render(sanitizeUntrusted(filepath.Base(s.root)) + " · " + sanitizeUntrusted(s.status.Branch)), styleMuted.Render("Filter [/]: " + sanitizeUntrusted(s.filter))}
	if s.root == "" {
		rows = []string{"Not a Git repository.", "Open an existing repository.", "Refresh [r] · Chat [q]"}
	}
	available := max(1, height-2)
	s.offset = min(s.offset, max(0, len(s.entries)-available))
	if s.cursor < s.offset {
		s.offset = s.cursor
	}
	if s.cursor >= s.offset+available {
		s.offset = s.cursor - available + 1
	}
	for i := s.offset; i < len(s.entries) && len(rows) < height; i++ {
		e := s.entries[i]
		label := e.group
		if e.path != "" {
			label = "  " + e.badge + " " + sourceLabel(e.path)
		} else {
			n := 0
			for _, f := range s.entries {
				if f.group == e.group && f.path != "" {
					n++
				}
			}
			label = fmt.Sprintf("%s (%d)", e.group, n)
		}
		if i == s.cursor {
			label = styleDiffHdr.Render("› " + label)
		} else {
			label = "  " + label
		}
		if m.width >= 90 || s.pane == 0 {
			m.hits.add("source:entry:"+strconv.Itoa(i), 0, 2+len(rows), sidebar, 1)
		}
		rows = append(rows, label)
	}
	content := s.text
	if s.root == "" {
		content = "Not a Git repository. Open fastllm inside an existing repository.\nRefresh [r] · Chat [q]"
	}
	files := ParseDiffForDisplay(strings.TrimRight(content, "\r\n"))
	s.view.SetWidth(detail)
	s.view.SetHeight(height)
	var diffRows []string
	if s.detail.ID != "" {
		diffRows = append(diffRows, styleDiffHdr.Render(s.detail.ID), styleMuted.Render(sanitizeUntrusted(s.detail.Author+" · "+s.detail.Timestamp)), sanitizeUntrusted(s.detail.Message))
		for _, f := range s.detail.Files {
			diffRows = append(diffRows, sourceLabel(f.Staged+" "+f.Path))
		}
	}
	if len(files) > 0 && files[0].MaxLine > 0 {
		for _, f := range files {
			filePath := f.NewPath
			if filePath == "/dev/null" {
				filePath = f.OldPath
			}
			label := sourceLabel(filePath)
			diffRows = append(diffRows, styleDiffHdr.Render(label))
			if detail >= 100 && !s.unified {
				before, after := "Index", "Working tree"
				if s.selected().group == "Conflicts" {
					before = "Ours (stage 2)"
				}
				if s.selected().group == "Staged Changes" {
					before, after = "HEAD", "Index"
				}
				if s.detail.ID != "" {
					before, after = "First parent", "Commit"
					if s.detail.Parent == "" {
						before = "Empty tree"
					}
				}
				diffRows = append(diffRows, PadRight(before, detail/2)+after)
				diffRows = append(diffRows, RenderSplitDiff(f, DetectLanguage(f.NewPath), detail)...)
			} else {
				var b strings.Builder
				renderFileDiff(&b, f, label, DetectLanguage(filePath), DefaultDiffOptions())
				diffRows = append(diffRows, b.String())
			}
		}
		content = strings.Join(diffRows, "\n")
	} else {
		content = strings.Join(diffRows, "\n") + "\n" + styleMuted.Render(sanitizeUntrusted(content))
	}
	if m.editor != nil {
		content = m.renderEditor()
	} else {
		offset := s.view.YOffset()
		s.view.SetContent(content)
		s.view.SetYOffset(offset)
		content = s.view.View()
	}
	var body string
	if m.width < 90 {
		if s.pane == 0 {
			body = sourceRows(rows, sidebar, height)
		} else {
			body = sourceRows(strings.Split(content, "\n"), detail, height)
		}
	} else {
		body = lipgloss.JoinHorizontal(lipgloss.Top, sourceRows(rows, sidebar, height), "│", sourceRows(strings.Split(content, "\n"), detail, height))
	}
	commitLabel := "Commit Staged Changes [Ctrl+Enter]"
	messageWidth := max(1, m.width-VisualLen(commitLabel)-2)
	message := PadRight(clampToWidth("Message [c]: "+sanitizeUntrusted(strings.ReplaceAll(s.message, "\n", " ↵ ")), messageWidth), messageWidth) + "  " + commitLabel
	m.hits.add("source:message", 0, 2+height, messageWidth, 1)
	m.hits.add("source:commit", messageWidth+2, 2+height, VisualLen(commitLabel), 1)
	notice := s.notice
	if notice == "" && s.watchError != "" {
		notice = s.watchError
	}
	if reason := m.sourceBusyReason(); reason != "" {
		notice = reason
	}
	if notice == "" {
		notice = "Stage [s/S all] · Unstage [u/U all] · Discard Changes [x/X all] · Open File [e] · Tab panes"
	}
	actions := []string{"Stage [s]", "Unstage [u]", "Discard [x]", "Open File [e]", "All: S/U/X"}
	actionIDs := []string{"source:stage", "source:unstage", "source:discard", "source:edit", ""}
	x = 0
	for i, label := range actions {
		if actionIDs[i] != "" {
			m.hits.add(actionIDs[i], x, m.height-1, VisualLen(label), 1)
		}
		x += VisualLen(label) + 2
	}
	focus := []string{"Sidebar", "Detail", "Commit message"}[s.pane]
	frame := m.fitFrame(nav + "\n" + styleMuted.Render("Focus: "+focus+" · Tab/Shift+Tab panes · ↑/↓ select · Enter diff · v unified/split · Esc Back/cancel") + "\n" + body + "\n" + message + "\n" + sanitizeUntrusted(notice) + "\n" + strings.Join(actions, "  "))
	if s.dialog != "" {
		lines := []string{styleDiffHdr.Render(s.dialog)}
		branchRows := map[int]int{}
		if s.dialog == "operation error" {
			parts := wrapRunes(sanitizeUntrusted(s.dialogText), max(1, m.width-10))
			available := max(1, m.height-8)
			s.dialogOffset = min(s.dialogOffset, max(0, len(parts)-available))
			lines = append(lines, parts[s.dialogOffset:min(len(parts), s.dialogOffset+available)]...)
			lines = append(lines, "↑/↓ scroll · Enter/Esc close")
		} else if s.dialog == "Discard" || s.dialog == "Delete untracked" {
			lines = append(lines, "Confirm affected files:")
			available := max(1, m.height-9)
			s.dialogOffset = min(s.dialogOffset, max(0, len(s.confirmPaths)-available))
			for _, p := range s.confirmPaths[s.dialogOffset:min(len(s.confirmPaths), s.dialogOffset+available)] {
				lines = append(lines, sourceLabel(p))
			}
			lines = append(lines, fmt.Sprintf("%d affected files · ↑/↓ scroll", len(s.confirmPaths)), "[y] confirm · [n]/Esc cancel")
		} else {
			lines = append(lines, "Filter/name: "+sanitizeUntrusted(s.dialogText))
			branches := s.filteredBranches()
			selected := min(s.branchCursor, max(0, len(branches)-1))
			start := max(0, selected-5)
			for i := start; i < min(len(branches), start+10); i++ {
				branchRows[len(lines)] = i
				prefix := "  "
				if i == selected {
					prefix = "› "
				}
				lines = append(lines, prefix+sanitizeUntrusted(branches[i].Name))
			}
			lines = append(lines, "Enter select · Ctrl+N create from HEAD · Esc cancel")
		}
		box := lipgloss.NewStyle().Padding(1, 2).Background(tuiColorCardBg).Render(strings.Join(lines, "\n"))
		box = lipgloss.NewStyle().MaxWidth(m.frameWidth()).MaxHeight(max(1, m.height)).Render(box)
		w, h := lipgloss.Width(box), lipgloss.Height(box)
		x, y := max((max(m.width, lipgloss.Width(frame))-w)/2, 0), max((max(m.height, lipgloss.Height(frame))-h)/2, 0)
		frame = m.overlayModal(frame, box)
		for row, index := range branchRows {
			if s.dialog == "branches" {
				m.hits.add("source:branch-row:"+strconv.Itoa(index), x+2, y+1+row, w-4, 1)
			}
		}
		if s.dialog == "Discard" || s.dialog == "Delete untracked" {
			m.hits.add("source:confirm", x+2, y+h-2, 12, 1)
			m.hits.add("source:cancel", x+16, y+h-2, w-18, 1)
		} else {
			m.hits.add("source:cancel", x+2, y+h-2, w-4, 1)
		}
		return frame
	}
	return frame
}

func (m *teaModel) handleSourceMouse(msg tea.MouseMsg) tea.Cmd {
	s := &m.source
	mouse := msg.Mouse()
	if s.dialog != "" {
		if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft {
			id := m.hits.at(mouse.X, mouse.Y)
			if id == "source:confirm" {
				return m.handleSourceKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
			}
			if id == "source:cancel" || id == "" {
				return m.handleSourceKey(tea.KeyPressMsg{Code: tea.KeyEscape})
			}
			if strings.HasPrefix(id, "source:branch-row:") {
				index, err := strconv.Atoi(strings.TrimPrefix(id, "source:branch-row:"))
				if err == nil {
					s.branchCursor = index
					return m.handleSourceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
				}
			}
		}
		return nil
	}
	if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft {
		id := m.hits.at(mouse.X, mouse.Y)
		switch {
		case id == "source:chat":
			if m.editor != nil {
				m.sourceExitEditor = true
				return m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEscape})
			}
			m.leaveSource()
		case id == "source:back":
			s.pane = 0
		case id == "source:refresh":
			return m.refreshSourceCmd()
		case id == "source:branch":
			return m.sourceBranchesCmd()
		case id == "source:message":
			s.pane = 2
		case id == "source:commit":
			return m.mutateSource("Commit", nil, s.message)
		case id == "source:stage":
			if s.selected().group != "History" && s.selected().group != "Staged Changes" {
				return m.mutateSource("Stage", m.sourcePaths(false), "")
			}
		case id == "source:unstage":
			if s.selected().group == "Staged Changes" {
				return m.mutateSource("Unstage", m.sourcePaths(false), "")
			}
		case id == "source:discard":
			m.sourceDiscard(false)
		case id == "source:edit":
			return m.openSourceEditor()
		case strings.HasPrefix(id, "source:entry:"):
			if m.editor != nil {
				if m.editor.dirty {
					s.pendingAction = "selection"
					s.pendingIndex, _ = strconv.Atoi(strings.TrimPrefix(id, "source:entry:"))
					if s.pendingIndex >= 0 && s.pendingIndex < len(s.entries) {
						s.pendingKey = s.entries[s.pendingIndex].key()
					}
					m.editor.prompt = editorPromptClose
					return nil
				}
				m.closeEditor()
			}
			i, err := strconv.Atoi(strings.TrimPrefix(id, "source:entry:"))
			if err == nil {
				s.cursor = i
				s.pane = 1
				return m.loadSourceSelection()
			}
		default:
			sidebar, _, height := m.sourceGeometry()
			if mouse.Y >= 2 && mouse.Y < 2+height && (m.width < 90 || mouse.X > sidebar) {
				s.pane = 1
			}
			if m.editor != nil {
				sidebar, _, _ := m.sourceGeometry()
				x := 0
				if m.width >= 90 {
					x = sidebar + 1
				}
				m.handleSourceEditorMouse(msg, x)
			}
		}
	} else if _, ok := msg.(tea.MouseWheelMsg); ok {
		sidebar, _, _ := m.sourceGeometry()
		if (m.width < 90 && s.pane == 0) || (m.width >= 90 && mouse.X < sidebar) {
			step := 3
			if mouse.Button == tea.MouseWheelUp {
				step = -3
			}
			s.cursor = min(max(0, s.cursor+step), max(0, len(s.entries)-1))
			return m.loadSourceSelection()
		}
		if m.editor != nil {
			x := 0
			if m.width >= 90 {
				x = sidebar + 1
			}
			m.handleSourceEditorMouse(msg, x)
		} else {
			s.view, _ = s.view.Update(msg)
		}
	} else if m.editor != nil {
		sidebar, _, _ := m.sourceGeometry()
		x := 0
		if m.width >= 90 {
			x = sidebar + 1
		}
		m.handleSourceEditorMouse(msg, x)
	}
	return nil
}

func (m *teaModel) handleSourceEditorMouse(msg tea.MouseMsg, x int) {
	mouse := msg.Mouse()
	mouse.X -= x
	mouse.Y -= 2
	switch msg.(type) {
	case tea.MouseClickMsg:
		m.handleEditorMouse(tea.MouseClickMsg(mouse))
	case tea.MouseMotionMsg:
		m.handleEditorMouse(tea.MouseMotionMsg(mouse))
	case tea.MouseReleaseMsg:
		m.handleEditorMouse(tea.MouseReleaseMsg(mouse))
	case tea.MouseWheelMsg:
		m.handleEditorMouse(tea.MouseWheelMsg(mouse))
	}
}
