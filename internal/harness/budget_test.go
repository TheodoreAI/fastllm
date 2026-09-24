package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fastllm/internal/execution"
	"fastllm/internal/llm"
)

func TestEffectiveBudgetDefaults(t *testing.T) {
	b := effectiveBudget(Budget{})
	if b.MaxDuration != defaultMaxDuration || b.MaxWrites != defaultMaxWrites ||
		b.MaxWriteBytes != defaultMaxWriteBytes || b.MaxWebRequests != defaultMaxWebRequests {
		t.Fatalf("defaults not applied: %+v", b)
	}
	if b.MaxTokens != 0 || b.MaxCostUSD != 0 {
		t.Fatal("tokens and cost are unlimited by default")
	}
	off := effectiveBudget(Budget{MaxDuration: -1, MaxWrites: -1, MaxWriteBytes: -1, MaxWebRequests: -1, MaxTokens: -1, MaxCostUSD: -1})
	if off != (Budget{}) {
		t.Fatalf("negative limits should mean unlimited: %+v", off)
	}
}

func TestMeterChargesWritesAndWeb(t *testing.T) {
	m := newBudgetMeter(Budget{MaxWrites: 2, MaxWriteBytes: 10, MaxWebRequests: 1})
	if r := m.charge("write_file", `{"path":"a","content":"12345"}`); r != "" {
		t.Fatal(r)
	}
	if r := m.charge("write_file", `{"path":"b","content":"123456"}`); !strings.Contains(r, "write budget") {
		t.Fatalf("an 11th byte should be refused: %q", r)
	}
	// The refused call was not counted, so a small one still fits.
	if r := m.charge("edit_file", `{"path":"a","search":"x","replace":"12345"}`); r != "" {
		t.Fatalf("a fitting write was refused: %q", r)
	}
	if r := m.charge("write_file", `{"path":"c","content":""}`); !strings.Contains(r, "limit of 2 file changes") {
		t.Fatalf("a third change should be refused: %q", r)
	}
	if m.charge("web_fetch", `{"url":"https://a.example"}`) != "" || !strings.Contains(m.charge("web_search", `{"query":"q"}`), "web requests") {
		t.Fatal("the web budget was not applied")
	}
	if m.charge("read_file", `{"path":"a"}`) != "" {
		t.Fatal("reads are not budgeted")
	}
}

func TestMeterExhaustion(t *testing.T) {
	m := newBudgetMeter(Budget{MaxTokens: 100})
	m.addTurn(TurnMetrics{TotalTokens: 60}, false)
	if m.exhausted() != "" {
		t.Fatal("60 of 100 tokens is not exhausted")
	}
	m.addTurn(TurnMetrics{TotalTokens: 60}, false)
	if !strings.Contains(m.exhausted(), "token budget") {
		t.Fatal("120 of 100 tokens should be exhausted")
	}

	c := newBudgetMeter(Budget{MaxCostUSD: 1})
	c.addTurn(TurnMetrics{EstimatedCost: 0.4, CostKnown: true}, true)
	if c.exhausted() != "" {
		t.Fatal("$0.40 of $1 is not exhausted")
	}
	c.addTurn(TurnMetrics{CostKnown: false}, true)
	if !strings.Contains(c.exhausted(), "cannot be enforced") {
		t.Fatal("an unpriced billable model must stop a cost-limited run")
	}

	d := newBudgetMeter(Budget{MaxDuration: time.Millisecond})
	time.Sleep(5 * time.Millisecond)
	if !strings.Contains(d.exhausted(), "time budget") {
		t.Fatal("time budget should be exhausted")
	}
	var none *budgetMeter
	if none.exhausted() != "" || none.charge("write_file", "{}") != "" {
		t.Fatal("a nil meter limits nothing")
	}
}

// loopingLLM keeps reading the same file, as a stuck model would.
func loopingLLM() *mockLLM {
	turns := make([]func([]llm.Message) (llm.Message, error), 50)
	for i := range turns {
		turns[i] = func([]llm.Message) (llm.Message, error) {
			call := llm.ToolCall{ID: "read", Type: "function"}
			call.Function.Name = "read_file"
			call.Function.Arguments = `{"path":"a.txt"}`
			return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}, nil
		}
	}
	return &mockLLM{turns: turns}
}

func TestTokenBudgetStopsALoopingRun(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(strings.Repeat("word ", 400)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Measure one turn, then allow roughly three.
	probe := NewRunner(loopingLLM(), root, "test-model")
	one, _ := probe.Run(context.Background(), RunRequest{Task: "loop", WorkingDir: root, Model: "test-model", MaxTurns: 1}, nil)
	probe.Close()
	perTurn := one.Metrics.TotalTokens
	if perTurn == 0 {
		t.Fatal("probe measured no tokens")
	}

	mock := loopingLLM()
	r := NewRunner(mock, root, "test-model")
	defer r.Close()
	result, _ := r.Run(context.Background(), RunRequest{
		Task: "loop", WorkingDir: root, Model: "test-model", MaxTurns: 50,
		Budget: Budget{MaxTokens: 3 * perTurn},
	}, nil)
	if !strings.Contains(result.Error, "token budget exhausted") {
		t.Fatalf("result error = %q", result.Error)
	}
	if mock.turnIndex < 2 || mock.turnIndex > 4 {
		t.Fatalf("a three-turn budget stopped after %d model calls", mock.turnIndex)
	}
}

func TestWriteBudgetRefusesFurtherWrites(t *testing.T) {
	root := t.TempDir()
	write := func(name string) func([]llm.Message) (llm.Message, error) {
		return func([]llm.Message) (llm.Message, error) {
			call := llm.ToolCall{ID: name, Type: "function"}
			call.Function.Name = "write_file"
			call.Function.Arguments = `{"path":"` + name + `","content":"x"}`
			return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}, nil
		}
	}
	mock := &mockLLM{turns: []func([]llm.Message) (llm.Message, error){write("a.txt"), write("b.txt")}}
	r := NewRunner(mock, root, "test-model")
	defer r.Close()
	result, _ := r.Run(context.Background(), RunRequest{
		Task: "write twice", WorkingDir: root, Model: "test-model", PermissionMode: PermissionEdit,
		Budget: Budget{MaxWrites: 1},
	}, nil)
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal("the first write should land")
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); !os.IsNotExist(err) {
		t.Fatal("the second write exceeded the budget but landed")
	}
	var refused bool
	for _, turn := range result.History {
		for _, call := range turn.ToolCalls {
			if strings.Contains(call.Result, "budget exhausted") {
				refused = true
			}
		}
	}
	if !refused {
		t.Fatal("the model was not told why the write was refused")
	}
}

// fakeProcess is a background process that never exits.
type fakeProcess struct{}

func (fakeProcess) Snapshot() execution.ProcessState { return execution.ProcessState{} }
func (fakeProcess) ReadOutput(context.Context, uint64) (execution.Output, error) {
	return execution.Output{}, nil
}
func (fakeProcess) Wait(context.Context) (execution.Result, error) { return execution.Result{}, nil }
func (fakeProcess) Stop(context.Context) error                     { return nil }

func TestBackgroundProcessCap(t *testing.T) {
	pm := NewProcessManager()
	for i := 0; i < maxBackgroundProcesses; i++ {
		id := "proc-fake-" + string(rune('a'+i))
		pm.processes[id] = trackedProcess{id: id, process: fakeProcess{}, command: "sleep"}
	}
	if _, _, err := pm.StartTracked("echo one more", t.TempDir()); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("a ninth process should be refused: %v", err)
	}
}

func TestSetBudgetValues(t *testing.T) {
	m := permissionTestModel(t)
	for _, c := range [][2]string{{"tokens", "200k"}, {"cost", "$2.50"}, {"duration", "45m"}} {
		if err := m.setRuntimeValue(c[0], c[1]); err != nil {
			t.Fatalf("/set %s %s: %v", c[0], c[1], err)
		}
	}
	if m.budget.MaxTokens != 200000 || m.budget.MaxCostUSD != 2.5 || m.budget.MaxDuration != 45*time.Minute {
		t.Fatalf("budget = %+v", m.budget)
	}
	if err := m.setRuntimeValue("tokens", "off"); err != nil || effectiveBudget(m.budget).MaxTokens != 0 {
		t.Fatal("off should mean unlimited")
	}
	if err := m.setRuntimeValue("cost", "lots"); err == nil {
		t.Fatal("a nonsense cost should be rejected")
	}
	if card := StripANSI(FormatRuntimeCard(m.runtimeSettings(), "s")); !strings.Contains(card, "cost $2.50") || !strings.Contains(card, "time 45m0s") {
		t.Fatalf("runtime card: %s", card)
	}
}
