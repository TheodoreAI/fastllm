//go:build !windows

package execution

// IsolatedBackend returns this platform's isolated backend. None exists here
// yet, so isolation requests keep failing rather than running unconfined.
func IsolatedBackend() (Backend, bool) { return nil, false }

// RevokeIsolatedBackend has nothing to undo where no isolated backend exists.
func RevokeIsolatedBackend() error { return nil }

// IsolationIdentity names no identity where no isolated backend exists.
func IsolationIdentity() (string, error) { return "", nil }

// IsolationSetupPresent is false: nothing is ever granted here.
func IsolationSetupPresent() (bool, error) { return false, nil }

// RevokeIsolationSetup has nothing to revoke where no isolated backend exists.
func RevokeIsolationSetup(string) error { return nil }
