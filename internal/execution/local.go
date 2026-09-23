package execution

import (
	"context"
	"os/exec"
	"runtime"
	"time"
)

// LocalBackendName identifies the default backend. It admits commands by
// policy and bounds their output and lifetime, but it is not an OS sandbox:
// an admitted command can still read outside the workspace and use the network.
const LocalBackendName = "local"

type localBackend struct{}

func (localBackend) Name() string                    { return LocalBackendName }
func (localBackend) Isolated() bool                  { return false }
func (localBackend) Available(context.Context) error { return nil }

func (localBackend) Open(ctx context.Context, spec ScopeSpec) (BackendScope, error) {
	return &localScope{spec: spec, lifetime: ctx}, nil
}

type localScope struct {
	spec     ScopeSpec
	lifetime context.Context
}

// Close releases nothing: local processes are owned by the scope lifetime.
func (*localScope) Close(context.Context) error { return nil }

func (l *localScope) Start(ctx context.Context, launch Launch) (Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := launch.Command
	processCtx, cancel := context.WithTimeout(l.lifetime, launch.Timeout)
	var cmd *exec.Cmd
	if command.Executable != "" {
		cmd = exec.CommandContext(processCtx, command.Executable, command.Args...)
	} else if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(processCtx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command.Shell)
	} else {
		cmd = exec.CommandContext(processCtx, "sh", "-c", command.Shell)
	}
	cmd.Stdin = command.Stdin
	cmd.Dir = launch.Dir
	cmd.Env = append([]string(nil), l.spec.Environment...)
	cmd.WaitDelay = 250 * time.Millisecond
	buffer := newOutput(l.spec.MaxOutputBytes)
	cmd.Stdout = streamWriter{buffer, "stdout"}
	cmd.Stderr = streamWriter{buffer, "stderr"}
	cleanup, afterStart, err := configureProcess(cmd)
	if err != nil {
		cancel()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		cancel()
		cleanup()
		return nil, err
	}
	if err = afterStart(); err != nil {
		cancel()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		cleanup()
		return nil, err
	}
	p := &managedProcess{
		state:  ProcessState{ID: launch.ID, PID: cmd.Process.Pid, StartedAt: time.Now()},
		output: buffer,
		done:   make(chan struct{}),
		cancel: cancel,
	}
	go func() {
		err := cmd.Wait()
		cleanup()
		state := p.Snapshot()
		state.Exited = true
		state.ExitCode = cmd.ProcessState.ExitCode()
		state.Reason = exitReason(processCtx)
		if state.Reason == "exit" && err != nil && state.ExitCode == 0 {
			state.ExitCode = 1
		}
		p.exited(state)
	}()
	return p, nil
}
