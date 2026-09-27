package harness

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The find bar searches the transcript (Ctrl+F or /find). It highlights
// matches itself rather than through viewport.SetHighlights: in bubbles
// v2.2.1 that maps match offsets onto the wrong lines and columns whenever
// the content carries colour codes, and the transcript always does.

// transcriptSearch is the open find bar: the query and where it matches.
type transcriptSearch struct {
	query   string
	matches []searchMatch
	current int // index into matches, -1 when there are none
}

// searchMatch is one hit, as a transcript line and a cell range within it.
type searchMatch struct {
	line, start, end int
}

// openSearch shows the find bar in place of the input box.
func (m *teaModel) openSearch(query string) {
	m.search = &transcriptSearch{query: query, current: -1}
	m.input.Blur()
	m.refreshSearch(true)
}

// closeSearch removes the highlights and returns to the input box, leaving
// the transcript scrolled to where the search left it.
func (m *teaModel) closeSearch() {
	m.search = nil
	m.input.Focus()
	m.viewport.SetContent(m.historyText.String())
}

// syncTranscript shows the transcript in the viewport, highlighted while the
// find bar is open. follow scrolls to the newest output; a search keeps the
// reader where they are instead.
func (m *teaModel) syncTranscript(follow bool) {
	if m.search != nil {
		m.refreshSearch(false)
		return
	}
	m.viewport.SetContent(m.historyText.String())
	if follow {
		m.viewport.GotoBottom()
	}
}

// refreshSearch finds the query in the transcript and redraws it with the
// matches highlighted. jump selects the first match at or below the top of
// the view, as a new query should; otherwise the selection stays put.
func (m *teaModel) refreshSearch(jump bool) {
	s := m.search
	lines := strings.Split(m.historyText.String(), "\n")
	s.matches = findMatches(lines, s.query)
	switch {
	case len(s.matches) == 0:
		s.current = -1
	case jump || s.current < 0:
		s.current = 0
		for i, match := range s.matches {
			if match.line >= m.viewport.YOffset() {
				s.current = i
				break
			}
		}
	case s.current >= len(s.matches):
		s.current = len(s.matches) - 1
	}
	m.viewport.SetContent(highlightMatches(lines, s))
	if jump {
		m.showCurrentMatch()
	}
}

// stepSearch selects the next (delta 1) or previous (-1) match, wrapping.
func (m *teaModel) stepSearch(delta int) {
	s := m.search
	if len(s.matches) == 0 {
		return
	}
	s.current = (s.current + delta + len(s.matches)) % len(s.matches)
	lines := strings.Split(m.historyText.String(), "\n")
	m.viewport.SetContent(highlightMatches(lines, s))
	m.showCurrentMatch()
}

// showCurrentMatch scrolls the selected match to the middle of the view when
// it is off screen.
func (m *teaModel) showCurrentMatch() {
	s := m.search
	if s.current < 0 {
		return
	}
	line, top, height := s.matches[s.current].line, m.viewport.YOffset(), m.viewport.Height()
	if line < top || line >= top+height {
		m.viewport.SetYOffset(max(0, line-height/2))
	}
}

// findMatches returns every case-insensitive occurrence of query in lines,
// by visible cell position.
func findMatches(lines []string, query string) []searchMatch {
	if query == "" {
		return nil
	}
	var matches []searchMatch
	for i, line := range lines {
		plain := StripANSI(line)
		haystack, needle := strings.ToLower(plain), strings.ToLower(query)
		if len(haystack) != len(plain) {
			// Lower-casing changed byte lengths, so offsets would drift;
			// match this line exactly instead.
			haystack, needle = plain, query
		}
		for from := 0; ; {
			idx := strings.Index(haystack[from:], needle)
			if idx < 0 {
				break
			}
			idx += from
			start := lipgloss.Width(plain[:idx])
			end := start + lipgloss.Width(plain[idx:idx+len(needle)])
			matches = append(matches, searchMatch{line: i, start: start, end: end})
			from = idx + len(needle)
		}
	}
	return matches
}

// highlightMatches returns the transcript with every match marked and the
// selected one marked differently.
func highlightMatches(lines []string, s *transcriptSearch) string {
	if len(s.matches) == 0 {
		return strings.Join(lines, "\n")
	}
	hit := lipgloss.NewStyle().Background(tuiColorTrack).Foreground(tuiColorWhite)
	selected := lipgloss.NewStyle().Background(tuiColorCyan).Foreground(tuiColorBrandFg).Bold(true)
	out := append([]string(nil), lines...)
	for i := 0; i < len(s.matches); {
		line := s.matches[i].line
		var ranges []lipgloss.Range
		for ; i < len(s.matches) && s.matches[i].line == line; i++ {
			style := hit
			if i == s.current {
				style = selected
			}
			ranges = append(ranges, lipgloss.NewRange(s.matches[i].start, s.matches[i].end, style))
		}
		out[line] = lipgloss.StyleRanges(out[line], ranges...)
	}
	return strings.Join(out, "\n")
}

// handleSearchKey handles keys while the find bar is open.
func (m *teaModel) handleSearchKey(msg tea.KeyPressMsg) tea.Cmd {
	s := m.search
	switch msg.String() {
	case "esc", "ctrl+c":
		m.closeSearch()
	case "enter", "down", "ctrl+n", "f3":
		m.stepSearch(1)
	case "shift+enter", "up", "ctrl+p", "shift+f3":
		m.stepSearch(-1)
	case "pgup":
		m.viewport.ScrollUp(5)
	case "pgdown":
		m.viewport.ScrollDown(5)
	case "backspace":
		if runes := []rune(s.query); len(runes) > 0 {
			s.query = string(runes[:len(runes)-1])
			m.refreshSearch(true)
		}
	default:
		if typed := printableRunes([]rune(msg.Text)); typed != "" {
			s.query += typed
			m.refreshSearch(true)
		}
	}
	return nil
}

// addSearchText appends pasted text to the query, up to its first line.
func (m *teaModel) addSearchText(text string) {
	if line, _, _ := strings.Cut(text, "\n"); printableRunes([]rune(line)) != "" {
		m.search.query += printableRunes([]rune(line))
		m.refreshSearch(true)
	}
}

// renderSearchBar draws the find bar, padded to the input box's height so
// opening it never moves the rest of the frame.
func (m *teaModel) renderSearchBar() string {
	s := m.search
	count := styleMuted.Render("type to search")
	switch {
	case s.query != "" && len(s.matches) == 0:
		count = ColorYellow("no matches")
	case len(s.matches) > 0:
		count = styleMuted.Render(fmt.Sprintf("%d of %d", s.current+1, len(s.matches)))
	}
	width := m.inputBoxWidth() - 2 // inside the box border
	bar := clampToWidth(styleDiffHdr.Render("find ")+s.query+ColorCyan("▏")+"  "+count, width) + "\n" +
		clampToWidth(styleMuted.Render("Enter/↓ next · ↑ previous · Esc close"), width)
	return lipgloss.NewStyle().Height(m.input.Height()).Render(bar)
}
