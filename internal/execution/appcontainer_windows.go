//go:build windows

package execution

import (
	"context"
	"crypto/sha256"
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

// appContainerName is the legacy shared identity, retained only for revocation.
const appContainerName = "fastllm.sandbox"

const workspaceContainerPrefix = "fastllm.workspace."

const (
	procThreadAttributeSecurityCapabilities = 0x00020009
	hresultAlreadyExists                    = 0x800700B7

	fileAllAccess      windows.ACCESS_MASK = 0x001F01FF
	fileReadExecute    windows.ACCESS_MASK = 0x001200A9
	fileReadAttributes windows.ACCESS_MASK = windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE

	// A ceiling on descendants, so a fork bomb exhausts its job, not the host.
	maxContainerProcesses = 512
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

// NewAppContainerBackend runs commands inside a per-workspace AppContainer holding no
// capabilities, so the kernel denies it the network and every object not
// granted to it. The token is inherited by every descendant, and each command
// runs in a kill-on-close Job Object, so confinement needs no mediation by
// fastllm after launch.
//
// Opening a scope grants the container full access to the workspace and read
// access to the Go module cache the workspace resolves, which holds any
// auto-selected toolchain, and maps a drive letter to the workspace for
// commands to start in. Grants persist, are detected so each tree is granted
// once, and are recorded so RevokeIsolatedBackend can remove them.
func NewAppContainerBackend() Backend { return &appContainerBackend{} }

type appContainerBackend struct{}

type containerIdentity struct {
	name   string
	sid    *windows.SID
	folder string
}

func (*appContainerBackend) Name() string   { return AppContainerBackendName }
func (*appContainerBackend) Isolated() bool { return true }

func (*appContainerBackend) Available(context.Context) error {
	for _, proc := range []*windows.LazyProc{procCreateAppContainerProfile, procDeleteAppContainerProfile, procDeriveAppContainerSid, procGetAppContainerFolderPath} {
		if err := proc.Find(); err != nil {
			return fmt.Errorf("AppContainer API unavailable: %w", err)
		}
	}
	return nil
}

func workspaceContainerName(workspace string) string {
	// Do not fold case: Windows also supports case-sensitive directories.
	digest := sha256.Sum256([]byte(filepath.Clean(workspace)))
	return fmt.Sprintf("%s%x", workspaceContainerPrefix, digest[:16])
}

func openIdentity(profileName string) (*containerIdentity, error) {
	if err := (&appContainerBackend{}).Available(context.Background()); err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(profileName)
	if err != nil {
		return nil, err
	}
	var sid *windows.SID
	hr, _, _ := procCreateAppContainerProfile.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0, 0, uintptr(unsafe.Pointer(&sid)))
	if uint32(hr) == hresultAlreadyExists {
		hr, _, _ = procDeriveAppContainerSid.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&sid)))
	}
	if hr != 0 {
		return nil, fmt.Errorf("AppContainer profile %q: HRESULT 0x%08x", profileName, uint32(hr))
	}
	// Both APIs allocate the SID. Keep a Go-owned copy and free the native one.
	ownedSID, err := sid.Copy()
	windows.FreeSid(sid)
	if err != nil {
		return nil, err
	}
	sid = ownedSID
	sidString, err := windows.UTF16PtrFromString(sid.String())
	if err != nil {
		return nil, err
	}
	var folder *uint16
	hr, _, _ = procGetAppContainerFolderPath.Call(uintptr(unsafe.Pointer(sidString)), uintptr(unsafe.Pointer(&folder)))
	if hr != 0 {
		return nil, fmt.Errorf("AppContainer folder: HRESULT 0x%08x", uint32(hr))
	}
	identity := &containerIdentity{name: profileName, sid: sid, folder: windows.UTF16PtrToString(folder)}
	windows.CoTaskMemFree(unsafe.Pointer(folder))
	return identity, nil
}

// profilesDirectory is the directory holding every user profile, C:\Users on
// a default install. The container cannot read it, and only an administrator
// can change that.
func profilesDirectory() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_UserProfiles, 0)
}

func (b *appContainerBackend) Open(ctx context.Context, spec ScopeSpec) (BackendScope, error) {
	// The manager supplies a canonical workspace, so aliases reuse an identity.
	identity, err := openIdentity(workspaceContainerName(spec.Workspace))
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
	drive, unmap, err := mapWorkspaceDrive(spec.Workspace)
	if err != nil {
		return nil, err
	}
	return &appContainerScope{spec: spec, identity: identity, lifetime: ctx, environment: block, drive: drive, unmap: unmap}, nil
}

// grant makes the workspace and its toolchain reachable to the container, and
// records each tree so a revoke can find it.
func (b *appContainerBackend) grant(identity *containerIdentity, workspace string) error {
	record, unlock, err := lockWorkspaceGrants(identity.name)
	if err != nil {
		return err
	}
	defer unlock()

	// Recorded before granting, so an interrupted grant is still revoked.
	if err := record.add(treeGrant, workspace); err != nil {
		return err
	}
	if _, err := grantTree(workspace, identity.sid, fileAllAccess); err != nil {
		return fmt.Errorf("grant workspace to sandbox: %w", err)
	}
	for _, dir := range goReadPaths(workspace) {
		if err := record.add(treeGrant, dir); err != nil {
			return err
		}
		_, err := grantTree(dir, identity.sid, fileReadExecute)
		// A toolchain the user cannot re-permission, such as one under
		// Program Files, is normally readable to containers already.
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			continue
		}
		if err != nil {
			return fmt.Errorf("grant %s to sandbox: %w", dir, err)
		}
	}
	return nil
}

// IsolationIdentity names the sandbox identity, so an elevated helper can be
// told exactly which identity to grant rather than looking up its own, which
// differs when another administrator account approves the elevation.
func IsolationIdentity() (string, error) {
	identity, err := openIdentity(appContainerName)
	if err != nil {
		return "", err
	}
	return identity.sid.String(), nil
}

// IsolationSetupPresent reports whether the sandbox holds a grant on the
// profiles directory. The backend no longer needs one, since commands start
// from a workspace drive, but an earlier one-time setup made it, and only an
// administrator can remove it.
func IsolationSetupPresent() (bool, error) {
	identity, err := openIdentity(appContainerName)
	if err != nil {
		return false, err
	}
	profiles, err := profilesDirectory()
	if err != nil {
		return false, err
	}
	return directGrant(profiles, identity.sid)
}

// RevokeIsolationSetup removes the profiles-directory grant. It needs
// administrator rights.
func RevokeIsolationSetup(sidString string) error {
	sid, profiles, err := setupTarget(sidString)
	if err != nil {
		return err
	}
	return revokeSelf(profiles, sid)
}

func setupTarget(sidString string) (*windows.SID, string, error) {
	sid, err := windows.StringToSid(sidString)
	if err != nil {
		return nil, "", fmt.Errorf("sandbox identity %q: %w", sidString, err)
	}
	// Only an AppContainer identity may receive this grant, never a user or
	// group handed to an elevated process by mistake.
	if !strings.HasPrefix(sid.String(), "S-1-15-2-") {
		return nil, "", fmt.Errorf("%s is not an AppContainer identity", sid)
	}
	profiles, err := profilesDirectory()
	return sid, profiles, err
}

// RevokeIsolatedBackend removes every permission the sandbox was granted and
// deletes its identity, returning the machine to its state before first use.
// The profiles-directory grant must be revoked first, with administrator
// rights; while it remains the identity is kept, so the grant never outlives
// the identity it names. A backend constructed earlier must not be used
// afterwards.
func RevokeIsolatedBackend() error {
	identity, err := openIdentity(appContainerName)
	if err != nil {
		return err
	}
	if present, err := IsolationSetupPresent(); err != nil {
		return err
	} else if present {
		return errors.New("the sandbox's profiles-directory grant is still present; revoke it with administrator rights first")
	}
	if err := revokeIdentity(identity, grantRecord{path: filepath.Join(identity.folder, "grants.txt")}); err != nil {
		return err
	}
	return revokeWorkspaceIdentities()
}

func revokeIdentity(identity *containerIdentity, record grantRecord) error {
	entries, err := record.entries()
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		// Read-only system trees can be recorded before a denied grant attempt.
		// Do not require WRITE_DAC (or propagate ACL changes) if no grant exists.
		direct, err := directGrant(entry.path, identity.sid)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("inspect grant %s: %w", entry.path, err))
			continue
		}
		if !direct {
			continue
		}
		revoke := revokeTree
		if entry.kind == selfGrant {
			revoke = revokeSelf
		}
		err = revoke(entry.path, identity.sid)
		if err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			errs = append(errs, fmt.Errorf("revoke %s: %w", entry.path, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	name, err := windows.UTF16PtrFromString(identity.name)
	if err != nil {
		return err
	}
	if hr, _, _ := procDeleteAppContainerProfile.Call(uintptr(unsafe.Pointer(name))); hr != 0 {
		return fmt.Errorf("delete AppContainer profile: HRESULT 0x%08x", uint32(hr))
	}
	return nil
}

type grantKind int

const (
	treeGrant grantKind = iota // the path and everything beneath it
	selfGrant                  // the directory itself only
)

type grantEntry struct {
	kind grantKind
	path string
}

// grantRecord is an append-only list of granted paths, one per line. Workspace
// identities keep it in controller storage; only legacy records live in HOME.
// A tree grant is a bare path; a directory-only grant is prefixed "self\t".
type grantRecord struct{ path string }

const (
	selfPrefix = "self\t"
	treePrefix = "tree\t"
)

func (r grantRecord) entries() ([]grantEntry, error) {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []grantEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		// Earlier records also wrote tree grants with a "tree\t" prefix; a
		// path never starts with either prefix, so all three forms are read.
		if path, ok := strings.CutPrefix(line, selfPrefix); ok {
			entries = append(entries, grantEntry{selfGrant, strings.TrimSpace(path)})
		} else if path, ok := strings.CutPrefix(line, treePrefix); ok {
			entries = append(entries, grantEntry{treeGrant, strings.TrimSpace(path)})
		} else if path := strings.TrimSpace(line); path != "" {
			entries = append(entries, grantEntry{treeGrant, path})
		}
	}
	return entries, nil
}

func (r grantRecord) add(kind grantKind, path string) error {
	existing, err := r.entries()
	if err != nil {
		return err
	}
	for _, entry := range existing {
		if entry.kind == kind && strings.EqualFold(entry.path, path) {
			return nil
		}
	}
	file, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	line := path
	if kind == selfGrant {
		line = selfPrefix + path
	}
	_, err = fmt.Fprintln(file, line)
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
	// drive is the letter ("W:") commands see the workspace as.
	drive string
	unmap func()
}

// Close releases the workspace drive. Grants are durable by design, and the
// scope has already stopped its processes.
func (s *appContainerScope) Close(context.Context) error {
	s.unmap()
	return nil
}

// CommandWorkspace is the workspace as commands see it: the root of the
// workspace drive.
func (s *appContainerScope) CommandWorkspace() string { return s.drive + `\` }

// commandDir translates a host directory inside the workspace to the same
// directory under the workspace drive.
func (s *appContainerScope) commandDir(dir string) (string, error) {
	rel, err := filepath.Rel(s.spec.Workspace, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("working directory %q is outside the workspace", dir)
	}
	if rel == "." {
		return s.drive + `\`, nil
	}
	return s.drive + `\` + rel, nil
}

func (s *appContainerScope) Start(ctx context.Context, launch Launch) (Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if launch.Command.Stdin != nil {
		return nil, errAppContainerStdin
	}
	application, commandLine, err := containerCommandLine(launch.Command)
	if err != nil {
		return nil, err
	}
	dir, err := s.commandDir(launch.Dir)
	if err != nil {
		return nil, err
	}
	job, err := containerJob()
	if err != nil {
		return nil, err
	}
	processCtx, cancel := context.WithTimeout(s.lifetime, launch.Timeout)
	started, err := s.create(application, commandLine, dir, job)
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
	// Tool hosts can run on a private desktop whose ACL excludes AppContainer
	// tokens. Inheriting it makes user32 initialization fail before the shell
	// executes (STATUS_DLL_INIT_FAILED). Use the interactive desktop without
	// widening any ACL or adding capabilities; CREATE_NO_WINDOW stays in force.
	startup.Desktop, _ = windows.UTF16PtrFromString(`winsta0\default`)
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
// CreateProcess. Shell commands use the same PowerShell invocation as the
// local backend, so a command means the same thing under either backend.
func containerCommandLine(command Command) (string, string, error) {
	if command.Shell != "" {
		shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		argv := []string{shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command.Shell}
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
