package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDirectoryChange(t *testing.T) {
	tests := []struct {
		command string
		path    string
		handled bool
	}{
		{command: "cd ..", path: "..", handled: true},
		{command: `cd "folder with spaces"`, path: "folder with spaces", handled: true},
		{command: "Set-Location ../project", path: "../project", handled: true},
		{command: "chdir /d C:\\work", path: `C:\work`, handled: true},
		{command: "cd", path: "", handled: true},
		{command: "pwd", handled: false},
		{command: "echo cd ..", handled: false},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			path, handled := parseDirectoryChange(tt.command)
			if path != tt.path || handled != tt.handled {
				t.Fatalf("parseDirectoryChange(%q) = (%q, %v); want (%q, %v)", tt.command, path, handled, tt.path, tt.handled)
			}
		})
	}
}

func TestResolveInteractiveDirectory(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveInteractiveDirectory(root, "child")
	if err != nil {
		t.Fatalf("resolve child: %v", err)
	}
	if resolved != child {
		t.Fatalf("resolved directory = %q; want %q", resolved, child)
	}

	if _, err := resolveInteractiveDirectory(root, "missing"); err == nil {
		t.Fatal("expected missing directory to fail")
	}
}
