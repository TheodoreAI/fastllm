package harness

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// SanitizedEnvironment returns an attenuated process environment for model-spawned commands.
// It preserves standard system variables (PATH, HOME, USER, TMPDIR, compiler paths, etc.)
// while scrubbing API keys, tokens, secrets, and private credentials.
func SanitizedEnvironment() []string {
	sensitiveKeywords := []string{
		"KEY", "SECRET", "TOKEN", "PASSWORD", "PASSWD", "AUTH", "CREDENTIAL",
		"PRIVATE", "CERT", "SIGNING",
	}

	var sanitized []string
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 0 {
			continue
		}
		key := strings.ToUpper(parts[0])

		if isEssentialEnv(key) {
			sanitized = append(sanitized, env)
			continue
		}

		sensitive := false
		for _, kw := range sensitiveKeywords {
			if strings.Contains(key, kw) {
				sensitive = true
				break
			}
		}
		if !sensitive {
			sanitized = append(sanitized, env)
		}
	}
	return sanitized
}

func isEssentialEnv(key string) bool {
	switch key {
	case "PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TEMP", "TMP",
		"LANG", "LC_ALL", "LC_CTYPE", "TERM", "COLORTERM",
		"GOROOT", "GOPATH", "GOBIN", "GOPROXY", "GONOSUMDB", "GONOPROXY", "GOPRIVATE",
		"CARGO_HOME", "RUSTUP_HOME", "JAVA_HOME", "NODE_PATH", "NVM_DIR",
		"SYSTEMROOT", "WINDIR", "PROGRAMFILES", "PROGRAMFILES(X86)", "APPDATA", "LOCALAPPDATA", "COMSPEC", "PATHEXT":
		return true
	default:
		return false
	}
}

// syncBuffer is a thread-safe ring-like buffer for capturing process output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (sb *syncBuffer) Write(p []byte) (n int, err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	// Prevent unbounded memory growth (cap at 1MB per process)
	if sb.buf.Len() > 1024*1024 {
		trimmed := sb.buf.Bytes()[512*1024:]
		sb.buf.Reset()
		sb.buf.Write(trimmed)
	}
	return sb.buf.Write(p)
}

func (sb *syncBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.String()
}

// BackgroundProcess tracks a single spawned background command.
type BackgroundProcess struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	Command   string    `json:"command"`
	StartTime time.Time `json:"start_time"`
	Exited    bool      `json:"exited"`
	ExitCode  int       `json:"exit_code"`
}

type managedProcess struct {
	BackgroundProcess
	cmd    *exec.Cmd
	output *syncBuffer
}

// ProcessManager manages life-cycle of background commands.
type ProcessManager struct {
	mu        sync.Mutex
	processes map[string]*managedProcess
	counter   int
}

// NewProcessManager creates a new ProcessManager.
func NewProcessManager() *ProcessManager {
	return &ProcessManager{
		processes: make(map[string]*managedProcess),
	}
}

// Start launches a shell command in the background and returns its process record.
func (pm *ProcessManager) Start(command string, dir string) (*BackgroundProcess, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.counter++
	id := fmt.Sprintf("proc-%d", pm.counter)

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/c", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("invalid working directory: %w", err)
	}
	cmd.Dir = absDir
	cmd.Env = SanitizedEnvironment()

	buf := &syncBuffer{}
	cmd.Stdout = buf
	cmd.Stderr = buf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start background command: %w", err)
	}

	proc := &managedProcess{
		BackgroundProcess: BackgroundProcess{
			ID:        id,
			PID:       cmd.Process.Pid,
			Command:   command,
			StartTime: time.Now(),
		},
		cmd:    cmd,
		output: buf,
	}
	pm.processes[id] = proc

	// Monitor completion in background goroutine
	go func() {
		err := cmd.Wait()
		pm.mu.Lock()
		defer pm.mu.Unlock()
		proc.Exited = true
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				proc.ExitCode = exitErr.ExitCode()
			} else {
				proc.ExitCode = 1
			}
		} else {
			proc.ExitCode = 0
		}
	}()

	snapshot := proc.BackgroundProcess
	return &snapshot, nil
}

// Status returns the current status and latest output for a process.
func (pm *ProcessManager) Status(id string) (*BackgroundProcess, string, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	proc, ok := pm.processes[id]
	if !ok {
		return nil, "", fmt.Errorf("process %q not found", id)
	}

	snapshot := proc.BackgroundProcess
	return &snapshot, proc.output.String(), nil
}

// Kill terminates a background process.
func (pm *ProcessManager) Kill(id string) error {
	pm.mu.Lock()
	proc, ok := pm.processes[id]
	if !ok {
		pm.mu.Unlock()
		return fmt.Errorf("process %q not found", id)
	}
	if proc.Exited || proc.cmd.Process == nil {
		pm.mu.Unlock()
		return nil
	}
	process := proc.cmd.Process
	pid := proc.PID
	pm.mu.Unlock()

	if runtime.GOOS == "windows" {
		// Taskkill /T /F to kill process tree on Windows
		_ = exec.Command("taskkill", "/PID", fmt.Sprintf("%d", pid), "/T", "/F").Run()
	} else {
		_ = process.Kill()
	}

	return nil
}

// List returns all registered background processes.
func (pm *ProcessManager) List() []*BackgroundProcess {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	result := make([]*BackgroundProcess, 0, len(pm.processes))
	for _, p := range pm.processes {
		snapshot := p.BackgroundProcess
		result = append(result, &snapshot)
	}
	return result
}

// KillAll kills all active background processes.
func (pm *ProcessManager) KillAll() {
	pm.mu.Lock()
	ids := make([]string, 0, len(pm.processes))
	for _, p := range pm.processes {
		if !p.Exited {
			ids = append(ids, p.ID)
		}
	}
	pm.mu.Unlock()

	for _, id := range ids {
		_ = pm.Kill(id)
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
