package harness

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// The sidebar stacks Session and Context above the Changes list. They are read
// from the live model on every frame, so, unlike the welcome text in the
// transcript, they never go stale after /model, /dir, or Shift+Tab.

const sidebarLabelWidth = 10 // "workspace" plus one space

// renderSidebar draws the sidebar as exactly height rows of changesColumnWidth
// with its top-left corner at (x, y), and registers each file row it draws as
// a click target.
func (m *teaModel) renderSidebar(x, y, height int) string {
	inner := changesColumnWidth - 2
	if inner < 1 || height < 1 {
		return ""
	}
	top := m.sidebarTopRows(inner, height)
	changes, fileAt := m.changes.Rows(inner, height-len(top))
	for i, idx := range fileAt {
		if idx >= 0 && len(top)+i < height {
			id := hitChangeFile + strconv.Itoa(idx)
			m.hits.add(id, x, y+len(top)+i, changesColumnWidth, 1)
			if id == m.hover {
				changes[i] = styleHoverRow.Render(PadRight(clampToWidth(StripANSI(changes[i]), inner), inner))
			}
		}
	}
	return renderSidebarColumn(append(top, changes...), inner, height)
}

// sidebarTopRows returns the Session and Context sections, or nothing when the
// sidebar is too short to hold them and still list a few changed files.
func (m *teaModel) sidebarTopRows(inner, height int) []string {
	var rows []string
	rows = append(rows, sidebarSectionHeader("Session", inner)...)
	rows = append(rows, m.sessionRows()...)
	rows = append(rows, "")
	rows = append(rows, sidebarSectionHeader("Context", inner)...)
	rows = append(rows, m.contextRows(inner)...)
	rows = append(rows, "")
	if m.runner != nil && m.runner.agents != nil {
		if summary := m.runner.agents.Summary(); summary.Total > 0 {
			rows = append(rows, sidebarSectionHeader("Agents", inner)...)
			rows = append(rows, styleMuted.Render(fmt.Sprintf("%d active / %d total · %s tok", summary.Pending+summary.Running, summary.Total, compactCount(summary.TotalTokens))), "")
		}
	}
	if m.processMgr != nil && m.processMgr.ActiveCount() > 0 {
		rows = append(rows, sidebarSectionHeader("Processes", inner)...)
		rows = append(rows, styleMuted.Render(fmt.Sprintf("%d servers · /ps for details", m.processMgr.ActiveCount())), "")
	}
	const minChangesRows = 5
	if height-len(rows) < minChangesRows {
		return nil
	}
	return rows
}

func sidebarSectionHeader(title string, inner int) []string {
	return []string{styleDiffHdr.Render(title)}
}

func sidebarKV(label, value string) string {
	return styleMuted.Render(PadRight(label, sidebarLabelWidth)) + value
}

func (m *teaModel) sessionRows() []string {
	rows := []string{sidebarKV("model", ColorBrightWhite(m.modelName))}
	if host := m.endpointHost(); host != "" {
		rows = append(rows, sidebarKV("endpoint", host))
	}
	mode := styleMuted.Render("shell")
	if m.mode == modeAgent {
		mode = lipgloss.NewStyle().Foreground(permissionModeColor(m.permissionMode)).Render(strings.ToLower(m.permissionMode.Label()))
	}
	rows = append(rows, sidebarKV("mode", mode))

	dir := filepath.Base(m.workingDir)
	if dir == "" || dir == "." {
		dir = m.workingDir
	}
	rows = append(rows, sidebarKV("workspace", truncatePathLeft(dir, changesColumnWidth-2-sidebarLabelWidth)))

	commands := ColorYellow("off")
	if m.allowCommands {
		commands = ColorGreen("on")
	}
	rules := styleMuted.Render("none")
	if n := len(m.rules); n > 0 {
		rules = fmt.Sprintf("%d", n)
	}
	rows = append(rows, sidebarKV("rules", rules+styleMuted.Render(" "+SymDot+" cmds ")+commands))
	return rows
}

// endpointHost names where the active model is served, without the scheme or
// the /v1 suffix every OpenAI-compatible URL shares.
func (m *teaModel) endpointHost() string {
	if m.settings == nil {
		return ""
	}
	endpoint := m.settings.FindModel(m.modelName)
	if endpoint == nil {
		return ""
	}
	url := strings.TrimSpace(endpoint.URL)
	if url == "" {
		return styleMuted.Render(endpoint.Provider)
	}
	url = strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	url = strings.TrimSuffix(strings.TrimSuffix(url, "/"), "/v1")
	return truncateText(url, changesColumnWidth-2-sidebarLabelWidth)
}

func (m *teaModel) contextRows(inner int) []string {
	used, budget := m.contextUsage()
	var rows []string
	if budget > 0 {
		// Size the bar to whatever the label and counts leave of the row.
		bar := inner - VisualLen(formatContextGauge(used, budget, 1)) + 1
		if bar < 4 {
			bar = 4
		}
		rows = append(rows, formatContextGauge(used, budget, bar))
	}
	if tm := m.latestMetrics; tm != nil {
		rows = append(rows, styleMuted.Render(fmt.Sprintf("last turn %.1fs %s %.0f tok/s",
			tm.Duration.Seconds(), SymDot, tm.TokensPerSecond)))
	}
	if len(rows) == 0 {
		rows = append(rows, styleMuted.Render("No usage yet."))
	}
	return rows
}
