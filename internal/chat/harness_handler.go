package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fastllm/internal/harness"
	"fastllm/internal/store"
)

// HarnessRun handles POST /api/harness/run.
// Accepts a JSON harness.RunRequest. If the caller requests an SSE stream
// (via Accept: text/event-stream or ?stream=true), progress events are streamed
// in real-time. Otherwise, it blocks until task completion and returns the full
// JSON RunResult.
func (h *Handler) HarnessRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req harness.RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON payload: %v", err), http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.Task) == "" {
		http.Error(w, "task description is required", http.StatusBadRequest)
		return
	}
	// An omitted mode runs as plan (read-only); a misspelled one is an error
	// rather than silently read-only, so the caller learns why nothing changed.
	if strings.TrimSpace(string(req.PermissionMode)) != "" {
		mode, err := harness.ParsePermissionMode(string(req.PermissionMode))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req.PermissionMode = mode
	}

	// The request chooses its own authority, so the server caps it: a caller
	// holding the token still cannot exceed what the operator allowed.
	if harness.ModeRank(req.PermissionMode) > harness.ModeRank(h.MaxMode) {
		http.Error(w, fmt.Sprintf("forbidden: permission_mode %q exceeds this server's maximum %q (FASTLLM_MAX_MODE)",
			harness.NormalizeMode(req.PermissionMode), harness.NormalizeMode(h.MaxMode)), http.StatusForbidden)
		return
	}

	if req.WorkingDir == "" {
		if root := h.Files.GetRoot(); root != "" {
			req.WorkingDir = root
		} else {
			req.WorkingDir, _ = os.Getwd()
		}
	}
	if !withinAllowedRoots(req.WorkingDir, h.AllowedRoots) {
		http.Error(w, fmt.Sprintf("forbidden: working_dir %q is outside this server's allowed roots (FASTLLM_ALLOWED_ROOTS)", req.WorkingDir), http.StatusForbidden)
		return
	}

	if req.Model == "" && h.LLM != nil {
		req.Model = h.LLM.ChatModel()
	}

	convID := req.ConversationID
	if h.DB != nil {
		if convID == 0 {
			newID, err := store.CreateConversation(h.DB, defaultWorkspace, conversationTitle(req.Task))
			if err == nil {
				convID = newID
				req.ConversationID = convID
			}
		}
		if convID > 0 {
			_, _ = store.SaveMessage(h.DB, defaultWorkspace, convID, "user", req.Task, nil)
		}
	}

	// Headless runs share one log per day; each record carries its own time.
	req.Audit = harness.OpenAuditLog("server-" + time.Now().UTC().Format("20060102"))

	runner := harness.NewRunner(h.LLM, req.WorkingDir, req.Model)
	defer runner.Close()

	saveFinalResponse := func(res *harness.RunResult) {
		if convID > 0 && h.DB != nil {
			var text string
			if res != nil && res.FinalResponse != "" {
				text = res.FinalResponse
			} else if res != nil && res.Success {
				text = "Task completed successfully."
			} else if res != nil && !res.Success {
				if res.Error != "" {
					text = "Task failed: " + res.Error
				} else {
					text = "Task failed."
				}
			}
			if text != "" {
				_, _ = store.SaveMessage(h.DB, defaultWorkspace, convID, "assistant", text, nil)
			}
		}
	}

	isStream := strings.Contains(r.Header.Get("Accept"), "text/event-stream") || r.URL.Query().Get("stream") == "true"
	if isStream {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		flusher.Flush()

		var mu sync.Mutex
		writeSSE := func(eventType string, data []byte) {
			mu.Lock()
			defer mu.Unlock()
			if eventType != "" {
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
			} else {
				fmt.Fprintf(w, "data: %s\n\n", data)
			}
			flusher.Flush()
		}

		writePing := func() {
			mu.Lock()
			defer mu.Unlock()
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}

		// Keepalive ticker to prevent intermediate proxies, browsers, or Windows TCP timeouts
		// from cutting off the stream with "Error in input stream" during long LLM inference or tool execution.
		doneChan := make(chan struct{})
		go func() {
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					writePing()
				case <-doneChan:
					return
				case <-r.Context().Done():
					return
				}
			}
		}()

		onEvent := func(ev harness.Event) {
			payload, err := json.Marshal(ev)
			if err == nil {
				writeSSE(string(ev.Type), payload)
			}
		}

		if convID > 0 {
			onEvent(harness.Event{
				Type:           harness.EventConversation,
				ConversationID: convID,
			})
		}

		res, _ := runner.Run(r.Context(), req, onEvent)
		saveFinalResponse(res)

		close(doneChan)
		writeSSE("done", []byte("{}"))
		return
	}

	res, err := runner.Run(r.Context(), req, nil)
	saveFinalResponse(res)
	if err != nil && res == nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if res.Success {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	_ = json.NewEncoder(w).Encode(res)
}

// withinAllowedRoots reports whether dir, with symlinks resolved, lies inside
// one of roots. No roots means no restriction (the server always sets some).
func withinAllowedRoots(dir string, roots []string) bool {
	if len(roots) == 0 {
		return true
	}
	resolve := func(p string) (string, bool) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", false
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		return abs, true
	}
	target, ok := resolve(dir)
	if !ok {
		return false
	}
	for _, root := range roots {
		base, ok := resolve(root)
		if !ok {
			continue
		}
		rel, err := filepath.Rel(base, target)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}
