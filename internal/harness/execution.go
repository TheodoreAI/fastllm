package harness

import (
	"context"
	"fastllm/internal/execution"
	"fmt"
	"io"
	"os"
	"time"
)

func executionPolicy(req RunRequest, commands bool) execution.Policy {
	p := policyForRequest(req)
	return execution.Policy{Commands: commands && p.commands, Write: p.write, Network: p.network}
}

func (r *Runner) executeCommand(ctx context.Context, root, command string, timeout time.Duration, live bool) string {
	s := execution.FromContext(ctx)
	if s == nil {
		// Direct internal callers and tests explicitly use local model authority.
		m := execution.NewManager()
		defer m.Close(context.Background())
		var err error
		s, err = m.Open(ctx, execution.Options{Workspace: root, Policy: execution.LocalPolicy(), Timeout: timeout, MaxOutputBytes: 64 * 1024})
		if err != nil {
			return "Error: " + err.Error()
		}
	}
	p, err := s.Start(ctx, execution.Command{Shell: command, Timeout: timeout})
	if err != nil {
		return "Error: " + err.Error()
	}
	if live {
		var cursor uint64
		for {
			out, err := p.ReadOutput(ctx, cursor)
			if err != nil {
				break
			}
			for _, chunk := range out.Chunks {
				_, _ = io.WriteString(os.Stdout, chunk.Data)
			}
			cursor = out.Next
			if out.Done {
				break
			}
		}
	}
	result, err := p.Wait(ctx)
	if err != nil {
		_ = p.Stop(context.Background())
		return "Command canceled: " + err.Error()
	}
	output := result.Output
	if result.Truncated {
		output = "[earlier output truncated]\n" + output
	}
	if result.Reason == "timeout" {
		return fmt.Sprintf("Command timed out after %v.\nOutput so far:\n%s", timeout, output)
	}
	if err := result.Err(); err != nil {
		return fmt.Sprintf("Exit code: %d (Error: %v)\nOutput:\n%s", result.ExitCode, err, output)
	}
	return "Exit code: 0\nOutput:\n" + output
}

func runUserCommand(ctx context.Context, root, command string) (execution.Result, error) {
	m := execution.NewManager()
	defer m.Close(context.Background())
	s, err := m.OpenUser(ctx, root)
	if err != nil {
		return execution.Result{}, err
	}
	result, err := execution.Run(execution.WithScope(ctx, s), execution.Command{Shell: command})
	if err == nil {
		err = result.Err()
	}
	return result, err
}
