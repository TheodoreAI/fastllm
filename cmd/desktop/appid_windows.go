//go:build windows

package main

import (
	"log"
	"unsafe"

	"golang.org/x/sys/windows"
)

// setAppUserModelID calls shell32.dll!SetCurrentProcessExplicitAppUserModelID
// — golang.org/x/sys/windows doesn't wrap this one, so it's declared
// directly against shell32.dll, same technique
// internal/screenshot/screenshot_windows.go and
// internal/terminal/jobobject_windows.go already use for APIs that
// package doesn't cover. Must run before any window is created (so right
// at the top of main(), before wails.Run) — Windows reads this per-process
// setting to decide taskbar grouping, and a window created before it's set
// keeps whatever auto-derived ID it started with. See appUserModelID's
// doc comment (in main.go) for why this matters for the pinned shortcut
// specifically. Failure is logged, not fatal — worst case is the
// pre-existing two-icon taskbar behavior, not a broken app.
func setAppUserModelID(id string) {
	idPtr, err := windows.UTF16PtrFromString(id)
	if err != nil {
		log.Printf("setAppUserModelID: %v", err)
		return
	}
	modShell32 := windows.NewLazySystemDLL("shell32.dll")
	procSetAppID := modShell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	// Returns an HRESULT: 0 (S_OK) is success, any other value is a
	// failure code — the inverse of the usual Win32 "0 = failure"
	// convention most of this codebase's other syscalls follow (compare
	// screenshot_windows.go's procs, which return 0 on failure).
	if ret, _, callErr := procSetAppID.Call(uintptr(unsafe.Pointer(idPtr))); ret != 0 {
		log.Printf("SetCurrentProcessExplicitAppUserModelID: HRESULT 0x%x: %v", ret, callErr)
	}
}
