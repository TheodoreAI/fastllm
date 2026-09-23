package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fastllm/internal/llm"
)

// Sections render from most to least stable, so a provider prefix cache survives
// a mode or skill change, and empty sections leave no stray separators.
func TestBuildSystemPromptOrdersSectionsStableFirst(t *testing.T) {
	prompt := BuildSystemPrompt(PromptContext{
		Skills:      []Skill{{Name: "review", Description: "Review code"}},
		Rules:       []RuleFile{{Filename: "AGENTS.md", Content: "Always write tests."}},
		Environment: Environment{WorkingDir: "/repo", Platform: "linux/amd64", Shell: "sh", Date: "2026-09-23"},
		TurnExtra:   "<active_skill name=\"review\">\nCheck errors.\n</active_skill>",
		Mode:        planModePrompt,
	})
	order := []string{"<operating_rules>", "<skills>", "<project_rules source=\"AGENTS.md\">", "<environment>", "<active_skill", "<mode name=\"plan\">"}
	last := -1
	for _, marker := range order {
		index := strings.Index(prompt, marker)
		if index < 0 {
			t.Fatalf("prompt is missing %q:\n%s", marker, prompt)
		}
		if index < last {
			t.Fatalf("%q is out of order:\n%s", marker, prompt)
		}
		last = index
	}
	if strings.Contains(prompt, "\n\n\n") {
		t.Fatalf("prompt has an empty section:\n%q", prompt)
	}

	bare := BuildSystemPrompt(PromptContext{})
	if bare != DefaultSystemPrompt {
		t.Fatalf("an empty context should render only the default prompt, got:\n%s", bare)
	}
	if custom := BuildSystemPrompt(PromptContext{Base: "Be terse."}); custom != "Be terse." {
		t.Fatalf("a base override should replace the default prompt, got %q", custom)
	}
}

// The TUI used to fold the rules into its system prompt, and Runner.Run then
// discovered and appended them again, so every rules file reached the model twice.
func TestTeaTurnSendsEachRulesFileOnce(t *testing.T) {
	client := &skillCaptureLLM{messages: make(chan []llm.Message, 1)}
	m := newBusyModel(t, client)
	// A .git directory stops rule discovery here instead of walking up into the
	// real home directory.
	if err := os.Mkdir(filepath.Join(m.workingDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.workingDir, "AGENTS.md"), []byte("RULE-MARKER-7f3a"), 0o644); err != nil {
		t.Fatal(err)
	}

	if cmd := m.handleAgentSubmit("hello"); cmd == nil {
		t.Fatal("submit did not start a turn")
	}
	collectFinishEvents(t, m)

	system := (<-client.messages)[0].Content
	if got := strings.Count(system, "RULE-MARKER-7f3a"); got != 1 {
		t.Fatalf("rules file appears %d times in the system prompt, want 1:\n%s", got, system)
	}
	if !strings.Contains(system, "<environment>") || !strings.Contains(system, "model: test-model") {
		t.Fatalf("system prompt has no environment section:\n%s", system)
	}
}

// A finished TUI turn keeps its tool traffic for the next turn instead of only
// the prompt and the final answer.
func TestTeaTurnRecordsRunTranscript(t *testing.T) {
	m := newBusyModel(t, &skillCaptureLLM{messages: make(chan []llm.Message, 1)})
	m.pendingPrompt = "read main.go"
	var call llm.ToolCall
	call.ID = "call-1"
	call.Function.Name = "read_file"
	transcript := []llm.Message{
		{Role: "user", Content: "read main.go"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{call}},
		{Role: "tool", Content: "package main", ToolCallID: "call-1"},
		{Role: "assistant", Content: "It is the entry point."},
	}
	m.recordCompletedPrompt("It is the entry point.", transcript)
	if len(m.sessionMessages) != len(transcript) || m.sessionMessages[2].ToolCallID != "call-1" {
		t.Fatalf("session messages = %#v", m.sessionMessages)
	}
}

func TestRunTranscriptAppendsFinalAnswerAndDropsSystem(t *testing.T) {
	got := runTranscript([]llm.Message{
		{Role: "system", Content: "prompt"},
		{Role: "user", Content: "task"},
	}, "done")
	if len(got) != 2 || got[0].Content != "task" || got[1].Role != "assistant" || got[1].Content != "done" {
		t.Fatalf("transcript = %#v", got)
	}
}

// A write_file call carries a whole file in its arguments; leaving that out made
// compaction see an agent transcript as far smaller than what was sent.
func TestMessageCharacterCountIncludesToolArguments(t *testing.T) {
	var call llm.ToolCall
	call.Function.Name = "write_file"
	call.Function.Arguments = strings.Repeat("x", 5000)
	got := messageCharacterCount([]llm.Message{{Role: "assistant", ToolCalls: []llm.ToolCall{call}}})
	if got < 5000 {
		t.Fatalf("messageCharacterCount = %d, want at least 5000", got)
	}
}

// In a replayed TUI transcript there is no system message, and the newest request
// is not at index 1. Compaction must still keep the session's opening request,
// keep the newest request verbatim, and never emit a mid-conversation system message.
func TestCompactionKeepsLatestRequestAndEmitsNoSystemMessage(t *testing.T) {
	messages := []llm.Message{{Role: "user", Content: "opening request"}, {Role: "assistant", Content: "ok"}}
	latest := "please fix the parser and keep the public API unchanged " + strings.Repeat("detail ", 40)
	messages = append(messages, llm.Message{Role: "user", Content: latest})
	for i := 0; i < 6; i++ {
		var call llm.ToolCall
		call.ID = "c"
		call.Function.Name = "run_command"
		call.Function.Arguments = `{"command":"go test ./..."}`
		messages = append(messages,
			llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}},
			llm.Message{Role: "tool", Content: strings.Repeat("FAIL x\n", 80), ToolCallID: "c"})
	}
	messages = append(messages, llm.Message{Role: "assistant", Content: "still working"})

	compacted, ok := OnlineCompactMessages(messages, CompactionConfig{MaxTotalChars: 1500, KeepRecentMessages: 1, MaxToolOutputChars: 100})
	if !ok {
		t.Fatal("expected compaction")
	}
	if compacted[0].Content != "opening request" {
		t.Fatalf("opening request was not preserved: %#v", compacted[0])
	}
	for _, message := range compacted {
		if message.Role == "system" {
			t.Fatalf("compaction emitted a system message: %#v", message)
		}
	}
	if !isConversationSummary(compacted[1]) || !strings.Contains(compacted[1].Content, strings.TrimSpace(latest)) {
		t.Fatalf("summary does not carry the latest request verbatim:\n%s", compacted[1].Content)
	}

	// A second compaction carries the first summary forward instead of losing it.
	again := append(append([]llm.Message(nil), compacted...), messages[3:]...)
	recompacted, ok := OnlineCompactMessages(again, CompactionConfig{MaxTotalChars: 1500, KeepRecentMessages: 1, MaxToolOutputChars: 100})
	if !ok || !strings.Contains(recompacted[1].Content, "Earlier checkpoint:") {
		t.Fatalf("recompaction dropped the earlier summary: %#v", recompacted)
	}
}

func TestRulesListOutermostFirstAndTruncateLargeFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "service")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT "+strings.Repeat("r", maxRuleFileChars)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("NEAREST"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := FormatRulesForPrompt(DiscoverWorkspaceRules(sub))
	if strings.Index(prompt, "ROOT") > strings.Index(prompt, "NEAREST") {
		t.Fatalf("nearest rules file should come last:\n%s", prompt)
	}
	if !strings.Contains(prompt, "[... truncated") {
		t.Fatal("oversized rules file was not truncated")
	}
}
