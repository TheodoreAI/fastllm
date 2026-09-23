package execution

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"sync"
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

type localProcess struct {
	mu     sync.Mutex
	state  ProcessState
	output *outputBuffer
	done   chan struct{}
	cancel context.CancelFunc
}

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
	p := &localProcess{
		state:  ProcessState{ID: launch.ID, PID: cmd.Process.Pid, StartedAt: time.Now()},
		output: buffer,
		done:   make(chan struct{}),
		cancel: cancel,
	}
	go func() {
		err := cmd.Wait()
		cleanup()
		state := p.state
		state.Exited = true
		state.ExitCode = cmd.ProcessState.ExitCode()
		state.Reason = "exit"
		if processCtx.Err() == context.DeadlineExceeded {
			state.Reason = "timeout"
		} else if processCtx.Err() != nil {
			state.Reason = "canceled"
		} else if err != nil && state.ExitCode == 0 {
			state.ExitCode = 1
		}
		p.mu.Lock()
		p.state = state
		p.mu.Unlock()
		cancel()
		buffer.finish()
		close(p.done)
	}()
	return p, nil
}

func (p *localProcess) Snapshot() ProcessState { p.mu.Lock(); defer p.mu.Unlock(); return p.state }
func (p *localProcess) ReadOutput(ctx context.Context, cursor uint64) (Output, error) {
	return p.output.read(ctx, cursor)
}
func (p *localProcess) Wait(ctx context.Context) (Result, error) {
	select {
	case <-p.done:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	out, _ := p.output.read(context.Background(), 0)
	r := Result{ProcessState: p.Snapshot(), Truncated: out.Truncated}
	var stdout, stderr, combined strings.Builder
	for _, chunk := range out.Chunks {
		combined.WriteString(chunk.Data)
		if chunk.Stream == "stdout" {
			stdout.WriteString(chunk.Data)
		} else {
			stderr.WriteString(chunk.Data)
		}
	}
	r.Stdout = stdout.String()
	r.Stderr = stderr.String()
	r.Output = combined.String()
	return r, nil
}
func (p *localProcess) Stop(ctx context.Context) error { p.cancel(); _, err := p.Wait(ctx); return err }
