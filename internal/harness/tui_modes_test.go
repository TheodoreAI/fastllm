package harness

import (
	"strings"
	"testing"
	"time"

	"fastllm/internal/llm"

	tea "github.com/charmbracelet/bubbletea"
)

// pumpEvents feeds a turn's events through Update until the turn finishes, as
// the Bubble Tea runtime would.
func pumpEvents(t *testing.T, m *teaModel) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-m.eventChan:
			if !ok {
				return
			}
			m.Update(teaAgentEventMsg(ev))
		case <-deadline:
			t.Fatal("timed out waiting for the turn to finish")
		}
	}
}

func TestShiftTabCyclesModesAndClearsGrants(t *testing.T) {
	m := newBusyModel(t, &mockLLM{})
	m.permissionMode = PermissionAgent
	m.permissionController().Grant("write_file")
	want := []PermissionMode{PermissionEdit, PermissionFull, PermissionPlan, PermissionAgent}
	for _, mode := range want {
		m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
		if m.permissionMode != mode || m.permissionController().Mode != mode {
			t.Fatalf("Shift+Tab gave %q (controller %q), want %q", m.permissionMode, m.permissionController().Mode, mode)
		}
		if m.permissionController().HasGrant("write_file") {
			t.Fatalf("session grant survived switching to %s", mode)
		}
		if !strings.Contains(stripANSI(m.View()), "◈ "+strings.ToUpper(mode.Label())) {
			t.Fatalf("header does not show %s", mode.Label())
		}
	}
}

// I6: a running turn keeps the mode it started with, so switching is refused
// rather than shown but not applied.
func TestModeCannotChangeDuringATurn(t *testing.T) {
	m := permissionTestModel(t)
	m.isExecuting = true
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.permissionMode != PermissionAgent {
		t.Fatalf("Shift+Tab changed the mode mid-turn to %q", m.permissionMode)
	}
	if err := m.setRuntimeValue("permissions", "full"); err == nil || m.permissionMode != PermissionAgent {
		t.Fatalf("/set permissions changed the mode mid-turn: %v, %q", err, m.permissionMode)
	}
}

func TestLegacySessionModesLoad(t *testing.T) {
	for stored, want := range map[PermissionMode]PermissionMode{
		"ask": PermissionAgent, "read-only": PermissionPlan, "auto": PermissionFull,
		"edit": PermissionEdit, "nonsense": PermissionPlan,
	} {
		m := permissionTestModel(t)
		if err := m.loadSession(&InteractiveSession{WorkingDir: m.workingDir, Runtime: InteractiveRuntime{PermissionMode: stored}}); err != nil {
			t.Fatal(err)
		}
		if m.permissionMode != want || m.permissionController().Mode != want {
			t.Fatalf("stored %q loaded as %q, want %q", stored, m.permissionMode, want)
		}
	}
}

func planningLLM() *mockLLM {
	return &mockLLM{turns: []func([]llm.Message) (llm.Message, error){
		func([]llm.Message) (llm.Message, error) {
			call := llm.ToolCall{ID: "plan-1", Type: "function"}
			call.Function.Name = "submit_plan"
			call.Function.Arguments = `{"plan":"1. create notes.txt"}`
			return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}, nil
		},
		func(messages []llm.Message) (llm.Message, error) {
			return llm.Message{Role: "assistant", Content: "implemented"}, nil
		},
	}}
}

// Approval is the user's act: the plan arrives, the mode stays plan until a
// key is pressed, and approving switches the mode and starts the work.
func TestPlanApprovalSwitchesModeAndStartsTheWork(t *testing.T) {
	mock := planningLLM()
	m := newBusyModel(t, mock)
	m.permissionMode = PermissionPlan
	m.handleAgentSubmit("add notes")
	pumpEvents(t, m)

	if m.pendingPlan != "1. create notes.txt" || m.permissionMode != PermissionPlan {
		t.Fatalf("after the plan: pending=%q mode=%q", m.pendingPlan, m.permissionMode)
	}
	if !strings.Contains(stripANSI(m.View()), "Plan ready") {
		t.Fatal("approval choices are not shown")
	}
	// Ordinary keys do not type into the prompt or start anything.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if m.isExecuting || m.pendingPlan == "" {
		t.Fatal("an unrelated key resolved the plan")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if m.permissionMode != PermissionEdit || m.pendingPlan != "" || !m.isExecuting {
		t.Fatalf("approve-to-edit: mode=%q pending=%q executing=%v", m.permissionMode, m.pendingPlan, m.isExecuting)
	}
	if !strings.Contains(m.pendingPrompt, "Implement the approved plan") || !strings.Contains(m.pendingPrompt, "create notes.txt") {
		t.Fatalf("implementation turn prompt: %q", m.pendingPrompt)
	}
	pumpEvents(t, m)
	last := toolNames(mock.toolsSeen[len(mock.toolsSeen)-1])
	if !strings.Contains(strings.Join(last, ","), "write_file") || strings.Contains(strings.Join(last, ","), "run_command") {
		t.Fatalf("implementation turn did not run under edit: %v", last)
	}
}

func TestKeepPlanningLeavesModeUnchanged(t *testing.T) {
	m := newBusyModel(t, planningLLM())
	m.permissionMode = PermissionPlan
	m.handleAgentSubmit("add notes")
	pumpEvents(t, m)
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.permissionMode != PermissionPlan || m.pendingPlan != "" || m.isExecuting {
		t.Fatalf("esc: mode=%q pending=%q executing=%v", m.permissionMode, m.pendingPlan, m.isExecuting)
	}
}

// Sessions default to plan: an unset mode, whether from an old saved session
// or a controller built without one, is read-only until raised.
func TestUnsetModeDefaultsToPlan(t *testing.T) {
	if got := loadedPermissionMode(""); got != PermissionPlan {
		t.Fatalf("a saved session without a mode loads as %q", got)
	}
	if got := NewPermissionController("", nil).Mode; got != PermissionPlan {
		t.Fatalf("a controller without a mode starts as %q", got)
	}
}
