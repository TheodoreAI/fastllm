package harness

import (
	"context"
	"encoding/json"
	"fastllm/internal/files"
	"fastllm/internal/llm"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockLLM struct {
	chatModel string
	turns     []func(messages []llm.Message) (llm.Message, error)
	turnIndex int
	toolsSeen [][]llm.Tool
}

func (m *mockLLM) Chat(ctx context.Context, model string, messages []llm.Message, tools []llm.Tool, thinkLevel string) (llm.Message, error) {
	m.toolsSeen = append(m.toolsSeen, append([]llm.Tool(nil), tools...))
	if m.turnIndex >= len(m.turns) {
		return llm.Message{Role: "assistant", Content: "No more planned turns"}, nil
	}
	fn := m.turns[m.turnIndex]
	m.turnIndex++
	return fn(messages)
}

func (m *mockLLM) ChatModel() string {
	return m.chatModel
}

func TestRunnerInteractiveAuthorizationCanDenyMutation(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockLLM{turns: []func([]llm.Message) (llm.Message, error){
		func([]llm.Message) (llm.Message, error) {
			call := llm.ToolCall{ID: "write-1", Type: "function"}
			call.Function.Name = "write_file"
			call.Function.Arguments = `{"path":"denied.txt","content":"must not exist"}`
			return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}, nil
		},
		func(messages []llm.Message) (llm.Message, error) {
			if got := messages[len(messages)-1].Content; !strings.Contains(got, "Permission denied") {
				t.Fatalf("model did not receive denial result: %q", got)
			}
			return llm.Message{Role: "assistant", Content: "denial handled"}, nil
		},
	}}
	runner := NewRunner(mock, tmpDir, "test-model")
	var askedName, askedSummary string
	result, err := runner.Run(context.Background(), RunRequest{
		Task: "try a write", WorkingDir: tmpDir, Model: "test-model", PermissionMode: PermissionAgent,
		Authorize: func(c ConsentRequest) bool {
			name, summary := c.Tool, c.Summary
			askedName, askedSummary = name, summary
			return false
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if askedName != "write_file" || !strings.Contains(askedSummary, "denied.txt") {
		t.Fatalf("authorization request = %q %q", askedName, askedSummary)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "denied.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied mutation created file: %v", err)
	}
	if result.FinalResponse != "denial handled" {
		t.Fatalf("final response = %q", result.FinalResponse)
	}
}

func TestRunnerInteractiveAuthorizationDeniesFusedFollowUp(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockLLM{turns: []func([]llm.Message) (llm.Message, error){
		func([]llm.Message) (llm.Message, error) {
			call := llm.ToolCall{ID: "write-1", Type: "function"}
			call.Function.Name = "write_file"
			call.Function.Arguments = `{"path":"denied_fused.txt","content":"must not exist","then_run":{"command":"echo dangerous"}}`
			return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}, nil
		},
		func(messages []llm.Message) (llm.Message, error) {
			got := messages[len(messages)-1].Content
			if !strings.Contains(got, "Permission denied") || !strings.Contains(got, "fused follow-up command") {
				t.Fatalf("model did not receive fused denial result: %q", got)
			}
			return llm.Message{Role: "assistant", Content: "fused denial handled"}, nil
		},
	}}
	runner := NewRunner(mock, tmpDir, "test-model")
	var authorizedCalls []string
	result, err := runner.Run(context.Background(), RunRequest{
		Task: "try a fused write", WorkingDir: tmpDir, Model: "test-model", PermissionMode: PermissionAgent,
		Authorize: func(c ConsentRequest) bool {
			name, summary := c.Tool, c.Summary
			authorizedCalls = append(authorizedCalls, name+":"+summary)
			if name == "run_command" {
				return false
			}
			return true
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(authorizedCalls) != 2 {
		t.Fatalf("expected 2 authorization checks, got: %v", authorizedCalls)
	}
	if !strings.Contains(authorizedCalls[0], "write_file") || !strings.Contains(authorizedCalls[0], "denied_fused.txt") {
		t.Fatalf("unexpected first authorization call: %v", authorizedCalls[0])
	}
	if authorizedCalls[1] != "run_command:command=echo dangerous" {
		t.Fatalf("unexpected second authorization call: %v", authorizedCalls[1])
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "denied_fused.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied fused follow-up should not have created file: %v", err)
	}
	if result.FinalResponse != "fused denial handled" {
		t.Fatalf("final response = %q", result.FinalResponse)
	}
}

func TestRunnerExplicitlyDisablesCommands(t *testing.T) {
	mock := &mockLLM{turns: []func([]llm.Message) (llm.Message, error){
		func([]llm.Message) (llm.Message, error) { return llm.Message{Role: "assistant", Content: "done"}, nil },
	}}
	runner := NewRunner(mock, t.TempDir(), "test-model")
	runner.AllowCommands = true
	result, err := runner.Run(context.Background(), RunRequest{
		Task: "no commands", CommandsConfigured: true, AllowCommands: false, PermissionMode: PermissionFull,
	}, nil)
	if err != nil || result.FinalResponse != "done" {
		t.Fatalf("explicit command disable run failed: result=%+v err=%v", result, err)
	}
	for _, tool := range mock.toolsSeen[0] {
		if tool.Function.Name == "run_command" || tool.Function.Name == "process_status" || tool.Function.Name == "kill_process" {
			t.Fatalf("command tool %q was exposed while explicitly disabled", tool.Function.Name)
		}
	}
}

func TestRunner_AutonomousFileEditing(t *testing.T) {
	tmpDir := t.TempDir()

	mock := &mockLLM{
		chatModel: "test-model",
		turns: []func(messages []llm.Message) (llm.Message, error){
			// Turn 1: write_file
			func(messages []llm.Message) (llm.Message, error) {
				args, _ := json.Marshal(map[string]string{
					"path":    "pkg/test.txt",
					"content": "line 1\nold string\nline 3",
				})
				return llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{
							ID:   "call_1",
							Type: "function",
							Function: struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							}{
								Name:      "write_file",
								Arguments: string(args),
							},
						},
					},
				}, nil
			},
			// Turn 2: edit_file
			func(messages []llm.Message) (llm.Message, error) {
				args, _ := json.Marshal(map[string]string{
					"path":    "pkg/test.txt",
					"search":  "old string",
					"replace": "new harness string",
				})
				return llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{
							ID:   "call_2",
							Type: "function",
							Function: struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							}{
								Name:      "edit_file",
								Arguments: string(args),
							},
						},
					},
				}, nil
			},
			// Turn 3: finish_task
			func(messages []llm.Message) (llm.Message, error) {
				args, _ := json.Marshal(map[string]string{
					"summary": "Updated pkg/test.txt successfully",
					"answer":  "done",
				})
				return llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{
							ID:   "call_3",
							Type: "function",
							Function: struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							}{
								Name:      "finish_task",
								Arguments: string(args),
							},
						},
					},
				}, nil
			},
		},
	}

	runner := NewRunner(mock, tmpDir, "test-model")
	events := make([]Event, 0)
	res, err := runner.Run(context.Background(), RunRequest{
		Task: "Update the text file", PermissionMode: PermissionFull,
		WorkingDir: tmpDir,
	}, func(ev Event) {
		events = append(events, ev)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got false (error: %s)", res.Error)
	}
	if res.Turns != 3 {
		t.Errorf("expected 3 turns, got %d", res.Turns)
	}

	contentBytes, err := os.ReadFile(filepath.Join(tmpDir, "pkg/test.txt"))
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	content := string(contentBytes)
	if !strings.Contains(content, "new harness string") {
		t.Errorf("expected edited content to contain 'new harness string', got %q", content)
	}
	if strings.Contains(content, "old string") {
		t.Errorf("expected 'old string' to be replaced, got %q", content)
	}
}

func TestRunner_RunCommand(t *testing.T) {
	tmpDir := t.TempDir()

	mock := &mockLLM{
		chatModel: "test-model",
		turns: []func(messages []llm.Message) (llm.Message, error){
			// Turn 1: run_command
			func(messages []llm.Message) (llm.Message, error) {
				args, _ := json.Marshal(map[string]string{
					"command": "echo test_harness_echo",
				})
				return llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{
							ID:   "call_cmd",
							Type: "function",
							Function: struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							}{
								Name:      "run_command",
								Arguments: string(args),
							},
						},
					},
				}, nil
			},
			// Turn 2: conclude in plain text
			func(messages []llm.Message) (llm.Message, error) {
				return llm.Message{
					Role:    "assistant",
					Content: "Command finished cleanly.",
				}, nil
			},
		},
	}

	runner := NewRunner(mock, tmpDir, "test-model")
	res, err := runner.Run(context.Background(), RunRequest{
		Task: "Run test command", PermissionMode: PermissionFull,
		WorkingDir:    tmpDir,
		AllowCommands: true,
	}, nil)

	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got false")
	}
	if len(res.History) < 1 || len(res.History[0].ToolCalls) < 1 {
		t.Fatalf("expected at least one recorded tool call")
	}
	output := res.History[0].ToolCalls[0].Result
	if !strings.Contains(output, "test_harness_echo") {
		t.Errorf("expected command output to contain 'test_harness_echo', got: %s", output)
	}
}

func TestRunner_EditFileErrors(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "example.txt")
	_ = os.WriteFile(filePath, []byte("repeat repeat repeat"), 0o644)

	runner := NewRunner(&mockLLM{chatModel: "test"}, tmpDir, "test")

	// Ambiguous match (3 occurrences)
	resAmbiguous := runner.executeEditFile(runnerFile(tmpDir), "example.txt", "repeat", "single")
	if !strings.Contains(resAmbiguous, "matches 3 times") {
		t.Errorf("expected ambiguous error, got %q", resAmbiguous)
	}

	// Missing match
	resMissing := runner.executeEditFile(runnerFile(tmpDir), "example.txt", "missing_string", "sub")
	if !strings.Contains(resMissing, "not found") {
		t.Errorf("expected not found error, got %q", resMissing)
	}

	// Non-existent file
	resNonExistent := runner.executeEditFile(runnerFile(tmpDir), "does_not_exist.txt", "a", "b")
	if !strings.Contains(resNonExistent, "does not exist") {
		t.Errorf("expected does not exist error, got %q", resNonExistent)
	}
}

func TestExecuteToolRejectsTypeInvalidArguments(t *testing.T) {
	runner := NewRunner(nil, t.TempDir(), "test-model")
	defer runner.Close()
	result := runner.executeTool(toolExecutionContext{
		ctx:            context.Background(),
		request:        RunRequest{PermissionMode: PermissionFull, AllowCommands: true},
		workingDir:     runner.DefaultWorkingDir,
		allowCommands:  true,
		processManager: NewProcessManager(),
	}, "run_command", `{"command":"echo hi","timeout_seconds":"not-a-number"}`)
	if !strings.Contains(result.output, "malformed tool arguments") {
		t.Fatalf("invalid typed arguments were not rejected: %q", result.output)
	}
}

func TestRunner_TurnLimit(t *testing.T) {
	tmpDir := t.TempDir()

	// Infinite tool-calling mock
	mock := &mockLLM{
		chatModel: "test-model",
		turns: []func(messages []llm.Message) (llm.Message, error){
			func(messages []llm.Message) (llm.Message, error) {
				return llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{
							ID:   "call_loop",
							Type: "function",
							Function: struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							}{
								Name:      "list_files",
								Arguments: `{"path":""}`,
							},
						},
					},
				}, nil
			},
			func(messages []llm.Message) (llm.Message, error) {
				return llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{
						{
							ID:   "call_loop_2",
							Type: "function",
							Function: struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							}{
								Name:      "list_files",
								Arguments: `{"path":""}`,
							},
						},
					},
				}, nil
			},
			// Final response when asked to summarize
			func(messages []llm.Message) (llm.Message, error) {
				return llm.Message{
					Role:    "assistant",
					Content: "Stopped due to limit",
				}, nil
			},
		},
	}

	runner := NewRunner(mock, tmpDir, "test-model")
	res, err := runner.Run(context.Background(), RunRequest{
		Task:       "Infinite loop task",
		WorkingDir: tmpDir,
		MaxTurns:   2,
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Turns != 2 {
		t.Errorf("expected 2 turns, got %d", res.Turns)
	}
	if !strings.Contains(res.Error, "max turns limit") {
		t.Errorf("expected max turns error, got %q", res.Error)
	}
}

func runnerFile(root string) *files.Reader {
	return files.New(root, true)
}
