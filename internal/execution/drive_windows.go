//go:build windows

package execution

import (
	"errors"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

// Commands start in a drive letter whose root is the workspace. PowerShell
// and MSYS programs such as git check every directory above their working
// directory, and the container cannot read C:\Users or list the profile
// folders; a drive root has no directory above it, so the container needs no
// access outside the workspace at all.
//
// Windows stacks definitions of one letter, so every scope pushes its own and
// pops it on close. Two scopes, or two fastllm processes, on one workspace
// share a letter safely, and a definition a crash left behind is reused
// rather than claiming another letter.

var driveMu sync.Mutex

const driveFlags = windows.DDD_NO_BROADCAST_SYSTEM

// mapWorkspaceDrive pushes a drive-letter definition for workspace and
// returns the drive ("W:") and the function that pops it.
func mapWorkspaceDrive(workspace string) (string, func(), error) {
	driveMu.Lock()
	defer driveMu.Unlock()
	target, err := windows.UTF16PtrFromString(workspace)
	if err != nil {
		return "", nil, err
	}
	for _, drive := range driveCandidates(workspace) {
		name, _ := windows.UTF16PtrFromString(drive)
		if err := windows.DefineDosDevice(driveFlags, name, target); err != nil {
			continue
		}
		// Another process may have taken the letter between the check and
		// the definition; never shadow a drive that is not ours.
		if current, ok := driveTarget(drive); !ok || !strings.EqualFold(current, workspace) {
			_ = windows.DefineDosDevice(driveFlags|windows.DDD_REMOVE_DEFINITION|windows.DDD_EXACT_MATCH_ON_REMOVE, name, target)
			continue
		}
		var once sync.Once
		unmap := func() {
			once.Do(func() {
				driveMu.Lock()
				defer driveMu.Unlock()
				_ = windows.DefineDosDevice(driveFlags|windows.DDD_REMOVE_DEFINITION|windows.DDD_EXACT_MATCH_ON_REMOVE, name, target)
			})
		}
		return drive, unmap, nil
	}
	return "", nil, errors.New("no free drive letter for the sandbox workspace")
}

// driveCandidates lists letters to try, from Z down: first any letter
// already mapped to this workspace, then letters no drive uses.
func driveCandidates(workspace string) []string {
	used, _ := windows.GetLogicalDrives()
	var mine, free []string
	for letter := 'Z'; letter >= 'D'; letter-- {
		drive := string(letter) + ":"
		if target, ok := driveTarget(drive); ok {
			if strings.EqualFold(target, workspace) {
				mine = append(mine, drive)
			}
			continue
		}
		if used&(1<<uint(letter-'A')) == 0 {
			free = append(free, drive)
		}
	}
	return append(mine, free...)
}

// driveTarget returns the path drive currently maps to, when it is a
// drive-letter alias for a directory.
func driveTarget(drive string) (string, bool) {
	name, err := windows.UTF16PtrFromString(drive)
	if err != nil {
		return "", false
	}
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.QueryDosDevice(name, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 {
		return "", false
	}
	// The result is a list of definitions, newest first.
	current := windows.UTF16ToString(buffer)
	path, ok := strings.CutPrefix(current, `\??\`)
	return path, ok
}
