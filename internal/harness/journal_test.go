package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fastllm/internal/llm"
)

func toolCallTurn(name, args string) func([]llm.Message) (llm.Message, error) {
	return func([]llm.Message) (llm.Message, error) {
		call := llm.ToolCall{ID: name, Type: "function"}
		call.Function.Name = name
		call.Function.Arguments = args
		return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}, nil
	}
}

// runEdits runs one prompt in edit mode whose model makes the given calls.
func runEdits(t *testing.T, root string, journal *WriteJournal, task string, calls ...func([]llm.Message) (llm.Message, error)) {
	t.Helper()
	r := NewRunner(&mockLLM{turns: calls}, root, "test-model")
	defer r.Close()
	if _, err := r.Run(context.Background(), RunRequest{
		Task: task, WorkingDir: root, Model: "test-model", PermissionMode: PermissionEdit, Journal: journal,
	}, nil); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "<missing>"
	}
	return string(data)
}

// Not a git repository: undo must work anyway.
func TestUndoRevertsTheLastPrompt(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "a.txt")
	if err := os.WriteFile(existing, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal := NewWriteJournal()
	runEdits(t, root, journal, "change things",
		toolCallTurn("write_file", `{"path":"new.txt","content":"created"}`),
		toolCallTurn("edit_file", `{"path":"a.txt","search":"original","replace":"changed"}`),
		toolCallTurn("edit_file", `{"path":"a.txt","search":"changed","replace":"changed twice"}`),
	)
	if readFile(t, existing) != "changed twice\n" || readFile(t, filepath.Join(root, "new.txt")) != "created" {
		t.Fatal("the run did not make its changes")
	}

	report, err := journal.Undo(false)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, existing); got != "original\n" {
		t.Fatalf("a.txt = %q, want the state before the prompt", got)
	}
	if got := readFile(t, filepath.Join(root, "new.txt")); got != "<missing>" {
		t.Fatal("a file the prompt created should be deleted")
	}
	if len(report.Restored) != 1 || len(report.Deleted) != 1 || report.Label != "change things" {
		t.Fatalf("report = %+v", report)
	}
	if _, err := journal.Undo(false); err == nil {
		t.Fatal("nothing should be left to undo")
	}
}

func TestUndoGoesBackOnePromptAtATime(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	journal := NewWriteJournal()
	runEdits(t, root, journal, "first", toolCallTurn("write_file", `{"path":"notes.txt","content":"one"}`))
	runEdits(t, root, journal, "second", toolCallTurn("write_file", `{"path":"notes.txt","content":"two"}`))

	if report, _ := journal.Undo(false); report.Label != "second" || readFile(t, path) != "one" {
		t.Fatalf("first undo: %q, file %q", report.Label, readFile(t, path))
	}
	if report, _ := journal.Undo(false); report.Label != "first" || readFile(t, path) != "<missing>" {
		t.Fatalf("second undo: %q, file %q", report.Label, readFile(t, path))
	}
}

// The user's own edits after the model wrote a file are theirs.
func TestUndoLeavesFilesTheUserChanged(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal := NewWriteJournal()
	runEdits(t, root, journal, "edit", toolCallTurn("write_file", `{"path":"a.txt","content":"model"}`))
	if err := os.WriteFile(path, []byte("model, then the user"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := journal.Undo(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Conflicts) != 1 || readFile(t, path) != "model, then the user" {
		t.Fatalf("a user-edited file was reverted: %+v, %q", report, readFile(t, path))
	}
	if !strings.Contains(StripANSI(FormatUndoReport(report)), "/undo force") {
		t.Fatal("the report should explain how to force it")
	}
	report, err = journal.Undo(true)
	if err != nil || len(report.Restored) != 1 || readFile(t, path) != "original" {
		t.Fatalf("force: %+v %v, file %q", report, err, readFile(t, path))
	}
}

// Writes the monitor or the budget refused never happened, so there is
// nothing of theirs to undo.
func TestRefusedWritesAreNotJournaled(t *testing.T) {
	root := gitWorkspace(t)
	journal := NewWriteJournal()
	runEdits(t, root, journal, "try", toolCallTurn("write_file", `{"path":".git/config","content":"x"}`))
	if _, err := journal.Undo(false); err == nil {
		t.Fatal("a refused write left something to undo")
	}
}

func TestTUIUndoCommand(t *testing.T) {
	m := permissionTestModel(t)
	path := filepath.Join(m.workingDir, "x.txt")
	runEdits(t, m.workingDir, m.writeJournal(), "make x", toolCallTurn("write_file", `{"path":"x.txt","content":"x"}`))
	m.handleAgentSubmit("/undo")
	if readFile(t, path) != "<missing>" {
		t.Fatal("/undo did not revert the prompt")
	}
	if !strings.Contains(StripANSI(m.historyText.String()), "deleted") {
		t.Fatalf("/undo output: %s", StripANSI(m.historyText.String()))
	}
	m.handleAgentSubmit("/undo")
	if !strings.Contains(m.historyText.String(), "no model file changes") {
		t.Fatal("a second /undo should say there is nothing left")
	}
}
