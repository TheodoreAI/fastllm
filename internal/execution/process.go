package execution

import (
	"context"
	"strings"
	"sync"
)

// managedProcess is the backend-neutral process record: state, bounded output,
// completion, and cancellation. Backends differ only in how they start a
// process and learn that it exited; every observable behavior lives here.
type managedProcess struct {
	mu     sync.Mutex
	state  ProcessState
	output *outputBuffer
	done   chan struct{}
	cancel context.CancelFunc
}

// exited records the final state once the process and its output are done.
func (p *managedProcess) exited(state ProcessState) {
	p.mu.Lock()
	p.state = state
	p.mu.Unlock()
	p.cancel()
	p.output.finish()
	close(p.done)
}

// exitReason classifies an exit by what ended the process's context.
func exitReason(processCtx context.Context) string {
	switch processCtx.Err() {
	case nil:
		return "exit"
	case context.DeadlineExceeded:
		return "timeout"
	default:
		return "canceled"
	}
}

func (p *managedProcess) Snapshot() ProcessState { p.mu.Lock(); defer p.mu.Unlock(); return p.state }

func (p *managedProcess) ReadOutput(ctx context.Context, cursor uint64) (Output, error) {
	return p.output.read(ctx, cursor)
}

func (p *managedProcess) Wait(ctx context.Context) (Result, error) {
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

func (p *managedProcess) Stop(ctx context.Context) error {
	p.cancel()
	_, err := p.Wait(ctx)
	return err
}
