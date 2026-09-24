package harness

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// sessionsPicker is the state of the /sessions menu. Printable keys always go
// to the filter (or the rename buffer), so actions sit on non-character keys
// (Delete, F2) where a typed letter can never trigger them. Ctrl+D/Ctrl+R are
// aliases, but Windows terminals do not always deliver Ctrl+letter chords.
type sessionsPicker struct {
	all           []InteractiveSession
	visible       []int // indexes into all, after filtering
	cursor        int   // index into visible
	filter        string
	hereOnly      bool
	confirmDelete bool
	renaming      bool
	renameBuf     string
	notice        string
}

func (m *teaModel) openSessionsModal() {
	if m.isExecuting || m.shellExecuting {
		m.statusNotice = "Finish or cancel the current run first."
		return
	}
	if m.sessionStore == nil {
		m.appendHistory(styleDiffDel.Render("Session persistence is unavailable.\n\n"))
		return
	}
	picker := &sessionsPicker{}
	if err := m.reloadSessionsPicker(picker); err != nil {
		m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Cannot list sessions: %v\n\n", err)))
		return
	}
	for i, idx := range picker.visible {
		if picker.all[idx].ID == m.activeSessionID() {
			picker.cursor = i
			break
		}
	}
	m.sessionsModal = picker
	m.input.Blur()
}

func (m *teaModel) closeSessionsModal() {
	m.sessionsModal = nil
	m.input.Focus()
}

// reloadSessionsPicker re-reads the store and orders sessions from the current
// directory first, each group keeping List's most-recent-first order.
func (m *teaModel) reloadSessionsPicker(p *sessionsPicker) error {
	sessions, err := m.sessionStore.List()
	if err != nil {
		return err
	}
	here, elsewhere := []InteractiveSession{}, []InteractiveSession{}
	for _, session := range sessions {
		if samePath(session.WorkingDir, m.workingDir) {
			here = append(here, session)
		} else {
			elsewhere = append(elsewhere, session)
		}
	}
	p.all = append(here, elsewhere...)
	m.refilterSessions(p)
	return nil
}

func (m *teaModel) refilterSessions(p *sessionsPicker) {
	needle := strings.ToLower(strings.TrimSpace(p.filter))
	p.visible = p.visible[:0]
	for i, session := range p.all {
		if p.hereOnly && !samePath(session.WorkingDir, m.workingDir) {
			continue
		}
		if needle != "" {
			haystack := strings.ToLower(session.Title + "\n" + session.ID + "\n" + session.WorkingDir)
			if !strings.Contains(haystack, needle) {
				continue
			}
		}
		p.visible = append(p.visible, i)
	}
	if p.cursor >= len(p.visible) {
		p.cursor = len(p.visible) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// highlighted returns the session under the cursor, or nil when none match.
func (p *sessionsPicker) highlighted() *InteractiveSession {
	if p.cursor < 0 || p.cursor >= len(p.visible) {
		return nil
	}
	return &p.all[p.visible[p.cursor]]
}

func (m *teaModel) handleSessionsModalKey(msg tea.KeyMsg) tea.Cmd {
	p := m.sessionsModal
	switch {
	case p.confirmDelete:
		return m.handleSessionsDeleteKey(msg)
	case p.renaming:
		return m.handleSessionsRenameKey(msg)
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		m.closeSessionsModal()
	case tea.KeyEsc:
		if p.filter != "" {
			p.filter = ""
			m.refilterSessions(p)
		} else {
			m.closeSessionsModal()
		}
	case tea.KeyEnter:
		target := p.highlighted()
		m.closeSessionsModal()
		if target == nil || target.ID == m.activeSessionID() {
			return nil
		}
		// List returns full sessions, but reload so the resumed copy is
		// exactly what is on disk now.
		loaded, err := m.sessionStore.Load(target.ID)
		if err != nil {
			m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Cannot resume session: %v\n\n", err)))
			return nil
		}
		return m.resumeSession(loaded)
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
	case tea.KeyDown:
		if p.cursor < len(p.visible)-1 {
			p.cursor++
		}
	case tea.KeyHome:
		p.cursor = 0
	case tea.KeyEnd:
		p.cursor = len(p.visible) - 1
		if p.cursor < 0 {
			p.cursor = 0
		}
	case tea.KeyTab:
		p.hereOnly = !p.hereOnly
		m.refilterSessions(p)
	case tea.KeyDelete, tea.KeyCtrlD:
		target := p.highlighted()
		switch {
		case target == nil:
		case target.ID == m.activeSessionID():
			p.notice = "Start or resume another session before deleting the active session."
		default:
			p.notice = ""
			p.confirmDelete = true
		}
	case tea.KeyF2, tea.KeyCtrlR:
		if target := p.highlighted(); target != nil {
			p.notice = ""
			p.renaming = true
			p.renameBuf = target.Title
		}
	case tea.KeyBackspace:
		if runes := []rune(p.filter); len(runes) > 0 {
			p.filter = string(runes[:len(runes)-1])
			m.refilterSessions(p)
		}
	case tea.KeySpace:
		p.filter += " "
		m.refilterSessions(p)
	case tea.KeyRunes:
		p.filter += printableRunes(msg.Runes)
		m.refilterSessions(p)
	}
	return nil
}

func (m *teaModel) handleSessionsDeleteKey(msg tea.KeyMsg) tea.Cmd {
	p := m.sessionsModal
	confirmed := msg.Type == tea.KeyEnter ||
		(msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && (msg.Runes[0] == 'y' || msg.Runes[0] == 'Y'))
	cancelled := msg.Type == tea.KeyEsc || msg.Type == tea.KeyCtrlC ||
		(msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && (msg.Runes[0] == 'n' || msg.Runes[0] == 'N'))
	if !confirmed && !cancelled {
		return nil
	}
	p.confirmDelete = false
	if cancelled {
		return nil
	}
	target := p.highlighted()
	if target == nil {
		return nil
	}
	title := target.Title
	if err := m.sessionStore.Delete(target.ID); err != nil {
		p.notice = "Delete failed: " + err.Error()
		return nil
	}
	if err := m.reloadSessionsPicker(p); err != nil {
		p.notice = "Cannot list sessions: " + err.Error()
		return nil
	}
	p.notice = "Deleted " + strconv.Quote(title) + "."
	return nil
}

func (m *teaModel) handleSessionsRenameKey(msg tea.KeyMsg) tea.Cmd {
	p := m.sessionsModal
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		p.renaming = false
	case tea.KeyBackspace:
		if runes := []rune(p.renameBuf); len(runes) > 0 {
			p.renameBuf = string(runes[:len(runes)-1])
		}
	case tea.KeySpace:
		p.renameBuf += " "
	case tea.KeyRunes:
		p.renameBuf += printableRunes(msg.Runes)
	case tea.KeyEnter:
		target := p.highlighted()
		title := strings.TrimSpace(p.renameBuf)
		if target == nil || title == "" {
			p.notice = "Session title cannot be empty."
			return nil
		}
		p.renaming = false
		// The active session is autosaved from memory, so rename it there;
		// writing only the file would be overwritten by the next save.
		if target.ID == m.activeSessionID() {
			m.activeSession.Title = title
			m.activeSession.CustomTitle = true
			if err := m.saveSession(); err != nil {
				p.notice = "Rename failed: " + err.Error()
				return nil
			}
		} else if _, err := m.sessionStore.Rename(target.ID, title); err != nil {
			p.notice = "Rename failed: " + err.Error()
			return nil
		}
		target.Title = title
		target.CustomTitle = true
		p.notice = "Renamed to " + strconv.Quote(title) + "."
	}
	return nil
}

func (m *teaModel) renderSessionsModal() string {
	p := m.sessionsModal
	width, height := m.width, m.height
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	contentWidth := width - 8
	if contentWidth > 110 {
		contentWidth = 110
	}
	if contentWidth < 40 {
		contentWidth = 40
	}
	rowWidth := contentWidth - 2

	scope := "all dirs"
	if p.hereOnly {
		scope = "this dir"
	}
	header := fmt.Sprintf("SESSIONS · %d saved · %s", len(p.all), scope)
	if len(p.visible) != len(p.all) {
		header = fmt.Sprintf("SESSIONS · %d of %d · %s", len(p.visible), len(p.all), scope)
	}
	lines := []string{styleAgentBadge.Render(header)}

	caret := ColorCyan("▏")
	if p.renaming {
		lines = append(lines, clampToWidth(ColorYellow("rename: ")+p.renameBuf+caret, rowWidth))
	} else if p.filter != "" {
		lines = append(lines, clampToWidth(styleMuted.Render("filter: ")+p.filter+caret, rowWidth))
	} else {
		lines = append(lines, styleMuted.Render("type to filter"))
	}
	lines = append(lines, "")

	if len(p.visible) == 0 {
		if len(p.all) == 0 {
			lines = append(lines, styleMuted.Render("No saved sessions."))
		} else {
			lines = append(lines, styleMuted.Render("No sessions match."))
		}
	} else {
		// Each entry is two lines plus a blank separator.
		visibleRows := (height - 12) / 3
		if visibleRows < 1 {
			visibleRows = 1
		}
		start := p.cursor - visibleRows/2
		if start < 0 {
			start = 0
		}
		if maxStart := len(p.visible) - visibleRows; start > maxStart && maxStart > 0 {
			start = maxStart
		}
		end := start + visibleRows
		if end > len(p.visible) {
			end = len(p.visible)
		}
		now := time.Now()
		for i := start; i < end; i++ {
			session := p.all[p.visible[i]]
			isSelected := i == p.cursor
			isActive := session.ID == m.activeSessionID()

			prefix := "  "
			if isSelected {
				prefix = ColorCyan(StyleBold("› "))
			}
			indicator := styleMuted.Render("○ ")
			if isActive {
				indicator = ColorGreen("● ")
			}
			right := styleMuted.Render(relativeAge(now, session.UpdatedAt))
			if samePath(session.WorkingDir, m.workingDir) {
				right += " " + styleStatusNotice.Render("(here)")
			}
			titleWidth := rowWidth - 4 - VisualLen(right) - 2
			title := truncateText(session.Title, titleWidth)
			titleStr := ColorBrightWhite(StyleBold(title))
			if isSelected {
				titleStr = ColorCyan(StyleBold(title))
			}
			gap := rowWidth - 4 - VisualLen(title) - VisualLen(right)
			if gap < 1 {
				gap = 1
			}
			row1 := prefix + indicator + titleStr + strings.Repeat(" ", gap) + right

			details := []string{abbreviateHome(session.WorkingDir)}
			if session.Model != "" {
				details = append(details, session.Model)
			}
			details = append(details, fmt.Sprintf("%d msgs", len(session.Messages)))
			if session.Metrics.TotalTokens > 0 {
				details = append(details, compactCount(session.Metrics.TotalTokens)+" tok")
			}
			row2 := "    " + styleMuted.Render(strings.Join(details, " · "))

			lines = append(lines, clampToWidth(row1, rowWidth), clampToWidth(row2, rowWidth))
			if i < end-1 {
				lines = append(lines, "")
			}
		}
	}

	lines = append(lines, "")
	if p.notice != "" {
		lines = append(lines, clampToWidth(ColorYellow(p.notice), rowWidth))
	}
	switch {
	case p.confirmDelete:
		title := ""
		if target := p.highlighted(); target != nil {
			title = target.Title
		}
		lines = append(lines, clampToWidth(styleDiffDel.Render("Delete "+strconv.Quote(title)+"?")+
			styleMuted.Render(" y/Enter confirm · n/Esc cancel"), rowWidth))
	case p.renaming:
		lines = append(lines, styleMuted.Render("Enter save · Esc cancel"))
	default:
		lines = append(lines,
			styleMuted.Render("↑/↓ move · Enter resume · Tab this dir/all · F2 rename"),
			styleMuted.Render("Del delete · Esc clear filter / close"))
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(tuiColorCyan).
		Background(tuiColorCardBg).
		Padding(0, 1).
		Width(contentWidth).
		Render(strings.Join(lines, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceBackground(tuiColorDarkBg))
}

// relativeAge renders how long ago t was, coarsely: "just now", "5m ago".
func relativeAge(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Local().Format("2006-01-02")
	}
}

// samePath compares directories the way the filesystem does: case-insensitive
// on Windows, and ignoring trailing separators and dot segments.
func samePath(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// printableRunes drops control characters. Some Windows terminals deliver a
// Ctrl+letter chord as a rune event carrying NUL instead of a control key,
// which would otherwise land in the filter as an invisible "space".
func printableRunes(runes []rune) string {
	var b strings.Builder
	for _, r := range runes {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
