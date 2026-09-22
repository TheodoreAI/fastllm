package chat

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fastllm/internal/files"
	"fastllm/internal/harness"
	"fastllm/internal/llm"
)

func TestHarnessRun_Validation(t *testing.T) {
	h := &Handler{
		Files: files.New("", false),
		LLM:   nil,
	}

	t.Run("empty task rejected", func(t *testing.T) {
		body := bytes.NewBufferString(`{"task":""}`)
		req := httptest.NewRequest(http.MethodPost, "/api/harness/run", body)
		w := httptest.NewRecorder()

		h.HarnessRun(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status 400, got %d", w.Code)
		}
	})

	t.Run("invalid json rejected", func(t *testing.T) {
		body := bytes.NewBufferString(`{not-json}`)
		req := httptest.NewRequest(http.MethodPost, "/api/harness/run", body)
		w := httptest.NewRecorder()

		h.HarnessRun(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status 400, got %d", w.Code)
		}
	})
}

func TestHarnessRun_JSON(t *testing.T) {
	tmpDir := t.TempDir()

	mockClient := llm.New("http://localhost:11434/v1", "", "test-model", "")
	mockRouter := llm.NewRouter(mockClient, llm.CloudProviderConfig{})

	h := &Handler{
		Files: files.New(tmpDir, true),
		LLM:   mockRouter,
	}

	// We test with mock client pointing to a dummy local, but here we can mock the runner directly or test the endpoint formatting
	body, _ := json.Marshal(harness.RunRequest{
		Task:       "Say hello",
		WorkingDir: tmpDir,
		MaxTurns:   1,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/harness/run", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.HarnessRun(w, req)
	// Because mockClient reaches a non-listening port, it will return an error or status
	// The key is that the handler executes and handles it without panicking
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected application/json content type, got %s", w.Header().Get("Content-Type"))
	}
}

func TestHarnessRun_Streaming(t *testing.T) {
	tmpDir := t.TempDir()

	mockClient := llm.New("http://localhost:11434/v1", "", "test-model", "")
	mockRouter := llm.NewRouter(mockClient, llm.CloudProviderConfig{})

	h := &Handler{
		Files: files.New(tmpDir, true),
		LLM:   mockRouter,
	}

	body, _ := json.Marshal(harness.RunRequest{
		Task:       "Say hello",
		WorkingDir: tmpDir,
		MaxTurns:   1,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/harness/run", bytes.NewReader(body))
	req.Header.Set("Accept", "text/event-stream")
	w := httptest.NewRecorder()

	h.HarnessRun(w, req)
	if !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Errorf("expected text/event-stream, got %s", w.Header().Get("Content-Type"))
	}
}
