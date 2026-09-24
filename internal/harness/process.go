package harness

import (
	"context"
	"fastllm/internal/execution"
	"fmt"
	"sort"
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
	id      string
	process execution.Process
	command string
}

type ProcessManager struct {
	mu        sync.Mutex
	scope     *execution.Scope
	owner     *execution.Manager
	scopes    map[string]*execution.Scope
	processes map[string]trackedProcess
	nextID    int
}

func NewProcessManager(scopes ...*execution.Scope) *ProcessManager {
	pm := &ProcessManager{
		processes: make(map[string]trackedProcess),
		scopes:    make(map[string]*execution.Scope),
	}
	if len(scopes) > 0 {
		pm.scope = scopes[0]
	}
	return pm
}

func (pm *ProcessManager) getScopeLocked(dir string) (*execution.Scope, error) {
	if pm.scope != nil {
		return pm.scope, nil
	}
	if pm.owner == nil {
		pm.owner = execution.NewManager()
	}
	if s, ok := pm.scopes[dir]; ok {
		return s, nil
	}
	s, err := pm.owner.OpenUserWithOptions(context.Background(), execution.Options{
		Workspace:      dir,
		Policy:         execution.LocalPolicy(),
		Timeout:        24 * time.Hour,
		MaxOutputBytes: 10 * 1024 * 1024,
	})
	if err != nil {
		return nil, err
	}
	pm.scopes[dir] = s
	return s, nil
}

func (pm *ProcessManager) Start(command, dir string) (*BackgroundProcess, error) {
	_, bp, err := pm.StartTracked(command, dir)
	return bp, err
}

func (pm *ProcessManager) StartTracked(command, dir string) (execution.Process, *BackgroundProcess, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	s, err := pm.getScopeLocked(dir)
	if err != nil {
		return nil, nil, err
	}
	// A loop that keeps starting servers would otherwise exhaust the machine.
	running := 0
	for _, tracked := range pm.processes {
		if !tracked.process.Snapshot().Exited {
			running++
		}
	}
	if running >= maxBackgroundProcesses {
		return nil, nil, fmt.Errorf("%d background processes are already running, the limit; stop one first (kill_process, or /kill <id>)", running)
	}

	p, err := s.Start(context.Background(), execution.Command{
		Shell:   command,
		Timeout: 24 * time.Hour,
	})
	if err != nil {
		return nil, nil, err
	}

	pm.nextID++
	id := fmt.Sprintf("proc-%d", pm.nextID)
	tracked := trackedProcess{id: id, process: p, command: command}
	pm.processes[id] = tracked

	return p, processSnapshot(id, p, command), nil
}

func processSnapshot(id string, p execution.Process, command string) *BackgroundProcess {
	s := p.Snapshot()
	return &BackgroundProcess{
		ID:        id,
		PID:       s.PID,
		Command:   command,
		StartTime: s.StartedAt,
		Exited:    s.Exited,
		ExitCode:  s.ExitCode,
	}
}

func (pm *ProcessManager) findLocked(id string) (trackedProcess, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return trackedProcess{}, false
	}
	if tp, ok := pm.processes[id]; ok {
		return tp, true
	}
	if tp, ok := pm.processes["proc-"+id]; ok {
		return tp, true
	}
	for _, tp := range pm.processes {
		s := tp.process.Snapshot()
		if s.ID == id || fmt.Sprintf("%d", s.PID) == id {
			return tp, true
		}
	}
	return trackedProcess{}, false
}

func (pm *ProcessManager) Remove(id string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	tracked, ok := pm.findLocked(id)
	if ok {
		delete(pm.processes, tracked.id)
	}
}

func (pm *ProcessManager) Status(id string) (*BackgroundProcess, string, error) {
	pm.mu.Lock()
	tracked, ok := pm.findLocked(id)
	pm.mu.Unlock()
	if !ok {
		return nil, "", fmt.Errorf("unknown process %q", id)
	}
	// Polling is non-blocking even before the first byte has arrived.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	out, _ := tracked.process.ReadOutput(ctx, 0)
	var text strings.Builder
	if out.Truncated {
		text.WriteString("[earlier output truncated]\n")
	}
	for _, chunk := range out.Chunks {
		text.WriteString(chunk.Data)
	}
	return processSnapshot(tracked.id, tracked.process, tracked.command), text.String(), nil
}

func (pm *ProcessManager) Logs(id string, maxLines int) (*BackgroundProcess, string, error) {
	pm.mu.Lock()
	tracked, ok := pm.findLocked(id)
	pm.mu.Unlock()
	if !ok {
		return nil, "", fmt.Errorf("unknown process %q", id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	out, _ := tracked.process.ReadOutput(ctx, 0)
	var text strings.Builder
	if out.Truncated {
		text.WriteString("[earlier output truncated]\n")
	}
	for _, chunk := range out.Chunks {
		text.WriteString(chunk.Data)
	}
	raw := text.String()
	if maxLines > 0 {
		lines := strings.Split(strings.TrimRight(raw, "\r\n"), "\n")
		if len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
			raw = strings.Join(lines, "\n")
		}
	}
	return processSnapshot(tracked.id, tracked.process, tracked.command), raw, nil
}

func (pm *ProcessManager) Kill(id string) error {
	id = strings.TrimSpace(id)
	if id == "all" || id == "--all" || id == "-a" {
		pm.KillAll()
		return nil
	}
	pm.mu.Lock()
	tracked, ok := pm.findLocked(id)
	pm.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown process %q", id)
	}
	return tracked.process.Stop(context.Background())
}

func (pm *ProcessManager) List() []*BackgroundProcess {
	if pm == nil {
		return nil
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	result := make([]*BackgroundProcess, 0, len(pm.processes))
	for _, tracked := range pm.processes {
		result = append(result, processSnapshot(tracked.id, tracked.process, tracked.command))
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].StartTime.Before(result[j].StartTime)
	})
	return result
}

func (pm *ProcessManager) ActiveCount() int {
	if pm == nil {
		return 0
	}
	procs := pm.List()
	active := 0
	for _, p := range procs {
		if !p.Exited {
			active++
		}
	}
	return active
}

func (pm *ProcessManager) KillAll() {
	if pm == nil {
		return
	}
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
	if pm == nil {
		return "No background processes."
	}
	procs := pm.List()
	if len(procs) == 0 {
		return "No background processes."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-10s %-8s %-12s %-10s %s\n", "ID", "PID", "STATUS", "DURATION", "COMMAND"))
	sb.WriteString(strings.Repeat("-", 65) + "\n")

	for _, p := range procs {
		status := "RUNNING"
		if p.Exited {
			status = fmt.Sprintf("EXIT %d", p.ExitCode)
		}
		duration := time.Since(p.StartTime).Round(time.Second)
		cmdSummary := sanitizeUntrusted(p.Command)
		if len(cmdSummary) > 35 {
			cmdSummary = cmdSummary[:32] + "..."
		}
		sb.WriteString(fmt.Sprintf("%-10s %-8d %-12s %-10s %s\n", p.ID, p.PID, status, duration, cmdSummary))
	}

	return sb.String()
}
