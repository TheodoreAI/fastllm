package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"fastllm/internal/llm"
)

func cancellationWrite(id, path string) llm.ToolCall {
	call := llm.ToolCall{ID: id, Type: "function"}
	call.Function.Name = "write_file"
	call.Function.Arguments = `{"path":"` + path + `","content":"changed"}`
	return call
}

func TestCancellationRejectsLateModelReply(t *testing.T) {
	for _, reply := range []llm.Message{
		{Role: "assistant", Content: "late answer"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{cancellationWrite("late", "late.txt")}},
	} {
		t.Run(reply.Content+"tool", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &mockLLM{turns: []func([]llm.Message) (llm.Message, error){func([]llm.Message) (llm.Message, error) {
				cancel() // Model clients can return success concurrently with cancellation.
				return reply, nil
			}}}
			r := NewRunner(client, t.TempDir(), "test-model")
			defer r.Close()
			var turns, calls, finished int
			result, err := r.Run(ctx, RunRequest{Task: "test", PermissionMode: PermissionFull}, func(ev Event) {
				switch ev.Type {
				case EventTurnStart:
					turns++
				case EventToolCall:
					calls++
				case EventTaskFinished:
					finished++
				}
			})
			if !errors.Is(err, context.Canceled) || result.Success || calls != 0 || turns != 1 || finished != 1 || client.turnIndex != 1 {
				t.Fatalf("cancellation revived work: result=%+v err=%v turns=%d calls=%d finished=%d", result, err, turns, calls, finished)
			}
			if _, err := os.Stat(filepath.Join(r.DefaultWorkingDir, "late.txt")); !os.IsNotExist(err) {
				t.Fatalf("late reply mutated disk: %v", err)
			}
		})
	}
}

func TestCancellationStopsRemainingToolsAndNextTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &mockLLM{turns: []func([]llm.Message) (llm.Message, error){func([]llm.Message) (llm.Message, error) {
		return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{cancellationWrite("one", "one.txt"), cancellationWrite("two", "two.txt")}}, nil
	}}}
	r := NewRunner(client, t.TempDir(), "test-model")
	defer r.Close()
	var calls int
	result, err := r.Run(ctx, RunRequest{Task: "test", PermissionMode: PermissionFull}, func(ev Event) {
		if ev.Type == EventToolCall {
			calls++
		}
		if ev.Type == EventToolResult {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || result.Success || calls != 1 || client.turnIndex != 1 {
		t.Fatalf("cancellation continued tools: calls=%d chats=%d result=%+v err=%v", calls, client.turnIndex, result, err)
	}
	if _, err := os.Stat(filepath.Join(r.DefaultWorkingDir, "one.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.DefaultWorkingDir, "two.txt")); !os.IsNotExist(err) {
		t.Fatalf("second tool ran: %v", err)
	}
}

func TestCancellationDuringApprovalPreventsMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &mockLLM{turns: []func([]llm.Message) (llm.Message, error){func([]llm.Message) (llm.Message, error) {
		return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{cancellationWrite("late", "late.txt")}}, nil
	}}}
	r := NewRunner(client, t.TempDir(), "test-model")
	defer r.Close()
	_, err := r.Run(ctx, RunRequest{Task: "test", PermissionMode: PermissionAgent, Authorize: func(ConsentRequest) bool { cancel(); return true }}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(r.DefaultWorkingDir, "late.txt")); !os.IsNotExist(err) {
		t.Fatalf("canceled approval mutated disk: %v", err)
	}
}

func TestCancellationBeforeRunMakesNoModelCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &mockLLM{}
	r := NewRunner(client, t.TempDir(), "test-model")
	defer r.Close()
	_, err := r.Run(ctx, RunRequest{Task: "test"}, nil)
	if !errors.Is(err, context.Canceled) || len(client.toolsSeen) != 0 {
		t.Fatalf("err=%v chats=%d", err, len(client.toolsSeen))
	}
}

func TestEscapeIgnoresQueuedActivityAndLateSuccess(t *testing.T) {
	m := newBusyModel(t, &mockLLM{})
	m.isExecuting = true
	m.cancelTurn = func() {}
	m.eventChan = make(chan Event)
	m.lastResponse = "previous run's answer"
	m.pendingPrompt = "canceled prompt"
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !strings.Contains(m.windowTitle(), "canceling") || m.progressBar() != nil {
		t.Fatal("canceled turn still appears to be working")
	}
	m.statusNotice = ""
	if !strings.Contains(m.renderFooter(), "Canceling agent turn") {
		t.Fatal("clearing status notice revived the running-turn hint")
	}
	before := m.historyText.String()
	for _, ev := range []Event{
		{Type: EventTurnStart, Turn: 42},
		{Type: EventTokenDelta, Response: "revived\n"},
		{Type: EventPlanProposed, Response: "revived plan"},
		{Type: EventToolCall, ToolCall: &ToolCallRecord{Name: "write_file"}},
	} {
		m.Update(teaAgentEventMsg(ev))
	}
	if m.historyText.String() != before || m.pendingPlan != "" || m.activeTurn == 42 {
		t.Fatal("queued events revived canceled run")
	}
	m.Update(teaAgentEventMsg(Event{Type: EventTaskFinished, Result: &RunResult{Success: true, FinalResponse: "late success"}}))
	if m.isExecuting || m.canceling || m.pendingPlan != "" || m.turnFailed || !strings.Contains(StripANSI(m.historyText.String()), "Task canceled.") || strings.Contains(m.historyText.String(), "late success") {
		t.Fatalf("bad canceled completion: %s", m.historyText.String())
	}
	for _, message := range m.sessionMessages {
		if message.Content == "previous run's answer" {
			t.Fatal("canceled prompt reused an earlier run's answer")
		}
	}
	old := m.eventChan
	before = m.historyText.String()
	m.Update(teaRunEventMsg{source: old, event: Event{Type: EventTokenDelta, Response: "late after completion\n"}})
	if m.historyText.String() != before {
		t.Fatal("late event changed a completed run")
	}
	m.eventChan = make(chan Event)
	m.isExecuting = true
	m.Update(teaRunEventMsg{source: old, event: Event{Type: EventTaskFinished}})
	if !m.isExecuting {
		t.Fatal("old completion stopped new turn")
	}
}

func TestEventWaitCapturesItsRunChannel(t *testing.T) {
	m := newBusyModel(t, &mockLLM{})
	old := make(chan Event, 1)
	m.eventChan = old
	wait := m.waitForNextEvent()
	m.eventChan = make(chan Event, 1)
	old <- Event{Type: EventTurnStart, Turn: 7}
	msg := wait().(teaRunEventMsg)
	if msg.source != old || msg.event.Turn != 7 {
		t.Fatal("event wait changed to another run")
	}
}

func TestEscapeDuringApprovalCancelsTurn(t *testing.T) {
	m := newBusyModel(t, &mockLLM{})
	canceled := false
	m.isExecuting = true
	m.cancelTurn = func() { canceled = true }
	reply := make(chan permissionDecision, 1)
	m.pendingPermission = &teaPermissionRequestMsg{ToolName: "write_file", Reply: reply}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if (<-reply).Allow || !canceled || !m.canceling || m.pendingPermission != nil {
		t.Fatal("Escape denied tool but did not stop the turn")
	}
}

type lateCancellationClient struct {
	started chan struct{}
	release chan struct{}
}

func (c *lateCancellationClient) Chat(context.Context, string, []llm.Message, []llm.Tool, string) (llm.Message, error) {
	close(c.started)
	<-c.release // Deliberately model a client that ignores cancellation.
	return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{cancellationWrite("late", "late.txt")}}, nil
}

func TestEscapeStopsWorkerDespiteLateModelReply(t *testing.T) {
	client := &lateCancellationClient{started: make(chan struct{}), release: make(chan struct{})}
	m := newBusyModel(t, client)
	t.Cleanup(m.runner.Close)
	if m.handleAgentSubmit("write a file") == nil {
		t.Fatal("turn did not start")
	}
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		close(client.release)
		t.Fatal("model request did not start")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	close(client.release)
	for {
		select {
		case ev, ok := <-m.eventChan:
			if !ok {
				if m.isExecuting || m.canceling || m.activeTurn != 0 || m.pendingPrompt != "" {
					t.Fatal("worker completion revived canceled turn")
				}
				if _, err := os.Stat(filepath.Join(m.workingDir, "late.txt")); !os.IsNotExist(err) {
					t.Fatalf("late worker reply wrote a file: %v", err)
				}
				return
			}
			m.Update(teaRunEventMsg{event: ev, source: m.eventChan})
		case <-time.After(5 * time.Second):
			t.Fatal("canceled worker did not finish")
		}
	}
}
