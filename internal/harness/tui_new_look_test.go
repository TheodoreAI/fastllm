package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"fastllm/internal/llm"
	"github.com/muesli/termenv"
)

func newLookModel(t *testing.T, width, height int, conversation bool) *teaModel {
	t.Helper()
	m := newWideModel(t)
	m.input = newChatInput()
	m.updatePromptAndPlaceholder()
	if conversation {
		m.sessionMessages = []llm.Message{{Role: "user", Content: "Explain this code"}}
		m.appendHistory(formatSubmittedPrompt("Explain this code"))
		m.appendHistory(formatAssistantAnswer("The handler validates the request and starts a bounded agent run.", width-4))
	}
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func assertNewLookFrame(t *testing.T, m *teaModel) string {
	t.Helper()
	frame := m.render()
	lines := strings.Split(frame, "\n")
	if len(lines) != m.height {
		t.Fatalf("frame has %d rows, terminal has %d", len(lines), m.height)
	}
	for i, line := range lines {
		if VisualLen(line) > m.frameWidth() {
			t.Fatalf("row %d exceeds frame: %d > %d", i, VisualLen(line), m.frameWidth())
		}
	}
	if cursor := m.terminalCursor(); cursor != nil && (cursor.X < 0 || cursor.X >= m.width || cursor.Y < 0 || cursor.Y >= m.height) {
		t.Fatalf("cursor outside terminal: %#v", cursor)
	}
	return StripANSI(frame)
}

func TestNewLookLayoutsFitTerminal(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 30}, {160, 45}, {24, 10}} {
		for _, conversation := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dx%d/conversation=%v", size[0], size[1], conversation), func(t *testing.T) {
				m := newLookModel(t, size[0], size[1], conversation)
				frame := assertNewLookFrame(t, m)
				if !strings.Contains(frame, "fastllm") && !strings.Contains(frame, "████") {
					t.Fatal("missing brand")
				}
				if size[0] >= 80 && !strings.Contains(frame, "commands") {
					t.Fatal("footer hints missing")
				}
				if conversation == m.showHome() {
					t.Fatal("incorrect home/session state")
				}
				m.input.SetValue("first line\nsecond line\nthird line")
				m.pendingAttachments = []llm.Attachment{{Name: "reference.png", Type: "image"}}
				assertNewLookFrame(t, m)
				m.openCommandPalette()
				assertNewLookFrame(t, m)
			})
		}
	}
}

func TestNewLookComposerClickTargetsFollowResize(t *testing.T) {
	m := newLookModel(t, 80, 24, false)
	for _, size := range [][2]int{{80, 24}, {160, 45}, {100, 30}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.pendingAttachments = []llm.Attachment{{Name: "notes.pdf", Type: "document"}}
		frame := m.render()
		l := m.layout()
		y := frameRowContaining(t, frame, l.composerX, m.modelName)
		x := frameColumnOf(t, frame, y, m.modelName)
		click(m, x+1, y)
		if !m.modelsModal {
			t.Fatal("composer model target did not open picker")
		}
		m.closeModelsModal()
		m.render()
		if m.inputOrigin.X != l.composerX+3 || m.inputOrigin.Y != l.composerY+2 {
			t.Fatalf("attachment shifted cursor incorrectly: %#v", m.inputOrigin)
		}
	}
}

func TestNewLookPaletteStagesDestructiveAndArgumentCommands(t *testing.T) {
	for _, query := range []string{"/discard", "/set turns", "/attach", "/kill", "/clear"} {
		m := newLookModel(t, 80, 24, false)
		m.openCommandPalette()
		m.commandPalette.query = query
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.commandPalette != nil || m.input.Value() == "" || m.isExecuting {
			t.Fatalf("%s was not staged safely", query)
		}
		if m.historyText.Len() != 0 {
			t.Fatalf("%s executed from palette", query)
		}
	}
	for _, feature := range []string{"image", "sandbox", "audit", "background", "import", "skills", "permissions", "/edit"} {
		if len(commandActions(feature)) == 0 {
			t.Fatalf("palette cannot discover %s", feature)
		}
	}
}

func TestNewLookLeaderTimeoutAndPalettePreserveDraft(t *testing.T) {
	m := newLookModel(t, 80, 24, false)
	m.input.SetValue("unfinished draft")
	m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	first := m.leaderGeneration
	m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	m.Update(teaLeaderTimeoutMsg{generation: first})
	if !m.leaderPending {
		t.Fatal("stale timeout canceled current leader")
	}
	m.Update(teaLeaderTimeoutMsg{generation: m.leaderGeneration})
	if m.leaderPending {
		t.Fatal("leader did not expire")
	}
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.commandPalette == nil {
		t.Fatal("Ctrl+P did not open palette")
	}
	m.Update(tea.PasteMsg{Content: "must not enter draft"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.input.Value() != "unfinished draft" {
		t.Fatal("palette changed draft")
	}
}

func TestNewLookPaletteOpensOptionalArgumentPickers(t *testing.T) {
	m := newLookModel(t, 80, 24, false)
	m.openCommandPalette()
	m.commandPalette.query = "/theme"
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.themeModal == nil || m.commandPalette != nil {
		t.Fatal("theme action did not open its picker")
	}
	m.closeThemeModal()
}

func TestNewLookApprovalRemainsVisibleOverPalette(t *testing.T) {
	m := newLookModel(t, 80, 24, true)
	m.openCommandPalette()
	reply := make(chan permissionDecision, 1)
	m.pendingPermission = &teaPermissionRequestMsg{ToolName: "write_file", Summary: "path=notes.txt", Reply: reply}
	frame := m.render()
	if !strings.Contains(StripANSI(frame), "Permission required") || strings.Contains(StripANSI(frame), "Search commands") {
		t.Fatal("palette obscured the approval")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if (<-reply).Allow {
		t.Fatal("Escape approved permission")
	}
}

func TestNewLookAutocompleteOwnsTabAndShiftTab(t *testing.T) {
	m := newLookModel(t, 80, 24, false)
	m.historyIdx = -1
	m.input.SetValue("/the")
	m.refreshSuggestions()
	before := m.permissionMode
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.permissionMode != before || !strings.HasPrefix(m.input.Value(), "/theme") {
		t.Fatal("autocomplete did not own Tab keys")
	}
}

func TestNewLookTabModesAndShellRemainAvailable(t *testing.T) {
	m := newLookModel(t, 80, 24, false)
	m.permissionMode = PermissionPlan
	for _, mode := range []PermissionMode{PermissionAgent, PermissionEdit, PermissionFull, PermissionPlan} {
		m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		if m.permissionMode != mode {
			t.Fatalf("Tab selected %s, want %s", m.permissionMode, mode)
		}
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.permissionMode != PermissionFull {
		t.Fatal("Shift+Tab did not reverse cycle")
	}
	m.handleAgentSubmit("/shell")
	if m.mode != modeShell {
		t.Fatal("shell unavailable")
	}
	m.handleAgentSubmit("/shell")
	if m.mode != modeAgent {
		t.Fatal("shell did not toggle back")
	}
	m.isExecuting = true
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.permissionMode != PermissionFull {
		t.Fatal("permissions changed during run")
	}
}

func TestNewLookPermissionScrollAndInputPriority(t *testing.T) {
	m := newLookModel(t, 40, 16, true)
	m.input.SetValue("draft")
	reply := make(chan permissionDecision, 1)
	m.pendingPermission = &teaPermissionRequestMsg{ToolName: "run_command", Summary: strings.Repeat("full request details ", 80) + "END OF REQUEST", Reply: reply}
	frame := assertNewLookFrame(t, m)
	if !strings.Contains(frame, "PgUp/PgDn") {
		t.Fatalf("long approval has no scrolling hint:\n%s", frame)
	}
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.commandPalette != nil {
		t.Fatal("palette stole permission input")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.approvalOffset == 0 {
		t.Fatal("approval did not scroll")
	}
	m.approvalOffset = 10000
	frame = assertNewLookFrame(t, m)
	if !strings.Contains(frame, "END OF REQUEST") {
		t.Fatal("request tail inaccessible")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if (<-reply).Allow {
		t.Fatal("Enter granted permission")
	}
	if m.input.Value() != "draft" {
		t.Fatal("approval changed draft")
	}
}

func TestNewLookSidebarTogglePreservesConversation(t *testing.T) {
	m := newLookModel(t, 160, 45, true)
	before := m.historyText.String()
	if !m.showChangesColumn() {
		t.Fatal("wide sidebar hidden")
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	if m.showChangesColumn() || m.historyText.String() != before {
		t.Fatal("sidebar toggle changed conversation")
	}
	m.handleAgentSubmit("/sidebar")
	if !m.showChangesColumn() {
		t.Fatal("sidebar command did not restore sidebar")
	}
	assertNewLookFrame(t, m)
}

// Set FASTLLM_TUI_PREVIEW_DIR to export actual rendered frames for visual review.
func TestNewLookPreviewFrames(t *testing.T) {
	dir := os.Getenv("FASTLLM_TUI_PREVIEW_DIR")
	if dir == "" {
		t.Skip("optional preview export")
	}
	isolateTheme(t)
	colorProfile = func() termenv.Profile { return termenv.TrueColor }
	if err := ApplyTheme(defaultThemeName); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"home", "conversation", "palette", "permission"} {
		m := newLookModel(t, 160, 45, scenario != "home")
		m.workingDir = "C:/projects/fastllm"
		if scenario == "palette" {
			m.openCommandPalette()
			m.commandPalette.query = ""
		}
		if scenario == "permission" {
			m.pendingPermission = &teaPermissionRequestMsg{ToolName: "write_file", Summary: "Write internal/harness/tui_layout.go in the active workspace"}
		}
		frame := m.render()
		if err := os.WriteFile(filepath.Join(dir, scenario+".ansi"), []byte(frame), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, scenario+".txt"), []byte(StripANSI(frame)), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
