package harness

import (
	"bufio"
	"strings"
	"testing"
)

// The controller is only the prompt; outside agent mode it approves nothing,
// because every other mode is decided by the monitor without asking.
func TestPermissionControllerOnlyAsksInAgentMode(t *testing.T) {
	for _, mode := range []PermissionMode{PermissionPlan, PermissionEdit, PermissionFull, "", "bogus"} {
		controller := NewPermissionController(mode, bufio.NewScanner(strings.NewReader("y\n")))
		if mode == "" {
			controller.Mode = ""
		}
		if controller.Authorize(toolConsent("run_command", "command=go test")) {
			t.Fatalf("controller in mode %q approved without the monitor", mode)
		}
	}
}

func TestPermissionControllerAskAndSessionGrant(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("a\n"))
	controller := NewPermissionController(PermissionAgent, scanner)
	if !controller.Authorize(toolConsent("write_file", "path=x")) {
		t.Fatal("session grant was rejected")
	}
	if !controller.Authorize(toolConsent("write_file", "path=y")) {
		t.Fatal("remembered session grant was not applied")
	}
	if controller.Authorize(toolConsent("run_command", "command=bad")) {
		t.Fatal("EOF should deny an ungranted permission")
	}
}

func TestParsePermissionMode(t *testing.T) {
	cases := map[string]PermissionMode{
		"plan": PermissionPlan, "read-only": PermissionPlan, "readonly": PermissionPlan,
		"agent": PermissionAgent, "ask": PermissionAgent, " Agent ": PermissionAgent,
		"edit": PermissionEdit, "edit-only": PermissionEdit,
		"full": PermissionFull, "full-access": PermissionFull, "auto": PermissionFull,
	}
	for value, want := range cases {
		got, err := ParsePermissionMode(value)
		if err != nil || got != want {
			t.Fatalf("ParsePermissionMode(%q) = %q, %v; want %q", value, got, err, want)
		}
	}
	for _, value := range []string{"", "unsafe", "yolo", "root"} {
		if _, err := ParsePermissionMode(value); err == nil {
			t.Fatalf("ParsePermissionMode(%q) accepted an invalid mode", value)
		}
	}
}

func TestNormalizeModeFailsClosed(t *testing.T) {
	for _, value := range []PermissionMode{"", "unsafe", "FULL ACCESS", "auto-approve"} {
		if got := NormalizeMode(value); got != PermissionPlan {
			t.Fatalf("NormalizeMode(%q) = %q, want plan", value, got)
		}
	}
	if NormalizeMode("auto") != PermissionFull || NormalizeMode("ask") != PermissionAgent {
		t.Fatal("legacy names did not map to their new modes")
	}
}

func TestPermissionModeCycle(t *testing.T) {
	want := []PermissionMode{PermissionAgent, PermissionEdit, PermissionFull, PermissionPlan}
	mode := PermissionPlan
	for _, next := range want {
		mode = mode.Next()
		if mode != next {
			t.Fatalf("Next() = %q, want %q", mode, next)
		}
	}
	if PermissionMode("garbage").Next() != PermissionAgent {
		t.Fatal("an unknown mode should cycle as plan")
	}
}

func TestInteractiveToolsPlanOffersNothingMutating(t *testing.T) {
	for _, tool := range interactiveTools(true, PermissionPlan, true) {
		switch toolClasses[tool.Function.Name] {
		case classRead, classInspect, classPlan:
		default:
			t.Fatalf("plan tools include %q", tool.Function.Name)
		}
	}
}

func TestFusedMutationRequiresIndependentCommandApproval(t *testing.T) {
	controller := NewPermissionController(PermissionAgent, bufio.NewScanner(strings.NewReader("y\nn\n")))
	if !controller.Authorize(toolConsent("edit_file", "path=main.go")) {
		t.Fatal("mutation approval was rejected")
	}
	if controller.Authorize(toolConsent("run_command", "command=go test ./...")) {
		t.Fatal("follow-up command should require and respect its own denial")
	}
}

func TestPermissionControllerClearGrants(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("a\n"))
	controller := NewPermissionController(PermissionAgent, scanner)
	if !controller.Authorize(toolConsent("write_file", "path=x")) {
		t.Fatal("session grant was rejected")
	}
	if !controller.Authorize(toolConsent("write_file", "path=y")) {
		t.Fatal("session grant should be cached")
	}
	controller.ClearGrants()
	if controller.Authorize(toolConsent("write_file", "path=z")) {
		t.Fatal("grant should be cleared, but write_file was allowed without input")
	}
}

func TestCapabilityGrantLifecycle(t *testing.T) {
	controller := NewPermissionController(PermissionAgent, nil)
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
	controller := NewPermissionController(PermissionAgent, nil)
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
	controller := NewPermissionController(PermissionAgent, scanner)
	if !controller.Authorize(toolConsent("write_file", "path=a.txt")) {
		t.Fatal("initial grant failed")
	}
	if !controller.Authorize(toolConsent("write_file", "path=b.txt")) {
		t.Fatal("cached grant failed")
	}

	active := controller.ActiveGrants()
	if len(active) != 1 {
		t.Fatalf("expected 1 active grant, got %d", len(active))
	}

	if controller.RevokeGrant(active[0].ID) != 1 {
		t.Fatal("expected grant to be revoked")
	}

	if !controller.Authorize(toolConsent("write_file", "path=c.txt")) {
		t.Fatal("subsequent authorization after revocation should prompt and succeed with 'y'")
	}
	if controller.Authorize(toolConsent("write_file", "path=d.txt")) {
		t.Fatal("expected EOF to deny once single-use permission expired")
	}
}

// toolConsent asks for a tool-wide grant, which is what these controller
// mechanics tests exercise; scoped grants are tested in grants_test.go.
func toolConsent(tool, summary string) ConsentRequest {
	return ConsentRequest{Tool: tool, Summary: summary, Scope: GrantScope{Tool: tool, Kind: scopeTool}}
}
