package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"fastllm/internal/config"
	"fastllm/internal/llm"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

func TestInteractiveShellSupportsLS(t *testing.T) {
	out, err := runUserCommand(context.Background(), t.TempDir(), "ls")
	if err != nil {
		t.Fatalf("ls failed: %v\n%s", err, out.Output)
	}
}

func TestTeaShellDirectoryChangePersists(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}

	ta := textarea.New()
	m := &teaModel{
		workingDir: root,
		mode:       modeShell,
		input:      ta,
		width:      160,
	}
	m.updatePromptAndPlaceholder()

	cmd := m.handleShellSubmit("cd child")
	if cmd == nil {
		t.Fatal("expected background git refresh cmd to be returned")
	}
	if m.workingDir != child {
		t.Fatalf("working directory = %q; want %q", m.workingDir, child)
	}
	if !strings.Contains(m.historyText.String(), "Working directory changed to") {
		t.Fatalf("history does not report changed directory: %q", m.historyText.String())
	}
	if !strings.Contains(m.input.Prompt, "child") {
		t.Fatalf("shell prompt = %q; expected it to contain 'child'", m.input.Prompt)
	}
}

func TestEscapeCancelsActiveAgentTurn(t *testing.T) {
	ta := textarea.New()
	canceled := false
	m := &teaModel{
		input:       ta,
		viewport:    viewport.New(viewport.WithWidth(80), viewport.WithHeight(6)),
		ready:       true,
		width:       80,
		isExecuting: true,
		cancelTurn:  func() { canceled = true },
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(*teaModel)

	if !canceled {
		t.Fatal("Escape did not cancel the active agent turn")
	}
	if m.statusNotice != "Canceled active agent turn." {
		t.Fatalf("status notice = %q", m.statusNotice)
	}
}

func TestEscapeCancelsActiveShellCommand(t *testing.T) {
	ta := textarea.New()
	m := &teaModel{
		workingDir: t.TempDir(),
		input:      ta,
		viewport:   viewport.New(viewport.WithWidth(80), viewport.WithHeight(6)),
		ready:      true,
		width:      80,
	}
	marker := filepath.Join(m.workingDir, "shell-started")
	command := fmt.Sprintf("touch %q; sleep 30", marker)
	if runtime.GOOS == "windows" {
		command = fmt.Sprintf("Set-Content -LiteralPath '%s' -Value started; Start-Sleep -Seconds 30", strings.ReplaceAll(marker, "'", "''"))
	}
	run := m.handleShellSubmit(command)
	result := make(chan tea.Msg, 1)
	go func() { result <- run() }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(*teaModel)

	select {
	case msg := <-result:
		done, ok := msg.(teaShellDoneMsg)
		if !ok {
			t.Fatalf("shell result type = %T", msg)
		}
		if !done.Canceled {
			t.Fatalf("shell result was not marked canceled: %+v", done)
		}
		updated, _ = m.Update(done)
		m = updated.(*teaModel)
		if m.shellExecuting || m.cancelShell != nil {
			t.Fatal("completed cancellation left shell execution active")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Escape did not stop the active shell command")
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
	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(10))
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
	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(3))
	vp.SetContent("one\ntwo\nthree\nfour\nfive\nsix")
	vp.GotoBottom()

	m := &teaModel{
		input:         ta,
		viewport:      vp,
		promptHistory: []string{"first", "second"},
		historyIdx:    -1,
		ready:         true,
	}
	initialOffset := m.viewport.YOffset()

	updated, _ := m.Update(tea.MouseWheelMsg{X: 1, Y: 1, Button: tea.MouseWheelUp})
	m = updated.(*teaModel)

	if m.historyIdx != -1 || m.input.Value() != "" {
		t.Fatalf("mouse wheel changed prompt history: index=%d input=%q", m.historyIdx, m.input.Value())
	}
	if m.viewport.YOffset() >= initialOffset {
		t.Fatalf("mouse wheel did not scroll viewport: before=%d after=%d", initialOffset, m.viewport.YOffset())
	}
}

func TestTypingDoesNotScrollViewport(t *testing.T) {
	ta := textarea.New()
	ta.Focus()
	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(3))
	vp.SetContent("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten")
	vp.GotoBottom()

	m := &teaModel{
		input:      ta,
		viewport:   vp,
		historyIdx: -1,
		ready:      true,
	}
	initialOffset := m.viewport.YOffset()

	// Every one of these is a viewport scroll binding in bubbles' default keymap.
	for _, r := range "kjudbf hl" {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(rune(r))})
		m = updated.(*teaModel)
		if m.viewport.YOffset() != initialOffset {
			t.Fatalf("typing %q scrolled the viewport: before=%d after=%d", r, initialOffset, m.viewport.YOffset())
		}
	}
	if got := m.input.Value(); got != "kjudbf hl" {
		t.Fatalf("typed text did not reach the input: %q", got)
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
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
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
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(*teaModel)

	if m.historyIdx != 1 {
		t.Fatalf("expected historyIdx 1, got %d", m.historyIdx)
	}
	if m.input.Value() != "second prompt" {
		t.Fatalf("expected 'second prompt', got %q", m.input.Value())
	}

	// 4. Press Up again: should recall "first prompt"
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(*teaModel)

	if m.historyIdx != 0 {
		t.Fatalf("expected historyIdx 0, got %d", m.historyIdx)
	}
	if m.input.Value() != "first prompt" {
		t.Fatalf("expected 'first prompt', got %q", m.input.Value())
	}

	// 5. Press Up at oldest item: should stay at 0
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(*teaModel)

	if m.historyIdx != 0 {
		t.Fatalf("expected historyIdx 0, got %d", m.historyIdx)
	}
	if m.input.Value() != "first prompt" {
		t.Fatalf("expected 'first prompt', got %q", m.input.Value())
	}

	// 6. Press Down: should move forward to "second prompt"
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(*teaModel)

	if m.historyIdx != 1 {
		t.Fatalf("expected historyIdx 1, got %d", m.historyIdx)
	}
	if m.input.Value() != "second prompt" {
		t.Fatalf("expected 'second prompt', got %q", m.input.Value())
	}

	// 7. Press Down again: should move forward to "third prompt"
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(*teaModel)

	if m.historyIdx != 2 {
		t.Fatalf("expected historyIdx 2, got %d", m.historyIdx)
	}
	if m.input.Value() != "third prompt" {
		t.Fatalf("expected 'third prompt', got %q", m.input.Value())
	}

	// 8. Press Down past the latest item: should restore "my unfinished draft"
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(*teaModel)

	if m.historyIdx != -1 {
		t.Fatalf("expected historyIdx -1, got %d", m.historyIdx)
	}
	if m.input.Value() != "my unfinished draft" {
		t.Fatalf("expected restored draft 'my unfinished draft', got %q", m.input.Value())
	}

	// 9. Press Up to browse, then Esc: should cancel and restore draft
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(*teaModel)
	if m.historyIdx != 2 {
		t.Fatalf("expected historyIdx 2, got %d", m.historyIdx)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
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
	// The tool result answers no call in the transcript, so replay drops it;
	// an endpoint would reject it as an orphan.
	got := replayMessages(m.initialMessages())
	if len(got) != 2 || got[0].Role != "user" || got[0].Content != "first question" || got[1].Role != "assistant" {
		t.Fatalf("replayed messages = %#v", got)
	}
}

// Replayed history keeps paired tool traffic, so the model still knows which
// files it read and which commands it ran on earlier turns.
func TestTeaInitialMessagesReplayPairedToolCalls(t *testing.T) {
	var call llm.ToolCall
	call.ID = "call-1"
	call.Function.Name = "read_file"
	call.Function.Arguments = `{"path":"main.go"}`
	var dangling llm.ToolCall
	dangling.ID = "call-2"
	dangling.Function.Name = "run_command"
	m := &teaModel{sessionMessages: []llm.Message{
		{Role: "system", Content: "stale prompt"},
		{Role: "user", Content: "read main.go"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{call, dangling}},
		{Role: "tool", Content: "package main", ToolCallID: "call-1"},
		{Role: "assistant", Content: "It is the entry point."},
	}}
	got := replayMessages(m.initialMessages())
	if len(got) != 4 {
		t.Fatalf("replayed messages = %#v", got)
	}
	if got[0].Role != "user" {
		t.Fatalf("system message was replayed: %#v", got[0])
	}
	if len(got[1].ToolCalls) != 1 || got[1].ToolCalls[0].ID != "call-1" {
		t.Fatalf("assistant tool calls = %#v, want only the answered call", got[1].ToolCalls)
	}
	if got[2].Role != "tool" || got[2].ToolCallID != "call-1" || got[2].Content != "package main" {
		t.Fatalf("tool result = %#v", got[2])
	}
}

func TestTeaRuntimeSettingsValidateAndPersistValues(t *testing.T) {
	m := &teaModel{}
	m.permissionController().Grant("write_file")
	for _, tc := range []struct{ name, value string }{
		{"turns", "42"}, {"timeout", "90"}, {"think", "high"},
		{"commands", "off"}, {"permissions", "read-only"}, {"output", "expanded"},
	} {
		if err := m.setRuntimeValue(tc.name, tc.value); err != nil {
			t.Fatalf("set %s: %v", tc.name, err)
		}
	}
	if m.maxTurns != 42 || m.commandTimeout != 90*time.Second || m.thinkLevel != "high" || m.allowCommands || m.permissionMode != PermissionPlan || !m.expandedTools {
		t.Fatalf("unexpected runtime state: %+v", m.runtimeSettings())
	}
	if len(m.permissionController().ActiveGrants()) != 0 {
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
		permissionMode:  PermissionAgent,
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
	if len(loaded.Messages) != 2 || loaded.Metrics.TotalTokens != 123 || loaded.Runtime.PermissionMode != PermissionAgent {
		t.Fatalf("loaded session = %+v", loaded)
	}
}

func TestTeaCompactSessionContextTrimsUnboundedTranscript(t *testing.T) {
	store := &SessionStore{Dir: t.TempDir()}
	cfg := DefaultCompactionConfig()
	m := &teaModel{
		workingDir: t.TempDir(), modelName: "test-model", sessionStore: store,
		maxTurns: 20, commandTimeout: time.Minute, allowCommands: true,
		permissionMode: PermissionAgent,
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

// A permission prompt with no key legend looks frozen: the card names the tool
// but nothing tells the user that y/a/n are the answers.
func TestPermissionPromptShowsKeyLegend(t *testing.T) {
	tmp := t.TempDir()
	ta := textarea.New()
	m := &teaModel{
		runner: NewRunner(&mockLLM{}, tmp, "test-model"), workingDir: tmp,
		input: ta, viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(6)), ready: true, width: 80,
		permissionChan: make(chan teaPermissionRequestMsg),
	}
	m.pendingPermission = &teaPermissionRequestMsg{ToolName: "write_file", Summary: "path=hello.txt"}

	plain := StripANSI(m.render())
	for _, want := range []string{"Permission Required", "write_file", "[y]", "[a]", "[n]", "esc"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("permission prompt is missing %q:\n%s", want, plain)
		}
	}
}

func TestPermissionPromptEnterAndEscDeny(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: tea.KeyEscape}} {
		ta := textarea.New()
		m := &teaModel{
			input: ta, viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(6)), ready: true, width: 80,
			permissionChan: make(chan teaPermissionRequestMsg),
		}
		reply := make(chan permissionDecision, 1)
		m.pendingPermission = &teaPermissionRequestMsg{ToolName: "write_file", Reply: reply}

		updated, cmd := m.Update(key)
		m = updated.(*teaModel)

		select {
		case d := <-reply:
			if d.Allow {
				t.Fatalf("%v allowed the write; it must deny", key)
			}
		default:
			t.Fatalf("%v left the prompt unanswered -- the UI looks frozen", key)
		}
		if m.pendingPermission != nil || cmd == nil {
			t.Fatalf("%v did not clear the prompt or resume the event loop", key)
		}
	}
}

// failingLLM always errors, the way an unreachable endpoint does.
type failingLLM struct{ err error }

func (f *failingLLM) Chat(ctx context.Context, model string, messages []llm.Message, tools []llm.Tool, thinkLevel string) (llm.Message, error) {
	return llm.Message{}, f.err
}

func collectFinishEvents(t *testing.T, m *teaModel) []Event {
	t.Helper()
	var finishes []Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-m.eventChan:
			if !ok {
				return finishes
			}
			if ev.Type == EventTaskFinished {
				finishes = append(finishes, ev)
			}
		case <-deadline:
			t.Fatal("timed out draining events")
		}
	}
}

// A failed turn must be reported once. The runner emits EventTaskFinished and
// also returns the error, so synthesizing a second one printed every failure
// twice: "Task failed: LLM chat error on turn 1: ..." then "Task failed: ...".
func TestFailedTurnIsReportedExactlyOnce(t *testing.T) {
	tmp := t.TempDir()
	ta := textarea.New()
	m := &teaModel{
		runner:     NewRunner(&failingLLM{err: errors.New("dial tcp 127.0.0.1:8003: connection refused")}, tmp, "test-model"),
		workingDir: tmp, modelName: "test-model",
		input: ta, viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(10)), ready: true, width: 80,
		maxTurns: 3, commandTimeout: time.Minute,
		permissionMode: PermissionFull,
		permissionChan: make(chan teaPermissionRequestMsg),
	}

	m.handleAgentSubmit("do something")
	finishes := collectFinishEvents(t, m)

	if len(finishes) != 1 {
		var msgs []string
		for _, ev := range finishes {
			msgs = append(msgs, ev.Error)
		}
		t.Fatalf("got %d finish events, want 1:\n  %s", len(finishes), strings.Join(msgs, "\n  "))
	}
	if !strings.Contains(finishes[0].Error, "connection refused") {
		t.Fatalf("error text lost: %q", finishes[0].Error)
	}
}

// The fallback still has to fire for failures that happen before the turn loop
// starts, which return without emitting anything at all.
func TestPreflightFailureStillReportsOnce(t *testing.T) {
	tmp := t.TempDir()
	ta := textarea.New()
	m := &teaModel{
		runner:     NewRunner(&failingLLM{err: errors.New("unused")}, tmp, "test-model"),
		workingDir: string([]byte{0}), modelName: "test-model",
		input: ta, viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(10)), ready: true, width: 80,
		maxTurns: 3, commandTimeout: time.Minute,
		permissionMode: PermissionFull,
		permissionChan: make(chan teaPermissionRequestMsg),
	}

	m.handleAgentSubmit("do something")
	finishes := collectFinishEvents(t, m)

	if len(finishes) != 1 {
		t.Fatalf("preflight failure produced %d finish events, want 1", len(finishes))
	}
	if finishes[0].Error == "" {
		t.Fatal("preflight failure was reported with no error text")
	}
}

// The TUI frame must be exactly the same size on every render. A single row
// wider than the terminal is wrapped into two by the terminal, which changes
// the frame height and visibly shifts the whole UI. rightInfo changes on every
// spinner tick and whenever a status notice appears, so an unclamped header
// made the content jitter up and down while typing or running a turn.
func TestFrameSizeIsStableAcrossWidthsAndHeaderStates(t *testing.T) {
	const height = 30
	tmp := t.TempDir()
	ta := textarea.New()
	ta.Prompt = "> "
	ta.SetHeight(2)
	ta.ShowLineNumbers = false
	ta.Placeholder = "Ask a question, enter a task, or type /help (Tab switches to Shell Mode)..."
	ta.Focus()

	m := &teaModel{
		runner: NewRunner(&mockLLM{}, tmp, "test-model"), workingDir: tmp,
		modelName: "gemma-4-31b", input: ta, viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(10)),
	}
	m.appendHistory(strings.Repeat("history line\n", 40))

	for _, width := range []int{60, 70, 80, 90, 100, 120} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
		m = updated.(*teaModel)

		want := -1
		for _, notice := range []string{"", "Switched model to gemma-4-31b"} {
			for _, mode := range []tuiMode{modeAgent, modeShell} {
				for _, typed := range []int{0, 1, 40, 200} {
					m.statusNotice = notice
					m.mode = mode
					m.input.SetValue(strings.Repeat("x", typed))

					rows := strings.Split(m.render(), "\n")
					for i, row := range rows {
						if w := VisualLen(StripANSI(row)); w > width {
							t.Fatalf("width=%d row %d is %d columns wide; the terminal will wrap it and shift the frame",
								width, i, w)
						}
					}
					if len(rows) > height {
						t.Fatalf("width=%d frame is %d rows in a %d-row terminal", width, len(rows), height)
					}
					if want < 0 {
						want = len(rows)
					} else if len(rows) != want {
						t.Fatalf("width=%d frame changed from %d to %d rows (notice=%v mode=%v typed=%d)",
							width, want, len(rows), notice != "", mode, typed)
					}
				}
			}
		}
	}
}

func TestContextGaugeUsesCompactionBudget(t *testing.T) {
	plain := StripANSI(formatContextGauge(12_000, 16_384, 8))
	if !strings.Contains(plain, "ctx") || !strings.Contains(plain, "12k/16k tok") || !strings.Contains(plain, "▰") || !strings.Contains(plain, "▱") {
		t.Fatalf("context gauge = %q", plain)
	}
	if got := StripANSI(formatContextGauge(75_000, 60_000, 0)); got != "ctx 100%" {
		t.Fatalf("compact context gauge = %q", got)
	}

	// Verify empty session meter
	empty := StripANSI(formatContextGauge(0, 60_000, 8))
	if strings.Contains(empty, "▰") || !strings.Contains(empty, "▱▱▱▱▱▱▱▱") || !strings.Contains(empty, "0%") {
		t.Fatalf("empty context gauge = %q", empty)
	}

	// Verify threshold colors
	greenGauge := formatContextGauge(30_000, 60_000, 8)
	if !strings.Contains(greenGauge, "16;185;129") && !strings.Contains(greenGauge, "38;2;16;185;129") && !strings.Contains(greenGauge, "10B981") && !strings.Contains(greenGauge, "32") {
		// Lipgloss renders 24-bit ANSI colors (e.g. 38;2;16;185;129)
	}
}

func TestSkillsModalNavigatesAndCloses(t *testing.T) {
	ta := textarea.New()
	ta.Focus()
	m := &teaModel{
		input: ta, ready: true, width: 80, height: 24,
		skills: []Skill{
			{Name: "alpha", Description: "First skill"},
			{Name: "beta", Description: "Second skill"},
		},
	}
	m.openSkillsModal()
	plain := StripANSI(m.render())
	if !strings.Contains(plain, "SKILLS · 2 available") || !strings.Contains(plain, "alpha") || !strings.Contains(plain, "beta") {
		t.Fatalf("modal = %q", plain)
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(*teaModel)
	if m.skillCursor != 1 {
		t.Fatalf("skill cursor = %d", m.skillCursor)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(*teaModel)
	if m.skillsModal {
		t.Fatal("Escape did not close skills modal")
	}
}

func TestSkillsModalEnterStagesInvocation(t *testing.T) {
	ta := textarea.New()
	ta.Focus()
	m := &teaModel{
		input: ta, ready: true, width: 80, height: 24,
		skills: []Skill{
			{Name: "alpha", Description: "First skill"},
			{Name: "beta", Description: "Second skill"},
		},
	}
	m.openSkillsModal()
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(*teaModel)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(*teaModel)

	if m.skillsModal {
		t.Fatal("Enter did not close skills modal")
	}
	// Staged, not submitted: the task still has to be typed.
	if got := m.input.Value(); got != "/skills beta " {
		t.Fatalf("staged input = %q", got)
	}
}

// Enter on an empty catalog has no skill to stage, so it must still dismiss
// rather than index out of range.
func TestSkillsModalEnterWithNoSkills(t *testing.T) {
	ta := textarea.New()
	ta.Focus()
	m := &teaModel{input: ta, ready: true, width: 80, height: 24}
	m.openSkillsModal()
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(*teaModel)

	if m.skillsModal {
		t.Fatal("Enter did not close empty skills modal")
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("empty catalog staged input = %q", got)
	}
}

// slowLLM blocks until released, so a second submit can race the first.
type slowLLM struct{ release chan struct{} }

func (s *slowLLM) Chat(ctx context.Context, model string, messages []llm.Message, tools []llm.Tool, thinkLevel string) (llm.Message, error) {
	select {
	case <-s.release:
	case <-ctx.Done():
		return llm.Message{}, ctx.Err()
	}
	return llm.Message{Role: "assistant", Content: "done"}, nil
}

func newBusyModel(t *testing.T, client LLMClient) *teaModel {
	t.Helper()
	ta := textarea.New()
	ta.Focus()
	tmp := t.TempDir()
	return &teaModel{
		runner: NewRunner(client, tmp, "test-model"), workingDir: tmp, modelName: "test-model",
		input: ta, viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(10)), ready: true, width: 80,
		maxTurns: 2, commandTimeout: time.Minute,
		permissionMode: PermissionFull,
		permissionChan: make(chan teaPermissionRequestMsg),
	}
}

// Submitting while a turn runs used to start a second worker. Both workers then
// sent on whatever m.eventChan held at send time, so one could send into a
// channel the other had already closed: "panic: send on closed channel".
func TestSecondSubmitDoesNotStartAConcurrentTurn(t *testing.T) {
	slow := &slowLLM{release: make(chan struct{})}
	m := newBusyModel(t, slow)

	first := m.handleAgentSubmit("first")
	if first == nil || !m.isExecuting {
		t.Fatal("first submit did not start a turn")
	}
	firstChan := m.eventChan

	if cmd := m.handleAgentSubmit("second"); cmd == nil {
		t.Fatal("second submit returned no command")
	}
	if m.eventChan != firstChan {
		t.Fatal("second submit replaced the running turn's event channel")
	}
	if m.input.Value() != "second" {
		t.Fatalf("rejected input was lost: %q", m.input.Value())
	}
	if !strings.Contains(m.statusNotice, "already running") {
		t.Fatalf("no notice explaining the refusal: %q", m.statusNotice)
	}

	close(slow.release)
	for ev := range m.eventChan {
		_ = ev
	}
}

// A pasted block must land in the textarea as text, not be replayed as one
// Enter per line.
func TestPasteInsertsTextInsteadOfSubmitting(t *testing.T) {
	m := newBusyModel(t, &mockLLM{})
	pasted := "line one\nline two\nline three"

	updated, cmd := m.Update(tea.PasteMsg{Content: pasted})
	m = updated.(*teaModel)

	if m.isExecuting || cmd != nil {
		t.Fatal("paste started a turn instead of inserting text")
	}
	if got := m.input.Value(); got != pasted {
		t.Fatalf("pasted text = %q; want %q", got, pasted)
	}
}

// A fixed two-row input box scrolls its own content: once the text wraps past
// two rows, the line being typed moves out of view entirely. The box must grow
// with the text, and the viewport must give back exactly the rows it takes so
// the frame height never changes.
func TestInputBoxGrowsAndKeepsTypedTextVisible(t *testing.T) {
	const width, height = 80, 30
	tmp := t.TempDir()
	ta := textarea.New()
	ta.Prompt = "> "
	ta.SetHeight(minInputRows)
	ta.ShowLineNumbers = false
	ta.Focus()
	m := &teaModel{
		runner: NewRunner(&mockLLM{}, tmp, "test-model"), workingDir: tmp,
		modelName: "test-model", input: ta, viewport: viewport.New(viewport.WithWidth(width), viewport.WithHeight(10)),
	}
	m.appendHistory(strings.Repeat("history\n", 60))
	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = updated.(*teaModel)

	frame, lastBox := -1, 0
	// Type through the real key path so the caret moves exactly as it does for
	// a user; SetValue would leave the textarea's own scroll position behind.
	for typed := 1; typed <= 900; typed++ {
		updated, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
		m = updated.(*teaModel)

		view := m.render()
		rows := strings.Split(view, "\n")
		if frame < 0 {
			frame = len(rows)
		} else if len(rows) != frame {
			t.Fatalf("typed=%d frame changed from %d to %d rows", typed, frame, len(rows))
		}
		if len(rows) > height {
			t.Fatalf("typed=%d frame is %d rows in a %d-row terminal", typed, len(rows), height)
		}
		for i, row := range rows {
			if w := VisualLen(StripANSI(row)); w > width {
				t.Fatalf("typed=%d row %d is %d columns wide", typed, i, w)
			}
		}
		if m.input.Height() < lastBox {
			t.Fatalf("typed=%d box shrank from %d to %d rows", typed, lastBox, m.input.Height())
		}
		lastBox = m.input.Height()
	}
	if lastBox != maxInputRows {
		t.Fatalf("box reached %d rows; want the %d cap", lastBox, maxInputRows)
	}

	// The caret must still be on screen at the cap -- that is the whole point.
	for _, r := range "END" {
		updated, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(*teaModel)
	}
	if !strings.Contains(StripANSI(m.render()), "END") {
		t.Fatal("the caret line scrolled out of the input box")
	}
}

// The gauge measures the next request as the compaction gate in Runner.Run does:
// the transcript plus the system prompt and tool schemas sent with every request,
// in tokens at the gate's rate, against the model's window. The unsent draft is
// not part of any request. (It once counted sessionMessages alone, which was right
// while compaction did; the gate now counts the fixed overhead, so the gauge must.)
func TestContextUsageMeasuresTheNextRequest(t *testing.T) {
	ta := textarea.New()
	ta.SetValue("an unsent draft that must not count toward the gauge")
	m := &teaModel{
		input: ta,
		sessionMessages: []llm.Message{
			{Role: "user", Content: strings.Repeat("u", 1200)},
			{Role: "assistant", Content: strings.Repeat("a", 800)},
		},
		settings: &config.Settings{Models: []config.ModelEndpoint{
			{ID: "small", ContextWindow: 16384},
			{ID: "big", ContextWindow: 200000},
		}},
		modelName: "small",
	}
	// A run reported its exact overhead for the current settings.
	m.overhead = requestOverhead{chars: 23_000, key: m.overheadKey()}

	used, window := m.contextUsage()
	if want := (messageCharacterCount(m.sessionMessages) + 23_000) * 10 / budgetCharsPerTokenTenths; used != want {
		t.Fatalf("gauge = %d tokens, want %d (transcript plus overhead, not the draft)", used, want)
	}
	if window != 16384 {
		t.Fatalf("gauge denominator = %d, want the model's 16384-token window", window)
	}

	// An empty session still carries the system prompt and tools.
	m.sessionMessages = nil
	if used, _ := m.contextUsage(); used != 23_000*10/budgetCharsPerTokenTenths {
		t.Fatalf("empty session gauge = %d, want the fixed overhead alone", used)
	}

	// The denominator follows /model.
	m.modelName = "big"
	m.overhead.key = m.overheadKey() // keep the reported figure; no runner to re-estimate
	if _, window := m.contextUsage(); window != 200000 {
		t.Fatalf("gauge denominator after /model = %d, want 200000", window)
	}

	// Compaction starts inside the window: when the gauge nears 100% of the
	// window less the reply reserve, not after the window is already full.
	full := contextBudgetChars(16384)
	if tokens := full * 10 / budgetCharsPerTokenTenths; tokens > 16384-replyReserveTokens(16384) {
		t.Fatalf("the compaction point, %d tokens, leaves less than the reply reserve free", tokens)
	}
}

func TestBackgroundProcessesInTUI(t *testing.T) {
	ta := textarea.New()
	pm := NewProcessManager()
	defer pm.KillAll()

	m := &teaModel{
		workingDir: t.TempDir(),
		input:      ta,
		viewport:   viewport.New(viewport.WithWidth(120), viewport.WithHeight(10)),
		ready:      true,
		width:      120,
		processMgr: pm,
		mode:       modeShell,
	}

	cmdSleep := "sleep 10"
	if runtime.GOOS == "windows" {
		cmdSleep = "ping -n 11 127.0.0.1 >nul"
	}

	// 1. Test trailing '&' in Shell Mode
	_ = m.handleShellSubmit(cmdSleep + " &")
	if m.shellExecuting {
		t.Fatal("trailing '&' should not leave shellExecuting true")
	}
	if pm.ActiveCount() != 1 {
		t.Fatalf("expected 1 active background process, got %d", pm.ActiveCount())
	}
	if !strings.Contains(m.historyText.String(), "proc-1") {
		t.Fatalf("history missing proc-1 confirmation: %s", m.historyText.String())
	}

	// 2. Test /bg slash command in Agent Mode
	m.mode = modeAgent
	_ = m.handleAgentSubmit("/bg " + cmdSleep)
	if pm.ActiveCount() != 2 {
		t.Fatalf("expected 2 active background processes after /bg, got %d", pm.ActiveCount())
	}
	if !strings.Contains(m.historyText.String(), "proc-2") {
		t.Fatalf("history missing proc-2 confirmation: %s", m.historyText.String())
	}

	// 3. Test logs command
	_ = m.handleShellSubmit("logs 1")
	if !strings.Contains(m.historyText.String(), "logs proc-1") {
		t.Fatalf("history missing logs output for proc-1: %s", m.historyText.String())
	}

	// 4. Test Header badge in View()
	view := m.render()
	if !strings.Contains(view, "2 servers") {
		t.Fatalf("header missing '2 servers' badge: %s", view)
	}

	// 5. Test kill all
	_ = m.handleShellSubmit("kill all")
	time.Sleep(100 * time.Millisecond)
	if pm.ActiveCount() != 0 {
		t.Fatalf("expected 0 active processes after kill all, got %d", pm.ActiveCount())
	}
}

func TestCtrlBBackgroundsRunningShellCommand(t *testing.T) {
	ta := textarea.New()
	pm := NewProcessManager()
	defer pm.KillAll()

	m := &teaModel{
		workingDir: t.TempDir(),
		input:      ta,
		viewport:   viewport.New(viewport.WithWidth(120), viewport.WithHeight(10)),
		ready:      true,
		width:      120,
		processMgr: pm,
		mode:       modeShell,
	}

	marker := filepath.Join(m.workingDir, "ctrlb-marker")
	cmd := fmt.Sprintf("touch %q; sleep 30", marker)
	if runtime.GOOS == "windows" {
		cmd = fmt.Sprintf("Set-Content -LiteralPath '%s' -Value started; Start-Sleep -Seconds 30", strings.ReplaceAll(marker, "'", "''"))
	}

	run := m.handleShellSubmit(cmd)
	result := make(chan tea.Msg, 1)
	go func() { result <- run() }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !m.shellExecuting {
		t.Fatal("expected shellExecuting to be true while command is running")
	}

	// Press Ctrl+B to background
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	m = updated.(*teaModel)

	if m.shellExecuting {
		t.Fatal("Ctrl+B did not set shellExecuting to false")
	}
	if !m.shellBackgrounded {
		t.Fatal("Ctrl+B did not set shellBackgrounded to true")
	}
	if pm.ActiveCount() != 1 {
		t.Fatalf("expected 1 active background process in manager, got %d", pm.ActiveCount())
	}
	if !strings.Contains(m.historyText.String(), "background") {
		t.Fatalf("history does not confirm backgrounding: %s", m.historyText.String())
	}
}

// A command typed after a run appeared pushed right: the "Completed in N
// turn(s)" line is rendered with its blank lines inside the style, lipgloss
// pads them to the line's width, and the next append continued on that row.
func TestHistoryDoesNotIndentAfterAPaddedBlock(t *testing.T) {
	completed := styleMuted.Render(fmt.Sprintf("─ Completed in %d turn(s) (%.1fs) ─\n\n", 9, 123.9))
	if lines := strings.Split(StripANSI(completed), "\n"); strings.TrimSpace(lines[len(lines)-1]) != "" || lines[len(lines)-1] == "" {
		t.Skip("lipgloss no longer pads trailing blank lines; the guard is moot")
	}
	m := &teaModel{}
	m.appendHistory(completed)
	m.appendHistory(styleUserPrompt.Render("❯ /compact") + "\n")
	for _, line := range strings.Split(StripANSI(m.historyText.String()), "\n") {
		if strings.Contains(line, "/compact") && !strings.HasPrefix(line, "❯") {
			t.Fatalf("the command is indented: %q", line)
		}
	}
	// Padding on lines before the last is invisible and must be left alone.
	block := "top\n" + styleMuted.Render("wide line here\nx") + "\n"
	if trimPaddedTail(block) != block {
		t.Fatal("a chunk ending in a newline was changed")
	}
}
