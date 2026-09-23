package harness

import (
	"context"
	"fastllm/internal/execution"
	"fmt"
	"strings"
	"sync"
	"time"
)

func SanitizedEnvironment() []string { return execution.SanitizedEnvironment() }

type BackgroundProcess struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	Command   string    `json:"command"`
	StartTime time.Time `json:"start_time"`
	Exited    bool      `json:"exited"`
	ExitCode  int       `json:"exit_code"`
}
type trackedProcess struct {
	process execution.Process
	command string
}
type ProcessManager struct {
	mu        sync.Mutex
	scope     *execution.Scope
	owner     *execution.Manager
	processes map[string]trackedProcess
}

func NewProcessManager(scopes ...*execution.Scope) *ProcessManager {
	pm := &ProcessManager{processes: make(map[string]trackedProcess)}
	if len(scopes) > 0 {
		pm.scope = scopes[0]
	}
	return pm
}
func (pm *ProcessManager) Start(command, dir string) (*BackgroundProcess, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.scope == nil {
		pm.owner = execution.NewManager()
		s, err := pm.owner.Open(context.Background(), execution.Options{Workspace: dir, Policy: execution.LocalPolicy()})
		if err != nil {
			return nil, err
		}
		pm.scope = s
	}
	p, err := pm.scope.Start(context.Background(), execution.Command{Shell: command})
	if err != nil {
		return nil, err
	}
	pm.processes[p.Snapshot().ID] = trackedProcess{p, command}
	return processSnapshot(p, command), nil
}
func processSnapshot(p execution.Process, command string) *BackgroundProcess {
	s := p.Snapshot()
	return &BackgroundProcess{ID: s.ID, PID: s.PID, Command: command, StartTime: s.StartedAt, Exited: s.Exited, ExitCode: s.ExitCode}
}
func (pm *ProcessManager) Status(id string) (*BackgroundProcess, string, error) {
	pm.mu.Lock()
	tracked, ok := pm.processes[id]
	pm.mu.Unlock()
	if !ok {
		return nil, "", fmt.Errorf("unknown process %q", id)
	}
	// Polling is non-blocking even before the first byte has arrived.
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	out, _ := tracked.process.ReadOutput(ctx, 0)
	var text strings.Builder
	if out.Truncated {
		text.WriteString("[earlier output truncated]\n")
	}
	for _, chunk := range out.Chunks {
		text.WriteString(chunk.Data)
	}
	return processSnapshot(tracked.process, tracked.command), text.String(), nil
}
func (pm *ProcessManager) Kill(id string) error {
	pm.mu.Lock()
	tracked, ok := pm.processes[id]
	pm.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown process %q", id)
	}
	return tracked.process.Stop(context.Background())
}
func (pm *ProcessManager) List() []*BackgroundProcess {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	result := make([]*BackgroundProcess, 0, len(pm.processes))
	for _, tracked := range pm.processes {
		result = append(result, processSnapshot(tracked.process, tracked.command))
	}
	return result
}
func (pm *ProcessManager) KillAll() {
	pm.mu.Lock()
	var processes []execution.Process
	for _, tracked := range pm.processes {
		processes = append(processes, tracked.process)
	}
	owner := pm.owner
	pm.mu.Unlock()
	for _, p := range processes {
		_ = p.Stop(context.Background())
	}
	if owner != nil {
		_ = owner.Close(context.Background())
	}
}

// FormatProcessTable returns a formatted text table of running/exited processes.
func (pm *ProcessManager) FormatProcessTable() string {
	procs := pm.List()
	if len(procs) == 0 {
		return "No background processes."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-10s %-8s %-12s %-10s %s\n", "ID", "PID", "STATUS", "DURATION", "COMMAND"))
	sb.WriteString(strings.Repeat("-", 60) + "\n")

	for _, p := range procs {
		status := "RUNNING"
		if p.Exited {
			status = fmt.Sprintf("EXIT %d", p.ExitCode)
		}
		duration := time.Since(p.StartTime).Round(time.Second)
		cmdSummary := p.Command
		if len(cmdSummary) > 30 {
			cmdSummary = cmdSummary[:27] + "..."
		}
		sb.WriteString(fmt.Sprintf("%-10s %-8d %-12s %-10s %s\n", p.ID, p.PID, status, duration, cmdSummary))
	}

	return sb.String()
}
