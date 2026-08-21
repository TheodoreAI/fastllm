package lint

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNormalizeSeverity(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"error", "error"},
		{"warning", "warning"},
		{"advice", "warning"},   // oxlint's third severity collapses to warning, not error
		{"", "warning"},         // unrecognized/missing value must not default to "error"
		{"ERROR", "warning"},    // exact match only — no case-insensitive surprise upgrades
		{"critical", "warning"}, // a hypothetical future oxlint severity also falls back safely
	}
	for _, tt := range tests {
		if got := normalizeSeverity(tt.in); got != tt.want {
			t.Errorf("normalizeSeverity(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func oxlintBinName() string {
	if runtime.GOOS == "windows" {
		return "oxlint.cmd"
	}
	return "oxlint"
}

// writeFakeOxlint drops an executable at dir/node_modules/.bin/<name> so
// findOxlint has something real to discover — content doesn't matter for
// findOxlint's own tests, which never execute it.
func writeFakeOxlint(t *testing.T, dir string) string {
	t.Helper()
	binDir := filepath.Join(dir, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, oxlintBinName())
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return binPath
}

func TestFindOxlintInImmediateNodeModules(t *testing.T) {
	dir := t.TempDir()
	wantBin := writeFakeOxlint(t, dir)

	target := filepath.Join(dir, "src", "app.js")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}

	binPath, workDir, err := findOxlint(target)
	if err != nil {
		t.Fatalf("findOxlint returned error: %v", err)
	}
	if binPath != wantBin {
		t.Errorf("binPath = %q, want %q", binPath, wantBin)
	}
	if workDir != dir {
		t.Errorf("workDir = %q, want %q", workDir, dir)
	}
}

func TestFindOxlintWalksUpToParent(t *testing.T) {
	dir := t.TempDir()
	wantBin := writeFakeOxlint(t, dir)

	// oxlint lives at the project root, but the saved file is several
	// directories deeper — findOxlint must walk up past all of them.
	target := filepath.Join(dir, "src", "components", "deep", "Widget.jsx")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}

	binPath, workDir, err := findOxlint(target)
	if err != nil {
		t.Fatalf("findOxlint returned error: %v", err)
	}
	if binPath != wantBin {
		t.Errorf("binPath = %q, want %q", binPath, wantBin)
	}
	if workDir != dir {
		t.Errorf("workDir = %q, want project root %q, not the file's own directory", workDir, dir)
	}
}

func TestFindOxlintStopsAtNearestNodeModules(t *testing.T) {
	dir := t.TempDir()
	// An outer node_modules with oxlint...
	writeFakeOxlint(t, dir)
	// ...and a nested project with its OWN node_modules/oxlint closer to
	// the target file. The nearer one must win, not the outer one.
	nested := filepath.Join(dir, "packages", "app")
	nestedBin := writeFakeOxlint(t, nested)

	target := filepath.Join(nested, "src", "app.js")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}

	binPath, _, err := findOxlint(target)
	if err != nil {
		t.Fatalf("findOxlint returned error: %v", err)
	}
	if binPath != nestedBin {
		t.Errorf("binPath = %q, want the nearer nested install %q", binPath, nestedBin)
	}
}

func TestFindOxlintReturnsErrorWhenNoneFound(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "src", "app.js")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := findOxlint(target); err == nil {
		t.Error("expected an error when no oxlint install exists anywhere above the file")
	}
}

func TestLintSkipsUnsupportedExtensions(t *testing.T) {
	dir := t.TempDir()
	writeFakeOxlint(t, dir) // present, but must never be reached for a skipped extension

	_, err := Lint(context.Background(), filepath.Join(dir, "README.md"))
	if err != ErrNotSupported {
		t.Errorf("got %v, want ErrNotSupported for a non-JS extension", err)
	}
}

func TestLintReturnsErrNotSupportedWithNoOxlintInstall(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "app.js")
	if err := os.WriteFile(target, []byte("const x = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Lint(context.Background(), target)
	if err != ErrNotSupported {
		t.Errorf("got %v, want ErrNotSupported when no oxlint binary can be found", err)
	}
}

// TestLintParsesOxlintJSONOutput exercises Lint end-to-end against a fake
// "oxlint" script instead of a real install — real oxlint's exact JSON
// shape is documented in Lint's parsing struct; this locks in that Lint
// correctly maps it into Diagnostic (including the severity normalization
// and the offset/length extraction from the first label) without needing
// oxlint itself present in CI.
func TestLintParsesOxlintJSONOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake oxlint script is a POSIX shell script")
	}

	dir := t.TempDir()
	binDir := filepath.Join(dir, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeOutput := `{"diagnostics":[
		{"message":"'x' is never used","code":"no-unused-vars","severity":"error","labels":[{"span":{"offset":6,"length":1}}]},
		{"message":"prefer const","code":"prefer-const","severity":"advice","labels":[{"span":{"offset":0,"length":3}}]},
		{"message":"file-level issue","code":"no-empty-file","severity":"error","labels":[]}
	]}`
	script := "#!/bin/sh\ncat <<'EOF'\n" + fakeOutput + "\nEOF\nexit 1\n"
	binPath := filepath.Join(binDir, "oxlint")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(dir, "app.js")
	if err := os.WriteFile(target, []byte("let x = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diagnostics, err := Lint(context.Background(), target)
	if err != nil {
		t.Fatalf("Lint returned error: %v", err)
	}
	if len(diagnostics) != 3 {
		t.Fatalf("got %d diagnostics, want 3", len(diagnostics))
	}
	if diagnostics[0].Severity != "error" {
		t.Errorf("diagnostics[0].Severity = %q, want %q", diagnostics[0].Severity, "error")
	}
	if diagnostics[0].Offset != 6 || diagnostics[0].Length != 1 {
		t.Errorf("diagnostics[0] offset/length = %d/%d, want 6/1", diagnostics[0].Offset, diagnostics[0].Length)
	}
	if diagnostics[1].Severity != "warning" {
		t.Errorf("diagnostics[1].Severity = %q, want %q — \"advice\" must normalize to \"warning\"", diagnostics[1].Severity, "warning")
	}
	if diagnostics[1].Rule != "prefer-const" {
		t.Errorf("diagnostics[1].Rule = %q, want %q", diagnostics[1].Rule, "prefer-const")
	}
	if diagnostics[2].Offset != 0 || diagnostics[2].Length != 0 {
		t.Errorf("diagnostics[2] offset/length = %d/%d, want 0/0 — an empty labels array must not fabricate a span", diagnostics[2].Offset, diagnostics[2].Length)
	}
}
