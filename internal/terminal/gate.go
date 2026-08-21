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
	mu        sync.RWMutex
	enabled   bool
	injectEnv bool
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

// SetInjectEnv is the live counterpart of TerminalSettings.InjectLiveChatEnv
// — see handler.go's use of InjectEnv when building a new session's
// environment.
func (g *Gate) SetInjectEnv(inject bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.injectEnv = inject
}

func (g *Gate) InjectEnv() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.injectEnv
}

// BaseURLHolder holds the live "this server's own address" value, used to
// build the OPENAI_BASE_URL a spawned terminal session gets when
// InjectEnv() is on. Not known at appserver.Build() time — the entrypoint
// (cmd/server, cmd/desktop) only learns its actual bound loopback port
// after Build() returns, once it starts listening — so this is set once,
// shortly after, and read fresh on every terminal spawn.
type BaseURLHolder struct {
	mu  sync.RWMutex
	url string
}

func NewBaseURLHolder() *BaseURLHolder {
	return &BaseURLHolder{}
}

func (h *BaseURLHolder) Set(url string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.url = url
}

func (h *BaseURLHolder) Get() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.url
}
