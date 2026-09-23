//go:build windows

package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// AppContainerBackendName identifies the Windows isolated backend.
const AppContainerBackendName = "appcontainer"

// appContainerName is one fixed identity, so grants are made once and reused
// rather than accumulating per run.
const appContainerName = "fastllm.sandbox"

const (
	procThreadAttributeSecurityCapabilities = 0x00020009
	hresultAlreadyExists                    = 0x800700B7

	fileAllAccess   windows.ACCESS_MASK = 0x001F01FF
	fileReadExecute windows.ACCESS_MASK = 0x001200A9

	// A ceiling on descendants, so a fork bomb exhausts its job, not the host.
	maxContainerProcesses = 512

	// workspaceDrive is the PowerShell drive a sandboxed shell runs from.
	// PowerShell rebuilds a location from the volume root and checks every
	// directory on the way; the container cannot read C:\Users, so a location
	// beneath it fails and PowerShell falls back to C:\. A drive rooted at the
	// workspace has nothing above it to check. Native programs still receive
	// the real host directory.
	workspaceDrive = "Workspace"
)

var (
	userenv                        = windows.NewLazySystemDLL("userenv.dll")
	procCreateAppContainerProfile  = userenv.NewProc("CreateAppContainerProfile")
	procDeleteAppContainerProfile  = userenv.NewProc("DeleteAppContainerProfile")
	procDeriveAppContainerSid      = userenv.NewProc("DeriveAppContainerSidFromAppContainerName")
	procGetAppContainerFolderPath  = userenv.NewProc("GetAppContainerFolderPath")
	errAppContainerStdin           = errors.New("appcontainer backend does not forward stdin")
	errAppContainerUnsupportedPath = errors.New("appcontainer backend requires an absolute executable path")
)

// securityCapabilities mirrors SECURITY_CAPABILITIES. The container holds no
// capabilities, so the kernel grants it no network access and no access to
// any object that does not name it explicitly.
type securityCapabilities struct {
	AppContainerSid *windows.SID
	Capabilities    *windows.SIDAndAttributes
	CapabilityCount uint32
	Reserved        uint32
}

// IsolatedBackend returns this platform's isolated backend.
func IsolatedBackend() (Backend, bool) { return NewAppContainerBackend(), true }

// NewAppContainerBackend runs commands inside one fixed AppContainer holding no
// capabilities, so the kernel denies it the network and every object not
// granted to it. The token is inherited by every descendant, and each command
// runs in a kill-on-close Job Object, so confinement needs no mediation by
// fastllm after launch.
//
// Opening a scope grants the container full access to the workspace and read
// access to the Go module cache the workspace resolves, which holds any
// auto-selected toolchain. Grants persist, are detected so each tree is
// granted once, and are recorded so RevokeIsolatedBackend can remove them.
func NewAppContainerBackend() Backend { return &appContainerBackend{} }

type appContainerBackend struct {
	once     sync.Once
	identity *containerIdentity
	err      error
	// grants serializes permission changes and the grant record in-process.
	grants sync.Mutex
}

type containerIdentity struct {
	sid    *windows.SID
	folder string
}

func (*appContainerBackend) Name() string   { return AppContainerBackendName }
func (*appContainerBackend) Isolated() bool { return true }

func (b *appContainerBackend) Available(context.Context) error {
	_, err := b.profile()
	return err
}

// profile creates or reopens the container identity and its private folder.
func (b *appContainerBackend) profile() (*containerIdentity, error) {
	b.once.Do(func() { b.identity, b.err = openIdentity() })
	return b.identity, b.err
}

func openIdentity() (*containerIdentity, error) {
	for _, proc := range []*windows.LazyProc{procCreateAppContainerProfile, procDeleteAppContainerProfile, procDeriveAppContainerSid, procGetAppContainerFolderPath} {
		if err := proc.Find(); err != nil {
			return nil, fmt.Errorf("AppContainer API unavailable: %w", err)
		}
	}
	name, err := windows.UTF16PtrFromString(appContainerName)
	if err != nil {
		return nil, err
	}
	var sid *windows.SID
	hr, _, _ := procCreateAppContainerProfile.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0, 0, uintptr(unsafe.Pointer(&sid)))
	if uint32(hr) == hresultAlreadyExists {
		hr, _, _ = procDeriveAppContainerSid.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&sid)))
	}
	if hr != 0 {
		return nil, fmt.Errorf("AppContainer profile %q: HRESULT 0x%08x", appContainerName, uint32(hr))
	}
	sidString, err := windows.UTF16PtrFromString(sid.String())
	if err != nil {
		return nil, err
	}
	var folder *uint16
	hr, _, _ = procGetAppContainerFolderPath.Call(uintptr(unsafe.Pointer(sidString)), uintptr(unsafe.Pointer(&folder)))
	if hr != 0 {
		return nil, fmt.Errorf("AppContainer folder: HRESULT 0x%08x", uint32(hr))
	}
	identity := &containerIdentity{sid: sid, folder: windows.UTF16PtrToString(folder)}
	windows.CoTaskMemFree(unsafe.Pointer(folder))
	return identity, nil
}

func (b *appContainerBackend) Open(ctx context.Context, spec ScopeSpec) (BackendScope, error) {
	identity, err := b.profile()
	if err != nil {
		return nil, err
	}
	if err := b.grant(identity, spec.Workspace); err != nil {
		return nil, err
	}

	// The container's own folder is writable to it by default; scratch state
	// lives there instead of the user's profile or the workspace.
	temp := filepath.Join(identity.folder, "Temp")
	cache := filepath.Join(identity.folder, "go-build")
	for _, dir := range []string{temp, cache} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	block, err := environmentBlock(spec.Environment, map[string]string{
		"TEMP":    temp,
		"TMP":     temp,
		"GOCACHE": cache,
		// Git reads its global config from HOME, which the container cannot
		// read under the user's profile.
		"HOME": identity.folder,
		// Process creation for an AppContainer fails with an unrelated
		// environment error when LOCALAPPDATA is missing from the block.
		"LOCALAPPDATA": os.Getenv("LOCALAPPDATA"),
	})
	if err != nil {
		return nil, err
	}
	return &appContainerScope{spec: spec, identity: identity, lifetime: ctx, environment: block}, nil
}

// grant makes the workspace and its toolchain reachable to the container, and
// records each tree so a revoke can find it later.
func (b *appContainerBackend) grant(identity *containerIdentity, workspace string) error {
	b.grants.Lock()
	defer b.grants.Unlock()
	record := grantRecord{path: filepath.Join(identity.folder, "grants.txt")}

	// Recorded before granting, so an interrupted grant is still revoked.
	if err := record.add(workspace); err != nil {
		return err
	}
	if _, err := grantTree(workspace, identity.sid, fileAllAccess); err != nil {
		return fmt.Errorf("grant workspace to sandbox: %w", err)
	}
	for _, dir := range goReadPaths(workspace) {
		_, err := grantTree(dir, identity.sid, fileReadExecute)
		// A toolchain the user cannot re-permission, such as one under
		// Program Files, is normally readable to containers already.
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			continue
		}
		if err != nil {
			return fmt.Errorf("grant %s to sandbox: %w", dir, err)
		}
		// Recorded even when the grant already existed, so one made before
		// the record did is still revoked.
		if err := record.add(dir); err != nil {
			return err
		}
	}
	return nil
}

// RevokeIsolatedBackend removes every permission the sandbox was granted and
// deletes its identity, returning the machine to its state before first use.
// A backend constructed earlier must not be used afterwards.
func RevokeIsolatedBackend() error {
	identity, err := openIdentity()
	if err != nil {
		return err
	}
	record := grantRecord{path: filepath.Join(identity.folder, "grants.txt")}
	paths, err := record.paths()
	if err != nil {
		return err
	}
	var errs []error
	for _, path := range paths {
		err := revokeTree(path, identity.sid)
		if err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			errs = append(errs, fmt.Errorf("revoke %s: %w", path, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	name, err := windows.UTF16PtrFromString(appContainerName)
	if err != nil {
		return err
	}
	if hr, _, _ := procDeleteAppContainerProfile.Call(uintptr(unsafe.Pointer(name))); hr != 0 {
		return fmt.Errorf("delete AppContainer profile: HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

// grantRecord is an append-only list of granted trees, one path per line,
// kept in the container's own folder so it is deleted with the identity it
// describes.
type grantRecord struct{ path string }

func (r grantRecord) paths() ([]string, error) {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		if path := strings.TrimSpace(line); path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func (r grantRecord) add(path string) error {
	existing, err := r.paths()
	if err != nil {
		return err
	}
	for _, recorded := range existing {
		if strings.EqualFold(recorded, path) {
			return nil
		}
	}
	file, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(file, path)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

type appContainerScope struct {
	spec        ScopeSpec
	identity    *containerIdentity
	lifetime    context.Context
	environment []uint16
}

// Close releases nothing: grants are durable by design, and processes are
// owned by the scope lifetime.
func (*appContainerScope) Close(context.Context) error { return nil }

func (s *appContainerScope) Start(ctx context.Context, launch Launch) (Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if launch.Command.Stdin != nil {
		return nil, errAppContainerStdin
	}
	application, commandLine, err := containerCommandLine(launch.Command, s.spec.Workspace, launch.Dir)
	if err != nil {
		return nil, err
	}
	job, err := containerJob()
	if err != nil {
		return nil, err
	}
	processCtx, cancel := context.WithTimeout(s.lifetime, launch.Timeout)
	started, err := s.create(application, commandLine, launch.Dir, job)
	if err != nil {
		cancel()
		windows.CloseHandle(job)
		return nil, err
	}

	buffer := newOutput(s.spec.MaxOutputBytes)
	p := &managedProcess{
		state:  ProcessState{ID: launch.ID, PID: int(started.pid), StartedAt: time.Now()},
		output: buffer,
		done:   make(chan struct{}),
		cancel: cancel,
	}
	var readers sync.WaitGroup
	for _, pipe := range []struct {
		file   *os.File
		stream string
	}{{started.stdout, "stdout"}, {started.stderr, "stderr"}} {
		readers.Add(1)
		go func(file *os.File, stream string) {
			defer readers.Done()
			_, _ = io.Copy(streamWriter{buffer, stream}, file)
			file.Close()
		}(pipe.file, pipe.stream)
	}

	exited := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-processCtx.Done():
			_ = windows.TerminateJobObject(job, 1)
		case <-exited:
		}
	}()
	go func() {
		_, _ = windows.WaitForSingleObject(started.process, windows.INFINITE)
		close(exited)
		<-watcherDone
		var code uint32
		_ = windows.GetExitCodeProcess(started.process, &code)
		windows.CloseHandle(started.process)
		// Closing the job kills descendants that outlived the command, which
		// also releases their copies of the output pipes.
		windows.CloseHandle(job)
		readers.Wait()
		state := p.Snapshot()
		state.Exited = true
		state.ExitCode = int(code)
		state.Reason = exitReason(processCtx)
		p.exited(state)
	}()
	return p, nil
}

type startedProcess struct {
	process        windows.Handle
	pid            uint32
	stdout, stderr *os.File
}

// create starts the process suspended inside the container, assigns it to its
// job, and only then lets it run, so no descendant can exist outside the job.
func (s *appContainerScope) create(application, commandLine, dir string, job windows.Handle) (*startedProcess, error) {
	app, err := windows.UTF16PtrFromString(application)
	if err != nil {
		return nil, err
	}
	line, err := windows.UTF16PtrFromString(commandLine)
	if err != nil {
		return nil, err
	}
	directory, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return nil, err
	}

	// Inheritable handles exist only while this lock is held, and the handle
	// list below restricts the child to exactly these three.
	syscall.ForkLock.Lock()
	stdoutRead, stdoutWrite, err := inheritablePipe()
	if err != nil {
		syscall.ForkLock.Unlock()
		return nil, err
	}
	stderrRead, stderrWrite, err := inheritablePipe()
	if err != nil {
		syscall.ForkLock.Unlock()
		closeHandles(stdoutRead, stdoutWrite)
		return nil, err
	}
	null, err := inheritableNull()
	if err != nil {
		syscall.ForkLock.Unlock()
		closeHandles(stdoutRead, stdoutWrite, stderrRead, stderrWrite)
		return nil, err
	}
	pi, err := s.createProcess(app, line, directory, []windows.Handle{null, stdoutWrite, stderrWrite})
	closeHandles(null, stdoutWrite, stderrWrite)
	syscall.ForkLock.Unlock()
	if err != nil {
		closeHandles(stdoutRead, stderrRead)
		return nil, err
	}

	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		closeHandles(pi.Process, pi.Thread, stdoutRead, stderrRead)
		return nil, err
	}
	_, err = windows.ResumeThread(pi.Thread)
	windows.CloseHandle(pi.Thread)
	if err != nil {
		_ = windows.TerminateJobObject(job, 1)
		closeHandles(pi.Process, stdoutRead, stderrRead)
		return nil, err
	}
	return &startedProcess{
		process: pi.Process,
		pid:     pi.ProcessId,
		stdout:  os.NewFile(uintptr(stdoutRead), "stdout"),
		stderr:  os.NewFile(uintptr(stderrRead), "stderr"),
	}, nil
}

func (s *appContainerScope) createProcess(app, line, dir *uint16, handles []windows.Handle) (*windows.ProcessInformation, error) {
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	capabilities := securityCapabilities{AppContainerSid: s.identity.sid}
	if err := attributes.Update(procThreadAttributeSecurityCapabilities, unsafe.Pointer(&capabilities), unsafe.Sizeof(capabilities)); err != nil {
		return nil, err
	}
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return nil, err
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES
	startup.StdInput, startup.StdOutput, startup.StdErr = handles[0], handles[1], handles[2]
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED)
	if err := windows.CreateProcess(app, line, nil, nil, true, flags, &s.environment[0], dir, &startup.StartupInfo, &pi); err != nil {
		return nil, err
	}
	return &pi, nil
}

func containerJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	info.BasicLimitInformation.ActiveProcessLimit = maxContainerProcesses
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// containerCommandLine resolves the program on the host and quotes argv for
// CreateProcess. Shell commands use the local backend's PowerShell invocation,
// entered through the workspace drive, so a command means the same thing
// under either backend.
func containerCommandLine(command Command, workspace, dir string) (string, string, error) {
	if command.Shell != "" {
		location := workspaceDrive + `:\`
		if rel, err := filepath.Rel(workspace, dir); err == nil && rel != "." {
			location += rel
		}
		script := "New-PSDrive -Name " + workspaceDrive + " -PSProvider FileSystem -Root " + psLiteral(workspace) + " | Out-Null\n" +
			"Set-Location -LiteralPath " + psLiteral(location) + "\n" +
			command.Shell
		shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		argv := []string{shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}
		return shell, joinCommandLine(argv), nil
	}
	application, err := exec.LookPath(command.Executable)
	if err != nil {
		return "", "", err
	}
	if application, err = filepath.Abs(application); err != nil {
		return "", "", errAppContainerUnsupportedPath
	}
	return application, joinCommandLine(append([]string{application}, command.Args...)), nil
}

// psLiteral quotes s as a PowerShell single-quoted string, in which only a
// doubled quote is special.
func psLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func joinCommandLine(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = windows.EscapeArg(arg)
	}
	return strings.Join(quoted, " ")
}

// environmentBlock merges overrides into the scope environment, keeps one
// entry per name (Windows names are case-insensitive), and sorts it as
// CreateProcess expects.
func environmentBlock(base []string, overrides map[string]string) ([]uint16, error) {
	merged := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		merged[strings.ToUpper(name)] = name + "=" + value
	}
	for name, value := range overrides {
		if value != "" {
			merged[strings.ToUpper(name)] = name + "=" + value
		}
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var block []uint16
	for _, key := range keys {
		entry, err := windows.UTF16FromString(merged[key])
		if err != nil {
			return nil, err
		}
		block = append(block, entry...)
	}
	return append(block, 0), nil
}

func inheritablePipe() (read, write windows.Handle, err error) {
	attributes := windows.SecurityAttributes{InheritHandle: 1}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	if err = windows.CreatePipe(&read, &write, &attributes, 0); err != nil {
		return 0, 0, err
	}
	if err = windows.SetHandleInformation(read, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		closeHandles(read, write)
		return 0, 0, err
	}
	return read, write, nil
}

func inheritableNull() (windows.Handle, error) {
	attributes := windows.SecurityAttributes{InheritHandle: 1}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	name, err := windows.UTF16PtrFromString("NUL")
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &attributes, windows.OPEN_EXISTING, 0, 0)
}

func closeHandles(handles ...windows.Handle) {
	for _, h := range handles {
		windows.CloseHandle(h)
	}
}

// goReadPaths finds the module cache the workspace's toolchain uses. The
// toolchain itself lives inside it when Go auto-selected one, as it does for a
// go.mod newer than the installed release. It runs the host's go, through the
// local backend, with no enclosing scope: this is controller work, not a
// model command.
func goReadPaths(workspace string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := RunLocal(ctx, workspace, LocalPolicy(), Command{Executable: "go", Args: []string{"env", "GOROOT", "GOMODCACHE"}})
	if err != nil || result.Err() != nil {
		return nil
	}
	var found []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		path := strings.TrimSpace(line)
		if path == "" || !filepath.IsAbs(path) {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		}
	}
	// Parents first, so a toolchain inside the module cache is covered by the
	// cache's grant instead of being walked twice.
	sort.Slice(found, func(i, j int) bool { return len(found[i]) < len(found[j]) })
	var paths []string
	for _, path := range found {
		if !within(path, paths) {
			paths = append(paths, path)
		}
	}
	return paths
}

// within reports whether path is inside one of roots, so a toolchain under the
// module cache is granted once, with its parent.
func within(path string, roots []string) bool {
	for _, root := range roots {
		if rel, err := filepath.Rel(root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
