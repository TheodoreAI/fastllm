package config

import (
	"path/filepath"
	"runtime"
	"testing"
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
