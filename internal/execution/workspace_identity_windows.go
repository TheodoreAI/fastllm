//go:build windows

package execution

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Grant records belong to the controller, not the container's writable HOME.
// They are also the index used by -revoke-sandbox to find workspace identities.
func workspaceGrantsDir() (string, error) {
	base, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "fastllm", "sandbox-grants"), nil
}

func validWorkspaceContainerName(name string) bool {
	suffix, ok := strings.CutPrefix(name, workspaceContainerPrefix)
	if !ok || len(suffix) != 32 {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

// An OS file lock serializes grant/revoke operations across backend instances
// and fastllm processes. A shared lock also prevents lost ACL updates when two
// identities grant the same toolchain tree. It remains so contenders lock one file.
func lockWorkspaceGrants(name string) (grantRecord, func(), error) {
	if !validWorkspaceContainerName(name) {
		return grantRecord{}, nil, errors.New("invalid workspace container name")
	}
	dir, err := workspaceGrantsDir()
	if err != nil {
		return grantRecord{}, nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return grantRecord{}, nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, "grants.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return grantRecord{}, nil, err
	}
	overlap := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlap); err != nil {
		file.Close()
		return grantRecord{}, nil, err
	}
	unlock := func() {
		_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlap)
		_ = file.Close()
	}
	return grantRecord{path: filepath.Join(dir, name+".txt")}, unlock, nil
}

func revokeWorkspaceIdentities() error {
	dir, err := workspaceGrantsDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".txt")
		if entry.IsDir() || name == entry.Name() || !validWorkspaceContainerName(name) {
			continue
		}
		errs = append(errs, revokeWorkspaceIdentity(name))
	}
	return errors.Join(errs...)
}

func revokeWorkspaceIdentity(name string) error {
	record, unlock, err := lockWorkspaceGrants(name)
	if err != nil {
		return err
	}
	defer unlock()
	identity, err := openIdentity(name)
	if err != nil {
		return err
	}
	if err := revokeIdentity(identity, record); err != nil {
		return err
	}
	if err := os.Remove(record.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
