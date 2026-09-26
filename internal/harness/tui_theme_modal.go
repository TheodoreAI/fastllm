package harness

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

func (m *teaModel) handleThemeModalKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.themeModal
	switch msg.String() {
	case "esc", "ctrl+c", "q", "Q":
		_ = ApplyTheme(p.original)
		m.closeThemeModal()
	case "enter":
		name := builtinThemes[p.cursor].Name
		m.closeThemeModal()
		return m.commitTheme(name)
	case "up", "k":
		m.previewThemeAt(p.cursor - 1)
	case "down", "j":
		m.previewThemeAt(p.cursor + 1)
	case "home":
		m.previewThemeAt(0)
	case "end":
		m.previewThemeAt(len(builtinThemes) - 1)
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

	availableHeight := height - 7
	allFit := len(builtinThemes)*2 <= availableHeight
	start := 0
	end := len(builtinThemes)

	if !allFit {
		maxVisible := (availableHeight - 2) / 2
		if maxVisible < 2 {
			maxVisible = 2
		}
		if maxVisible > len(builtinThemes) {
			maxVisible = len(builtinThemes)
		}
		start = p.cursor - maxVisible/2
		if start < 0 {
			start = 0
		}
		if start+maxVisible > len(builtinThemes) {
			start = len(builtinThemes) - maxVisible
			if start < 0 {
				start = 0
			}
		}
		end = start + maxVisible
		if end > len(builtinThemes) {
			end = len(builtinThemes)
		}

		if start > 0 {
			lines = append(lines, clampToWidth("    "+styleMuted.Render(fmt.Sprintf("▲ %d more above", start)), rowWidth))
		}
	}

	for i := start; i < end; i++ {
		t := builtinThemes[i]
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

	if !allFit && end < len(builtinThemes) {
		lines = append(lines, clampToWidth("    "+styleMuted.Render(fmt.Sprintf("▼ %d more below", len(builtinThemes)-end)), rowWidth))
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
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(tuiColorDarkBg)))
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
