package harness

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// maxSuggestionRows caps the dropdown; longer lists scroll around the cursor.
const maxSuggestionRows = 8

// suggestState is the slash-command dropdown above the input box. It is
// derived from the input text on every keystroke; nothing about it is saved.
type suggestState struct {
	items  []suggestion
	cursor int
	// usage is shown in the status bar when the typed command takes free text
	// that has no list of values, e.g. "/search <query>".
	usage string
	// value is the input the items were computed from, so an unchanged input
	// is not recomputed (stage-2 sources read sessions from disk).
	value string
	// dismissedFor hides the list after Esc until the input changes.
	dismissedFor string
	visible      int // rows last reserved in the layout
}

// suggestionsAllowed reports whether the dropdown may show at all: only for a
// single-line slash command typed in agent mode with nothing else in charge
// of the keyboard.
func (m *teaModel) suggestionsAllowed(value string) bool {
	return m.mode == modeAgent &&
		strings.HasPrefix(value, "/") &&
		!strings.Contains(value, "\n") &&
		m.historyIdx == -1 &&
		m.pendingPermission == nil &&
		m.pendingPlan == "" &&
		!m.diffModal && !m.modelsModal && !m.skillsModal &&
		m.sessionsModal == nil && m.themeModal == nil
}

// refreshSuggestions recomputes the dropdown from the input text.
func (m *teaModel) refreshSuggestions() {
	value := m.input.Value()
	s := &m.suggest
	if value != s.value {
		previous := s.items
		s.value = value
		s.items, s.usage = nil, ""
		if value != s.dismissedFor {
			s.dismissedFor = ""
		}
		if m.suggestionsAllowed(value) && s.dismissedFor == "" {
			s.items, s.usage = m.computeSuggestions(value)
		}
		if !sameSuggestions(previous, s.items) {
			s.cursor = 0
		}
	} else if !m.suggestionsAllowed(value) {
		s.items, s.usage = nil, ""
	}
	if s.cursor >= len(s.items) {
		s.cursor = 0
	}
	if rows := m.suggestionRows(); rows != s.visible {
		s.visible = rows
		m.resizeViewport()
	}
}

// computeSuggestions returns command names while the first word is being
// typed, then the known values of the argument being typed.
func (m *teaModel) computeSuggestions(value string) ([]suggestion, string) {
	if !strings.Contains(value, " ") {
		return matchCommands(value), ""
	}
	fields := strings.Fields(value)
	cmd := strings.ToLower(fields[0])
	done, partial := fields[1:], ""
	if !strings.HasSuffix(value, " ") {
		done, partial = fields[1:len(fields)-1], fields[len(fields)-1]
	}
	values, known := m.argumentValues(cmd, done)
	if !known {
		usage := ""
		if c, ok := findSlashCommand(cmd); ok && len(done) == 0 && strings.ContainsAny(c.Usage, "<[") {
			usage = "usage: " + c.Usage + " · " + c.Desc
		}
		return nil, usage
	}
	return filterValues(values, partial), ""
}

func sameSuggestions(a, b []suggestion) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Insert != b[i].Insert || a[i].Label != b[i].Label {
			return false
		}
	}
	return true
}

// suggestionRows is the height the dropdown takes, borders included.
func (m *teaModel) suggestionRows() int {
	n := len(m.suggest.items)
	if n == 0 {
		return 0
	}
	if n > maxSuggestionRows {
		n = maxSuggestionRows
	}
	return n + 2
}

// completedLine is the input with the highlighted item in place of the word
// being typed.
func (m *teaModel) completedLine(item suggestion) string {
	value := m.input.Value()
	if i := strings.LastIndex(value, " "); i >= 0 {
		return value[:i+1] + item.Insert
	}
	return item.Insert
}

// takesArguments reports whether Tab should leave a trailing space after a
// completed item: it needs an argument, or it is a command that accepts
// optional ones or has a list of values to offer next.
func (m *teaModel) takesArguments(line string, item suggestion) bool {
	if item.NeedsMore {
		return true
	}
	if strings.Contains(line, " ") {
		return false // a completed argument value
	}
	if c, ok := findSlashCommand(item.Insert); ok && strings.ContainsAny(c.Usage, "<[") {
		return true
	}
	_, known := m.argumentValues(strings.ToLower(item.Insert), nil)
	return known
}

func (m *teaModel) setInputLine(line string) {
	m.input.SetValue(line)
	m.input.CursorEnd()
}

// handleSuggestKey handles the keys the dropdown owns while it is shown. It
// reports false for everything else so the input behaves as it always has.
func (m *teaModel) handleSuggestKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	s := &m.suggest
	if len(s.items) == 0 {
		return false, nil
	}
	item := s.items[s.cursor]
	// Modified chords (alt+enter, shift+tab, ...) have their own strings, so
	// they fall through to the input untouched.
	switch msg.String() {
	case "up":
		if s.cursor > 0 {
			s.cursor--
		}
		return true, nil
	case "down":
		if s.cursor < len(s.items)-1 {
			s.cursor++
		}
		return true, nil
	case "esc":
		if m.isExecuting || m.shellExecuting {
			return false, nil // Esc cancels the running turn first
		}
		s.dismissedFor = m.input.Value()
		s.value = "" // force the next refresh to apply the dismissal
		return true, nil
	case "tab":
		line := m.completedLine(item)
		if m.takesArguments(line, item) {
			line += " "
		}
		m.setInputLine(line)
		return true, nil
	case "enter":
		value := strings.TrimSpace(m.input.Value())
		// A command typed out in full runs exactly as it always did.
		if !strings.Contains(value, " ") {
			if _, ok := findSlashCommand(value); ok {
				return false, nil
			}
		}
		// Smart Enter: run unless a required argument is still missing.
		line := m.completedLine(item)
		if item.NeedsMore {
			m.setInputLine(line + " ")
			return true, nil
		}
		if isDestructive(strings.Fields(line)[0]) {
			// Never run a destructive command from a highlighted row: fill it
			// in and hide the list, so a second Enter is a deliberate choice.
			m.setInputLine(line)
			s.dismissedFor = line
			s.value = ""
			return true, nil
		}
		return true, m.runSuggestedLine(line)
	}
	return false, nil
}

// runSuggestedLine submits line exactly as if it had been typed and entered.
func (m *teaModel) runSuggestedLine(line string) tea.Cmd {
	m.addPromptHistory(line)
	m.historyIdx = -1
	m.historyDraft = ""
	m.input.Reset()
	return m.handleAgentSubmit(line)
}

// renderSuggestions draws the dropdown at the input box's width.
func (m *teaModel) renderSuggestions() string {
	s := &m.suggest
	if len(s.items) == 0 {
		return ""
	}
	inner := m.inputBoxWidth() - 2
	if inner < 20 {
		inner = 20
	}
	start := s.cursor - maxSuggestionRows/2
	if start < 0 {
		start = 0
	}
	if maxStart := len(s.items) - maxSuggestionRows; start > maxStart {
		start = maxStart
	}
	if start < 0 {
		start = 0
	}
	end := start + maxSuggestionRows
	if end > len(s.items) {
		end = len(s.items)
	}

	labelWidth := 0
	for _, item := range s.items[start:end] {
		if w := VisualLen(item.Label); w > labelWidth {
			labelWidth = w
		}
	}
	if labelWidth > 28 {
		labelWidth = 28
	}

	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		item := s.items[i]
		label := PadRight(truncateText(item.Label, labelWidth), labelWidth)
		descWidth := inner - 2 - labelWidth - 2
		desc := ""
		if descWidth >= 8 {
			desc = truncateText(item.Desc, descWidth)
		}
		if i == s.cursor {
			row := "› " + label + "  " + desc
			lines = append(lines, lipgloss.NewStyle().Bold(true).
				Foreground(tuiColorBrandFg).Background(tuiColorCyan).
				Width(inner).Render(clampToWidth(row, inner)))
			continue
		}
		row := "  " + ColorBrightWhite(label) + "  " + styleMuted.Render(desc)
		lines = append(lines, clampToWidth(row, inner))
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(tuiColorBorder).
		Padding(0, 1).
		Width(m.inputBoxWidth()).
		Render(strings.Join(lines, "\n"))
}

// suggestionHints replaces the status-bar key hints while the dropdown or a
// usage line is showing. ok is false when neither is.
func (m *teaModel) suggestionHints() (hints string, ok bool) {
	switch {
	case len(m.suggest.items) > 0:
		return "↑/↓: Select  •  Tab: Complete  •  Enter: Run  •  Esc: Hide", true
	case m.suggest.usage != "":
		return m.suggest.usage, true
	}
	return "", false
}
