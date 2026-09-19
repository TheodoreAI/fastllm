package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fastllm/internal/files"
	"fastllm/internal/llm"
)

func TestCheckWriteFreshness(t *testing.T) {
	const path = "src/main.go"
	const content = "package main\n"

	t.Run("new file is always exempt", func(t *testing.T) {
		if got := checkWriteFreshness(map[string]string{}, path, "", false); got != "" {
			t.Errorf("got %q, want no rejection for a non-existent file", got)
		}
	})

	t.Run("no prior read is rejected", func(t *testing.T) {
		got := checkWriteFreshness(map[string]string{}, path, content, true)
		if got == "" {
			t.Fatal("got no rejection, want one — path was never read this turn")
		}
		if !strings.Contains(got, "read_file") {
			t.Errorf("rejection message %q doesn't mention read_file", got)
		}
	})

	t.Run("read followed by write on unchanged content passes", func(t *testing.T) {
		lastRead := map[string]string{path: hashContent(content)}
		if got := checkWriteFreshness(lastRead, path, content, true); got != "" {
			t.Errorf("got %q, want no rejection — content matches what was read", got)
		}
	})

	t.Run("content changed since read is rejected", func(t *testing.T) {
		lastRead := map[string]string{path: hashContent(content)}
		changed := content + "\nfunc main() {}\n"
		got := checkWriteFreshness(lastRead, path, changed, true)
		if got == "" {
			t.Fatal("got no rejection, want one — on-disk content no longer matches what was read")
		}
		if !strings.Contains(got, "changed since") {
			t.Errorf("rejection message %q doesn't explain the file changed", got)
		}
	})

	t.Run("read of a different path doesn't satisfy this path's check", func(t *testing.T) {
		lastRead := map[string]string{"other/file.go": hashContent(content)}
		got := checkWriteFreshness(lastRead, path, content, true)
		if got == "" {
			t.Fatal("got no rejection, want one — the read was for a different path")
		}
	})
}

func TestCountLines(t *testing.T) {
	tests := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"one line, no trailing newline", 1},
		{"line1\n", 1},
		{"line1\nline2\n", 2},
		{"line1\nline2\nline3", 3},
	}
	for _, tt := range tests {
		if got := countLines(tt.s); got != tt.want {
			t.Errorf("countLines(%q) = %d, want %d", tt.s, got, tt.want)
		}
	}
}

func TestRunFileToolsSearchFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc needle() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.go"), []byte("package main\n\nfunc other() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	round := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round++
		w.Header().Set("Content-Type", "application/json")

		if round == 1 {
			var req struct {
				Tools []llm.Tool `json:"tools"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			found := false
			for _, tool := range req.Tools {
				if tool.Function.Name == "search_files" {
					found = true
				}
			}
			if !found {
				t.Error("search_files was not advertised in the tools sent to the model")
			}

			resp := map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": "",
						"tool_calls": []map[string]any{{
							"id":   "call_1",
							"type": "function",
							"function": map[string]string{
								"name":      "search_files",
								"arguments": `{"pattern":"func needle"}`,
							},
						}},
					},
				}},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		var req struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		last := req.Messages[len(req.Messages)-1]
		if last.Role != "tool" {
			t.Fatalf("round 2 request's last message role = %q, want %q", last.Role, "tool")
		}
		if !strings.Contains(last.Content, "main.go") || !strings.Contains(last.Content, "func needle") {
			t.Errorf("tool result content %q doesn't contain the expected match from main.go", last.Content)
		}
		if strings.Contains(last.Content, "other.go") {
			t.Errorf("tool result content %q unexpectedly matched other.go", last.Content)
		}

		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "found it",
				},
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	router := llm.NewRouter(llm.New(server.URL, "", "test-model", ""), llm.CloudProviderConfig{})
	h := New(nil, router, files.New(dir, false))

	messages := []llm.Message{{Role: "user", Content: "where is needle defined?"}}
	reads, _, _, _ := h.runFileTools(context.Background(), "test-model", &messages, "", 0)

	if round != 2 {
		t.Fatalf("got %d requests to fake LLM, want 2", round)
	}
	if len(reads) != 0 {
		t.Errorf("got %d file reads recorded, want 0", len(reads))
	}
}

func TestRunFileToolsRunTest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.25.0\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main_test.go"), []byte("package main\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	round := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round++
		w.Header().Set("Content-Type", "application/json")

		if round == 1 {
			var req struct {
				Tools []llm.Tool `json:"tools"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			found := false
			for _, tool := range req.Tools {
				if tool.Function.Name == "run_test" {
					found = true
				}
			}
			if !found {
				t.Error("run_test was not advertised in tools")
			}

			resp := map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{
						"role":    "assistant",
						"content": "",
						"tool_calls": []map[string]any{{
							"id":   "call_1",
							"type": "function",
							"function": map[string]string{
								"name":      "run_test",
								"arguments": `{}`,
							},
						}},
					},
				}},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		var req struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		last := req.Messages[len(req.Messages)-1]
		if last.Role != "tool" {
			t.Fatalf("round 2 message role = %q, want %q", last.Role, "tool")
		}
		if !strings.Contains(last.Content, "Tests passed") {
			t.Errorf("tool result %q doesn't report tests passing", last.Content)
		}

		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "tests pass",
				},
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	router := llm.NewRouter(llm.New(server.URL, "", "test-model", ""), llm.CloudProviderConfig{})
	h := New(nil, router, files.New(dir, false))

	messages := []llm.Message{{Role: "user", Content: "are tests passing?"}}
	_, _, testChecks, _ := h.runFileTools(context.Background(), "test-model", &messages, "", 0)

	if round != 2 {
		t.Fatalf("got %d requests to fake LLM, want 2", round)
	}
	if len(testChecks) != 1 || !testChecks[0].Passed {
		t.Fatalf("test checks failed: %+v", testChecks)
	}
}

func TestHashContentStableAndDistinct(t *testing.T) {
	a := hashContent("hello")
	b := hashContent("hello")
	c := hashContent("world")
	if a != b {
		t.Errorf("hashContent not stable: %q != %q", a, b)
	}
	if a == c {
		t.Errorf("hashContent collided: %q == %q", a, c)
	}
}
