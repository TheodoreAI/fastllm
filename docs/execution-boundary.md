# Execution boundary

FastLLM routes model commands and workspace-triggered subprocesses through a
trusted broker in `internal/execution`. This is an execution boundary, **not an
OS sandbox** by itself. The default backend runs locally; on Windows an
AppContainer backend enforces isolation in the kernel. Requesting isolation
fails wherever no backend can enforce it. Never silently fall back to local
execution.

## Authority and workspace

The controller creates a scope with a canonical workspace, immutable policy,
environment, and limits. Model arguments may select a command and a relative
working directory, but cannot select trusted-user authority, host mounts, or an
execution backend. Approval remains in the controller; it cannot widen the
scope's permission ceiling. Children inherit or narrow parent capabilities.

Local subprocesses can write and use the network. Therefore model scopes require
command, write, and network permission before starting any subprocess, including
Git helpers. Restricted file searches use an in-process fallback; automatic Git
checkpoints are skipped when execution is prohibited.

File tools and commands must use the same workspace. Both backends expose the
canonical host workspace to the existing file adapter; the AppContainer backend
grants its identity access to that tree in place. A worker that copied the
workspace would also need a workspace adapter, and copying changes back would
have to validate paths and detect conflicting host edits.

## Ownership and lifecycle

A Runner owns an execution manager. Each autonomous run owns a scope, and each
asynchronous child agent owns a separate scope. Children retain the existing
Runner lifetime across interactive turns. Closing a run stops and joins its
commands; closing the Runner cancels agents and closes every remaining scope.

One process API supports Start, Snapshot, ReadOutput, Wait, and Stop. Foreground
execution waits; background execution returns a scope-owned ID. IDs and PIDs
are not authority outside their scope. Canceling a Wait only stops waiting;
canceling a scope or explicitly stopping a process terminates execution.

Output is bounded while being collected, with cursors and truncation indicators.
The backend never writes to the terminal. A slow or disconnected UI does not
block subprocess pipe drainage. Results distinguish launch errors, nonzero exit,
timeouts, cancellation, and output loss. Windows foreground and background model
commands both use PowerShell; Unix uses sh.

Approval revocation prevents subsequent launches. It does not undo completed
work or implicitly terminate already running processes. A fused edit/then_run
checks both permissions before modifying files; a runtime launch failure after
the edit is reported and is not represented as an atomic transaction.

## Execution paths

The boundary includes foreground/live commands, background processes, fused
follow-ups, Git-backed searches and checkpoints, builds/tests, and linters.
Internal helpers receive an already-bound scope through context and cannot
replace it with a broader policy or another root. Standalone local helper APIs
explicitly establish local authority when there is no enclosing scope.

Direct user shell commands use a separately constructed trusted scope and retain
the user's environment. Model tools cannot request that scope. Native folder
picker dialogs are a separate, explicitly audited host UI exception.

Scratch build directories are created by trusted code and explicitly attached as
derived scopes with the same policy and owner. Ordinary command arguments cannot
rebind a workspace. Disposable copies are not security isolation.

## Backend selection

A scope resolves exactly one backend at open time. `Backend` names a registered
implementation and empty selects `local`; `RequireIsolation` refuses any backend
that does not report enforcement. Registration is a controller act: model
arguments choose only among backends already registered on the manager, and can
neither add one nor shadow `local`.

Resolution never widens a request. An unknown name, an unisolated backend under
`RequireIsolation`, or a backend whose availability probe fails is an error, and
no scope is returned. There is no path from a failed isolation request to local
execution.

The scope, not the backend, is the authority. It checks policy, validates the
command shape, admits stdin, resolves the working directory inside the canonical
workspace, clamps the timeout, and assigns the process ID before a backend ever
sees a launch. A backend therefore cannot widen a scope; it can only fail to
narrow one. A backend that reports isolation it does not enforce is a defect, so
`Isolated` must track what the implementation actually establishes.

Per-scope backend resources are bound to the scope lifetime and released once.
The local backend owns nothing; an isolated backend owns its worker.

## Local backend

Local execution provides policy admission, canonical working-directory checks,
bounded output, time/process limits, environment filtering, and process cleanup.
It cannot prevent an admitted command from opening files outside its workspace,
using the network, accessing host services, or defeating Unix process-group
cleanup. Environment filtering is not credential isolation.

## Windows AppContainer backend

`appcontainer` is the isolated backend on Windows, built on `golang.org/x/sys`
with no external runtime. Every command runs under one fixed AppContainer
identity, `fastllm.sandbox`, holding no capabilities. The kernel checks every
file open and connection against that identity, so enforcement does not depend
on fastllm seeing the command's calls after launch. The token is inherited by
every descendant, and each command runs suspended until it is assigned to a
kill-on-close Job Object capped at 512 processes.

What it enforces, each verified by a containment test with a local-backend
control:

- No reads or writes outside the granted trees, including the user profile.
- No network, loopback included, so host services such as a local model server
  are unreachable.
- No descendant outlives its command.

Opening a scope grants the identity full access to the workspace and read access
to the Go module cache the workspace resolves, which holds any auto-selected
toolchain. Windows copies an inheritable grant onto every existing file, so the
first grant on the module cache takes seconds to minutes; later opens detect it.
Grants persist and are recorded in the identity's own folder, and
`RevokeIsolatedBackend` removes them and deletes the identity. Scratch state
(`TEMP`, `GOCACHE`, `HOME`) lives in that folder, not the profile or workspace.

Commands start in a drive letter mapped to the workspace, not at its host
path. PowerShell needs read-attributes on every directory above its working
directory, and Git for Windows, like other MSYS programs, needs list access on
every one. Under `C:\Users` that would mean an administrator grant on
`C:\Users` and exposing the names of everything in the user's home folder. A
drive root has no directory above it, so the container needs no access outside
the workspace. Each scope pushes its own definition of the letter and pops it
on close; Windows stacks definitions, so scopes and processes on one workspace
share a letter, and a definition left by a crash is reused and lasts until
logoff. The drive shows in Explorer while mapped. A sandboxed run tells the
model that commands see the workspace as that drive and should use
workspace-relative paths; absolute host paths under `C:\Users` do not resolve
inside commands. File tools keep the host path.

A custom environment block must carry `LOCALAPPDATA`, or process creation
fails with an unrelated environment error.

The sandbox is opt-in: `/set sandbox on` in either terminal mode, saved with
the session, or `-sandbox` on the command line. Child agents inherit it and
cannot turn it off. Where no isolated backend exists, turning it on is refused
and a sandboxed run fails rather than running unsandboxed. `-revoke-sandbox`
removes the grants and the identity, asking for administrator approval first
if an earlier version's grant on `C:\Users` is present.

It does not isolate the workspace from the model: whatever a command prints is
returned to the model, a legitimate channel no sandbox can close. It forwards no
stdin and sets no memory limit.

On macOS the isolated backend is Seatbelt (`internal/execution/seatbelt_*.go`),
the kernel sandbox behind App Sandbox, driven by `/usr/bin/sandbox-exec`. Each
command runs under a generated profile that denies by default. It may read only
the system directories programs need (`/usr`, `/bin`, `/System`, `/Library`,
Homebrew, `/private/etc`), the workspace, a private scratch folder, and the Go
toolchain the workspace resolves. It may write only the workspace and the
scratch folder, and it has no network. The user's home folder is unreadable, so
`HOME`, `TMPDIR`, and `GOCACHE` point into the scratch folder. File metadata
stays visible everywhere because nearly every tool stats paths. Paths reach the
profile as `-D` parameters, never as profile text. Nothing is granted
persistently, so there is nothing to revoke. `sandbox-exec` is deprecated by
Apple but works on current macOS; `Available` runs a probe under a
deny-by-default profile, and if it ever stops working, isolation is refused
rather than downgraded. Its containment tests (`seatbelt_darwin_test.go`) run
only on a Mac: `go test ./internal/execution -run Seatbelt -v`.

Other platforms register no isolated backend, so isolation requests there still
fail.

## Future backends

Another backend must establish the same filesystem, network, process, and
resource restrictions before running untrusted code, contain descendants, and
preserve the same workspace view. The trusted controller keeps LLM credentials
and model transport outside the worker. Model transport is a separate data
policy.

`Scope.Workspace` stays the canonical host path, because file tools read and
write it directly. A backend that presents a different path to the process must
map that exact tree; a copy would reintroduce path validation and host-edit
conflict detection. The AppContainer backend grants access to the tree in place
and maps a drive letter onto it, so commands and file tools see the same files;
`Scope.CommandWorkspace` names the path commands see.

## Validation

Regression tests cover denied launches, missing scopes, workspace escapes,
unsupported isolation, structured argv, unified shell semantics, cancellation,
scope/manager shutdown, background ownership, and bounded output. Backend
selection is covered with a recording backend: refusing unknown and unisolated
backends, refusing an unavailable one without falling back, releasing a backend
scope exactly once, inheriting backend and trusted-user standing through derived
scratch scopes, and confirming the backend receives no launch that admission
should have stopped.

The AppContainer backend has adversarial containment tests: reading a secret
and writing outside the workspace, connecting to a loopback listener, and a
descendant outliving its command. Each pairs the sandboxed run with the same
command on the local backend, which must succeed, and all three fail when the
container attribute is removed. PowerShell and git (init, commit, and the
`stash create` checkpoints use) run in a workspace under the user profile, and
fail when launched at the host path instead of the drive. Further tests cover
the drive's lifetime across scopes, exit codes, stream separation, timeout and
stop, a Go build on the auto-selected toolchain, grant revocation, and a full
sandboxed run in which the model cannot read a secret it can read unsandboxed.

An architecture test rejects every production process constructor, from
`exec.Command` to `windows.CreateProcess`, outside the execution backends and
the audited folder picker. One further exception is documented rather than
detected: `internal/elevate` relaunches fastllm itself through a direct
`ShellExecuteEx` call, only on a user's `-revoke-sandbox`, to remove an
administrator grant.
