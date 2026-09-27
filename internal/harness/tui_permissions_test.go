package harness

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
)

func permissionTestModel(t *testing.T) *teaModel {
	t.Helper()
	return &teaModel{
		workingDir: t.TempDir(), permissionMode: PermissionAgent,
		input: textarea.New(), viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(6)),
		ready: true, width: 80,
	}
}

func TestTeaPermissionCommandsAreLocalAndRevocable(t *testing.T) {
	for _, revoke := range []string{"/permissions revoke grant-1", "/permissions revoke write_file", "/permissions clear", "/permission reset"} {
		t.Run(revoke, func(t *testing.T) {
			m := permissionTestModel(t)
			reply := make(chan permissionDecision, 1)
			m.pendingPermission = &teaPermissionRequestMsg{ToolName: "write_file", Reply: reply}
			m.resolvePermission(true, true)
			if !((<-reply).Allow) {
				t.Fatal("session approval was denied")
			}
			if cmd := m.handleAgentSubmit("/permissions list"); cmd != nil || m.isExecuting {
				t.Fatal("listing grants started an agent turn")
			}
			for _, want := range []string{"grant-1", "write_file", m.workingDir} {
				if !strings.Contains(m.historyText.String(), want) {
					t.Fatalf("listing lacks %q", want)
				}
			}
			m.Update(teaPermissionRequestMsg{ToolName: "write_file", Reply: reply})
			select {
			case decision := <-reply:
				if !decision.Allow {
					t.Fatal("cached grant was denied")
				}
			default:
				t.Fatal("session grant was not used")
			}
			if cmd := m.handleAgentSubmit(revoke); cmd != nil || m.isExecuting {
				t.Fatal("revocation started an agent turn")
			}
			if m.permissionController().HasGrant("write_file") {
				t.Fatal("grant survived revocation")
			}
			m.Update(teaPermissionRequestMsg{ToolName: "write_file", Reply: reply})
			if m.pendingPermission == nil {
				t.Fatal("revoked grant did not prompt again")
			}
			select {
			case <-reply:
				t.Fatal("revoked grant automatically authorized a write")
			default:
			}
		})
	}
}

func TestTeaWorkspaceChangesRevokeGrants(t *testing.T) {
	for _, prefix := range []string{"/dir ", "!cd "} {
		t.Run(prefix, func(t *testing.T) {
			m := permissionTestModel(t)
			grant := m.permissionController().Grant("write_file")
			destination := t.TempDir()
			argument := destination
			if prefix == "!cd " {
				argument = `"` + destination + `"`
			}
			m.handleAgentSubmit(prefix + argument)
			if m.workingDir != destination {
				t.Fatalf("directory did not change: %s", m.workingDir)
			}
			if !grant.Revoked || m.permissionController().HasGrant("write_file") {
				t.Fatal("workspace change retained authority")
			}
			if m.permissionController().Workspace != destination {
				t.Fatal("controller workspace was not updated")
			}
		})
	}
}

func TestTeaSessionTransitionsRevokeGrants(t *testing.T) {
	for _, transition := range []string{"new", "resume", "mode"} {
		t.Run(transition, func(t *testing.T) {
			m := permissionTestModel(t)
			grant := m.permissionController().Grant("run_command")
			var err error
			switch transition {
			case "new":
				err = m.startNewSession()
			case "resume":
				err = m.loadSession(&InteractiveSession{WorkingDir: m.workingDir, Runtime: InteractiveRuntime{PermissionMode: PermissionAgent}})
			case "mode":
				err = m.setRuntimeValue("permissions", "auto")
			}
			if err != nil {
				t.Fatal(err)
			}
			if !grant.Revoked || len(m.permissionController().ActiveGrants()) != 0 {
				t.Fatal("session transition retained grants")
			}
		})
	}
}

func TestTeaRejectsPermissionRequestsFromPreviousWorkspace(t *testing.T) {
	m := permissionTestModel(t)
	m.permissionController().Grant("write_file")
	reply := make(chan permissionDecision, 1)
	m.Update(teaPermissionRequestMsg{ToolName: "write_file", Workspace: t.TempDir(), Reply: reply})
	select {
	case decision := <-reply:
		if decision.Allow {
			t.Fatal("current workspace grant authorized a stale request")
		}
	default:
		t.Fatal("stale request was not rejected")
	}
	if m.pendingPermission != nil {
		t.Fatal("stale request opened an approval prompt")
	}
}
