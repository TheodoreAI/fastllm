package terminal

import "sync"

// Gate holds the live, in-process terminal-enabled flag. The WebSocket
// route is always registered (net/http.ServeMux routes can't be
// un-registered once the server is running), so this is the actual
// enforcement point — checked on every connection attempt — and the live
// counterpart to the persisted TerminalSettings row, mirroring how
// files.Reader.SetConfig lets PUT /api/settings/files take effect
// immediately without a restart.
type Gate struct {
	mu      sync.RWMutex
	enabled bool
}

func NewGate(enabled bool) *Gate {
	return &Gate{enabled: enabled}
}

func (g *Gate) SetEnabled(enabled bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.enabled = enabled
}

func (g *Gate) Enabled() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.enabled
}
