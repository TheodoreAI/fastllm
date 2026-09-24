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

func TestStripProviderPrefix_SelfHosted(t *testing.T) {
	// A bare name routes to the self-hosted endpoint only because the user
	// configured that id; no model name is compiled into the binary.
	rememberSelfHostedModel("example-model")
	t.Cleanup(func() { rememberSelfHostedModel("") })

	cases := []struct {
		model    string
		wantBare string
		wantProv string
		wantOK   bool
	}{
		{"selfhosted:example-model", "example-model", "selfhosted", true},
		{"cluster:example-model", "example-model", "selfhosted", true},
		{"selfhosted:vendor-models/Example-Model-30B", "vendor-models/Example-Model-30B", "selfhosted", true},
		{"example-model", "example-model", "selfhosted", true},
		{"example-model-30b", "example-model-30b", "selfhosted", true},
		{"vendor-models/Example-Model-30B", "vendor-models/Example-Model-30B", "selfhosted", true},
		{"some-unrelated-model", "", "", false},
	}
	for _, c := range cases {
		bare, prov, ok := stripProviderPrefix(c.model)
		if bare != c.wantBare || prov != c.wantProv || ok != c.wantOK {
			t.Errorf("stripProviderPrefix(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.model, bare, prov, ok, c.wantBare, c.wantProv, c.wantOK)
		}
	}
}

func TestStripProviderPrefixIgnoresBareNamesWithoutConfiguredEndpoint(t *testing.T) {
	rememberSelfHostedModel("")
	if _, _, ok := stripProviderPrefix("example-model"); ok {
		t.Error("a bare name must not route anywhere when no endpoint is configured")
	}
	// An explicit prefix still works: it names the provider outright.
	if _, prov, ok := stripProviderPrefix("selfhosted:example-model"); !ok || prov != "selfhosted" {
		t.Errorf("explicit prefix should route regardless of config, got (%q, %v)", prov, ok)
	}
}

func TestSupportsToolsAndVisionForSelfHostedProvider(t *testing.T) {
	// Capability follows the provider, not any particular model name.
	for _, m := range []string{"selfhosted:example-model", "cluster:anything", "selfhosted:vendor/Other-Model"} {
		if !SupportsToolsForModel(m) {
			t.Errorf("SupportsToolsForModel(%q) = false, want true", m)
		}
		if !SupportsVisionForModel(m) {
			t.Errorf("SupportsVisionForModel(%q) = false, want true", m)
		}
	}
}

func TestRouter_ListModels_CollapsesAliasesOntoConfiguredID(t *testing.T) {
	// A server advertising three --served-model-name aliases for one model.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":[{"id":"vendor-models/Example-Model-30B"},{"id":"example-model"},{"id":"example-model-30b"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	router := &Router{
		Local: New("http://localhost:11434", "", "dummy", ""),
		clouds: cloudClients{
			selfHosted:      New(ts.URL, "test-key", "example-model", ""),
			selfHostedModel: "example-model",
		},
	}

	models, err := router.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}

	var selfHosted []Model
	for _, m := range models {
		if m.Provider == "selfhosted" {
			selfHosted = append(selfHosted, m)
		}
	}
	if len(selfHosted) != 1 {
		t.Fatalf("expected the three aliases collapsed to 1, got %d: %+v", len(selfHosted), selfHosted)
	}
	if selfHosted[0].Name != "selfhosted:example-model" {
		t.Errorf("expected selfhosted:example-model, got %q", selfHosted[0].Name)
	}
}

func TestClientWithResponsesWireAPIAndHeaders(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotAPIKey string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("api-key")

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "resp_123",
			"output": [
				{
					"type": "message",
					"role": "assistant",
					"content": [{"type": "output_text", "text": "pong"}]
				}
			]
		}`))
	}))
	defer ts.Close()

	client := New(ts.URL, "", "gpt-5.3-codex", "")
	client.WireAPI = "responses"
	client.Headers = map[string]string{
		"api-key": "test-azure-key",
	}

	result, err := client.ChatWithUsage(context.Background(), "gpt-5.3-codex", []Message{{Role: "user", Content: "ping"}}, nil, "")
	if err != nil {
		t.Fatalf("ChatWithUsage failed: %v", err)
	}
	if gotPath != "/responses" {
		t.Errorf("expected path /responses, got %q", gotPath)
	}
	if gotAPIKey != "test-azure-key" {
		t.Errorf("expected api-key 'test-azure-key', got %q", gotAPIKey)
	}
	if gotAuth != "" {
		t.Errorf("expected empty Authorization header, got %q", gotAuth)
	}
	if result.Message.Content != "pong" {
		t.Errorf("expected content 'pong', got %q", result.Message.Content)
	}
}
