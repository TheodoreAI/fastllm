package harness

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// themePicker is the state of the /theme menu. Moving the cursor applies the
// highlighted theme immediately as a live preview; Esc restores original.
type themePicker struct {
	cursor   int
	original string
}

func (m *teaModel) openThemeModal() {
	picker := &themePicker{original: currentTheme.Name}
	for i, t := range builtinThemes {
		if t.Name == currentTheme.Name {
			picker.cursor = i
			break
		}
	}
	m.themeModal = picker
	m.input.Blur()
}

func (m *teaModel) closeThemeModal() {
	m.themeModal = nil
	m.input.Focus()
}

func (m *teaModel) previewThemeAt(cursor int) {
	if cursor < 0 || cursor >= len(builtinThemes) {
		return
	}
	m.themeModal.cursor = cursor
	_ = ApplyTheme(builtinThemes[cursor].Name)
}

func (m *teaModel) handleThemeModalKey(msg tea.KeyMsg) tea.Cmd {
	p := m.themeModal
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		_ = ApplyTheme(p.original)
		m.closeThemeModal()
	case tea.KeyEnter:
		name := builtinThemes[p.cursor].Name
		m.closeThemeModal()
		return m.commitTheme(name)
	case tea.KeyUp:
		m.previewThemeAt(p.cursor - 1)
	case tea.KeyDown:
		m.previewThemeAt(p.cursor + 1)
	case tea.KeyHome:
		m.previewThemeAt(0)
	case tea.KeyEnd:
		m.previewThemeAt(len(builtinThemes) - 1)
	case tea.KeyRunes:
		if len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case 'q', 'Q':
				_ = ApplyTheme(p.original)
				m.closeThemeModal()
			case 'j':
				m.previewThemeAt(p.cursor + 1)
			case 'k':
				m.previewThemeAt(p.cursor - 1)
			}
		}
	}
	return nil
}

// commitTheme applies and saves name, reporting the outcome in the status bar.
func (m *teaModel) commitTheme(name string) tea.Cmd {
	if err := ApplyTheme(name); err != nil {
		m.appendHistory(styleDiffDel.Render(err.Error()) + "\n\n")
		return nil
	}
	m.statusNotice = "Theme: " + currentTheme.Name
	if err := SaveThemePreference(currentTheme.Name); err != nil {
		m.statusNotice = "Theme applied but not saved: " + err.Error()
	}
	return m.clearStatusAfter(3 * time.Second)
}

// handleThemeSlash runs /theme: bare opens the picker, /theme <name> switches.
func (m *teaModel) handleThemeSlash(parts []string) tea.Cmd {
	if len(parts) < 2 {
		m.openThemeModal()
		return nil
	}
	return m.commitTheme(parts[1])
}

func (m *teaModel) renderThemeModal() string {
	p := m.themeModal
	width, height := m.width, m.height
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	contentWidth := width - 8
	if contentWidth > 76 {
		contentWidth = 76
	}
	if contentWidth < 40 {
		contentWidth = 40
	}
	rowWidth := contentWidth - 2

	lines := []string{styleAgentBadge.Render(fmt.Sprintf("THEMES · %d available", len(builtinThemes))), ""}
	nameWidth := 0
	for _, t := range builtinThemes {
		if len(t.Name) > nameWidth {
			nameWidth = len(t.Name)
		}
	}
	for i, t := range builtinThemes {
		isSelected := i == p.cursor
		prefix := "  "
		if isSelected {
			prefix = ColorCyan(StyleBold("› "))
		}
		indicator := styleMuted.Render("○ ")
		if t.Name == p.original {
			indicator = ColorGreen("● ")
		}
		name := ColorBrightWhite(StyleBold(PadRight(t.Name, nameWidth)))
		if isSelected {
			name = ColorCyan(StyleBold(PadRight(t.Name, nameWidth)))
		}
		lines = append(lines, clampToWidth(prefix+indicator+name+"  "+themeSwatches(t), rowWidth))
		lines = append(lines, clampToWidth("      "+styleMuted.Render(t.Description), rowWidth))
	}
	lines = append(lines, "",
		styleMuted.Render("↑/↓ preview · Enter save · Esc cancel"),
		styleMuted.Render("New colours apply to new output · /cls to redraw"))

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

// themeSwatches previews a theme's main colours as blocks. They are drawn in
// the theme's own hex values, not the active theme's, so every row shows
// its palette while another theme is being previewed.
func themeSwatches(t Theme) string {
	var b strings.Builder
	for _, hex := range []string{t.Accent, t.Accent2, t.Purple, t.Ok, t.Warn, t.Error, t.Muted} {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render("██"))
		b.WriteString(" ")
	}
	return strings.TrimRight(b.String(), " ")
}
