//go:build windows

package terminal

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// A Windows Job Object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE guarantees
// the spawned shell (and anything it launches) is terminated the instant
// its last handle to the job closes — including when fastllm.exe itself
// is killed or crashes, not just on a graceful Close(). Without this, a
// ConPTY session only ever asks the shell to exit (by closing its pipes);
// PowerShell usually obliges, but nothing forces it, and an abrupt process
// kill or power-off leaves it orphaned since Windows doesn't kill child
// processes by default. golang.org/x/sys/windows doesn't wrap the Job
// Object API, so the handful of calls needed are declared directly here
// against kernel32.dll.

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x2000
)

// jobobjectBasicLimitInformation mirrors JOBOBJECT_BASIC_LIMIT_INFORMATION;
// only LimitFlags is set, but the struct must match the OS layout exactly
// since it's passed by raw pointer.
type jobobjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

// ioCounters mirrors IO_COUNTERS, an unused-but-required member of
// JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

// jobobjectExtendedLimitInformation mirrors
// JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type jobobjectExtendedLimitInformation struct {
	BasicLimitInformation jobobjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

var (
	modkernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procCreateJobObjectW      = modkernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObj  = modkernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObj = modkernel32.NewProc("AssignProcessToJobObject")
)

// newKillOnCloseJob creates an unnamed Job Object configured so every
// process assigned to it dies as soon as the job's handle is closed
// (JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE) — which happens automatically when
// the fastllm process exits or is killed, since the OS closes all of a
// process's open handles on exit regardless of how it ended.
func newKillOnCloseJob() (windows.Handle, error) {
	h, _, err := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		return 0, err
	}
	job := windows.Handle(h)

	info := jobobjectExtendedLimitInformation{
		BasicLimitInformation: jobobjectBasicLimitInformation{
			LimitFlags: jobObjectLimitKillOnJobClose,
		},
	}
	ret, _, err := procSetInformationJobObj.Call(
		uintptr(job),
		jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if ret == 0 {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// assignProcessToJob puts pid under the job's kill-on-close policy. Needs
// PROCESS_TERMINATE | PROCESS_SET_QUOTA on the process handle.
func assignProcessToJob(job windows.Handle, pid int) error {
	proc, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_SET_QUOTA, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(proc)

	ret, _, err := procAssignProcessToJobObj.Call(uintptr(job), uintptr(proc))
	if ret == 0 {
		return err
	}
	return nil
}
