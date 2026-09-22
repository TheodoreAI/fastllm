package harness

import (
	"bufio"
	"strings"
	"testing"
)

func TestPermissionControllerModes(t *testing.T) {
	readOnly := NewPermissionController(PermissionReadOnly, nil)
	if readOnly.Authorize("write_file", "path=x") {
		t.Fatal("read-only mode allowed a write")
	}
	if !readOnly.Authorize("read_file", "path=x") {
		t.Fatal("read-only mode rejected a read")
	}

	auto := NewPermissionController(PermissionAuto, nil)
	if !auto.Authorize("run_command", "command=go test") {
		t.Fatal("auto mode rejected a command")
	}
}

func TestPermissionControllerAskAndSessionGrant(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("a\n"))
	controller := NewPermissionController(PermissionAsk, scanner)
	if !controller.Authorize("write_file", "path=x") {
		t.Fatal("session grant was rejected")
	}
	if !controller.Authorize("write_file", "path=y") {
		t.Fatal("remembered session grant was not applied")
	}
	if controller.Authorize("run_command", "command=bad") {
		t.Fatal("EOF should deny an ungranted permission")
	}
}

func TestParsePermissionMode(t *testing.T) {
	for _, value := range []string{"ask", "read-only", "auto"} {
		if _, err := ParsePermissionMode(value); err != nil {
			t.Fatalf("ParsePermissionMode(%q): %v", value, err)
		}
	}
	if _, err := ParsePermissionMode("unsafe"); err == nil {
		t.Fatal("expected invalid mode to fail")
	}
}

func TestInteractiveToolsReadOnly(t *testing.T) {
	tools := interactiveTools(true, PermissionReadOnly, false)
	for _, tool := range tools {
		if requiresPermission(tool.Function.Name) {
			t.Fatalf("read-only tools include mutating tool %q", tool.Function.Name)
		}
	}
}

func TestFusedMutationRequiresIndependentCommandApproval(t *testing.T) {
	controller := NewPermissionController(PermissionAsk, bufio.NewScanner(strings.NewReader("y\nn\n")))
	if !controller.Authorize("edit_file", "path=main.go") {
		t.Fatal("mutation approval was rejected")
	}
	if controller.Authorize("run_command", "command=go test ./...") {
		t.Fatal("follow-up command should require and respect its own denial")
	}
}

func TestPermissionControllerClearGrants(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("a\n"))
	controller := NewPermissionController(PermissionAsk, scanner)
	if !controller.Authorize("write_file", "path=x") {
		t.Fatal("session grant was rejected")
	}
	if !controller.Authorize("write_file", "path=y") {
		t.Fatal("session grant should be cached")
	}
	controller.ClearGrants()
	if controller.Authorize("write_file", "path=z") {
		t.Fatal("grant should be cleared, but write_file was allowed without input")
	}
}

func TestCapabilityGrantLifecycle(t *testing.T) {
	controller := NewPermissionController(PermissionAsk, nil)
	controller.SetWorkspace("/repo/a")

	grant1 := controller.Grant("write_file")
	if grant1 == nil || grant1.ID != "grant-1" || grant1.Tool != "write_file" {
		t.Fatalf("unexpected grant1: %+v", grant1)
	}
	grant2 := controller.Grant("run_command")
	if grant2 == nil || grant2.ID != "grant-2" || grant2.Tool != "run_command" {
		t.Fatalf("unexpected grant2: %+v", grant2)
	}

	active := controller.ActiveGrants()
	if len(active) != 2 {
		t.Fatalf("expected 2 active grants, got %d", len(active))
	}

	if !controller.HasGrant("write_file") || !controller.HasGrant("run_command") {
		t.Fatal("expected active grants for write_file and run_command")
	}

	// Revoke by ID
	revoked := controller.RevokeGrant("grant-1")
	if revoked != 1 {
		t.Fatalf("expected 1 grant revoked, got %d", revoked)
	}
	if controller.HasGrant("write_file") {
		t.Fatal("write_file should no longer have an active grant")
	}
	if !controller.HasGrant("run_command") {
		t.Fatal("run_command should still have an active grant")
	}

	// Revoke by tool name
	revoked = controller.RevokeGrant("run_command")
	if revoked != 1 {
		t.Fatalf("expected 1 grant revoked by tool name, got %d", revoked)
	}
	if controller.HasGrant("run_command") {
		t.Fatal("run_command should no longer have an active grant")
	}
	if len(controller.ActiveGrants()) != 0 {
		t.Fatalf("expected 0 active grants, got %d", len(controller.ActiveGrants()))
	}
}

func TestPermissionControllerWorkspaceSeparation(t *testing.T) {
	controller := NewPermissionController(PermissionAsk, nil)
	controller.SetWorkspace("/repo/alpha")
	controller.Grant("write_file")

	if !controller.HasGrant("write_file") {
		t.Fatal("expected grant in /repo/alpha")
	}

	// Changing workspace must clear active grants
	controller.SetWorkspace("/repo/beta")
	if controller.HasGrant("write_file") {
		t.Fatal("grants from /repo/alpha must not persist into /repo/beta")
	}
	if len(controller.ActiveGrants()) != 0 {
		t.Fatalf("expected 0 active grants after workspace switch, got %d", len(controller.ActiveGrants()))
	}
}

func TestPermissionControllerRevocationPromptsAgain(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("a\ny\n"))
	controller := NewPermissionController(PermissionAsk, scanner)
	if !controller.Authorize("write_file", "path=a.txt") {
		t.Fatal("initial grant failed")
	}
	if !controller.Authorize("write_file", "path=b.txt") {
		t.Fatal("cached grant failed")
	}

	active := controller.ActiveGrants()
	if len(active) != 1 {
		t.Fatalf("expected 1 active grant, got %d", len(active))
	}

	if controller.RevokeGrant(active[0].ID) != 1 {
		t.Fatal("expected grant to be revoked")
	}

	if !controller.Authorize("write_file", "path=c.txt") {
		t.Fatal("subsequent authorization after revocation should prompt and succeed with 'y'")
	}
	if controller.Authorize("write_file", "path=d.txt") {
		t.Fatal("expected EOF to deny once single-use permission expired")
	}
}
