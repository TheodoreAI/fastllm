package llm

import "testing"

var testTools = []Tool{
	{Type: "function", Function: ToolFunction{Name: "read_file"}},
	{Type: "function", Function: ToolFunction{Name: "write_file"}},
}

func TestParseFallbackToolCallRecoversPlainJSON(t *testing.T) {
	content := `{"name": "read_file", "arguments": {"path": "README.md"}}`
	call, ok := parseFallbackToolCall(content, testTools)
	if !ok {
		t.Fatal("expected a recovered tool call")
	}
	if call.Function.Name != "read_file" {
		t.Fatalf("got name %q", call.Function.Name)
	}
	if call.Function.Arguments != `{"path": "README.md"}` {
		t.Fatalf("got arguments %q", call.Function.Arguments)
	}
}

func TestParseFallbackToolCallHandlesWhitespace(t *testing.T) {
	content := "\n\n  {\n  \"name\": \"write_file\",\n  \"arguments\": {\n    \"path\": \"test.py\",\n    \"content\": \"print(1)\"\n  }\n}\n\n"
	call, ok := parseFallbackToolCall(content, testTools)
	if !ok {
		t.Fatal("expected a recovered tool call")
	}
	if call.Function.Name != "write_file" {
		t.Fatalf("got name %q", call.Function.Name)
	}
}

func TestParseFallbackToolCallRejectsUnknownToolName(t *testing.T) {
	content := `{"name": "delete_everything", "arguments": {"path": "/"}}`
	_, ok := parseFallbackToolCall(content, testTools)
	if ok {
		t.Fatal("expected rejection: tool name wasn't offered in this request")
	}
}

func TestParseFallbackToolCallRejectsProseWithEmbeddedJSON(t *testing.T) {
	content := `Sure, here's what that would look like: {"name": "read_file", "arguments": {"path": "x"}} — let me know if you want changes.`
	_, ok := parseFallbackToolCall(content, testTools)
	if ok {
		t.Fatal("expected rejection: JSON is not the entire message")
	}
}

func TestParseFallbackToolCallRejectsCodeFence(t *testing.T) {
	content := "```json\n{\"name\": \"read_file\", \"arguments\": {\"path\": \"x\"}}\n```"
	_, ok := parseFallbackToolCall(content, testTools)
	if ok {
		t.Fatal("expected rejection: wrapped in a code fence, not bare JSON")
	}
}

func TestParseFallbackToolCallRejectsPlainAnswer(t *testing.T) {
	content := "The first line of the README is \"# fastllm\"."
	_, ok := parseFallbackToolCall(content, testTools)
	if ok {
		t.Fatal("expected rejection: not JSON at all")
	}
}

func TestParseFallbackToolCallRejectsEmptyContent(t *testing.T) {
	_, ok := parseFallbackToolCall("", testTools)
	if ok {
		t.Fatal("expected rejection: empty content")
	}
}

func TestParseFallbackToolCallRejectsMissingArguments(t *testing.T) {
	content := `{"name": "read_file"}`
	_, ok := parseFallbackToolCall(content, testTools)
	if ok {
		t.Fatal("expected rejection: no arguments field")
	}
}

func TestParseFallbackToolCallRejectsWhenNoToolsOffered(t *testing.T) {
	content := `{"name": "read_file", "arguments": {"path": "x"}}`
	_, ok := parseFallbackToolCall(content, nil)
	if ok {
		t.Fatal("expected rejection: no tools were offered, so nothing to match against")
	}
}

func TestParseFallbackToolCallRejectsUnrelatedJSONObject(t *testing.T) {
	content := `{"result": "success", "value": 42}`
	_, ok := parseFallbackToolCall(content, testTools)
	if ok {
		t.Fatal("expected rejection: valid JSON but not the tool-call shape")
	}
}

func TestSupportsToolsAllowlist(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"gemma4:12b", true},
		{"gemma4:latest", true},
		{"gpt-oss:20b", true},
		{"qwen2.5-coder:7b", false}, // advertises "tools" capability but doesn't reliably use tool_calls
		{"llama3.2:latest", false},
		{"", false},
	}
	for _, c := range cases {
		if got := SupportsTools(c.model); got != c.want {
			t.Errorf("SupportsTools(%q) = %v, want %v", c.model, got, c.want)
		}
	}
}
