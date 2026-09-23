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
	Workspace        string
	Policy           Policy
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
	mu     sync.Mutex
	closed bool
	next   uint64
	scopes map[*Scope]struct{}
}

func NewManager() *Manager { return &Manager{scopes: make(map[*Scope]struct{})} }

type Scope struct {
	mu          sync.Mutex
	manager     *Manager
	options     Options
	workspace   string
	user        bool
	environment []string
	ctx         context.Context
	cancel      context.CancelFunc
	closed      bool
	next        uint64
	id          uint64
	processes   map[string]*localProcess
}

func (m *Manager) Open(ctx context.Context, opts Options) (*Scope, error) {
	return m.open(ctx, opts, false)
}

// OpenUser is reserved for direct human shell actions, never model arguments.
func (m *Manager) OpenUser(ctx context.Context, workspace string) (*Scope, error) {
	return m.open(ctx, Options{Workspace: workspace, Policy: LocalPolicy()}, true)
}

func (m *Manager) open(ctx context.Context, opts Options, user bool) (*Scope, error) {
	if opts.RequireIsolation {
		return nil, ErrUnsupported
	}
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
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	m.next++
	lifetime, cancel := context.WithCancel(ctx)
	s := &Scope{user: user, manager: m, options: opts, workspace: root, environment: processEnvironment(user), ctx: lifetime, cancel: cancel, id: m.next, processes: make(map[string]*localProcess)}
	m.scopes[s] = struct{}{}
	go func() { <-lifetime.Done(); _ = s.Close(context.Background()) }()
	return s, nil
}

func (s *Scope) Workspace() string { return s.workspace }

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

func (s *Scope) Process(id string) (Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.processes[id]
	if !ok {
		return nil, fmt.Errorf("unknown process %q in this scope", id)
	}
	return p, nil
}

func (s *Scope) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	processes := make([]*localProcess, 0, len(s.processes))
	for _, p := range s.processes {
		processes = append(processes, p)
	}
	s.mu.Unlock()
	for _, p := range processes {
		p.cancel()
	}
	for _, p := range processes {
		if _, err := p.Wait(ctx); err != nil {
			return err
		}
	}
	s.manager.mu.Lock()
	delete(s.manager.scopes, s)
	s.manager.mu.Unlock()
	return nil
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
// It preserves the bound policy, environment, cancellation and manager owner.
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
	child, err := parent.manager.Open(parent.ctx, opts)
	if err != nil {
		return nil, err
	}
	child.environment = append([]string(nil), parent.environment...)
	return child, nil
}
