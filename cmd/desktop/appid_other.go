//go:build !windows

package main

// setAppUserModelID is a Windows-taskbar-grouping concern only (see
// appid_windows.go and appUserModelID's doc comment in main.go) — macOS
// and Linux have no equivalent OS-level concept this maps onto, so this
// is a no-op there.
func setAppUserModelID(id string) {}
