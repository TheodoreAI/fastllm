# Execution boundary

FastLLM routes model commands and workspace-triggered subprocesses through a
trusted broker in `internal/execution`. This is an execution boundary, **not an
OS sandbox**. The first backend runs locally; an isolated backend is future work.
Requesting isolation must fail until a backend can enforce it. Never silently
fall back to local execution.

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

File tools and commands must use the same workspace. The local backend exposes
that canonical workspace to the existing file adapter. A future worker must
provide a workspace adapter as well as process execution; copying changes back
must validate paths and detect conflicting host edits.

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

## Local backend and future isolation

Local execution provides policy admission, canonical working-directory checks,
bounded output, time/process limits, environment filtering, and process cleanup.
It cannot prevent an admitted command from opening files outside its workspace,
using the network, accessing host services, or defeating Unix process-group
cleanup. Environment filtering is not credential isolation.

A future backend must establish OS filesystem, network, process, and resource
restrictions before running untrusted code, contain descendants, and preserve
the same workspace view. The trusted controller keeps LLM credentials and model
transport outside the worker. Model transport is a separate data policy.

## Validation

Regression tests cover denied launches, missing scopes, workspace escapes,
unsupported isolation, structured argv, unified shell semantics, cancellation,
scope/manager shutdown, background ownership, and bounded output. An architecture
test rejects production subprocess constructors outside execution backends and
the audited folder picker. Future isolation needs adversarial containment tests
before it can be advertised as a security boundary.
