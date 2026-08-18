//go:build !windows

package main

// menuTheme is a no-op stub on non-Windows platforms — the native
// dark-menu problem it works around (see menutheme_windows.go) is
// specific to Win32's HMENU rendering and doesn't exist on other OSes'
// native menu implementations.
type menuTheme struct{}

func (menuTheme) SetDark(dark bool) {}
