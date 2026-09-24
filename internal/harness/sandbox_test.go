package harness

import (
	"context"
	"errors"
	"fastllm/internal/execution"
	"strings"
	"testing"
	"time"
)

func TestSandboxOptionsLeaveAnUnsandboxedRunAlone(t *testing.T) {
	m := execution.NewManager()
	defer m.Close(context.Background())
	in := execution.Options{Workspace: t.TempDir(), Policy: execution.LocalPolicy()}
	out, err := sandboxOptions(m, in, false)
	if err != nil {
		t.Fatalf("sandboxOptions: %v", err)
	}
	if out.Backend != "" || out.RequireIsolation {
		t.Fatalf("sandbox off changed the options: %+v", out)
	}
}

func TestSandboxOptionsRequireIsolationOrFail(t *testing.T) {
	m := execution.NewManager()
	defer m.Close(context.Background())
	backend, _ := execution.IsolatedBackend()
	ready := sandboxReady()
	for attempt := 0; attempt < 2; attempt++ { // registering twice must be harmless
		out, err := sandboxOptions(m, execution.Options{Workspace: t.TempDir()}, true)
		if ready != nil {
			if err == nil {
				t.Fatalf("sandbox not ready (%v) but options were granted: %+v", ready, out)
			}
			return
		}
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if out.Backend != backend.Name() || !out.RequireIsolation {
			t.Fatalf("options = %+v, want backend %q with isolation required", out, backend.Name())
		}
	}
}

func TestTeaSandboxSettingValidatesAndPersists(t *testing.T) {
	store := &SessionStore{Dir: t.TempDir()}
	m := &teaModel{
		workingDir: t.TempDir(), modelName: "test-model", sessionStore: store,
		maxTurns: 20, commandTimeout: time.Minute, allowCommands: true, permissionMode: PermissionAgent,
	}
	if err := m.setRuntimeValue("sandbox", "maybe"); err == nil {
		t.Fatal("invalid sandbox value accepted")
	}

	err := m.setRuntimeValue("sandbox", "on")
	if ready := sandboxReady(); ready != nil {
		// No backend, or setup not done: turning it on must be refused.
		if err == nil || m.sandbox {
			t.Fatalf("sandbox turned on while not ready (%v): err=%v on=%v", ready, err, m.sandbox)
		}
		return
	}
	if err != nil || !m.sandbox || !m.runtimeSettings().Sandbox {
		t.Fatalf("sandbox on: err=%v on=%v", err, m.sandbox)
	}

	m.activeSession = store.New(m.workingDir, m.modelName, m.runtimeSettings())
	if err := m.saveSession(); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(m.activeSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Runtime.Sandbox {
		t.Fatal("saved session lost the sandbox setting")
	}

	if err := m.setRuntimeValue("sandbox", "off"); err != nil || m.sandbox {
		t.Fatalf("sandbox off: err=%v on=%v", err, m.sandbox)
	}
}

func TestRuntimeCardShowsSandbox(t *testing.T) {
	for want, on := range map[string]bool{"on": true, "off": false} {
		card := FormatRuntimeCard(InteractiveRuntime{MaxTurns: 1, CommandTimeout: time.Second, Sandbox: on}, "s")
		line := ""
		for _, l := range strings.Split(card, "\n") {
			if strings.Contains(l, "sandbox") {
				line = l
			}
		}
		if !strings.Contains(line, want) {
			t.Errorf("sandbox %v: card line %q does not say %q", on, line, want)
		}
	}
}

func TestSandboxUnavailableErrorNamesThePlatform(t *testing.T) {
	if err := errSandboxUnavailable(); err == nil || errors.Unwrap(err) != nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("error = %v", err)
	}
}

func TestSandboxNoticeNamesThePlatformsSandbox(t *testing.T) {
	if !strings.Contains(sandboxNoticeFor("darwin"), "Seatbelt") || strings.Contains(sandboxNoticeFor("darwin"), "drive letter") {
		t.Fatalf("macOS notice: %q", sandboxNoticeFor("darwin"))
	}
	if !strings.Contains(sandboxNoticeFor("windows"), "drive letter") {
		t.Fatal("the Windows notice lost its drive-letter explanation")
	}
}
