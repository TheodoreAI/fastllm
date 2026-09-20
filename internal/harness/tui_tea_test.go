package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fastllm/internal/llm"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

func TestInteractiveShellSupportsLS(t *testing.T) {
	cmd := newInteractiveShellCommand("ls")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ls failed: %v\n%s", err, out)
	}
}

func TestTeaShellDirectoryChangePersists(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}

	m := &teaModel{workingDir: root}
	if cmd := m.handleShellSubmit("cd child"); cmd != nil {
		t.Fatal("directory changes should complete synchronously")
	}
	if m.workingDir != child {
		t.Fatalf("working directory = %q; want %q", m.workingDir, child)
	}
	if !strings.Contains(m.historyText.String(), child) {
		t.Fatalf("history does not report changed directory: %q", m.historyText.String())
	}
}

func TestAgentPromptDoesNotAddBlankLines(t *testing.T) {
	got := stripANSI(formatSubmittedPrompt("okay in laymens terms"))
	if got != "\n\u25cf YOU  okay in laymens terms\n" {
		t.Fatalf("formatted prompt = %q; want labeled compact prompt", got)
	}
}

func TestAssistantAnswerHasVisibleBoundary(t *testing.T) {
	got := stripANSI(formatAssistantAnswer("The answer is 42.", 80))
	if !strings.Contains(got, "\n\u25cf ASSISTANT\nThe answer is 42.\n") {
		t.Fatalf("assistant answer lacks a visible boundary: %q", got)
	}
}

func TestCopyTranscriptStripsANSI(t *testing.T) {
	styled := formatSubmittedPrompt("question") + formatAssistantAnswer("answer", 80)
	plain := StripANSI(styled)
	if strings.Contains(plain, "\x1b[") || !strings.Contains(plain, "\u25cf YOU  question") || !strings.Contains(plain, "\u25cf ASSISTANT\nanswer") {
		t.Fatalf("unexpected plain transcript: %q", plain)
	}
}

func TestTaskFinishedRendersToolProvidedFinalAnswer(t *testing.T) {
	ta := textarea.New()
	vp := viewport.New(80, 10)
	m := &teaModel{input: ta, viewport: vp, ready: true, width: 80}

	updated, _ := m.Update(teaAgentEventMsg(Event{
		Type: EventTaskFinished,
		Result: &RunResult{
			Turns:         2,
			FinalResponse: "Finished through the tool.",
		},
	}))
	m = updated.(*teaModel)

	plain := StripANSI(m.historyText.String())
	if !strings.Contains(plain, "\u25cf ASSISTANT\nFinished through the tool.") {
		t.Fatalf("final answer was not rendered: %q", plain)
	}
	if m.lastResponse != "Finished through the tool." {
		t.Fatalf("last response = %q", m.lastResponse)
	}
}

func TestMouseWheelScrollsViewportWithoutChangingPromptHistory(t *testing.T) {
	ta := textarea.New()
	ta.Focus()
	vp := viewport.New(80, 3)
	vp.SetContent("one\ntwo\nthree\nfour\nfive\nsix")
	vp.GotoBottom()

	m := &teaModel{
		input:         ta,
		viewport:      vp,
		promptHistory: []string{"first", "second"},
		historyIdx:    -1,
		ready:         true,
	}
	initialOffset := m.viewport.YOffset

	updated, _ := m.Update(tea.MouseMsg{
		X: 1, Y: 1, Type: tea.MouseWheelUp, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress,
	})
	m = updated.(*teaModel)

	if m.historyIdx != -1 || m.input.Value() != "" {
		t.Fatalf("mouse wheel changed prompt history: index=%d input=%q", m.historyIdx, m.input.Value())
	}
	if m.viewport.YOffset >= initialOffset {
		t.Fatalf("mouse wheel did not scroll viewport: before=%d after=%d", initialOffset, m.viewport.YOffset)
	}
}

func TestPromptHistoryNavigation(t *testing.T) {
	ta := textarea.New()
	ta.Focus()

	m := &teaModel{
		input:         ta,
		promptHistory: []string{"first prompt", "second prompt", "third prompt"},
		historyIdx:    -1,
		ready:         true,
	}

	// 1. User types an unfinished draft
	m.input.SetValue("my unfinished draft")

	// 2. Press Up: should recall "third prompt" (the latest in history)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*teaModel)

	if m.historyIdx != 2 {
		t.Fatalf("expected historyIdx 2, got %d", m.historyIdx)
	}
	if m.input.Value() != "third prompt" {
		t.Fatalf("expected 'third prompt', got %q", m.input.Value())
	}
	if m.historyDraft != "my unfinished draft" {
		t.Fatalf("expected historyDraft 'my unfinished draft', got %q", m.historyDraft)
	}

	// 3. Press Up again: should recall "second prompt"
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*teaModel)

	if m.historyIdx != 1 {
		t.Fatalf("expected historyIdx 1, got %d", m.historyIdx)
	}
	if m.input.Value() != "second prompt" {
		t.Fatalf("expected 'second prompt', got %q", m.input.Value())
	}

	// 4. Press Up again: should recall "first prompt"
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*teaModel)

	if m.historyIdx != 0 {
		t.Fatalf("expected historyIdx 0, got %d", m.historyIdx)
	}
	if m.input.Value() != "first prompt" {
		t.Fatalf("expected 'first prompt', got %q", m.input.Value())
	}

	// 5. Press Up at oldest item: should stay at 0
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*teaModel)

	if m.historyIdx != 0 {
		t.Fatalf("expected historyIdx 0, got %d", m.historyIdx)
	}
	if m.input.Value() != "first prompt" {
		t.Fatalf("expected 'first prompt', got %q", m.input.Value())
	}

	// 6. Press Down: should move forward to "second prompt"
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*teaModel)

	if m.historyIdx != 1 {
		t.Fatalf("expected historyIdx 1, got %d", m.historyIdx)
	}
	if m.input.Value() != "second prompt" {
		t.Fatalf("expected 'second prompt', got %q", m.input.Value())
	}

	// 7. Press Down again: should move forward to "third prompt"
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*teaModel)

	if m.historyIdx != 2 {
		t.Fatalf("expected historyIdx 2, got %d", m.historyIdx)
	}
	if m.input.Value() != "third prompt" {
		t.Fatalf("expected 'third prompt', got %q", m.input.Value())
	}

	// 8. Press Down past the latest item: should restore "my unfinished draft"
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*teaModel)

	if m.historyIdx != -1 {
		t.Fatalf("expected historyIdx -1, got %d", m.historyIdx)
	}
	if m.input.Value() != "my unfinished draft" {
		t.Fatalf("expected restored draft 'my unfinished draft', got %q", m.input.Value())
	}

	// 9. Press Up to browse, then Esc: should cancel and restore draft
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*teaModel)
	if m.historyIdx != 2 {
		t.Fatalf("expected historyIdx 2, got %d", m.historyIdx)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*teaModel)
	if m.historyIdx != -1 {
		t.Fatalf("expected historyIdx -1 after Esc, got %d", m.historyIdx)
	}
	if m.input.Value() != "my unfinished draft" {
		t.Fatalf("expected restored draft 'my unfinished draft' after Esc, got %q", m.input.Value())
	}
}

func TestAddPromptHistory(t *testing.T) {
	m := &teaModel{
		promptHistory: make([]string, 0),
		historyIdx:    -1,
	}

	m.addPromptHistory("cmd 1")
	if len(m.promptHistory) != 1 || m.promptHistory[0] != "cmd 1" {
		t.Fatalf("expected [cmd 1], got %v", m.promptHistory)
	}

	// Consecutive duplicate should not be appended
	m.addPromptHistory("cmd 1")
	if len(m.promptHistory) != 1 {
		t.Fatalf("expected no duplicate, got %v", m.promptHistory)
	}

	m.addPromptHistory("cmd 2")
	if len(m.promptHistory) != 2 || m.promptHistory[1] != "cmd 2" {
		t.Fatalf("expected [cmd 1, cmd 2], got %v", m.promptHistory)
	}
}

func TestTeaInitialMessagesCarryConversationContext(t *testing.T) {
	m := &teaModel{sessionMessages: []llm.Message{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "tool", Content: "old tool output"},
	}}
	got := m.initialMessages()
	if len(got) != 2 || got[0].Role != "user" || got[0].Content != "first question" || got[1].Role != "assistant" {
		t.Fatalf("initial messages = %#v", got)
	}
}

func TestTeaRuntimeSettingsValidateAndPersistValues(t *testing.T) {
	m := &teaModel{sessionGrants: map[string]bool{"write_file": true}}
	for _, tc := range []struct{ name, value string }{
		{"turns", "42"}, {"timeout", "90"}, {"think", "high"},
		{"commands", "off"}, {"permissions", "read-only"}, {"output", "expanded"},
	} {
		if err := m.setRuntimeValue(tc.name, tc.value); err != nil {
			t.Fatalf("set %s: %v", tc.name, err)
		}
	}
	if m.maxTurns != 42 || m.commandTimeout != 90*time.Second || m.thinkLevel != "high" || m.allowCommands || m.permissionMode != PermissionReadOnly || !m.expandedTools {
		t.Fatalf("unexpected runtime state: %+v", m.runtimeSettings())
	}
	if len(m.sessionGrants) != 0 {
		t.Fatal("changing permission mode must clear session grants")
	}
	if err := m.setRuntimeValue("turns", "101"); err == nil {
		t.Fatal("expected invalid turn limit to fail")
	}
}

func TestTeaSessionRoundTripIncludesMetricsAndMessages(t *testing.T) {
	store := &SessionStore{Dir: t.TempDir()}
	m := &teaModel{
		workingDir: t.TempDir(), modelName: "test-model", sessionStore: store,
		maxTurns: 20, commandTimeout: time.Minute, allowCommands: true,
		permissionMode: PermissionAsk, sessionGrants: make(map[string]bool),
		sessionMessages: []llm.Message{{Role: "user", Content: "remember this"}, {Role: "assistant", Content: "remembered"}},
		sessionMetrics:  SessionMetrics{TotalTurns: 2, TotalTokens: 123},
	}
	m.activeSession = store.New(m.workingDir, m.modelName, m.runtimeSettings())
	if err := m.saveSession(); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(m.activeSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 2 || loaded.Metrics.TotalTokens != 123 || loaded.Runtime.PermissionMode != PermissionAsk {
		t.Fatalf("loaded session = %+v", loaded)
	}
}

func TestTeaCompactSessionContextTrimsUnboundedTranscript(t *testing.T) {
	store := &SessionStore{Dir: t.TempDir()}
	cfg := DefaultCompactionConfig()
	m := &teaModel{
		workingDir: t.TempDir(), modelName: "test-model", sessionStore: store,
		maxTurns: 20, commandTimeout: time.Minute, allowCommands: true,
		permissionMode: PermissionAsk, sessionGrants: make(map[string]bool),
	}
	// Well past the budget, and long enough that KeepRecentMessages cannot hold it all.
	turn := strings.Repeat("x", 4000)
	for i := 0; i < 40; i++ {
		m.sessionMessages = append(m.sessionMessages,
			llm.Message{Role: "user", Content: turn},
			llm.Message{Role: "assistant", Content: turn})
	}
	m.activeSession = store.New(m.workingDir, m.modelName, m.runtimeSettings())

	before := messageCharacterCount(m.sessionMessages)
	if before <= cfg.MaxTotalChars {
		t.Fatalf("fixture is inside budget (%d <= %d); test proves nothing", before, cfg.MaxTotalChars)
	}

	notice := m.compactSessionContext()
	after := messageCharacterCount(m.sessionMessages)
	if after >= before {
		t.Fatalf("transcript not compacted: %d -> %d", before, after)
	}
	if notice == "" {
		t.Fatal("expected a compaction notice")
	}

	// The compacted transcript is what persists and what gets replayed.
	loaded, err := store.Load(m.activeSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	if messageCharacterCount(loaded.Messages) != after {
		t.Fatalf("saved session not compacted: %d != %d", messageCharacterCount(loaded.Messages), after)
	}
}

func TestTeaCompactSessionContextQuietWellInsideBudget(t *testing.T) {
	m := &teaModel{sessionMessages: []llm.Message{
		{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"},
	}}
	if notice := m.compactSessionContext(); notice != "" {
		t.Fatalf("expected no notice for a short transcript, got %q", notice)
	}
}
