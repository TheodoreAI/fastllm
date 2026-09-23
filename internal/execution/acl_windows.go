//go:build windows

package execution

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// grantTree gives sid mask over path and everything beneath it, reporting
// whether it changed anything. Windows copies an inheritable grant onto every
// existing descendant, so the first grant on a large tree is slow; later calls
// find the grant and return at once.
func grantTree(path string, sid *windows.SID, mask windows.ACCESS_MASK) (bool, error) {
	dacl, err := treeDACL(path)
	if err != nil {
		return false, err
	}
	// A NULL DACL already admits everyone. Merging into it would produce a
	// DACL holding only this grant and lock every other principal out.
	if dacl == nil || hasGrant(dacl, sid, mask, true) {
		return false, nil
	}
	return true, setTree(path, dacl, accessEntry(sid, windows.GRANT_ACCESS, mask, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT))
}

// revokeTree removes every grant made directly to sid on path, and with it
// every copy Windows propagated to descendants.
func revokeTree(path string, sid *windows.SID) error {
	dacl, err := treeDACL(path)
	if err != nil || dacl == nil {
		return err
	}
	return setTree(path, dacl, accessEntry(sid, windows.REVOKE_ACCESS, 0, windows.NO_INHERITANCE))
}

func treeDACL(path string) (*windows.ACL, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	dacl, _, err := sd.DACL()
	return dacl, err
}

func setTree(path string, dacl *windows.ACL, entry windows.EXPLICIT_ACCESS) error {
	merged, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, dacl)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, merged, nil)
}

// grantSelf adds a non-inheritable grant to one directory. It writes the DACL
// through the directory's handle, which skips the descendant walk that
// SetNamedSecurityInfo performs; on the profiles directory or a home directory
// that walk would rewrite every file beneath it.
func grantSelf(path string, sid *windows.SID, mask windows.ACCESS_MASK) (bool, error) {
	granted := false
	err := rewriteSelf(path, func(dacl *windows.ACL) *windows.EXPLICIT_ACCESS {
		if hasGrant(dacl, sid, mask, false) {
			return nil
		}
		granted = true
		entry := accessEntry(sid, windows.GRANT_ACCESS, mask, windows.NO_INHERITANCE)
		return &entry
	})
	return granted, err
}

// revokeSelf removes every grant made directly to sid on one directory,
// without touching its descendants.
func revokeSelf(path string, sid *windows.SID) error {
	return rewriteSelf(path, func(*windows.ACL) *windows.EXPLICIT_ACCESS {
		entry := accessEntry(sid, windows.REVOKE_ACCESS, 0, windows.NO_INHERITANCE)
		return &entry
	})
}

// hasSelfGrant reports whether sid holds at least mask on path itself.
func hasSelfGrant(path string, sid *windows.SID, mask windows.ACCESS_MASK) (bool, error) {
	dacl, err := treeDACL(path)
	if err != nil {
		return false, err
	}
	return dacl == nil || hasGrant(dacl, sid, mask, false), nil
}

// rewriteSelf merges the entry change returns into one directory's DACL; a nil
// entry leaves the DACL alone.
func rewriteSelf(path string, change func(*windows.ACL) *windows.EXPLICIT_ACCESS) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)

	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return err
	}
	entry := change(dacl)
	if entry == nil {
		return nil
	}
	merged, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{*entry}, dacl)
	if err != nil {
		return err
	}
	updated, err := windows.NewSecurityDescriptor()
	if err != nil {
		return err
	}
	if err := updated.SetDACL(merged, true, false); err != nil {
		return err
	}
	// Keep the inheritance state as it was, so later propagation from the
	// parent treats this DACL exactly as before.
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	const preserved = windows.SE_DACL_AUTO_INHERITED | windows.SE_DACL_PROTECTED
	if err := updated.SetControl(preserved, control&preserved); err != nil {
		return err
	}
	return windows.SetKernelObjectSecurity(handle, windows.DACL_SECURITY_INFORMATION, updated)
}

func accessEntry(sid *windows.SID, mode windows.ACCESS_MODE, mask windows.ACCESS_MASK, inheritance uint32) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: mask,
		AccessMode:        mode,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

// hasGrant reports whether dacl already allows sid at least mask, directly or
// inherited from a granted parent. A tree grant must also reach descendants.
func hasGrant(dacl *windows.ACL, sid *windows.SID, mask windows.ACCESS_MASK, tree bool) bool {
	const reachesDescendants = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Mask&mask != mask || !(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(sid) {
			continue
		}
		if !tree || ace.Header.AceFlags&reachesDescendants == reachesDescendants {
			return true
		}
	}
	return false
}

// directGrant reports whether sid holds a grant on path that was written there
// rather than inherited, which is what a revoke must remove.
func directGrant(path string, sid *windows.SID) (bool, error) {
	dacl, err := treeDACL(path)
	if err != nil || dacl == nil {
		return false, err
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, err
		}
		if ace.Header.AceFlags&windows.INHERITED_ACE == 0 && (*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(sid) {
			return true, nil
		}
	}
	return false, nil
}
