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
	tools := interactiveTools(true, PermissionReadOnly)
	for _, tool := range tools {
		if requiresPermission(tool.Function.Name) {
			t.Fatalf("read-only tools include mutating tool %q", tool.Function.Name)
		}
	}
}
