package harness

import (
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func editorFit(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = ansi.Truncate(text, width, "")
	return text + strings.Repeat(" ", max(0, width-ansi.StringWidth(text)))
}

func editorSplit(left, right string, width int) string {
	right = ansi.Truncate(right, max(0, width), "")
	return editorFit(left, width-ansi.StringWidth(right)) + right
}

func editorSurface(text, bg string) string {
	if !ColorsEnabled() || currentTheme.ANSI16 {
		return text
	}
	return fillSurface(text, bg)
}

func (m *teaModel) editorFocused() bool {
	return !m.blurred && (!m.source.active || m.source.pane == 1) && !m.editor.help && m.editor.prompt == editorPromptNone
}

func (m *teaModel) renderEditorHeader(width int) string {
	s := m.editor
	state := "Saved"
	if !s.exists {
		state = "New file"
	}
	if s.dirty {
		state = "Unsaved"
	}
	if !m.editorFocused() {
		state += " · unfocused"
	}
	right := " " + state + " "
	if s.dirty {
		right = ColorYellow(right)
	} else {
		right = styleMuted.Render(right)
	}
	path := sourceLabel(s.rel)
	if ansi.StringWidth(path)+ansi.StringWidth(right)+9 > width {
		path = sourceLabel(filepath.Base(s.rel))
	}
	title := ColorCyan(StyleBold(" Editor ")) + path
	return editorSurface(editorSplit(title, right, width), currentTheme.CardBg)
}

type editorAction struct {
	id, label string
}

func (m *teaModel) renderEditorActions(width, row int) string {
	actions := []editorAction{{"save", "Ctrl+S Save"}, {"diff", "Ctrl+D Diff"}, {"help", "F1 Help"}, {"close", "Esc Close"}}
	if width < 55 {
		actions = []editorAction{{"save", "Save"}, {"diff", "Diff"}, {"help", "F1 Help"}, {"close", "Close"}}
	}
	x, originX, originY := 1, 0, 0
	visible := true
	if m.source.active {
		sidebar, _, _ := m.sourceGeometry()
		originY = 2
		if m.width >= 90 {
			originX = sidebar + 1
		} else {
			visible = m.source.pane != 0
		}
	}
	var parts []string
	for _, action := range actions {
		label := action.label
		if x+ansi.StringWidth(label) > width {
			break
		}
		if visible {
			m.hits.add("editor:"+action.id, originX+x, originY+row, ansi.StringWidth(label), 1)
		}
		if action.id == "save" && m.sourceBusyReason() != "" {
			label = styleMuted.Render(label)
		} else {
			label = ColorCyan(label)
		}
		parts = append(parts, label)
		x += ansi.StringWidth(action.label) + 2
	}
	return editorFit(" "+strings.Join(parts, "  "), width)
}

func (m *teaModel) editorDialogOpen() bool {
	return m.editor != nil && (m.editor.help || m.editor.prompt != editorPromptNone)
}

// Dialogs own mouse input as well as keys, including clicks outside the panel.
func (m *teaModel) editorChromeMouse(msg tea.MouseMsg) (tea.Cmd, bool) {
	click, ok := msg.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft {
		return nil, m.editorDialogOpen()
	}
	id := m.hits.at(click.X, click.Y)
	if !strings.HasPrefix(id, "editor:") {
		return nil, m.editorDialogOpen()
	}
	key := tea.KeyPressMsg{}
	switch strings.TrimPrefix(id, "editor:") {
	case "save":
		key = tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "diff":
		key = tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	case "help":
		key.Code = tea.KeyF1
	case "close":
		key = tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}
	case "cancel":
		key.Code = tea.KeyEscape
	case "save-close":
		key = tea.KeyPressMsg{Code: 's', Text: "s"}
	case "discard":
		key = tea.KeyPressMsg{Code: 'd', Text: "d"}
	case "overwrite":
		key = tea.KeyPressMsg{Code: 'o', Text: "o"}
	case "reload":
		key = tea.KeyPressMsg{Code: 'r', Text: "r"}
	default:
		return nil, true
	}
	if m.source.active && !m.editorDialogOpen() {
		m.source.pane = 1
	}
	_, cmd := m.Update(key)
	return cmd, true
}

func (m *teaModel) renderEditorDialog(frame string) string {
	s := m.editor
	width := max(1, min(62, m.frameWidth()-8))
	var lines []string
	add := func(text string) { lines = append(lines, strings.Split(ansi.Hardwrap(text, width, false), "\n")...) }
	var actions []editorAction
	switch {
	case s.help:
		add(ColorCyan(StyleBold("Editor shortcuts")))
		add("Ctrl+S Save · Ctrl+D Diff · Esc Close")
		add("Ctrl+Z Undo · Ctrl+Y Redo")
		add("Ctrl+C / X / V Copy / Cut / Paste")
		add("Shift+arrows Select · Ctrl+A Select all")
		add("Tab Indent · Shift+Tab Outdent")
		add("Ctrl+Home / End Start / End of file")
		add("Alt+D Expand previous or deleted lines")
		if m.source.active {
			add("F6 / Shift+F6 Next / Previous pane")
		}
		add("+ Added · ~ Changed · previous lines are read-only")
		actions = []editorAction{{"cancel", "Esc Back to editor"}}
	case s.prompt == editorPromptClose:
		add(ColorYellow(StyleBold("Unsaved changes")))
		add("Save your edits before leaving this file?")
		actions = []editorAction{{"save-close", "S Save and continue"}, {"discard", "D Discard edits"}, {"cancel", "Esc Keep editing"}}
	case s.prompt == editorPromptConflict:
		add(ColorYellow(StyleBold("File changed on disk")))
		add("Choose which version to keep.")
		actions = []editorAction{{"overwrite", "O Overwrite disk with your edits"}, {"reload", "R Reload disk and discard your edits"}, {"cancel", "Esc Keep editing"}}
	}
	// Keep choices visible even for very long paths.
	path := sourceLabel(s.rel)
	if size := ansi.StringWidth(path); size > width {
		path = ansi.TruncateLeft(path, size-width+1, "…")
	}
	add(styleMuted.Render(path))
	if reason := m.sourceBusyReason(); reason != "" {
		add(ColorYellow(ansi.Truncate("Save unavailable: "+reason, width, "…")))
	}
	first := len(lines) + 1
	lines = append(lines, "")
	for _, action := range actions {
		lines = append(lines, ColorCyan(ansi.Truncate(action.label, width, "")))
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(tuiColorCyan).Padding(1, 2).Width(width + 6).Render(strings.Join(lines, "\n"))
	result := m.overlayModal(frame, editorSurface(box, currentTheme.CardBg))
	x, y := max(0, (m.width-lipgloss.Width(box))/2), max(0, (m.height-lipgloss.Height(box))/2)
	for i, action := range actions {
		m.hits.add("editor:"+action.id, x+3, y+2+first+i, min(width, ansi.StringWidth(action.label)), 1)
	}
	return result
}
