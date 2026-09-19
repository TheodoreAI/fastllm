package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

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

	if req.WorkingDir == "" {
		if root := h.Files.GetRoot(); root != "" {
			req.WorkingDir = root
		} else {
			req.WorkingDir, _ = os.Getwd()
		}
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

	runner := harness.NewRunner(h.LLM, req.WorkingDir, req.Model)

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
		flusher.Flush()

		onEvent := func(ev harness.Event) {
			payload, err := json.Marshal(ev)
			if err == nil {
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, payload)
				flusher.Flush()
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
