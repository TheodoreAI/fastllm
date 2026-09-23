//go:build !windows

package execution

// IsolatedBackend returns this platform's isolated backend. None exists here
// yet, so isolation requests keep failing rather than running unconfined.
func IsolatedBackend() (Backend, bool) { return nil, false }

// RevokeIsolatedBackend has nothing to undo where no isolated backend exists.
func RevokeIsolatedBackend() error { return nil }
