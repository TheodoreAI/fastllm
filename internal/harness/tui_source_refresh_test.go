package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A live Git refresh of the diff already on screen must not blank it or
// jump the scroll back to the top.
func TestSourceRefreshKeepsDiffScroll(t *testing.T) {
	root, m := sourceFixture(t)
	var b strings.Builder
	for range 300 {
		b.WriteString("changed line\n")
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.Update(m.refreshSourceCmd()())
	selectSource(t, m, "Changes", "file.txt")
	m.Update(m.loadSourceSelection()())
	m.render()
	m.source.view.ScrollDown(40)
	offset := m.source.view.YOffset()
	if offset == 0 {
		t.Fatal("fixture diff did not scroll")
	}

	cmd := m.refreshSourceCmd()
	m.Update(cmd())
	if strings.Contains(m.source.text, "Loading diff") {
		t.Fatal("refresh blanked the diff on screen")
	}
	if diffCmd := m.loadSourceSelection(); diffCmd != nil {
		m.Update(diffCmd())
	}
	m.render()
	if got := m.source.view.YOffset(); got != offset {
		t.Fatalf("refresh moved diff scroll from %d to %d", offset, got)
	}
}
