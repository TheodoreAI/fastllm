package harness

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// permissionModeColor matches colorForMode for lipgloss surfaces.
func permissionModeColor(mode PermissionMode) lipgloss.Color {
	switch NormalizeMode(mode) {
	case PermissionPlan:
		return tuiColorCyan
	case PermissionEdit:
		return tuiColorGreen
	case PermissionFull:
		return tuiColorYellow
	default:
		return tuiColorWhite
	}
}

// permissionModeSummary is the one-line reminder shown when a mode is chosen.
func permissionModeSummary(mode PermissionMode) string {
	switch NormalizeMode(mode) {
	case PermissionPlan:
		return "read-only, no network; ends with a plan to approve"
	case PermissionAgent:
		return "asks before every change or command"
	case PermissionEdit:
		return "edits files without asking; no commands"
	case PermissionFull:
		return "everything, without asking"
	}
	return ""
}

// setPermissionMode is the only way the TUI changes the mode, and it is only
// reached from user input. A running turn captured its mode when it started,
// so switching mid-turn would change the display but not the turn (I6).
func (m *teaModel) setPermissionMode(mode PermissionMode) error {
	if m.isExecuting {
		return fmt.Errorf("finish or Esc the current turn before changing permissions")
	}
	m.permissionMode = NormalizeMode(mode)
	m.permissionController().SetMode(m.permissionMode)
	return nil
}

func (m *teaModel) cyclePermissionMode() tea.Cmd {
	if err := m.setPermissionMode(m.permissionMode.Next()); err != nil {
		m.statusNotice = err.Error()
		return m.clearStatusAfter(3 * time.Second)
	}
	_ = m.saveSession()
	m.statusNotice = m.permissionMode.Label() + ": " + permissionModeSummary(m.permissionMode)
	return m.clearStatusAfter(3 * time.Second)
}

// updatePlanApproval handles keys while a submitted plan awaits a decision.
func (m *teaModel) updatePlanApproval(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		return m, m.resolvePlan("")
	case tea.KeyUp:
		if m.planCursor > 0 {
			m.planCursor--
		}
	case tea.KeyDown:
		if m.planCursor < len(planApprovals)-1 {
			m.planCursor++
		}
	case tea.KeyEnter:
		return m, m.resolvePlan(planApprovals[m.planCursor].Mode)
	case tea.KeyRunes:
		if len(msg.Runes) > 0 {
			for _, approval := range planApprovals {
				if msg.Runes[0] == approval.Key {
					return m, m.resolvePlan(approval.Mode)
				}
			}
		}
	}
	return m, nil
}

// resolvePlan applies the user's decision: "" keeps planning, any other mode
// switches to it and starts the implementation turn.
func (m *teaModel) resolvePlan(mode PermissionMode) tea.Cmd {
	plan := m.pendingPlan
	m.pendingPlan = ""
	m.planCursor = 0
	m.input.Focus()
	if mode == "" {
		m.statusNotice = "Kept planning."
		return m.clearStatusAfter(2 * time.Second)
	}
	if err := m.setPermissionMode(mode); err != nil {
		m.statusNotice = err.Error()
		return m.clearStatusAfter(3 * time.Second)
	}
	m.appendHistory(styleMuted.Render(fmt.Sprintf("Plan approved; switched to %s mode.\n\n", mode.Label())))
	return m.handleAgentSubmit(implementPlanTask(plan))
}

func (m *teaModel) renderPlanApproval() string {
	var b strings.Builder
	b.WriteString("  " + ColorCyan("Plan ready. Carry it out?") + "\n")
	for i, approval := range planApprovals {
		marker := "  "
		label := approval.Label
		if i == m.planCursor {
			marker = ColorCyan(SymPrompt + " ")
			label = ColorCyan(label)
		}
		fmt.Fprintf(&b, "  %s%s %s\n", marker, ColorGray("["+string(approval.Key)+"]"), label)
	}
	b.WriteString("  " + ColorGray("↑/↓ and enter, or a key; esc keeps planning"))
	return b.String()
}
