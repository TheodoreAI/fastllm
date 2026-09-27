package harness

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Click targets. A change row's ID carries the file's index in changes.files.
const (
	hitModel      = "model"
	hitMode       = "mode"
	hitModal      = "modal"
	hitChangeFile = "change:"
)

// hitMap records where the clickable parts of the last rendered frame are, as
// lipgloss layers, so a click resolves against what is actually on screen.
// The layers only carry bounds and are never drawn.
type hitMap struct {
	layers []*lipgloss.Layer
	comp   *lipgloss.Compositor
}

func (h *hitMap) reset() {
	h.layers = h.layers[:0]
	h.comp = nil
}

// add registers a w-by-rows region at (x, y) under id.
func (h *hitMap) add(id string, x, y, w, rows int) {
	if w < 1 || rows < 1 {
		return
	}
	block := strings.TrimSuffix(strings.Repeat(strings.Repeat(" ", w)+"\n", rows), "\n")
	h.layers = append(h.layers, lipgloss.NewLayer(block).ID(id).X(x).Y(y))
	h.comp = nil
}

// at returns the ID of the target under (x, y), or "" for none.
func (h *hitMap) at(x, y int) string {
	if len(h.layers) == 0 {
		return ""
	}
	if h.comp == nil {
		h.comp = lipgloss.NewCompositor(h.layers...)
	}
	return h.comp.Hit(x, y).ID()
}
