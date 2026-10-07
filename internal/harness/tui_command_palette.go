package harness

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type commandAction struct {
	usage, description, line string
	needsArgs                bool
}

type commandPicker struct {
	query  string
	cursor int
}

type teaLeaderTimeoutMsg struct{ generation uint64 }

func commandActions(query string) []commandAction {
	var actions []commandAction
	words := strings.Fields(strings.ToLower(query))
	for _, section := range commandSections {
		for _, row := range section.Rows {
			if !strings.HasPrefix(row.Usage, "/") {
				continue
			}
			matches := true
			for _, word := range words {
				if !strings.Contains(strings.ToLower(row.Usage+" "+row.Desc+" "+section.Title), word) {
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			fields := strings.Fields(row.Usage)
			line := strings.TrimSuffix(fields[0], ",")
			for _, field := range fields[1:] {
				if strings.HasPrefix(field, "/") || strings.ContainsAny(field, "<[") {
					break
				}
				line += " " + field
			}
			actions = append(actions, commandAction{usage: row.Usage, description: row.Desc, line: line, needsArgs: strings.Contains(row.Usage, "<")})
		}
	}
	return actions
}

func (m *teaModel) openCommandPalette() {
	m.leaderPending = false
	m.commandPalette = &commandPicker{}
	m.input.Blur()
}

func (m *teaModel) closeCommandPalette() {
	m.commandPalette = nil
	m.input.Focus()
}

func (m *teaModel) handleCommandPaletteKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.commandPalette
	actions := commandActions(p.query)
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+p":
		m.closeCommandPalette()
	case "up":
		p.cursor = max(0, p.cursor-1)
	case "down":
		p.cursor = min(max(0, len(actions)-1), p.cursor+1)
	case "home":
		p.cursor = 0
	case "end":
		p.cursor = max(0, len(actions)-1)
	case "backspace":
		runes := []rune(p.query)
		if len(runes) > 0 {
			p.query = string(runes[:len(runes)-1])
			p.cursor = 0
		}
	case "enter":
		if len(actions) == 0 {
			return nil
		}
		action := actions[min(p.cursor, len(actions)-1)]
		m.closeCommandPalette()
		if action.needsArgs || isDestructive(strings.Fields(action.line)[0]) {
			line := action.line
			if action.needsArgs {
				line += " "
			}
			m.setInputLine(line)
			m.suggest.dismissedFor = line
			m.suggest.value = ""
			return nil
		}
		// Palette actions belong to the app even while composing a shell command.
		return m.handleAgentSubmit(action.line)
	default:
		if msg.Mod == 0 && msg.Text != "" {
			p.query += printableRunes([]rune(msg.Text))
			p.cursor = 0
		}
	}
	return nil
}

func (m *teaModel) renderCommandPalette() string {
	p := m.commandPalette
	width := min(76, max(12, m.frameWidth()-8))
	inner := max(1, width-4)
	actions := commandActions(p.query)
	rows := min(10, max(1, m.height-10))
	start := max(0, min(p.cursor-rows/2, len(actions)-rows))
	end := min(len(actions), start+rows)
	query := p.query
	if query == "" {
		query = "Search commands"
	}
	lines := []string{styleDiffHdr.Render("Commands"), "", styleMuted.Render(clampToWidth(query, inner)), ""}
	for i := start; i < end; i++ {
		a := actions[i]
		row := clampToWidth(fmt.Sprintf("%-24s %s", a.usage, a.description), inner)
		style := styleMuted
		if i == p.cursor {
			style = lipgloss.NewStyle().Foreground(tuiColorBrandFg).Background(tuiColorCyan).Bold(true)
		}
		lines = append(lines, style.Width(inner).Render(row))
	}
	if len(actions) == 0 {
		lines = append(lines, styleMuted.Render("No matching commands"))
	}
	lines = append(lines, "", styleMuted.Render(clampToWidth("↑/↓ select · Enter choose · Esc close", inner)))
	return lipgloss.NewStyle().Background(tuiColorCardBg).Padding(1, 2).Width(width).Render(strings.Join(lines, "\n"))
}

func (m *teaModel) toggleSidebar() {
	m.sidebarHidden = !m.sidebarHidden
	m.input.SetWidth(max(1, m.inputBoxWidth()-4))
	m.viewport.SetWidth(max(1, m.conversationWidth()-4))
	m.resizeViewport()
	m.syncTranscript(false)
}

func (m *teaModel) handleCommonKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if m.leaderPending {
		m.leaderPending = false
		if msg.String() == "esc" {
			return true, nil
		}
		switch strings.ToLower(msg.String()) {
		case "g":
			return true, m.openSourceControl("")
		case "b":
			m.toggleSidebar()
			return true, nil
		case "n":
			return true, m.handleAgentSubmit("/new")
		case "l":
			return true, m.handleAgentSubmit("/sessions")
		case "m":
			m.openModelsModal()
			return true, nil
		case "t":
			m.openThemeModal()
			return true, nil
		case "s":
			return true, m.handleAgentSubmit("/status")
		case "c":
			return true, m.handleAgentSubmit("/compact")
		case "y":
			return true, m.handleAgentSubmit("/copy")
		case "q":
			return true, m.handleAgentSubmit("/exit")
		}
	}
	switch msg.String() {
	case "ctrl+p":
		m.openCommandPalette()
		return true, nil
	case "ctrl+x":
		m.leaderPending = true
		m.leaderGeneration++
		generation := m.leaderGeneration
		return true, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return teaLeaderTimeoutMsg{generation} })
	}
	return false, nil
}
