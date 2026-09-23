//go:build windows

package harness

import (
	"context"
	"encoding/json"
	"fastllm/internal/llm"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runReadingSecret drives one run whose model asks to read a file outside the
// workspace, and returns the tool result the model received.
func runReadingSecret(t *testing.T, sandbox bool, secret string) string {
	t.Helper()
	workspace := t.TempDir()
	quoted := "'" + strings.ReplaceAll(secret, "'", "''") + "'"
	args, _ := json.Marshal(map[string]string{"command": "Get-Content -LiteralPath " + quoted})
	var toolResult string
	mock := &mockLLM{turns: []func([]llm.Message) (llm.Message, error){
		func(messages []llm.Message) (llm.Message, error) {
			if sandbox && !strings.Contains(messages[0].Content, "isolated sandbox") {
				t.Errorf("sandboxed run did not tell the model about the sandbox:\n%s", messages[0].Content)
			}
			call := llm.ToolCall{ID: "read-1", Type: "function"}
			call.Function.Name = "run_command"
			call.Function.Arguments = string(args)
			return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}, nil
		},
		func(messages []llm.Message) (llm.Message, error) {
			toolResult = messages[len(messages)-1].Content
			return llm.Message{Role: "assistant", Content: "done"}, nil
		},
	}}
	runner := NewRunner(mock, workspace, "test-model")
	defer runner.Close()
	if _, err := runner.Run(context.Background(), RunRequest{
		Task: "read it", WorkingDir: workspace, Model: "test-model",
		AllowCommands: true, CommandsConfigured: true, PermissionMode: PermissionAuto, Sandbox: sandbox,
	}, nil); err != nil {
		t.Fatalf("Run (sandbox=%v): %v", sandbox, err)
	}
	return toolResult
}

func TestSandboxedRunCannotReadOutsideTheWorkspace(t *testing.T) {
	if err := sandboxReady(); err != nil {
		t.Skipf("sandbox not ready: %v", err)
	}
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("HARNESS-SECRET-91c2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if control := runReadingSecret(t, false, secret); !strings.Contains(control, "HARNESS-SECRET-91c2") {
		t.Fatalf("control: unsandboxed run could not read the secret, so this test proves nothing:\n%s", control)
	}
	if sandboxed := runReadingSecret(t, true, secret); strings.Contains(sandboxed, "HARNESS-SECRET-91c2") {
		t.Fatalf("sandboxed run read a file outside its workspace:\n%s", sandboxed)
	}
}
