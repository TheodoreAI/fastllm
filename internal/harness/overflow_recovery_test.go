package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"fastllm/internal/llm"
)

// ollamaOverflow is the error the client returns for the body Ollama sent when
// a fastllm request outgrew a 16k window.
func ollamaOverflow() error {
	return &llm.ContextOverflowError{Window: 16384, Err: errors.New("llm: chat completion failed: request (80956 tokens) exceeds the available context size (16384 tokens), try increasing it")}
}

// A model configured with too large a window: the first request is refused,
// the run adopts the stated window, compacts to it, and succeeds on the retry.
func TestRunRecoversFromAContextOverflow(t *testing.T) {
	workspace := t.TempDir()
	history := make([]InitialMessage, 0, 40)
	for i := 0; i < 20; i++ {
		history = append(history,
			InitialMessage{Role: "user", Content: fmt.Sprintf("question %d %s", i, strings.Repeat("q", 6000))},
			InitialMessage{Role: "assistant", Content: fmt.Sprintf("answer %d %s", i, strings.Repeat("a", 6000))})
	}

	var retried []llm.Message
	mock := &mockLLM{turns: []func([]llm.Message) (llm.Message, error){
		func([]llm.Message) (llm.Message, error) { return llm.Message{}, ollamaOverflow() },
		func(messages []llm.Message) (llm.Message, error) {
			retried = append([]llm.Message(nil), messages...)
			return llm.Message{Role: "assistant", Content: "done"}, nil
		},
	}}
	runner := NewRunner(mock, workspace, "local-model")
	defer runner.Close()
	runner.contextWindow = 131072 // what the config wrongly claims
	runner.ContextBudgetChars = contextBudgetChars(131072)

	var notices []string
	result, err := runner.Run(context.Background(), RunRequest{
		Task: "continue", WorkingDir: workspace, Model: "local-model", InitialMessages: history,
		AllowCommands: true, CommandsConfigured: true, PermissionMode: PermissionEdit,
	}, func(ev Event) {
		if ev.Type == EventNotice {
			notices = append(notices, ev.Response)
		}
	})
	if err != nil {
		t.Fatalf("the run failed instead of recovering: %v", err)
	}
	if retried == nil || result.FinalResponse != "done" {
		t.Fatalf("no retry reached the model (final %q)", result.FinalResponse)
	}
	tools := runner.toolsFor(RunRequest{AllowCommands: true, CommandsConfigured: true, PermissionMode: PermissionEdit})
	limit := requestBudget(CompactionConfig{MaxTotalChars: contextBudgetChars(16384)}, tools).MaxTotalChars
	if got := messageCharacterCount(retried); got > limit {
		t.Fatalf("the retried request is %d characters, over the %d a 16k window allows", got, limit)
	}
	if result.LearnedContextWindow != 16384 {
		t.Fatalf("LearnedContextWindow = %d, want 16384", result.LearnedContextWindow)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "16384") || !strings.Contains(notices[0], "/models detect") {
		t.Fatalf("notices = %q", notices)
	}
}

// A second refusal of the same request ends the run, as before: one retry only.
func TestRunRetriesAnOverflowOnlyOnce(t *testing.T) {
	calls := 0
	mock := &mockLLM{turns: []func([]llm.Message) (llm.Message, error){
		func([]llm.Message) (llm.Message, error) { calls++; return llm.Message{}, ollamaOverflow() },
		func([]llm.Message) (llm.Message, error) { calls++; return llm.Message{}, ollamaOverflow() },
		func([]llm.Message) (llm.Message, error) {
			calls++
			return llm.Message{Role: "assistant", Content: "unreached"}, nil
		},
	}}
	runner := NewRunner(mock, t.TempDir(), "local-model")
	defer runner.Close()
	_, err := runner.Run(context.Background(), RunRequest{Task: "x", Model: "local-model", PermissionMode: PermissionEdit}, nil)
	if _, ok := llm.AsContextOverflow(err); !ok {
		t.Fatalf("err = %v, want the second overflow", err)
	}
	if calls != 2 {
		t.Fatalf("model called %d times, want 2 (the request and one retry)", calls)
	}
}

func TestRecoveryShrinksWhenTheStatedWindowIsNoSmaller(t *testing.T) {
	// The provider stated the window fastllm already assumed: the estimate
	// undercounted, so the recovery must still ask for less.
	overflow := &llm.ContextOverflowError{Window: 16384, Err: errors.New("overflow")}
	window, _, _, ok := recoverContextOverflow(overflow, 16384, []llm.Message{{Role: "user", Content: "x"}}, nil)
	if !ok || window >= 16384 {
		t.Fatalf("window = %d (ok %v), want less than 16384", window, ok)
	}
	if _, _, _, ok := recoverContextOverflow(errors.New("rate limited"), 16384, nil, nil); ok {
		t.Fatal("a non-overflow error was treated as one")
	}
}
