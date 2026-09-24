package execution

import (
	"fmt"
	"strings"
)

// The macOS sandbox is Seatbelt, the kernel sandbox behind App Sandbox, driven
// by /usr/bin/sandbox-exec with a profile in Apple's SBPL. The profile is built
// here, without a build tag, so its rules are tested on every platform; only
// launching it is macOS-specific (seatbelt_darwin.go).
//
// It mirrors the Windows AppContainer grant: full access to the workspace,
// read access to the toolchain, a private scratch directory, no network, and
// nothing else. Reading is denied outside the system directories the
// toolchain needs, so the user's home (~/.ssh, ~/.aws, browser profiles) is
// unreadable; file metadata stays visible because nearly every tool stats
// paths. Paths reach the profile only as -D parameters, never spliced into
// the text, so an unusual workspace path cannot change what the profile says.

// seatbeltSystemReads are directories any binary may need to load and run.
var seatbeltSystemReads = []string{
	"/usr", "/bin", "/sbin", "/System", "/Library",
	"/opt/homebrew", "/usr/local",
	"/private/etc", "/private/var/db/timezone", "/private/var/db/dyld",
	"/Applications/Xcode.app", "/dev",
}

// seatbeltProfile returns a profile that reads extraReads toolchain
// directories from parameters READ_0 .. READ_n-1, alongside WORKSPACE and
// SCRATCH.
func seatbeltProfile(extraReads int) string {
	var b strings.Builder
	b.WriteString(`(version 1)
(deny default)

; Processes: run and fork, signal only within the sandbox.
(allow process-exec)
(allow process-fork)
(allow signal (target same-sandbox))
(allow process-info* (target same-sandbox))
(allow sysctl-read)

; Name and user lookups, and logging, which libc and git need.
(allow mach-lookup
  (global-name "com.apple.system.opendirectoryd.libinfo")
  (global-name "com.apple.system.opendirectoryd.membership")
  (global-name "com.apple.system.logger")
  (global-name "com.apple.system.notification_center"))

; Metadata (names, sizes) anywhere; contents only where granted below.
(allow file-read-metadata)
(allow file-read*
  (literal "/")
  (literal "/private")
  (literal "/private/var")
`)
	for _, dir := range seatbeltSystemReads {
		fmt.Fprintf(&b, "  (subpath %q)\n", dir)
	}
	b.WriteString("  (subpath (param \"WORKSPACE\"))\n  (subpath (param \"SCRATCH\"))")
	for i := 0; i < extraReads; i++ {
		fmt.Fprintf(&b, "\n  (subpath (param \"READ_%d\"))", i)
	}
	b.WriteString(`)

; Writing: the workspace, the private scratch directory, and the null devices.
(allow file-write*
  (subpath (param "WORKSPACE"))
  (subpath (param "SCRATCH"))
  (literal "/dev/null")
  (literal "/dev/zero")
  (literal "/dev/dtracehelper"))
(allow file-ioctl (literal "/dev/null") (literal "/dev/zero"))

; No network: nothing below allows network-outbound, network-inbound, or
; network-bind, so (deny default) denies them all.
`)
	return b.String()
}

// seatbeltArgs is the sandbox-exec argument list that runs argv inside the
// profile. workspace and scratch must be canonical (/private/var, not /var):
// Seatbelt matches the resolved path.
func seatbeltArgs(workspace, scratch string, reads []string, argv []string) []string {
	args := []string{"-D", "WORKSPACE=" + workspace, "-D", "SCRATCH=" + scratch}
	for i, dir := range reads {
		args = append(args, "-D", fmt.Sprintf("READ_%d=%s", i, dir))
	}
	args = append(args, "-p", seatbeltProfile(len(reads)))
	return append(args, argv...)
}
