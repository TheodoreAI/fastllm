package chat

import (
	"encoding/json"
	"io"
	"net/http"

	"fastllm/internal/store"
)

// GetNotes returns the workspace's scratchpad (see store.GetNotes) —
// intended both for the Editor sidebar's Notes panel and for a
// terminal-based AI CLI reading its own prior findings back via
// FASTLLM_BASE_URL (see internal/terminal/handler.go).
func (h *Handler) GetNotes(w http.ResponseWriter, r *http.Request) {
	content, err := store.GetNotes(h.DB, defaultWorkspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"content": content})
}

// SaveNotes overwrites the workspace's scratchpad — used by the Editor's
// Notes panel, whose textarea sends its full current content on every
// save rather than a diff.
func (h *Handler) SaveNotes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := store.SaveNotes(h.DB, defaultWorkspace, req.Content); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AppendNotes adds one line to the workspace's scratchpad without the
// caller needing to fetch and resend the whole thing first — the shape a
// terminal-based CLI actually wants when logging "here's what I just
// learned" one finding at a time. Accepts either a JSON body
// ({"text": "..."}) or, for a plain `curl --data-binary @- `-friendly
// request, a raw text/plain body.
func (h *Handler) AppendNotes(w http.ResponseWriter, r *http.Request) {
	var text string
	if ct := r.Header.Get("Content-Type"); ct == "application/json" {
		var req struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		text = req.Text
	} else {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		text = string(body)
	}
	content, err := store.AppendNotes(h.DB, defaultWorkspace, text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"content": content})
}
