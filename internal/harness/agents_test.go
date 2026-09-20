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
