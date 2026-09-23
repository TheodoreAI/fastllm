package execution

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

type localProcess struct {
	mu     sync.Mutex
	state  ProcessState
	output *outputBuffer
	done   chan struct{}
	cancel context.CancelFunc
}

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
	dir, err := s.resolveDir(command.Dir)
	if err != nil {
		return nil, err
	}
	timeout := command.Timeout
	if timeout <= 0 || timeout > s.options.Timeout {
		timeout = s.options.Timeout
	}
	processCtx, cancel := context.WithTimeout(s.ctx, timeout)
	var cmd *exec.Cmd
	if command.Executable != "" {
		cmd = exec.CommandContext(processCtx, command.Executable, command.Args...)
	} else if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(processCtx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command.Shell)
	} else {
		cmd = exec.CommandContext(processCtx, "sh", "-c", command.Shell)
	}
	if command.Stdin != nil && !s.user {
		cancel()
		return nil, ErrDenied
	}
	cmd.Stdin = command.Stdin
	cmd.Dir = dir
	cmd.Env = append([]string(nil), s.environment...)
	cmd.WaitDelay = 250 * time.Millisecond
	buffer := newOutput(s.options.MaxOutputBytes)
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
	s.next++
	p := &localProcess{state: ProcessState{ID: fmt.Sprintf("scope-%d-proc-%d", s.id, s.next), PID: cmd.Process.Pid, StartedAt: time.Now()}, output: buffer, done: make(chan struct{}), cancel: cancel}
	s.processes[p.state.ID] = p
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
