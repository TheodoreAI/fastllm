//go:build windows

// Package elevate relaunches this executable with administrator rights for
// the one setup step that needs them. It runs only on a direct user command,
// never from a model tool, and like the folder picker it is an audited
// exception to the rule that processes start only through internal/execution:
// the program it starts is fastllm itself.
package elevate

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	// errorCancelled is ERROR_CANCELLED: the user declined the prompt.
	errorCancelled = windows.Errno(1223)
)

var (
	shell32             = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
	// ErrDeclined reports that administrator approval was declined.
	ErrDeclined = errors.New("administrator approval was declined")
)

// shellExecuteInfo mirrors SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	size          uint32
	mask          uint32
	window        windows.Handle
	verb          *uint16
	file          *uint16
	parameters    *uint16
	directory     *uint16
	show          int32
	instance      windows.Handle
	idList        uintptr
	class         *uint16
	classKey      windows.Handle
	hotKey        uint32
	iconOrMonitor windows.Handle
	process       windows.Handle
}

// Elevated reports whether this process already holds administrator rights.
func Elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// Run starts this executable again with args, asking Windows for
// administrator approval, waits for it, and returns its exit code.
func Run(args []string) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = windows.EscapeArg(arg)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return 0, err
	}
	parameters, err := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	if err != nil {
		return 0, err
	}
	info := shellExecuteInfo{mask: seeMaskNoCloseProcess | seeMaskNoAsync, verb: verb, file: file, parameters: parameters, show: windows.SW_HIDE}
	info.size = uint32(unsafe.Sizeof(info))
	if ok, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		if errors.Is(callErr, errorCancelled) {
			return 0, ErrDeclined
		}
		return 0, fmt.Errorf("start elevated process: %w", callErr)
	}
	if info.process == 0 {
		return 0, errors.New("elevated process handle unavailable")
	}
	defer windows.CloseHandle(info.process)
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return 0, err
	}
	return int(code), nil
}
