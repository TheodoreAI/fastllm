package harness

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

type chatLayout struct {
	composerWidth, composerHeight int
	composerX, composerY          int
	transcriptHeight              int
}

func (m *teaModel) showHome() bool {
	return len(m.sessionMessages) == 0 && !m.isExecuting && !m.shellExecuting &&
		m.pendingPermission == nil && m.pendingPlan == "" && m.search == nil &&
		(m.historyText.Len() == 0 || m.historyText.String() == trimPaddedTail(m.formatWelcome()))
}

func composerBackground() string {
	if currentTheme.ElementBg != "" {
		return currentTheme.ElementBg
	}
	return currentTheme.CardBg
}

// Printed text helpers reset all SGR attributes after each colored fragment.
// Restore the containing surface's background so those resets do not punch
// terminal-colored holes into flat panels.
func fillSurface(content, background string) string {
	color := termenv.TrueColor.Color(background)
	if color == nil {
		return content
	}
	bg := "\033[" + color.Sequence(true) + "m"
	return bg + strings.NewReplacer("\033[0m", "\033[0m"+bg, "\033[m", "\033[m"+bg).Replace(content) + ansiReset
}

func (m *teaModel) layout() chatLayout {
	l := chatLayout{composerWidth: max(1, m.conversationWidth()-4), composerX: 2}
	if m.showHome() {
		l.composerWidth = min(75, max(1, m.frameWidth()-4))
		l.composerX = max(0, (m.frameWidth()-l.composerWidth)/2)
	}
	l.composerHeight = m.input.Height() + 3
	if len(m.pendingAttachments) > 0 {
		l.composerHeight++
	}
	if m.pendingPermission != nil {
		l.composerHeight = min(max(4, m.height-4), len(m.permissionPanelLines(l.composerWidth-4))+2)
	} else if m.pendingPlan != "" && !m.isExecuting {
		l.composerHeight = lipgloss.Height(m.renderPlanApproval()) + 2
	} else if m.search != nil {
		l.composerHeight = max(2, m.input.Height()) + 2
	}
	l.transcriptHeight = max(0, m.height-4-l.composerHeight-m.suggestionRows())
	l.composerY = 2 + l.transcriptHeight + m.suggestionRows()
	if m.showHome() {
		l.composerY = max(3, (m.height-l.composerHeight)/2+2)
		l.composerY = min(l.composerY, max(0, m.height-l.composerHeight-2-m.suggestionRows()))
	}
	return l
}

func (m *teaModel) permissionPanelLines(width int) []string {
	request := m.pendingPermission
	summary, hidden := revealHidden(request.Summary)
	width = max(1, width)
	lines := strings.Split(ansi.Wrap("Permission required · "+sanitizeUntrusted(request.ToolName), width, ""), "\n")
	for i, line := range lines {
		lines[i] = ColorYellow(line)
	}
	lines = append(lines, strings.Split(ansi.Wrap(summary, width, ""), "\n")...)
	if hidden {
		lines = append(lines, wrapRunes("Hidden/control characters are shown as ⟨…⟩. Approval runs the raw text. Deny unless expected.", max(1, width))...)
	}
	lines = append(lines, wrapRunes("[y] once · [n]/Enter deny · Esc deny and cancel turn", max(1, width))...)
	lines = append(lines, wrapRunes("[a] allow "+sanitizeUntrusted(request.scope().Describe())+" this session", max(1, width))...)
	return lines
}

func (m *teaModel) composerContent(l chatLayout) (string, bool) {
	if m.pendingPermission != nil {
		lines := m.permissionPanelLines(l.composerWidth - 4)
		available := max(1, l.composerHeight-2)
		if len(lines) > available {
			available = max(1, available-1)
			m.approvalOffset = min(max(0, m.approvalOffset), max(0, len(lines)-available))
			end := min(len(lines), m.approvalOffset+available)
			return strings.Join(append(lines[m.approvalOffset:end], styleMuted.Render(fmt.Sprintf("PgUp/PgDn · %d–%d of %d", m.approvalOffset+1, end, len(lines)))), "\n"), false
		}
		return strings.Join(lines, "\n"), false
	}
	if m.pendingPlan != "" && !m.isExecuting {
		return m.renderPlanApproval(), false
	}
	if m.search != nil {
		return m.renderSearchBar(), false
	}
	content := m.input.View()
	if len(m.pendingAttachments) > 0 {
		names := make([]string, 0, len(m.pendingAttachments))
		for _, att := range m.pendingAttachments {
			names = append(names, sanitizeUntrusted(att.Name))
		}
		content = styleMuted.Render(clampToWidth("Attached: "+strings.Join(names, " · "), max(1, l.composerWidth-4))) + "\n" + content
	}
	return content + "\n" + m.composerMetadata(l), true
}

func (m *teaModel) composerMetadata(l chatLayout) string {
	mode := strings.ToUpper(m.permissionMode.Label())
	if m.mode == modeShell {
		mode = "SHELL"
	}
	modeStyle := lipgloss.NewStyle().Foreground(permissionModeColor(m.permissionMode)).Background(lipgloss.Color(composerBackground())).Bold(true).Underline(m.hover == hitMode)
	modelStyle := styleMuted.Background(lipgloss.Color(composerBackground())).Underline(m.hover == hitModel)
	row := modeStyle.Render(mode) + "  " + modelStyle.Render(m.modelName)
	if m.thinkLevel != "" && m.thinkLevel != "off" {
		row += styleMuted.Render(" · thinking " + m.thinkLevel)
	}
	return clampToWidth(row, max(1, l.composerWidth-4))
}

func (m *teaModel) renderComposer(l chatLayout) string {
	content, inputShown := m.composerContent(l)
	accent := tuiColorCyan
	if m.mode == modeShell || m.pendingPermission != nil {
		accent = tuiColorYellow
	}
	panel := lipgloss.NewStyle().Background(lipgloss.Color(composerBackground())).
		Border(lipgloss.Border{Left: "┃"}, false, false, false, true).BorderForeground(accent).
		Padding(1, 1, 1, 2).Width(max(1, l.composerWidth)).Render(content)
	m.inputOrigin = nil
	if inputShown {
		inputY := l.composerY + 1
		if len(m.pendingAttachments) > 0 {
			inputY++
		}
		m.inputOrigin = &tea.Position{X: l.composerX + 3, Y: inputY}
		metaY := inputY + m.input.Height()
		mode := strings.ToUpper(m.permissionMode.Label())
		if m.mode == modeShell {
			mode = "SHELL"
		}
		if m.mode == modeAgent {
			m.hits.add(hitMode, l.composerX+3, metaY, VisualLen(mode), 1)
		}
		modelX := l.composerX + 3 + VisualLen(mode) + 2
		m.hits.add(hitModel, modelX, metaY, min(VisualLen(m.modelName), max(0, l.composerWidth-(modelX-l.composerX))), 1)
	}
	return fillSurface(panel, composerBackground())
}

func (m *teaModel) renderHome(l chatLayout) string {
	logoBlock := fastllmWordmark()
	if m.height < 18 || m.frameWidth() < 48 {
		logoBlock = styleDiffHdr.Render("fastllm")
	}
	logoX := max(0, (m.frameWidth()-lipgloss.Width(logoBlock))/2)
	logoY := max(0, l.composerY-m.suggestionRows()-lipgloss.Height(logoBlock)-2)
	layers := []*lipgloss.Layer{
		lipgloss.NewLayer(lipgloss.NewStyle().Width(m.frameWidth()).Height(max(1, m.height)).Render("")),
		lipgloss.NewLayer(logoBlock).X(logoX).Y(logoY),
		lipgloss.NewLayer(m.renderComposer(l)).X(l.composerX).Y(l.composerY),
	}
	if dropdown := m.renderSuggestions(); dropdown != "" {
		layers = append(layers, lipgloss.NewLayer(dropdown).X(l.composerX).Y(max(0, l.composerY-m.suggestionRows())))
	}
	if notice := m.formatWelcome(); notice != "" {
		layers = append(layers, lipgloss.NewLayer(clampToWidth(strings.TrimSpace(notice), m.frameWidth())).Y(max(0, l.composerY+l.composerHeight)))
	}
	layers = append(layers, lipgloss.NewLayer(m.renderFooter()).Y(max(0, m.height-1)))
	hints := clampToWidth("tab modes  ctrl+p commands", l.composerWidth)
	if m.statusNotice != "" {
		hints = clampToWidth(m.statusNotice, l.composerWidth)
	}
	if y := l.composerY + l.composerHeight + 1; y < m.height-1 {
		layers = append(layers, lipgloss.NewLayer(styleMuted.Render(hints)).X(l.composerX+max(0, l.composerWidth-VisualLen(hints))).Y(y))
	}
	return m.fitFrame(lipgloss.NewCompositor(layers...).Render())
}

func fastllmWordmark() string {
	glyphs := [][]string{
		{" ████", " █   ", "███  ", " █   ", " █   "},
		{"     ", " ███ ", "█  █ ", "█  █ ", " ████"},
		{"     ", " ████", "██   ", "   ██", "████ "},
		{" █   ", "████ ", " █   ", " █   ", "  ███"},
		{"█    ", "█    ", "█    ", "█    ", " ███ "},
		{"█    ", "█    ", "█    ", "█    ", " ███ "},
		{"     ", "█ █ █", "██ ██", "█ █ █", "█   █"},
	}
	rows := make([]string, 5)
	for row := range rows {
		for i, glyph := range glyphs {
			style := styleMuted
			if i >= 4 {
				style = styleDiffHdr
			}
			rows[row] += style.Render(glyph[row]) + " "
		}
	}
	return strings.Join(rows, "\n")
}

func (m *teaModel) renderChat() string {
	// Width() is the inner typing width, excluding the prompt. SetWidth takes
	// the complete textarea width; cache that value to avoid resetting its scroll.
	if width := max(1, m.inputBoxWidth()-4); m.chatInputWidth != width {
		m.input.SetWidth(width)
		m.chatInputWidth = width
		m.syncInputHeight()
	}
	l := m.layout()
	if m.showHome() {
		return m.renderHome(l)
	}
	// Resizing and painting use the same geometry, including attachment and approval rows.
	m.viewport.SetWidth(max(1, m.conversationWidth()-4))
	m.viewport.SetHeight(l.transcriptHeight)
	title := "fastllm"
	if m.activeSession != nil && m.activeSession.Title != "" {
		title += " / " + sanitizeUntrusted(m.activeSession.Title)
	}
	header := lipgloss.NewStyle().Foreground(tuiColorWhite).Bold(true).Render(clampToWidth(title, max(1, m.conversationWidth()-4)))
	layers := []*lipgloss.Layer{
		lipgloss.NewLayer(header).X(2),
		lipgloss.NewLayer(m.viewport.View()).X(2).Y(2),
		lipgloss.NewLayer(m.renderComposer(l)).X(l.composerX).Y(l.composerY),
		lipgloss.NewLayer(m.renderFooter()).Y(max(0, m.height-1)),
	}
	if dropdown := m.renderSuggestions(); dropdown != "" {
		layers = append(layers, lipgloss.NewLayer(dropdown).X(l.composerX).Y(2+l.transcriptHeight))
	}
	if m.showChangesColumn() {
		layers = append(layers, lipgloss.NewLayer(m.renderSidebar(m.conversationWidth(), 0, max(1, m.height-2))).X(m.conversationWidth()))
	}
	return m.fitFrame(lipgloss.NewCompositor(layers...).Render())
}

func (m *teaModel) renderFooter() string {
	if m.showHome() {
		left := abbreviateHome(m.workingDir)
		width := max(1, m.frameWidth()-4)
		if VisualLen(left)+12 > width {
			left = filepath.Base(m.workingDir)
		}
		return "  " + styleMuted.Render(clampToWidth(left+strings.Repeat(" ", max(1, width-VisualLen(left)-7))+"fastllm", width))
	}
	hints := "tab mode · ctrl+p commands · /shell"
	if m.mode == modeShell {
		hints = "/shell chat · enter run · ctrl+b background"
	}
	if s, ok := m.suggestionHints(); ok {
		hints = s
	}
	if m.isExecuting {
		hints = fmt.Sprintf("%s turn %d · %s · esc cancel", m.spinner.View(), m.activeTurn, time.Since(m.taskStarted).Round(time.Second))
	}
	if m.canceling {
		hints = "Canceling agent turn · waiting for it to stop"
	}
	if m.shellExecuting {
		hints = m.spinner.View() + " shell · ctrl+b background · esc cancel"
	}
	if m.leaderPending {
		hints = "ctrl+x → n new · l sessions · m models · t themes · b sidebar · s status · c compact · y copy · q exit"
	}
	if m.statusNotice != "" {
		hints = m.statusNotice
	}
	width := max(1, m.frameWidth()-4)
	right := abbreviateHome(m.workingDir)
	if m.processMgr != nil && m.processMgr.ActiveCount() > 0 {
		right = fmt.Sprintf("%d servers · ", m.processMgr.ActiveCount()) + right
	}
	if m.runner != nil && m.runner.agents != nil {
		if summary := m.runner.agents.Summary(); summary.Total > 0 {
			right = fmt.Sprintf("agents %d/%d · ", summary.Pending+summary.Running, summary.Total) + right
		}
	}
	if m.latestMetrics != nil && !m.isExecuting {
		right = fmt.Sprintf("%.0f tok/s · ", m.latestMetrics.TokensPerSecond) + right
	}
	if used, budget := m.contextUsage(); budget > 0 {
		right = formatContextGauge(used, budget, 0) + " · " + right
	}
	if VisualLen(right)+VisualLen(hints)+3 > width {
		right = filepath.Base(m.workingDir)
	}
	gap := width - VisualLen(right) - VisualLen(hints)
	if gap < 3 {
		return "  " + styleMuted.Render(clampToWidth(hints, width))
	}
	return "  " + styleMuted.Render(hints+strings.Repeat(" ", gap)+right)
}

func (m *teaModel) fitFrame(frame string) string {
	lines := strings.Split(frame, "\n")
	rows := max(1, m.height)
	if len(lines) > rows {
		lines = lines[:rows]
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, m.frameWidth(), "")
	}
	return strings.Join(lines, "\n")
}
