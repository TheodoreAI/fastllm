package execution

import (
	"context"
	"time"
)

// Backend runs a scope's commands. Registration grants no authority: the Scope
// performs policy admission, command validation, working-directory resolution,
// and process-ID assignment before a backend ever sees a launch.
type Backend interface {
	Name() string
	// Isolated reports whether the backend establishes OS-level filesystem,
	// network, process, and resource restrictions around a command. A backend
	// must never claim isolation it does not enforce.
	Isolated() bool
	// Available reports whether this host can run the backend right now. A
	// backend that cannot run must return an error here rather than degrade to
	// a weaker mode, so that requesting isolation can never yield local
	// execution.
	Available(ctx context.Context) error
	// Open binds backend resources to one scope. The supplied context is the
	// scope's lifetime and is canceled when the scope closes.
	Open(ctx context.Context, spec ScopeSpec) (BackendScope, error)
}

// BackendScope owns whatever the backend needs for a scope's lifetime. The
// local backend needs nothing; an isolated backend owns its worker.
type BackendScope interface {
	Start(ctx context.Context, launch Launch) (Process, error)
	Close(ctx context.Context) error
}

// ScopeSpec is the immutable description a scope hands to its backend.
type ScopeSpec struct {
	// Workspace is the canonical host path. A backend that presents a
	// different path to the process must map this exact tree, so file tools
	// and commands observe the same workspace.
	Workspace      string
	Policy         Policy
	Environment    []string
	Timeout        time.Duration
	MaxOutputBytes int
	MaxProcesses   int
	// User marks a directly constructed trusted-user scope. Model arguments
	// can never reach one.
	User bool
}

// Launch is one admitted command. The scope has already checked policy,
// validated the command shape, admitted stdin, resolved Dir inside the
// workspace, clamped the timeout, and assigned ID.
type Launch struct {
	ID      string
	Command Command
	// Dir is an absolute host path inside ScopeSpec.Workspace.
	Dir     string
	Timeout time.Duration
}
