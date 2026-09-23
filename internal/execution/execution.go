// Package execution owns admission, workspace identity, and subprocess lifetime.
// The local backend is not an OS sandbox.
package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	ErrDenied      = errors.New("execution disabled by policy")
	ErrClosed      = errors.New("execution scope is closed")
	ErrNoScope     = errors.New("execution requires a scope")
	ErrUnsupported = errors.New("requested execution isolation is unavailable")
)

type Policy struct{ Commands, Write, Network bool }

// LocalPolicy preserves unrestricted model execution for standalone local APIs.
// Enclosing scopes always take precedence over this explicit default.
func LocalPolicy() Policy { return Policy{true, true, true} }

type Options struct {
	Workspace string
	Policy    Policy
	// Backend names a registered backend; empty selects the local backend.
	Backend string
	// RequireIsolation refuses any backend that does not enforce OS-level
	// restrictions. It never downgrades to local execution.
	RequireIsolation bool
	Timeout          time.Duration
	MaxOutputBytes   int
	MaxProcesses     int
}

type Command struct {
	Executable string
	Args       []string
	Shell      string
	Dir        string // relative to the scope workspace
	Timeout    time.Duration
	Stdin      io.Reader // permitted only in a direct-user scope
}

type ProcessState struct {
	ID        string
	PID       int // diagnostic only
	StartedAt time.Time
	Exited    bool
	ExitCode  int
	Reason    string
}

type Result struct {
	ProcessState
	Stdout, Stderr, Output string
	Truncated              bool
}

func (r Result) Err() error {
	if r.Reason == "timeout" {
		return context.DeadlineExceeded
	}
	if r.Reason == "canceled" {
		return context.Canceled
	}
	if r.ExitCode != 0 {
		return fmt.Errorf("process exited with code %d", r.ExitCode)
	}
	return nil
}

type Process interface {
	Snapshot() ProcessState
	ReadOutput(context.Context, uint64) (Output, error)
	Wait(context.Context) (Result, error)
	Stop(context.Context) error
}

type Manager struct {
	mu       sync.Mutex
	closed   bool
	next     uint64
	scopes   map[*Scope]struct{}
	backends map[string]Backend
}

func NewManager() *Manager {
	return &Manager{
		scopes:   make(map[*Scope]struct{}),
		backends: map[string]Backend{LocalBackendName: localBackend{}},
	}
}

// Register adds a backend. The controller decides which backends exist; model
// arguments only select among the ones already registered.
func (m *Manager) Register(b Backend) error {
	if b == nil || b.Name() == "" {
		return errors.New("backend must have a name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if _, exists := m.backends[b.Name()]; exists {
		return fmt.Errorf("backend %q is already registered", b.Name())
	}
	m.backends[b.Name()] = b
	return nil
}

type Scope struct {
	mu          sync.Mutex
	manager     *Manager
	backend     BackendScope
	backendName string
	isolated    bool
	options     Options
	workspace   string
	user        bool
	environment []string
	ctx         context.Context
	cancel      context.CancelFunc
	closed      bool
	closeOnce   sync.Once
	closeErr    error
	closeDone   chan struct{}
	next        uint64
	id          uint64
	processes   map[string]Process
}

func (m *Manager) Open(ctx context.Context, opts Options) (*Scope, error) {
	return m.open(ctx, opts, false)
}

// OpenUser is reserved for direct human shell actions, never model arguments.
func (m *Manager) OpenUser(ctx context.Context, workspace string) (*Scope, error) {
	return m.open(ctx, Options{Workspace: workspace, Policy: LocalPolicy()}, true)
}

// backendLocked resolves a backend without ever widening the request.
func (m *Manager) backendLocked(opts Options) (Backend, error) {
	name := opts.Backend
	if name == "" {
		name = LocalBackendName
	}
	b, ok := m.backends[name]
	if !ok {
		if opts.RequireIsolation {
			return nil, fmt.Errorf("%w: no backend named %q", ErrUnsupported, name)
		}
		return nil, fmt.Errorf("no backend named %q", name)
	}
	if opts.RequireIsolation && !b.Isolated() {
		return nil, fmt.Errorf("%w: backend %q does not isolate", ErrUnsupported, name)
	}
	return b, nil
}

func (m *Manager) open(ctx context.Context, opts Options, user bool) (*Scope, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(opts.Workspace)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("execution workspace: %w", err)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = 1024 * 1024
	}
	if opts.MaxProcesses <= 0 {
		opts.MaxProcesses = 64
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	backend, err := m.backendLocked(opts)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.next++
	id := m.next
	m.mu.Unlock()

	// An unavailable backend is an error, never a reason to run elsewhere.
	if err := backend.Available(ctx); err != nil {
		return nil, fmt.Errorf("backend %q unavailable: %w", backend.Name(), err)
	}

	environment := processEnvironment(user)
	lifetime, cancel := context.WithCancel(ctx)
	bound, err := backend.Open(lifetime, ScopeSpec{
		Workspace:      root,
		Policy:         opts.Policy,
		Environment:    environment,
		Timeout:        opts.Timeout,
		MaxOutputBytes: opts.MaxOutputBytes,
		MaxProcesses:   opts.MaxProcesses,
		User:           user,
	})
	if err != nil {
		cancel()
		return nil, err
	}

	s := &Scope{
		manager:     m,
		backend:     bound,
		backendName: backend.Name(),
		isolated:    backend.Isolated(),
		options:     opts,
		workspace:   root,
		user:        user,
		environment: environment,
		ctx:         lifetime,
		cancel:      cancel,
		closeDone:   make(chan struct{}),
		id:          id,
		processes:   make(map[string]Process),
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		_ = bound.Close(context.Background())
		return nil, ErrClosed
	}
	m.scopes[s] = struct{}{}
	m.mu.Unlock()

	go func() { <-lifetime.Done(); _ = s.Close(context.Background()) }()
	return s, nil
}

func (s *Scope) Workspace() string { return s.workspace }

// Backend names the backend executing this scope's commands.
func (s *Scope) Backend() string { return s.backendName }

// Isolated reports whether this scope's commands run under OS-level
// restrictions. A false result means policy admission only.
func (s *Scope) Isolated() bool { return s.isolated }

func (s *Scope) Check() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkLocked()
}

func (s *Scope) checkLocked() error {
	if s.closed {
		return ErrClosed
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	p := s.options.Policy
	if !p.Commands || !p.Write || !p.Network {
		return ErrDenied
	}
	return nil
}

func (s *Scope) resolveDir(relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("working directory must be workspace-relative")
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(s.workspace, relative))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(s.workspace, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("working directory escapes workspace")
	}
	return dir, nil
}

// Start admits a command and hands it to the backend. Every check that decides
// what a command may do happens here, so no backend can widen a scope.
func (s *Scope) Start(ctx context.Context, command Command) (Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(); err != nil {
		return nil, err
	}
	if len(s.processes) >= s.options.MaxProcesses {
		return nil, fmt.Errorf("scope process limit reached")
	}
	if (command.Executable == "") == (strings.TrimSpace(command.Shell) == "") {
		return nil, fmt.Errorf("specify exactly one executable or shell command")
	}
	if command.Shell != "" && len(command.Args) != 0 {
		return nil, fmt.Errorf("shell command cannot include argv")
	}
	if command.Stdin != nil && !s.user {
		return nil, ErrDenied
	}
	dir, err := s.resolveDir(command.Dir)
	if err != nil {
		return nil, err
	}
	timeout := command.Timeout
	if timeout <= 0 || timeout > s.options.Timeout {
		timeout = s.options.Timeout
	}
	s.next++
	id := fmt.Sprintf("scope-%d-proc-%d", s.id, s.next)
	p, err := s.backend.Start(ctx, Launch{ID: id, Command: command, Dir: dir, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	s.processes[id] = p
	return p, nil
}

func (s *Scope) Process(id string) (Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.processes[id]
	if !ok {
		return nil, fmt.Errorf("unknown process %q in this scope", id)
	}
	return p, nil
}

// Close stops and joins the scope's commands, then releases backend resources.
// Concurrent callers share one shutdown rather than closing a backend twice.
func (s *Scope) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.closeErr = s.close(ctx)
		close(s.closeDone)
	})
	<-s.closeDone
	return s.closeErr
}

func (s *Scope) close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	processes := make([]Process, 0, len(s.processes))
	for _, p := range s.processes {
		processes = append(processes, p)
	}
	s.mu.Unlock()

	errs := make([]error, len(processes))
	var wg sync.WaitGroup
	for i, p := range processes {
		wg.Add(1)
		go func(i int, p Process) {
			defer wg.Done()
			errs[i] = p.Stop(ctx)
		}(i, p)
	}
	wg.Wait()

	err := errors.Join(errs...)
	if backendErr := s.backend.Close(ctx); err == nil {
		err = backendErr
	}

	s.manager.mu.Lock()
	delete(s.manager.scopes, s)
	s.manager.mu.Unlock()
	return err
}

func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	scopes := make([]*Scope, 0, len(m.scopes))
	for s := range m.scopes {
		scopes = append(scopes, s)
		s.cancel()
	}
	m.mu.Unlock()
	for _, s := range scopes {
		if err := s.Close(ctx); err != nil {
			return err
		}
	}
	return nil
}

type scopeKey struct{}

func WithScope(ctx context.Context, s *Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}
func FromContext(ctx context.Context) *Scope { s, _ := ctx.Value(scopeKey{}).(*Scope); return s }

// Run uses only existing authority. It never creates a scope implicitly.
func Run(ctx context.Context, cmd Command) (Result, error) {
	s := FromContext(ctx)
	if s == nil {
		return Result{}, ErrNoScope
	}
	p, err := s.Start(ctx, cmd)
	if err != nil {
		return Result{}, err
	}
	result, err := p.Wait(ctx)
	if err != nil {
		_ = p.Stop(context.Background())
		return Result{}, err
	}
	return result, nil
}

// RunLocal is an explicit local entry point for standalone helpers. A bound
// scope is reused, never replaced, even when the requested root is different.
func RunLocal(ctx context.Context, root string, policy Policy, cmd Command) (Result, error) {
	if s := FromContext(ctx); s != nil {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return Result{}, err
		}
		absolute, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return Result{}, err
		}
		rel, err := filepath.Rel(s.workspace, absolute)
		if err != nil {
			return Result{}, err
		}
		if cmd.Dir != "" {
			rel = filepath.Join(rel, cmd.Dir)
		}
		cmd.Dir = rel
		return Run(ctx, cmd)
	}
	m := NewManager()
	defer m.Close(context.Background())
	s, err := m.Open(ctx, Options{Workspace: root, Policy: policy})
	if err != nil {
		return Result{}, err
	}
	return Run(WithScope(ctx, s), cmd)
}

// DeriveScratch is for controller-created scratch copies, not tool arguments.
// It preserves the bound policy, backend, environment, cancellation, trusted-user
// standing, and manager owner.
func DeriveScratch(ctx context.Context, root string) (*Scope, error) {
	parent := FromContext(ctx)
	if parent == nil {
		return nil, ErrNoScope
	}
	if err := parent.Check(); err != nil {
		return nil, err
	}
	opts := parent.options
	opts.Workspace = root
	return parent.manager.open(parent.ctx, opts, parent.user)
}
