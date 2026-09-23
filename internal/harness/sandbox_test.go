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
	backend, available := execution.IsolatedBackend()
	for attempt := 0; attempt < 2; attempt++ { // registering twice must be harmless
		out, err := sandboxOptions(m, execution.Options{Workspace: t.TempDir()}, true)
		if !available {
			if err == nil {
				t.Fatal("sandbox on a platform without an isolated backend did not fail")
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
		maxTurns: 20, commandTimeout: time.Minute, allowCommands: true, permissionMode: PermissionAsk,
	}
	if err := m.setRuntimeValue("sandbox", "maybe"); err == nil {
		t.Fatal("invalid sandbox value accepted")
	}

	_, available := execution.IsolatedBackend()
	err := m.setRuntimeValue("sandbox", "on")
	if !available {
		if err == nil || m.sandbox {
			t.Fatalf("sandbox turned on without an isolated backend: err=%v on=%v", err, m.sandbox)
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
