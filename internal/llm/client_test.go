package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestStripProviderPrefix_OSU(t *testing.T) {
	cases := []struct {
		model    string
		wantBare string
		wantProv string
		wantOK   bool
	}{
		{"osu:muse-glimmer", "muse-glimmer", "osu", true},
		{"cluster:muse-glimmer", "muse-glimmer", "osu", true},
		{"osu:meta-models/Muse-Glimmer-30B", "meta-models/Muse-Glimmer-30B", "osu", true},
		{"muse-glimmer", "muse-glimmer", "osu", true},
		{"muse-glimmer-30b", "muse-glimmer-30b", "osu", true},
		{"meta-models/Muse-Glimmer-30B", "meta-models/Muse-Glimmer-30B", "osu", true},
		{"qwen2.5-coder:7b", "", "", false},
	}
	for _, c := range cases {
		bare, prov, ok := stripProviderPrefix(c.model)
		if bare != c.wantBare || prov != c.wantProv || ok != c.wantOK {
			t.Errorf("stripProviderPrefix(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.model, bare, prov, ok, c.wantBare, c.wantProv, c.wantOK)
		}
	}
}

func TestSupportsToolsAndVisionForModel_OSU(t *testing.T) {
	models := []string{
		"osu:muse-glimmer",
		"cluster:muse-glimmer",
		"osu:meta-models/Muse-Glimmer-30B",
		"muse-glimmer",
		"meta-models/Muse-Glimmer-30B",
	}
	for _, m := range models {
		if !SupportsToolsForModel(m) {
			t.Errorf("SupportsToolsForModel(%q) = false, want true", m)
		}
		if !SupportsVisionForModel(m) {
			t.Errorf("SupportsVisionForModel(%q) = false, want true", m)
		}
	}
}

func TestRouter_ListModels_DeduplicatesOSU(t *testing.T) {
	// Start a mock vLLM server returning 3 aliases for Muse Glimmer
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[{"id":"meta-models/Muse-Glimmer-30B"},{"id":"muse-glimmer"},{"id":"muse-glimmer-30b"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	router := &Router{
		Local: New("http://localhost:11434", "", "dummy", ""),
		clouds: cloudClients{
			osu: New(ts.URL, "test-key", "muse-glimmer", ""),
		},
	}

	models, err := router.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}

	var osuModels []Model
	for _, m := range models {
		if m.Provider == "osu" {
			osuModels = append(osuModels, m)
		}
	}

	if len(osuModels) != 1 {
		t.Fatalf("expected 1 deduplicated OSU model, got %d: %+v", len(osuModels), osuModels)
	}
	if osuModels[0].Name != "osu:muse-glimmer" {
		t.Errorf("expected osu:muse-glimmer, got %q", osuModels[0].Name)
	}
}

