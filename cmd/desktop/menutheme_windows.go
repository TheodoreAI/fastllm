//go:build windows

package main

import (
	"sync"

	"golang.org/x/sys/windows"
)

// Wails' WindowSetDarkTheme/WindowSetLightTheme only affect the custom
// title bar it draws itself — the native File/Edit/Help menu bar below it
// is a real Win32 HMENU, owned and painted entirely by user32.dll/
// uxtheme.dll, and Wails v2 has no API that reaches it. Left alone it
// always renders with Windows' light menu theme regardless of the app's
// own theme, which is why a dark app previously showed a stray white bar
// under the title bar.
//
// The undocumented but widely-relied-upon fix (used by Windows Terminal,
// VS Code, and others before Microsoft ships a public dark-menu API) is
// uxtheme.dll's ordinal-only SetPreferredAppMode export (ordinal 135),
// followed by FlushMenuThemes (ordinal 136) to make already-created
// menus pick up the new mode. There's no name-based export because
// Microsoft never made this public, so a Windows update could in
// principle change or remove it — the call is wrapped in recover() below
// specifically because of that risk, so a future OS build where the
// ordinals no longer resolve as expected fails silently back to the
// native light menu instead of crashing the app.
var (
	modUxtheme              = windows.NewLazySystemDLL("uxtheme.dll")
	procSetPreferredAppMode = modUxtheme.NewProc("#135")
	procFlushMenuThemes     = modUxtheme.NewProc("#136")

	menuThemeMu sync.Mutex
)

const (
	appModeDefault   = 0
	appModeAllowDark = 1
)

// menuTheme is bound into the webview (options.App.Bind) so the frontend
// can flip the native menu bar's theme alongside the in-app one — see
// useTheme.js, which calls this the same place it already calls Wails'
// own WindowSetDarkTheme/WindowSetLightTheme.
type menuTheme struct{}

func (menuTheme) SetDark(dark bool) {
	menuThemeMu.Lock()
	defer menuThemeMu.Unlock()
	// Best-effort: these are undocumented ordinal exports with no
	// compile-time signature guarantee, so a mismatched uxtheme.dll on
	// some future Windows build failing here should degrade to "menu
	// stays light," never a crash.
	defer func() { _ = recover() }()

	mode := uintptr(appModeAllowDark)
	if !dark {
		mode = appModeDefault
	}
	_, _, _ = procSetPreferredAppMode.Call(mode)
	_, _, _ = procFlushMenuThemes.Call()
}
