package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fastllm/internal/llm"
)

type agentTestLLM struct {
	chat func(context.Context, []llm.Message) (llm.Message, error)
}

func (c *agentTestLLM) Chat(ctx context.Context, _ string, messages []llm.Message, _ []llm.Tool, _ string) (llm.Message, error) {
	return c.chat(ctx, messages)
}

func TestGlobFilesSupportsRecursivePatterns(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"main.go", "internal/a.go", "internal/a_test.go", "web/app.ts"} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewRunner(nil, root, "test")

	got := runner.executeGlobFiles(context.Background(), root, "**/*_test.go", "")
	if strings.TrimSpace(got) != "internal/a_test.go" {
		t.Fatalf("recursive glob = %q", got)
	}
	got = runner.executeGlobFiles(context.Background(), root, "*.go", "internal")
	if !strings.Contains(got, "internal/a.go") || !strings.Contains(got, "internal/a_test.go") {
		t.Fatalf("scoped glob = %q", got)
	}
}

func TestAgentManagerReturnsStructuredResult(t *testing.T) {
	client := &agentTestLLM{chat: func(_ context.Context, _ []llm.Message) (llm.Message, error) {
		return llm.Message{Role: "assistant", Content: "child result"}, nil
	}}
	runner := NewRunner(client, t.TempDir(), "test-model")
	id, err := runner.agents.Spawn(RunRequest{
		WorkingDir: runner.DefaultWorkingDir, Model: "test-model", AllowCommands: false,
		PermissionMode: PermissionReadOnly,
	}, "inspect the project", "", "", 2)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := runner.agents.Status(id)
		if strings.Contains(status, "State: completed") {
			if !strings.Contains(status, "Result:\nchild result") {
				t.Fatalf("completed status lacks result: %q", status)
			}
			summary := runner.agents.Summary()
			if summary.Total != 1 || summary.Completed != 1 || summary.Pending != 0 || summary.Running != 0 {
				t.Fatalf("unexpected summary: %+v", summary)
			}
			if summary.TotalTokens <= 0 || summary.TotalTokens != summary.PromptTokens+summary.CompletionTokens {
				t.Fatalf("invalid token accounting: %+v", summary)
			}
			list := runner.agents.Status("")
			if !strings.Contains(list, "Agents: 1 known") || !strings.Contains(list, "Tokens:") || !strings.Contains(list, " tok | inspect the project") {
				t.Fatalf("agent list lacks counts or tokens: %q", list)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("agent did not complete: %s", runner.agents.Status(id))
}

func TestAgentStatusWaitsForCompletionInsteadOfPolling(t *testing.T) {
	release := make(chan struct{})
	client := &agentTestLLM{chat: func(ctx context.Context, _ []llm.Message) (llm.Message, error) {
		select {
		case <-release:
			return llm.Message{Role: "assistant", Content: "finished after wait"}, nil
		case <-ctx.Done():
			return llm.Message{}, ctx.Err()
		}
	}}
	runner := NewRunner(client, t.TempDir(), "test-model")
	id, err := runner.agents.Spawn(RunRequest{
		WorkingDir: runner.DefaultWorkingDir, Model: "test-model", PermissionMode: PermissionReadOnly,
	}, "wait for release", "", "", 2)
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan string, 1)
	go func() {
		result <- runner.agents.WaitStatus(context.Background(), id, time.Second)
	}()
	select {
	case got := <-result:
		t.Fatalf("status returned before child completed: %q", got)
	case <-time.After(30 * time.Millisecond):
	}

	close(release)
	select {
	case got := <-result:
		if !strings.Contains(got, "State: completed") || !strings.Contains(got, "Result:\nfinished after wait") {
			t.Fatalf("waited status lacks completed result: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("status did not return after child completed")
	}
}

func TestAgentStatusToolWaitsByDefault(t *testing.T) {
	release := make(chan struct{})
	client := &agentTestLLM{chat: func(ctx context.Context, _ []llm.Message) (llm.Message, error) {
		select {
		case <-release:
			return llm.Message{Role: "assistant", Content: "tool result"}, nil
		case <-ctx.Done():
			return llm.Message{}, ctx.Err()
		}
	}}
	runner := NewRunner(client, t.TempDir(), "test-model")
	parent := RunRequest{WorkingDir: runner.DefaultWorkingDir, Model: "test-model", PermissionMode: PermissionReadOnly}
	id, err := runner.agents.Spawn(parent, "wait through tool", "", "", 2)
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan string, 1)
	go func() {
		result <- runner.executeAgentTool(context.Background(), parent, "agent_status", `{"agent_id":"`+id+`"}`)
	}()
	select {
	case got := <-result:
		t.Fatalf("agent_status tool returned an active snapshot instead of waiting: %q", got)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case got := <-result:
		if !strings.Contains(got, "State: completed") || !strings.Contains(got, "Result:\ntool result") {
			t.Fatalf("agent_status tool result = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent_status tool did not wake when the child completed")
	}
}

func TestAgentStatusWaitHonorsContextCancellation(t *testing.T) {
	client := &agentTestLLM{chat: func(ctx context.Context, _ []llm.Message) (llm.Message, error) {
		<-ctx.Done()
		return llm.Message{}, ctx.Err()
	}}
	runner := NewRunner(client, t.TempDir(), "test-model")
	id, err := runner.agents.Spawn(RunRequest{
		WorkingDir: runner.DefaultWorkingDir, Model: "test-model", PermissionMode: PermissionReadOnly,
	}, "stay active", "", "", 2)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := runner.agents.WaitStatus(ctx, id, time.Minute)
	if !strings.Contains(got, "Wait canceled: context canceled") {
		t.Fatalf("canceled wait = %q", got)
	}
	if err := runner.agents.Cancel(id); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerCloseCancelsAndJoinsChildren(t *testing.T) {
	started := make(chan struct{})
	exited := make(chan struct{})
	client := &agentTestLLM{chat: func(ctx context.Context, _ []llm.Message) (llm.Message, error) {
		close(started)
		<-ctx.Done()
		close(exited)
		return llm.Message{}, ctx.Err()
	}}
	runner := NewRunner(client, t.TempDir(), "test-model")
	parent := RunRequest{WorkingDir: runner.DefaultWorkingDir, Model: "test-model", PermissionMode: PermissionReadOnly}
	if _, err := runner.agents.Spawn(parent, "wait until owner closes", "", "", 2); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("child did not start")
	}

	runner.Close()
	select {
	case <-exited:
	default:
		t.Fatal("Close returned before the child exited")
	}
	if _, err := runner.agents.Spawn(parent, "late child", "", "", 1); err == nil {
		t.Fatal("closed runner accepted another child")
	}
}

func TestAgentManagerEnforcesConcurrencyAndWorkspace(t *testing.T) {
	client := &agentTestLLM{chat: func(ctx context.Context, _ []llm.Message) (llm.Message, error) {
		<-ctx.Done()
		return llm.Message{}, ctx.Err()
	}}
	root := t.TempDir()
	runner := NewRunner(client, root, "test-model")
	parent := RunRequest{WorkingDir: root, Model: "test-model", PermissionMode: PermissionAuto}
	if _, err := runner.agents.Spawn(parent, "escape", "..", "", 1); err == nil || !strings.Contains(err.Error(), "must stay within") {
		t.Fatalf("expected workspace escape rejection, got %v", err)
	}

	var ids []string
	for i := 0; i < 3; i++ {
		id, err := runner.agents.Spawn(parent, "wait", "", "", 1)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := runner.agents.Spawn(parent, "too many", "", "", 1); err == nil {
		t.Fatal("expected concurrency limit error")
	}
	for _, id := range ids {
		if err := runner.agents.Cancel(id); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runner.agents.Summary().Canceled == len(ids) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	summary := runner.agents.Summary()
	if summary.Total != 3 || summary.Canceled != 3 || summary.Pending+summary.Running != 0 {
		t.Fatalf("unexpected post-cancel summary: %+v", summary)
	}
}

func TestDrainAgentInboxAddsParentGuidance(t *testing.T) {
	inbox := make(chan string, 1)
	inbox <- "focus on tests"
	messages := []llm.Message{{Role: "user", Content: "original"}}
	drainAgentInbox(&messages, inbox)
	if len(messages) != 2 || messages[1].Content != "Parent agent guidance: focus on tests" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestAgentToolDepthBoundary(t *testing.T) {
	runner := NewRunner(nil, t.TempDir(), "test")
	if got := len(runner.agents.Tools(0)); got != 4 {
		t.Fatalf("depth 0 tools = %d; want 4", got)
	}
	if got := len(runner.agents.Tools(1)); got != 4 {
		t.Fatalf("depth 1 tools = %d; want 4", got)
	}
	if got := len(runner.agents.Tools(2)); got != 0 {
		t.Fatalf("depth 2 tools = %d; want 0", got)
	}
}

func TestChildRunnerClonesDirectClientUsageState(t *testing.T) {
	client := llm.New("http://127.0.0.1:8000/v1", "key", "model", "embed")
	runner := NewRunner(client, t.TempDir(), "model")
	childA := runner.childRunner()
	childB := runner.childRunner()

	if childA.LLM == runner.LLM || childA.LLM == childB.LLM {
		t.Fatal("child runners must have isolated direct clients")
	}
	a := childA.LLM.(*llm.Client)
	b := childB.LLM.(*llm.Client)
	if a.HTTPClient != client.HTTPClient || b.HTTPClient != client.HTTPClient {
		t.Fatal("child clients should reuse the safe shared HTTP transport")
	}
	if a.BaseURL != client.BaseURL || a.ChatModel != client.ChatModel || a.APIKey != client.APIKey {
		t.Fatal("child client did not preserve endpoint configuration")
	}
}

func TestAgentMessagingRejectsUnknownAndCompletedAgents(t *testing.T) {
	client := &agentTestLLM{chat: func(_ context.Context, _ []llm.Message) (llm.Message, error) {
		return llm.Message{Role: "assistant", Content: "done"}, nil
	}}
	runner := NewRunner(client, t.TempDir(), "test")
	if err := runner.agents.Send("missing", "hello"); err == nil {
		t.Fatal("expected unknown agent message to fail")
	}
	id, err := runner.agents.Spawn(RunRequest{WorkingDir: runner.DefaultWorkingDir, Model: "test"}, "finish", "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && runner.agents.Summary().Completed == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if err := runner.agents.Send(id, "too late"); err == nil || !strings.Contains(err.Error(), "completed") {
		t.Fatalf("expected completed-agent message error, got %v", err)
	}
}

func TestGlobFilesRejectsInvalidPatternAndSupportsClasses(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewRunner(nil, root, "test")
	if got := runner.executeGlobFiles(context.Background(), root, "[ab].go", ""); !strings.Contains(got, "a.go") || !strings.Contains(got, "b.go") || strings.Contains(got, "c.txt") {
		t.Fatalf("character-class glob = %q", got)
	}
	if got := runner.executeGlobFiles(context.Background(), root, "[abc", ""); !strings.HasPrefix(got, "Error: invalid glob pattern") {
		t.Fatalf("invalid glob = %q", got)
	}
}

func TestSpawnAgent_AttenuatedCapabilities(t *testing.T) {
	tmpDir := t.TempDir()
	var toolsOffered []llm.Tool
	mock := &mockLLM{turns: []func([]llm.Message) (llm.Message, error){
		func(messages []llm.Message) (llm.Message, error) {
			return llm.Message{Role: "assistant", Content: "inspected tools"}, nil
		},
	}}
	runner := NewRunner(mock, tmpDir, "test-model")
	runner.AllowCommands = true

	// Spawn child with only "read" capability
	id, err := runner.agents.Spawn(RunRequest{
		WorkingDir: tmpDir, Model: "test-model",
	}, "confined read-only task", "", "", 1, SpawnOptions{
		Capabilities:  []string{"read"},
		NetworkPolicy: "none",
	})
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}

	// Wait for child to complete
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && runner.agents.Summary().Completed == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	record := runner.agents.agents[id]
	if record == nil || record.State != AgentCompleted {
		t.Fatalf("expected completed agent, got %+v", record)
	}

	if len(mock.toolsSeen) == 0 {
		t.Fatal("mock LLM was not called")
	}
	toolsOffered = mock.toolsSeen[0]

	for _, tool := range toolsOffered {
		name := tool.Function.Name
		if name == "write_file" || name == "edit_file" || name == "patch_file" {
			t.Fatalf("confined read agent was offered mutating tool %q", name)
		}
		if name == "web_search" || name == "web_fetch" {
			t.Fatalf("confined read agent was offered network tool %q", name)
		}
		if name == "run_command" || name == "process_status" || name == "kill_process" {
			t.Fatalf("confined read agent was offered command tool %q", name)
		}
		if name == "spawn_agent" {
			t.Fatalf("confined read agent was offered delegation tool %q", name)
		}
	}
}

func TestSpawnAgent_NetworkPolicy_Confinement(t *testing.T) {
	runner := NewRunner(nil, t.TempDir(), "test-model")
	req := RunRequest{
		WorkingDir:    t.TempDir(),
		NetworkPolicy: "none",
		Capabilities:  []string{"read", "write"},
	}

	resSearch := runner.executeTool(toolExecutionContext{
		ctx:     context.Background(),
		request: req,
	}, "web_search", `{"query":"secret"}`)
	if !strings.Contains(resSearch.output, "Network access is denied") {
		t.Fatalf("expected network denial for web_search, got: %q", resSearch.output)
	}

	resFetch := runner.executeTool(toolExecutionContext{
		ctx:     context.Background(),
		request: req,
	}, "web_fetch", `{"url":"https://example.com"}`)
	if !strings.Contains(resFetch.output, "Network access is denied") {
		t.Fatalf("expected network denial for web_fetch, got: %q", resFetch.output)
	}
}

func TestExecuteAgentTool_SpawnWithCapabilities(t *testing.T) {
	tmpDir := t.TempDir()
	client := &agentTestLLM{chat: func(_ context.Context, _ []llm.Message) (llm.Message, error) {
		return llm.Message{Role: "assistant", Content: "done"}, nil
	}}
	runner := NewRunner(client, tmpDir, "test-model")
	parent := RunRequest{WorkingDir: tmpDir, Model: "test-model"}

	rawArgs := `{"task":"confined task","capabilities":["read","network"],"network_policy":"none"}`
	out := runner.executeAgentTool(context.Background(), parent, "spawn_agent", rawArgs)
	if !strings.Contains(out, "Spawned agent-") {
		t.Fatalf("unexpected spawn output: %q", out)
	}

	record := runner.agents.agents["agent-1"]
	if record == nil {
		t.Fatal("expected agent-1 in manager")
	}
	if len(record.Capabilities) != 2 || record.Capabilities[0] != "read" || record.Capabilities[1] != "network" {
		t.Fatalf("unexpected capabilities recorded: %v", record.Capabilities)
	}
	if record.NetworkPolicy != "none" {
		t.Fatalf("unexpected network policy: %q", record.NetworkPolicy)
	}
}

