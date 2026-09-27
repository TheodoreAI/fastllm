package harness

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Match columns are visible cells, whatever colour codes and wide characters
// precede the match.
func TestFindMatchesCountsVisibleCells(t *testing.T) {
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000"))
	lines := []string{
		red.Render("gamma") + " Needle",
		"日本 needle needle",
		"nothing here",
	}
	got := findMatches(lines, "needle")
	want := []searchMatch{{0, 6, 12}, {1, 5, 11}, {1, 12, 18}}
	if len(got) != len(want) {
		t.Fatalf("matches = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %v; want %v", i, got[i], want[i])
		}
	}
}

// Highlighting restyles the matches without changing the text.
func TestHighlightMatchesKeepsText(t *testing.T) {
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000"))
	lines := []string{red.Render("gamma") + " needle", "plain needle"}
	s := &transcriptSearch{query: "needle", matches: findMatches(lines, "needle"), current: 1}
	out := highlightMatches(lines, s)
	if StripANSI(out) != StripANSI(strings.Join(lines, "\n")) {
		t.Fatalf("highlighting changed the text:\n%q", StripANSI(out))
	}
	if out == strings.Join(lines, "\n") {
		t.Fatal("nothing was highlighted")
	}
}

func newSearchModel(t *testing.T) *teaModel {
	t.Helper()
	m := newWideModel(t)
	m.input = newChatInput()
	m.viewport = viewport.New(viewport.WithWidth(100), viewport.WithHeight(10))
	for i := 0; i < 60; i++ {
		line := "filler line"
		if i%20 == 5 {
			line = "the needle is here"
		}
		m.appendHistory(line + "\n")
	}
	return m
}

func TestFindBarSearchesAndSteps(t *testing.T) {
	m := newSearchModel(t)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	m = updated.(*teaModel)
	if m.search == nil {
		t.Fatal("Ctrl+F did not open the find bar")
	}
	for _, r := range "NEEDLE" {
		updated, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(*teaModel)
	}
	if n := len(m.search.matches); n != 3 {
		t.Fatalf("found %d matches; want 3 (case-insensitive)", n)
	}
	if m.input.Value() != "" {
		t.Fatalf("typing into the find bar reached the input: %q", m.input.Value())
	}

	seen := map[int]bool{}
	for i := 0; i < 3; i++ {
		match := m.search.matches[m.search.current]
		top := m.viewport.YOffset()
		if match.line < top || match.line >= top+m.viewport.Height() {
			t.Fatalf("match on line %d is off screen (view %d-%d)", match.line, top, top+m.viewport.Height())
		}
		seen[m.search.current] = true
		updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated.(*teaModel)
	}
	if len(seen) != 3 {
		t.Fatalf("Enter visited %d distinct matches; want 3", len(seen))
	}
	if !strings.Contains(StripANSI(m.render()), "of 3") {
		t.Fatal("the find bar does not show the match count")
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(*teaModel)
	if m.search != nil || m.viewport.GetContent() != m.historyText.String() {
		t.Fatal("Esc did not close the find bar and remove the highlights")
	}
}

func TestFindCommandOpensWithQuery(t *testing.T) {
	m := newSearchModel(t)
	m.handleAgentSubmit("/find needle")
	if m.search == nil || m.search.query != "needle" || len(m.search.matches) != 3 {
		t.Fatalf("/find opened %+v", m.search)
	}
	// New output while searching keeps the highlights and the reader's place.
	top := m.viewport.YOffset()
	m.appendHistory("another needle\n")
	if len(m.search.matches) != 4 || m.viewport.YOffset() != top {
		t.Fatalf("after new output: %d matches, offset %d (was %d)", len(m.search.matches), m.viewport.YOffset(), top)
	}
}

// The real cursor sits right after the typed text, and is hidden when the
// input box is not showing the textarea.
func TestTerminalCursorFollowsCaret(t *testing.T) {
	m := newWideModel(t)
	m.input = newChatInput()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m = updated.(*teaModel)
	for _, r := range "hello" {
		updated, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(*teaModel)
	}
	v := m.View()
	if v.Cursor == nil {
		t.Fatal("no terminal cursor while typing")
	}
	rows := strings.Split(StripANSI(v.Content), "\n")
	row := []rune(rows[v.Cursor.Position.Y])
	x := v.Cursor.Position.X
	if x < 5 || x > len(row) || string(row[x-5:x]) != "hello" {
		t.Fatalf("cursor at (%d,%d) on row %q; want it just after \"hello\"", x, v.Cursor.Position.Y, string(row))
	}

	m.openSearch("")
	if m.View().Cursor != nil {
		t.Fatal("the cursor stayed visible with the find bar open")
	}
	m.closeSearch()
	m.openThemeModal()
	if m.View().Cursor != nil {
		t.Fatal("the cursor stayed visible under a modal")
	}
}

func TestTerminalColorsWaitForTheTerminal(t *testing.T) {
	t.Cleanup(func() { _ = ApplyTheme(defaultThemeName) })
	_ = ApplyTheme(defaultThemeName)
	themeFromPreference = true // keep the dark default on a dark reply
	t.Cleanup(func() { themeFromPreference = false })
	m := newWideModel(t)

	if v := m.View(); v.BackgroundColor != nil {
		t.Fatal("painted the background before the terminal reported its own")
	}
	updated, _ := m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#000000")})
	m = updated.(*teaModel)
	v := m.View()
	if v.BackgroundColor == nil || v.ForegroundColor == nil {
		t.Fatal("the theme's colours were not painted")
	}
	if r1, g1, b1, _ := v.BackgroundColor.RGBA(); true {
		r2, g2, b2, _ := lipgloss.Color(currentTheme.Bg).RGBA()
		if r1 != r2 || g1 != g2 || b1 != b2 {
			t.Fatal("the background is not the theme's")
		}
	}

	_ = ApplyTheme("terminal")
	if v := m.View(); v.BackgroundColor != nil {
		t.Fatal("the terminal theme must keep the terminal's own colours")
	}

	m2 := newWideModel(t)
	updated, _ = m2.Update(teaTermColorsTimeoutMsg{})
	m2 = updated.(*teaModel)
	_ = ApplyTheme(defaultThemeName)
	if m2.View().BackgroundColor == nil {
		t.Fatal("a terminal that never answers should still get the theme's colours")
	}
}
