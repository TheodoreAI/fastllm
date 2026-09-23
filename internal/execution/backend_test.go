package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackend records what the scope hands it, so tests can assert that
// admission happens before a backend is ever consulted.
type fakeBackend struct {
	name      string
	isolated  bool
	available error

	mu       sync.Mutex
	opens    []ScopeSpec
	launches []Launch
	closes   int
}

func (f *fakeBackend) Name() string   { return f.name }
func (f *fakeBackend) Isolated() bool { return f.isolated }
func (f *fakeBackend) Available(context.Context) error {
	return f.available
}

func (f *fakeBackend) Open(ctx context.Context, spec ScopeSpec) (BackendScope, error) {
	f.mu.Lock()
	f.opens = append(f.opens, spec)
	f.mu.Unlock()
	return &fakeScope{backend: f}, nil
}

func (f *fakeBackend) snapshot() ([]ScopeSpec, []Launch, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ScopeSpec(nil), f.opens...), append([]Launch(nil), f.launches...), f.closes
}

type fakeScope struct {
	backend *fakeBackend
}

func (s *fakeScope) Start(_ context.Context, launch Launch) (Process, error) {
	s.backend.mu.Lock()
	s.backend.launches = append(s.backend.launches, launch)
	s.backend.mu.Unlock()
	p := &fakeProcess{done: make(chan struct{})}
	p.state = ProcessState{ID: launch.ID, StartedAt: time.Now(), Exited: true, Reason: "exit"}
	close(p.done)
	return p, nil
}

func (s *fakeScope) Close(context.Context) error {
	s.backend.mu.Lock()
	s.backend.closes++
	s.backend.mu.Unlock()
	return nil
}

type fakeProcess struct {
	state ProcessState
	done  chan struct{}
}

func (p *fakeProcess) Snapshot() ProcessState { return p.state }
func (p *fakeProcess) ReadOutput(context.Context, uint64) (Output, error) {
	return Output{Done: true}, nil
}
func (p *fakeProcess) Wait(context.Context) (Result, error) {
	return Result{ProcessState: p.state}, nil
}
func (p *fakeProcess) Stop(context.Context) error { return nil }

func registerFake(t *testing.T, m *Manager, f *fakeBackend) {
	t.Helper()
	if err := m.Register(f); err != nil {
		t.Fatalf("Register: %v", err)
	}
}

func TestLocalBackendIsTheDefaultAndReportsNoIsolation(t *testing.T) {
	_, s := modelScope(t, Options{})
	if s.Backend() != LocalBackendName {
		t.Errorf("backend = %q, want %q", s.Backend(), LocalBackendName)
	}
	if s.Isolated() {
		t.Error("the local backend must never report isolation")
	}
}

func TestRequireIsolationRejectsTheLocalBackendByName(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	_, err := m.Open(context.Background(), Options{
		Workspace: t.TempDir(), Policy: LocalPolicy(),
		Backend: LocalBackendName, RequireIsolation: true,
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Open error = %v, want ErrUnsupported", err)
	}
}

func TestUnknownBackendIsRefused(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	if _, err := m.Open(context.Background(), Options{Workspace: t.TempDir(), Policy: LocalPolicy(), Backend: "nope"}); err == nil {
		t.Fatal("Open accepted an unregistered backend name")
	}
	_, err := m.Open(context.Background(), Options{
		Workspace: t.TempDir(), Policy: LocalPolicy(),
		Backend: "nope", RequireIsolation: true,
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Open error = %v, want ErrUnsupported", err)
	}
}

func TestIsolatedBackendSatisfiesTheRequirement(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	fake := &fakeBackend{name: "fake", isolated: true}
	registerFake(t, m, fake)

	s, err := m.Open(context.Background(), Options{
		Workspace: t.TempDir(), Policy: LocalPolicy(),
		Backend: "fake", RequireIsolation: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Backend() != "fake" || !s.Isolated() {
		t.Fatalf("scope backend = %q isolated = %v, want fake/true", s.Backend(), s.Isolated())
	}
	opens, _, _ := fake.snapshot()
	if len(opens) != 1 {
		t.Fatalf("backend opened %d times, want 1", len(opens))
	}
	if opens[0].Workspace != s.Workspace() {
		t.Errorf("spec workspace = %q, want the scope's canonical %q", opens[0].Workspace, s.Workspace())
	}
}

func TestUnavailableBackendNeverFallsBackToLocal(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	fake := &fakeBackend{name: "fake", isolated: true, available: errors.New("daemon not running")}
	registerFake(t, m, fake)

	s, err := m.Open(context.Background(), Options{
		Workspace: t.TempDir(), Policy: LocalPolicy(),
		Backend: "fake", RequireIsolation: true,
	})
	if err == nil {
		t.Fatalf("Open succeeded on backend %q with scope backend %q; an unavailable backend must fail", "fake", s.Backend())
	}
	if !strings.Contains(err.Error(), "daemon not running") {
		t.Errorf("error = %v, want it to report the backend's reason", err)
	}
	if opens, _, _ := fake.snapshot(); len(opens) != 0 {
		t.Errorf("backend was opened %d times despite being unavailable", len(opens))
	}
}

func TestAdmissionHappensBeforeTheBackendSeesALaunch(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	fake := &fakeBackend{name: "fake", isolated: true}
	registerFake(t, m, fake)

	denied, err := m.Open(context.Background(), Options{
		Workspace: t.TempDir(), Policy: Policy{Commands: false, Write: true, Network: true},
		Backend: "fake",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := denied.Start(context.Background(), Command{Shell: "echo denied"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("Start error = %v, want ErrDenied", err)
	}

	allowed, err := m.Open(context.Background(), Options{Workspace: t.TempDir(), Policy: LocalPolicy(), Backend: "fake"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for name, cmd := range map[string]Command{
		"workspace escape":       {Shell: "echo escape", Dir: ".."},
		"absolute dir":           {Shell: "echo absolute", Dir: t.TempDir()},
		"ambiguous shape":        {Executable: "go", Shell: "echo both"},
		"stdin in a model scope": {Shell: "echo stdin", Stdin: strings.NewReader("hi")},
	} {
		if _, err := allowed.Start(context.Background(), cmd); err == nil {
			t.Errorf("Start accepted %s", name)
		}
	}

	if _, launches, _ := fake.snapshot(); len(launches) != 0 {
		t.Fatalf("backend received %d launches that admission should have stopped", len(launches))
	}
}

func TestLaunchCarriesResolvedAuthority(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "nested")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	m := NewManager()
	defer m.Close(context.Background())
	fake := &fakeBackend{name: "fake", isolated: true}
	registerFake(t, m, fake)

	s, err := m.Open(context.Background(), Options{Workspace: root, Policy: LocalPolicy(), Backend: "fake", Timeout: time.Minute})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p, err := s.Start(context.Background(), Command{Shell: "echo nested", Dir: "nested"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, launches, _ := fake.snapshot()
	if len(launches) != 1 {
		t.Fatalf("backend received %d launches, want 1", len(launches))
	}
	launch := launches[0]
	if !filepath.IsAbs(launch.Dir) {
		t.Errorf("launch dir = %q, want an absolute host path", launch.Dir)
	}
	resolved, err := filepath.EvalSymlinks(sub)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if launch.Dir != resolved {
		t.Errorf("launch dir = %q, want %q", launch.Dir, resolved)
	}
	if launch.Timeout != time.Minute {
		t.Errorf("launch timeout = %v, want the scope ceiling %v", launch.Timeout, time.Minute)
	}
	if launch.ID == "" || launch.ID != p.Snapshot().ID {
		t.Errorf("launch ID = %q, want the scope-assigned ID %q", launch.ID, p.Snapshot().ID)
	}
	if _, err := s.Process(launch.ID); err != nil {
		t.Errorf("scope did not register the backend process: %v", err)
	}
}

func TestScopeCloseReleasesTheBackendExactlyOnce(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	fake := &fakeBackend{name: "fake", isolated: true}
	registerFake(t, m, fake)

	s, err := m.Open(context.Background(), Options{Workspace: t.TempDir(), Policy: LocalPolicy(), Backend: "fake"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Cancellation also drives a close; give that path a chance to race.
	time.Sleep(50 * time.Millisecond)
	if _, _, closes := fake.snapshot(); closes != 1 {
		t.Fatalf("backend scope closed %d times, want exactly 1", closes)
	}
}

func TestRegisterRejectsDuplicatesAndNamelessBackends(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	if err := m.Register(&fakeBackend{name: ""}); err == nil {
		t.Error("Register accepted a nameless backend")
	}
	if err := m.Register(&fakeBackend{name: LocalBackendName}); err == nil {
		t.Error("Register let a backend shadow the local backend")
	}
	registerFake(t, m, &fakeBackend{name: "fake"})
	if err := m.Register(&fakeBackend{name: "fake"}); err == nil {
		t.Error("Register accepted a duplicate name")
	}
}

func TestDerivedScratchInheritsBackendAndUserStanding(t *testing.T) {
	m := NewManager()
	defer m.Close(context.Background())
	user, err := m.OpenUser(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("OpenUser: %v", err)
	}
	child, err := DeriveScratch(WithScope(context.Background(), user), t.TempDir())
	if err != nil {
		t.Fatalf("DeriveScratch: %v", err)
	}
	if child.Backend() != user.Backend() {
		t.Errorf("scratch backend = %q, want the parent's %q", child.Backend(), user.Backend())
	}
	// Trusted-user standing must survive, or stdin would be denied in a
	// scratch copy of a scope that allows it.
	p, err := child.Start(context.Background(), Command{Shell: "echo scratch", Stdin: strings.NewReader("hi")})
	if err != nil {
		t.Fatalf("derived scratch lost trusted-user standing: %v", err)
	}
	if _, err := p.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}
