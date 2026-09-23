package harness

import (
	"errors"
	"fastllm/internal/elevate"
	"fastllm/internal/execution"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// sandboxFirstUseNotice tells the user what the sandbox does and why the
// first sandboxed command may pause.
const sandboxFirstUseNotice = "Sandbox on: commands run isolated from the rest of this machine, with no network, " +
	"from a drive letter mapped to the workspace. The first command grants the sandbox access to this workspace " +
	"and the Go module cache, which can take a minute once."

func errSandboxUnavailable() error {
	return fmt.Errorf("sandbox is not available on %s: no isolated execution backend exists for this platform", runtime.GOOS)
}

// sandboxReady reports why a sandbox cannot be turned on, if anything.
func sandboxReady() error {
	if _, ok := execution.IsolatedBackend(); !ok {
		return errSandboxUnavailable()
	}
	return nil
}

// sandboxOptions narrows opts to the platform's isolated backend when the
// session asked for a sandbox. It never widens a request: with no isolated
// backend here, the run fails rather than running commands unsandboxed.
func sandboxOptions(owner *execution.Manager, opts execution.Options, sandbox bool) (execution.Options, error) {
	if !sandbox {
		return opts, nil
	}
	if err := sandboxReady(); err != nil {
		return opts, err
	}
	backend, _ := execution.IsolatedBackend()
	// A concurrent opener may register first; either way it is registered.
	if err := owner.Register(backend); err != nil && !owner.HasBackend(backend.Name()) {
		return opts, err
	}
	opts.Backend = backend.Name()
	opts.RequireIsolation = true
	return opts, nil
}

// sandboxPromptNote tells the model how commands see the workspace when they
// run isolated, so it uses paths that resolve there.
func sandboxPromptNote(scope *execution.Scope) string {
	if !scope.Isolated() {
		return ""
	}
	note := "\n\nCommands run in an isolated sandbox with no network access and no access to files outside the workspace."
	if view := scope.CommandWorkspace(); !strings.EqualFold(filepath.Clean(view), filepath.Clean(scope.Workspace())) {
		note += fmt.Sprintf(" Inside commands the workspace is %s, not %s; use paths relative to the workspace in commands.", view, scope.Workspace())
	}
	return note
}

func sandboxLabel(sandbox bool) string {
	if sandbox {
		return "on"
	}
	return "off"
}

// Flags for the elevated half of revoke. The unelevated process names the
// identity, so the revoke reaches the right one even when another
// administrator account approves the prompt.
const (
	revokeSetupFlag = "sandbox-revoke-setup"
	reportFlag      = "sandbox-report"
)

// runSandboxRevoke undoes every grant the sandbox received and deletes its
// identity. A profiles-directory grant from an earlier one-time setup goes
// first, with administrator approval, so it never outlives the identity it
// names.
func runSandboxRevoke() int {
	present, err := execution.IsolationSetupPresent()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Checking sandbox grants failed: %v\n", err)
		return 1
	}
	if present {
		if err := elevatedStep(revokeSetupFlag, execution.RevokeIsolationSetup); err != nil {
			fmt.Fprintf(os.Stderr, "Revoking the sandbox's administrator grant failed: %v\n", err)
			return 1
		}
	}
	if err := execution.RevokeIsolatedBackend(); err != nil {
		fmt.Fprintf(os.Stderr, "Revoking the sandbox failed: %v\n", err)
		return 1
	}
	fmt.Println("Sandbox revoked: its permissions are removed and its identity is deleted.")
	return 0
}

// elevatedStep applies step to the sandbox identity, directly when already
// elevated, otherwise in an elevated copy of this program that reports its
// error through a file.
func elevatedStep(flag string, step func(string) error) error {
	sid, err := execution.IsolationIdentity()
	if err != nil {
		return err
	}
	if elevate.Elevated() {
		return step(sid)
	}
	report, err := os.CreateTemp("", "fastllm-sandbox-*.txt")
	if err != nil {
		return err
	}
	reportPath := report.Name()
	report.Close()
	defer os.Remove(reportPath)

	code, err := elevate.Run([]string{"-" + flag + "=" + sid, "-" + reportFlag + "=" + reportPath})
	if err != nil {
		return err
	}
	message, _ := os.ReadFile(reportPath)
	if text := strings.TrimSpace(string(message)); text != "" {
		return errors.New(text)
	}
	if code != 0 {
		return fmt.Errorf("elevated step exited with code %d", code)
	}
	return nil
}

// runElevatedSetupStep is the elevated half of elevatedStep: it runs step and
// writes any error to the report file the unelevated process reads.
func runElevatedSetupStep(sid, reportPath string, step func(string) error) int {
	err := step(sid)
	// An elevated process writes only a report file elevatedStep created, never
	// an arbitrary path from its command line.
	name := filepath.Base(reportPath)
	if strings.HasPrefix(name, "fastllm-sandbox-") && strings.HasSuffix(name, ".txt") {
		text := ""
		if err != nil {
			text = err.Error()
		}
		_ = os.WriteFile(filepath.Clean(reportPath), []byte(text), 0o600)
	}
	if err != nil {
		return 1
	}
	return 0
}
