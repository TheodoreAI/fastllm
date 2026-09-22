package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// The shape captured from vLLM (gemma-4-31b): the first fragment carries id and
// name, every later fragment carries the same index with those fields empty and
// a slice of the arguments. Latching is what makes the assembled call usable.
func TestToolCallAccumulatorLatchesNameAcrossFragments(t *testing.T) {
	acc := newToolCallAccumulator()
	first := streamToolCallDelta{Index: 0, ID: "chatcmpl-tool-99bf", Type: "function"}
	first.Function.Name = "write_file"
	acc.Add(first)

	for _, part := range []string{`{"path":`, `"notes.txt",`, `"content":"Go`, ` memory model"}`} {
		frag := streamToolCallDelta{Index: 0}
		frag.Function.Arguments = part
		acc.Add(frag)
	}

	calls := acc.Calls()
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].Function.Name != "write_file" {
		t.Fatalf("name = %q, want write_file (later fragments must not blank it)", calls[0].Function.Name)
	}
	if calls[0].ID != "chatcmpl-tool-99bf" {
		t.Fatalf("id = %q, want the id from the first fragment", calls[0].ID)
	}
	want := `{"path":"notes.txt","content":"Go memory model"}`
	if calls[0].Function.Arguments != want {
		t.Fatalf("arguments = %q, want %q", calls[0].Function.Arguments, want)
	}
	// The reassembled arguments must be valid JSON, which is the whole point of
	// concatenating fragments rather than taking the last one.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &parsed); err != nil {
		t.Fatalf("reassembled arguments are not valid JSON: %v", err)
	}
}

// Ollama (qwen3.5:9b) sends a whole call, arguments included, in one fragment.
func TestToolCallAccumulatorHandlesSingleFragment(t *testing.T) {
	acc := newToolCallAccumulator()
	only := streamToolCallDelta{Index: 0, ID: "call_ufk6", Type: "function"}
	only.Function.Name = "list_files"
	only.Function.Arguments = `{"path":"/tmp"}`
	acc.Add(only)

	calls := acc.Calls()
	if len(calls) != 1 || calls[0].Function.Name != "list_files" {
		t.Fatalf("single-fragment call not assembled: %+v", calls)
	}
	if calls[0].Function.Arguments != `{"path":"/tmp"}` {
		t.Fatalf("arguments = %q", calls[0].Function.Arguments)
	}
}

// Several calls in one turn must come back in the provider's index order, not
// Go's random map order.
func TestToolCallAccumulatorOrdersByIndex(t *testing.T) {
	for attempt := 0; attempt < 16; attempt++ {
		acc := newToolCallAccumulator()
		for _, idx := range []int{2, 0, 1} {
			frag := streamToolCallDelta{Index: idx, Type: "function"}
			frag.Function.Name = string(rune('a' + idx))
			frag.Function.Arguments = "{}"
			acc.Add(frag)
		}
		calls := acc.Calls()
		if len(calls) != 3 {
			t.Fatalf("got %d calls, want 3", len(calls))
		}
		if calls[0].Function.Name != "a" || calls[1].Function.Name != "b" || calls[2].Function.Name != "c" {
			t.Fatalf("calls out of index order: %q %q %q",
				calls[0].Function.Name, calls[1].Function.Name, calls[2].Function.Name)
		}
	}
}

func TestToolCallAccumulatorSkipsEmptyAndDefaultsType(t *testing.T) {
	acc := newToolCallAccumulator()
	acc.Add(streamToolCallDelta{Index: 0}) // nothing usable
	if calls := acc.Calls(); calls != nil {
		t.Fatalf("empty fragment produced a call: %+v", calls)
	}

	acc2 := newToolCallAccumulator()
	frag := streamToolCallDelta{Index: 0}
	frag.Function.Name = "ping"
	acc2.Add(frag)
	calls := acc2.Calls()
	if len(calls) != 1 || calls[0].Type != "function" {
		t.Fatalf("type not defaulted to function: %+v", calls)
	}
}

// The delta must actually unmarshal from the wire bytes vLLM sends, including
// the null name/id on continuation fragments.
func TestStreamToolCallDeltaUnmarshalsContinuationFragment(t *testing.T) {
	const wire = `{"index":0,"id":null,"type":null,"function":{"name":null,"arguments":"\"notes.txt\""}}`
	var delta streamToolCallDelta
	if err := json.Unmarshal([]byte(wire), &delta); err != nil {
		t.Fatalf("continuation fragment failed to unmarshal: %v", err)
	}
	if delta.Index != 0 || delta.ID != "" || delta.Function.Name != "" {
		t.Fatalf("unexpected delta: %+v", delta)
	}
	if delta.Function.Arguments != `"notes.txt"` {
		t.Fatalf("arguments = %q", delta.Function.Arguments)
	}
}

// A full chunk as vLLM frames it must parse, so a change to chatStreamChunk that
// drops tool_calls is caught here rather than at runtime.
func TestChatStreamChunkCarriesToolCalls(t *testing.T) {
	const wire = `{"choices":[{"index":0,"delta":{"role":"assistant","content":"","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write_file","arguments":"{\"path\":"}}]},"finish_reason":null}]}`
	var chunk chatStreamChunk
	if err := json.Unmarshal([]byte(wire), &chunk); err != nil {
		t.Fatalf("chunk failed to unmarshal: %v", err)
	}
	if len(chunk.Choices) != 1 {
		t.Fatalf("got %d choices", len(chunk.Choices))
	}
	deltas := chunk.Choices[0].Delta.ToolCalls
	if len(deltas) != 1 || deltas[0].Function.Name != "write_file" {
		t.Fatalf("tool call delta not decoded: %+v", deltas)
	}
	if !strings.HasPrefix(deltas[0].Function.Arguments, `{"path":`) {
		t.Fatalf("arguments fragment = %q", deltas[0].Function.Arguments)
	}
}
