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
	if dacl == nil || hasGrant(dacl, sid, mask) {
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

// hasGrant reports whether dacl already allows sid at least mask over the
// whole tree, directly or inherited from a granted parent.
func hasGrant(dacl *windows.ACL, sid *windows.SID, mask windows.ACCESS_MASK) bool {
	const reachesDescendants = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Mask&mask == mask && ace.Header.AceFlags&reachesDescendants == reachesDescendants &&
			(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(sid) {
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
