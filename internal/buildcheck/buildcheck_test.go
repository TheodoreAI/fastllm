package buildcheck

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// newTestModule creates a minimal, throwaway Go module on disk so Run can
// be exercised against a real "go build" without touching the real
// fastllm repo.
func newTestModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module testmod\n\ngo 1.25.0\n")
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunPassesOnValidProject(t *testing.T) {
	dir := newTestModule(t)
	result, err := Run(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !result.Passed {
		t.Fatalf("expected build to pass, got output: %s", result.Output)
	}
}

func TestRunFailsOnBrokenOverlay(t *testing.T) {
	dir := newTestModule(t)
	result, err := Run(context.Background(), dir, []Overlay{
		{Path: "main.go", Content: "package main\n\nfunc main() { this is not valid go }\n"},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Passed {
		t.Fatal("expected build to fail on invalid syntax")
	}
	if result.Output == "" {
		t.Fatal("expected non-empty output describing the failure")
	}
}

func TestRunOverlayFixesBrokenSource(t *testing.T) {
	dir := newTestModule(t)
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() { this is broken }\n")

	result, err := Run(context.Background(), dir, []Overlay{
		{Path: "main.go", Content: "package main\n\nfunc main() {}\n"},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !result.Passed {
		t.Fatalf("expected overlay to fix the build, got output: %s", result.Output)
	}

	// The real file on disk must be untouched — overlays only ever apply
	// to the scratch copy.
	onDisk, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != "package main\n\nfunc main() { this is broken }\n" {
		t.Fatal("Run must not modify the real project directory")
	}
}

func TestRunNewFileOverlay(t *testing.T) {
	dir := newTestModule(t)
	result, err := Run(context.Background(), dir, []Overlay{
		{Path: "extra/helper.go", Content: "package extra\n\nfunc Helper() int { return 1 }\n"},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !result.Passed {
		t.Fatalf("expected build to pass with a new, valid file, got output: %s", result.Output)
	}
}

func TestRunRejectsMissingGoModule(t *testing.T) {
	dir := t.TempDir() // no go.mod
	_, err := Run(context.Background(), dir, nil)
	if err != ErrNoGoModule {
		t.Fatalf("expected ErrNoGoModule, got %v", err)
	}
}

func TestRunRejectsOverlayPathEscape(t *testing.T) {
	dir := newTestModule(t)
	_, err := Run(context.Background(), dir, []Overlay{
		{Path: "../escape.go", Content: "package main\n"},
	})
	if err == nil {
		t.Fatal("expected an error for an overlay path escaping the project root")
	}
}

func TestRunTestsPassesOnValidProject(t *testing.T) {
	dir := newTestModule(t)
	mustWrite(t, filepath.Join(dir, "main_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n")

	result, err := RunTests(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("RunTests returned error: %v", err)
	}
	if !result.Passed {
		t.Fatalf("expected tests to pass, got output: %s", result.Output)
	}
}

func TestRunTestsFailsOnFailingTest(t *testing.T) {
	dir := newTestModule(t)
	mustWrite(t, filepath.Join(dir, "main_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"boom\") }\n")

	result, err := RunTests(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("RunTests returned error: %v", err)
	}
	if result.Passed {
		t.Fatal("expected tests to fail")
	}
	if result.Output == "" {
		t.Fatal("expected non-empty output describing the failure")
	}
}

func TestRunTestsOverlayFixesFailingTest(t *testing.T) {
	dir := newTestModule(t)
	mustWrite(t, filepath.Join(dir, "add.go"), "package main\n\nfunc add(a, b int) int { return a - b }\n")
	mustWrite(t, filepath.Join(dir, "add_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif add(2, 3) != 5 {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n")

	result, err := RunTests(context.Background(), dir, []Overlay{
		{Path: "add.go", Content: "package main\n\nfunc add(a, b int) int { return a + b }\n"},
	})
	if err != nil {
		t.Fatalf("RunTests returned error: %v", err)
	}
	if !result.Passed {
		t.Fatalf("expected overlay to fix the failing test, got output: %s", result.Output)
	}

	onDisk, err := os.ReadFile(filepath.Join(dir, "add.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != "package main\n\nfunc add(a, b int) int { return a - b }\n" {
		t.Fatal("RunTests must not modify the real project directory")
	}
}

func TestRunTestsRejectsMissingGoModule(t *testing.T) {
	dir := t.TempDir() // no go.mod
	_, err := RunTests(context.Background(), dir, nil)
	if err != ErrNoGoModule {
		t.Fatalf("expected ErrNoGoModule, got %v", err)
	}
}

func TestRunTestsInPlaceRunsAgainstRealRoot(t *testing.T) {
	dir := newTestModule(t)
	mustWrite(t, filepath.Join(dir, "main_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n")

	result, err := RunTestsInPlace(context.Background(), dir)
	if err != nil {
		t.Fatalf("RunTestsInPlace returned error: %v", err)
	}
	if !result.Passed {
		t.Fatalf("expected tests to pass, got output: %s", result.Output)
	}
}

func TestRunTestsInPlaceRejectsMissingGoModule(t *testing.T) {
	dir := t.TempDir() // no go.mod
	_, err := RunTestsInPlace(context.Background(), dir)
	if err != ErrNoGoModule {
		t.Fatalf("expected ErrNoGoModule, got %v", err)
	}
}

func TestRunSkipsGitDirectory(t *testing.T) {
	dir := newTestModule(t)
	// A .git directory with a file that isn't valid to copy as a normal
	// tracked file shouldn't break the copy or the build.
	mustWrite(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")

	result, err := Run(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !result.Passed {
		t.Fatalf("expected build to pass, got output: %s", result.Output)
	}
}
