package lsp

import "sync"

// Registry holds fastllm's one live gopls Session. Unlike
// terminal.Registry (which can have many concurrent shell sessions —
// one per open terminal tab), there is only ever one project root open
// at a time (see files.Reader), so there is only ever one Session worth
// tracking: Swap installs a new one and closes whatever was live before
// it, which is exactly what's needed both on server shutdown (CloseAll,
// mirroring terminal's) and when the opened folder changes (the
// frontend reconnects its WS, which calls Start again and Swaps in the
// replacement — see EditorView.jsx's closeAllTabs).
type Registry struct {
	mu      sync.Mutex
	current *Session
}

func NewRegistry() *Registry {
	return &Registry{}
}

// Swap installs newSession as the current session, closing (and
// returning) whatever session was previously live. Pass nil to just
// clear and close the current session.
func (r *Registry) Swap(newSession *Session) *Session {
	r.mu.Lock()
	old := r.current
	r.current = newSession
	r.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return old
}

// CloseAll closes the current session, if any. Nil-receiver-safe:
// Built.LSPRegistry is nil whenever gopls wasn't found on PATH, and
// shutdown paths (cmd/server's quitHandler, cmd/desktop's
// desktopApp.shutdown) call this unconditionally rather than checking
// first.
func (r *Registry) CloseAll() {
	if r == nil {
		return
	}
	r.Swap(nil)
}
