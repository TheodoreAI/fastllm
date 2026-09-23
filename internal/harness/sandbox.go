package harness

import (
	"fastllm/internal/execution"
	"fmt"
	"os"
	"runtime"
)

// sandboxFirstUseNotice tells the user why the first sandboxed command may
// pause: the sandbox is granted access to the workspace and module cache once.
const sandboxFirstUseNotice = "Sandbox on: commands run isolated from the rest of this machine, with no network. " +
	"The first command grants the sandbox access to this workspace and the Go module cache, which can take a minute once."

func errSandboxUnavailable() error {
	return fmt.Errorf("sandbox is not available on %s: no isolated execution backend exists for this platform", runtime.GOOS)
}

// sandboxOptions narrows opts to the platform's isolated backend when the
// session asked for a sandbox. It never widens a request: with no isolated
// backend here, the run fails rather than running commands unsandboxed.
func sandboxOptions(owner *execution.Manager, opts execution.Options, sandbox bool) (execution.Options, error) {
	if !sandbox {
		return opts, nil
	}
	backend, ok := execution.IsolatedBackend()
	if !ok {
		return opts, errSandboxUnavailable()
	}
	// A concurrent opener may register first; either way it is registered.
	if err := owner.Register(backend); err != nil && !owner.HasBackend(backend.Name()) {
		return opts, err
	}
	opts.Backend = backend.Name()
	opts.RequireIsolation = true
	return opts, nil
}

func sandboxLabel(sandbox bool) string {
	if sandbox {
		return "on"
	}
	return "off"
}

// runSandboxRevoke undoes every grant the sandbox received and deletes its
// identity, for the -revoke-sandbox flag.
func runSandboxRevoke() int {
	if err := execution.RevokeIsolatedBackend(); err != nil {
		fmt.Fprintf(os.Stderr, "Revoking the sandbox failed: %v\n", err)
		return 1
	}
	fmt.Println("Sandbox revoked: its permissions are removed and its identity is deleted.")
	return 0
}
