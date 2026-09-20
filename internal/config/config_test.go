package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unicode/utf16"
)

func TestLoadAndSaveSettings(t *testing.T) {
	tempDir := t.TempDir()

	cfgPath := filepath.Join(tempDir, ".fastllm", "config.json")
	s := DefaultSettings()
	s.DefaultModel = "custom-model"
	s.AddOrUpdateModel(ModelEndpoint{
		ID:   "custom-model",
		Name: "Custom Model",
		URL:  "http://localhost:9999/v1",
	})

	if _, err := SaveSettings(cfgPath, s); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}

	loaded, loadedPath, err := LoadSettings(tempDir)
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if loadedPath != cfgPath {
		t.Errorf("expected loadedPath %q, got %q", cfgPath, loadedPath)
	}
	if loaded.DefaultModel != "custom-model" {
		t.Errorf("expected default model 'custom-model', got %q", loaded.DefaultModel)
	}

	m := loaded.FindModel("custom-model")
	if m == nil || m.URL != "http://localhost:9999/v1" {
		t.Errorf("expected model endpoint with URL 'http://localhost:9999/v1', got %+v", m)
	}
}

func TestCandidateConfigPaths(t *testing.T) {
	// Use a path that is absolute on the platform running the test; "C:\project"
	// is just an ordinary relative name on Linux and macOS.
	workingDir := filepath.Join(string(filepath.Separator), "project")
	if runtime.GOOS == "windows" {
		workingDir = `C:\project`
	}
	paths := CandidateConfigPaths(workingDir)
	if len(paths) < 2 {
		t.Fatalf("expected at least 2 paths, got %d", len(paths))
	}
	if !filepath.IsAbs(paths[0]) {
		t.Errorf("expected absolute path, got %q", paths[0])
	}
}

// A key file written with PowerShell's ">" is UTF-16 with a BOM. TrimSpace
// cannot clean that up, so without decoding the key goes out mangled and the
// provider rejects it as invalid — which looks like a bad key, not an encoding
// bug. Every encoding below must yield the identical key.
func TestResolveAPIKeyHandlesWindowsEncodings(t *testing.T) {
	const want = "AQ.Ab8RN6K-example-token-value"

	encodings := map[string][]byte{
		"utf-8":             []byte(want),
		"utf-8 with BOM":    append([]byte{0xEF, 0xBB, 0xBF}, []byte(want)...),
		"utf-8 trailing nl": []byte(want + "\r\n"),
		"utf-16le with BOM": utf16le(want, true),
		"utf-16le no BOM":   utf16le(want, false),
		"utf-16be with BOM": utf16be(want),
	}

	for name, data := range encodings {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "key")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			endpoint := &ModelEndpoint{ID: "m", APIKeyFile: path}
			if got := endpoint.ResolveAPIKey(); got != want {
				t.Fatalf("key = %q (len %d); want %q", got, len(got), want)
			}
		})
	}
}

func utf16le(s string, bom bool) []byte {
	var out []byte
	if bom {
		out = append(out, 0xFF, 0xFE)
	}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

func utf16be(s string) []byte {
	out := []byte{0xFE, 0xFF}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u>>8), byte(u))
	}
	return out
}
