package terminal

import "sync"

// Registry tracks every live terminal Session so they can all be torn down
// together on server shutdown (see cmd/server's quitHandler) — without
// this, a graceful /api/quit would leave spawned powershell.exe processes
// orphaned since nothing else references them once their WebSocket closes.
type Registry struct {
	mu       sync.Mutex
	sessions map[Session]struct{}
}

func NewRegistry() *Registry {
	return &Registry{sessions: make(map[Session]struct{})}
}

func (r *Registry) add(s Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[s] = struct{}{}
}

func (r *Registry) remove(s Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, s)
}

// CloseAll closes every currently live session. Safe to call even if
// sessions are concurrently closing themselves via remove.
func (r *Registry) CloseAll() {
	r.mu.Lock()
	sessions := make([]Session, 0, len(r.sessions))
	for s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()

	for _, s := range sessions {
		_ = s.Close()
	}
}
