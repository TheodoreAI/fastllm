package harness

import (
	"image/color"
	"strings"
	"testing"

	"fastllm/internal/config"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

// newWideModel is a ready model wide enough to show the sidebar.
func newWideModel(t *testing.T) *teaModel {
	t.Helper()
	tmp := t.TempDir()
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Focus()
	return &teaModel{
		runner:         NewRunner(&mockLLM{}, tmp, "test-model"),
		workingDir:     tmp,
		checkpointMgr:  NewCheckpointManager(tmp),
		modelName:      "test-model",
		input:          ta,
		viewport:       viewport.New(viewport.WithWidth(140), viewport.WithHeight(20)),
		width:          140,
		height:         30,
		ready:          true,
		permissionMode: PermissionAgent,
		settings:       &config.Settings{Models: []config.ModelEndpoint{{ID: "test-model"}, {ID: "other-model"}}},
	}
}

// frameRowContaining returns the row of frame showing want at or after column
// x, so a test clicks where the text is actually drawn.
func frameRowContaining(t *testing.T, frame string, x int, want string) int {
	t.Helper()
	for y, line := range strings.Split(StripANSI(frame), "\n") {
		if runes := []rune(line); x < len(runes) && strings.Contains(string(runes[x:]), want) {
			return y
		}
	}
	t.Fatalf("no row shows %q from column %d:\n%s", want, x, StripANSI(frame))
	return -1
}

// frameColumnOf returns the column where want starts on row y of frame.
func frameColumnOf(t *testing.T, frame string, y int, want string) int {
	t.Helper()
	line := strings.Split(StripANSI(frame), "\n")[y]
	i := strings.Index(line, want)
	if i < 0 {
		t.Fatalf("row %d does not show %q: %q", y, want, line)
	}
	return len([]rune(line[:i]))
}

func click(m *teaModel, x, y int) *teaModel {
	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	return updated.(*teaModel)
}

func TestClickingHeaderModelOpensModelPicker(t *testing.T) {
	m := newWideModel(t)
	frame := m.render()
	m = click(m, frameColumnOf(t, frame, 0, "test-model")+2, 0)
	if !m.modelsModal {
		t.Fatal("clicking the model name in the header did not open the model picker")
	}
}

func TestClickingModeBadgeCyclesPermissionMode(t *testing.T) {
	m := newWideModel(t)
	frame := m.render()
	before := m.permissionMode
	m = click(m, frameColumnOf(t, frame, 0, strings.ToUpper(before.Label())), 0)
	if m.permissionMode == before {
		t.Fatalf("clicking the mode badge left the mode at %q", before)
	}
}

// A modal is drawn over the conversation rather than replacing it, a click
// inside it is ignored, and a click outside it dismisses it like Esc.
func TestModalOverlaysChatAndOutsideClickCloses(t *testing.T) {
	m := newWideModel(t)
	original := currentTheme.Name
	chatRows := strings.Count(m.renderChat(), "\n") + 1
	m.openThemeModal()
	frame := StripANSI(m.render())
	rows := strings.Split(frame, "\n")
	if !strings.Contains(rows[0], "fastllm") {
		t.Fatalf("the header should stay visible around the modal:\n%s", frame)
	}
	if !strings.Contains(frame, "Enter save") {
		t.Fatalf("the theme picker is not drawn:\n%s", frame)
	}
	if len(rows) != chatRows {
		t.Fatalf("the overlay made the frame %d rows; the chat underneath is %d", len(rows), chatRows)
	}

	m = click(m, m.width/2, m.height/2)
	if m.themeModal == nil {
		t.Fatal("a click inside the modal closed it")
	}
	m.render()
	m = click(m, 0, 0)
	if m.themeModal != nil {
		t.Fatal("a click outside the modal did not close it")
	}
	if currentTheme.Name != original {
		t.Fatalf("dismissing the picker left theme %q; want %q restored", currentTheme.Name, original)
	}
}

func TestWindowTitleAndProgressBarFollowState(t *testing.T) {
	m := newWideModel(t)

	if v := m.View(); v.ProgressBar != nil || !strings.HasPrefix(v.WindowTitle, "fastllm") {
		t.Fatalf("idle: progress %v, title %q", v.ProgressBar, v.WindowTitle)
	}

	m.isExecuting, m.activeTurn, m.activeTool = true, 3, "edit_file"
	v := m.View()
	if v.ProgressBar == nil || v.ProgressBar.State != tea.ProgressBarIndeterminate {
		t.Fatalf("running: progress %v", v.ProgressBar)
	}
	if !strings.Contains(v.WindowTitle, "turn 3") || !strings.Contains(v.WindowTitle, "edit_file") {
		t.Fatalf("running: title %q", v.WindowTitle)
	}

	m.pendingPermission = &teaPermissionRequestMsg{ToolName: "write_file"}
	if v := m.View(); v.ProgressBar.State != tea.ProgressBarWarning || !strings.Contains(v.WindowTitle, "approval") {
		t.Fatalf("awaiting approval: progress %v, title %q", v.ProgressBar, v.WindowTitle)
	}

	m.pendingPermission, m.isExecuting, m.turnFailed = nil, false, true
	if v := m.View(); v.ProgressBar.State != tea.ProgressBarError || !strings.Contains(v.WindowTitle, "failed") {
		t.Fatalf("failed: progress %v, title %q", v.ProgressBar, v.WindowTitle)
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = updated.(*teaModel)
	if m.View().ProgressBar != nil {
		t.Fatal("a key press did not clear the failed state")
	}
}

// A turn that ends while the window is in the background is flagged in the
// title until the window regains focus.
func TestTurnFinishedWhileBlurredIsFlagged(t *testing.T) {
	m := newBusyModel(t, &mockLLM{})
	m.width, m.height = 100, 30
	m.isExecuting = true

	updated, _ := m.Update(tea.BlurMsg{})
	m = updated.(*teaModel)
	updated, _ = m.Update(teaAgentEventMsg(Event{Type: EventTaskFinished}))
	m = updated.(*teaModel)
	if !m.finishedAway || !strings.HasPrefix(m.windowTitle(), "done") {
		t.Fatalf("finishedAway=%v title=%q", m.finishedAway, m.windowTitle())
	}

	updated, _ = m.Update(tea.FocusMsg{})
	m = updated.(*teaModel)
	if m.finishedAway || m.blurred {
		t.Fatal("focus did not clear the away state")
	}
}

// A cancelled turn is not a failure, even though the run reports an error.
func TestCanceledTurnIsNotReportedAsFailed(t *testing.T) {
	m := newBusyModel(t, &mockLLM{})
	m.isExecuting = true
	m.cancelTurn = func() {}
	m.cancelActiveOperation()
	updated, _ := m.Update(teaAgentEventMsg(Event{Type: EventTaskFinished, Error: "LLM chat error on turn 1: context canceled"}))
	m = updated.(*teaModel)
	if m.turnFailed {
		t.Fatal("a canceled turn was reported as failed")
	}

	m.isExecuting = true
	updated, _ = m.Update(teaAgentEventMsg(Event{Type: EventTaskFinished, Error: "LLM chat error on turn 1: boom"}))
	m = updated.(*teaModel)
	if !m.turnFailed {
		t.Fatal("a failed turn was not reported")
	}
}

func TestLightTerminalSwitchesDefaultTheme(t *testing.T) {
	t.Cleanup(func() {
		themeFromPreference = false
		_ = ApplyTheme(defaultThemeName)
	})
	_ = ApplyTheme(defaultThemeName)
	themeFromPreference = false
	m := newWideModel(t)
	m.historyText.Reset()
	m.appendHistory(m.formatWelcome())

	updated, _ := m.Update(tea.BackgroundColorMsg{Color: color.White})
	m = updated.(*teaModel)
	if currentTheme.Name != defaultLightThemeName {
		t.Fatalf("theme on a light terminal = %q; want %q", currentTheme.Name, defaultLightThemeName)
	}
	if diffBg != lightDiffBackgrounds {
		t.Fatal("diffs kept their dark backgrounds on a light theme")
	}
	if m.historyText.String() != trimPaddedTail(m.formatWelcome()) {
		t.Fatal("the greeting was not redrawn in the light theme")
	}

	// A theme the user saved is theirs to keep.
	_ = ApplyTheme("gruvbox")
	themeFromPreference = true
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	if currentTheme.Name != "gruvbox" {
		t.Fatalf("a saved theme was replaced by %q", currentTheme.Name)
	}
}

func TestLastCodeBlock(t *testing.T) {
	text := "intro\n```go\nfirst()\n```\nmiddle\n```\nsecond()\nthird()\n```\nend"
	if got := lastCodeBlock(text); got != "second()\nthird()" {
		t.Fatalf("lastCodeBlock = %q", got)
	}
	if got := lastCodeBlock("no code here"); got != "" {
		t.Fatalf("lastCodeBlock without a fence = %q", got)
	}
}

// stubClipboard records what the model writes to the local clipboard.
func stubClipboard(t *testing.T) *string {
	t.Helper()
	var got string
	previous := writeClipboard
	writeClipboard = func(s string) error { got = s; return nil }
	t.Cleanup(func() { writeClipboard = previous })
	return &got
}

func TestCtrlCCopiesInputSelection(t *testing.T) {
	copied := stubClipboard(t)
	m := newBusyModel(t, &mockLLM{})
	m.input.SetValue("hello world")

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}) // select all
	m = updated.(*teaModel)
	if !m.input.HasSelection() {
		t.Fatal("ctrl+g did not select the input")
	}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = updated.(*teaModel)
	if *copied != "hello world" {
		t.Fatalf("copied %q; want the selection", *copied)
	}
	if cmd == nil || m.input.Value() != "hello world" {
		t.Fatal("ctrl+c with a selection must copy, not quit or clear the input")
	}
}

func TestCopyCodeCopiesLastCodeBlock(t *testing.T) {
	copied := stubClipboard(t)
	m := newBusyModel(t, &mockLLM{})
	m.lastResponse = "Run this:\n```sh\ngo test ./...\n```\n"
	m.handleAgentSubmit("/copy code")
	if *copied != "go test ./..." {
		t.Fatalf("/copy code copied %q", *copied)
	}
}

func TestNewlineHintFollowsKeyboardSupport(t *testing.T) {
	m := newWideModel(t)
	if m.newlineKey() != "Ctrl+J" {
		t.Fatalf("without disambiguation the hint is %q", m.newlineKey())
	}
	updated, _ := m.Update(tea.KeyboardEnhancementsMsg{Flags: 1})
	m = updated.(*teaModel)
	if m.newlineKey() != "Shift+Enter" {
		t.Fatalf("with disambiguation the hint is %q", m.newlineKey())
	}
}
