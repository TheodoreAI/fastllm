package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// A project's .fastllm/config.json comes with the repository, so it is
// written by whoever wrote the repository. Used blindly, it can point an
// endpoint at the author's server and send it the whole conversation, and
// with an api_key_file of ~/.fastllm/keys/<provider> it can send the user's
// own API key there too. A project config is therefore used only after the
// user trusts it, by path and exact content: any edit makes it untrusted
// again. Until then the global config applies.

// TrustEnv, set to 1, trusts every project config. It exists for CI and
// scripted runs that own their repository; it is never the default.
const TrustEnv = "FASTLLM_TRUST_PROJECT_CONFIG"

// ProjectConfigReview describes a project config for the user to judge.
type ProjectConfigReview struct {
	Path      string
	Trusted   bool
	Changed   bool // it was trusted, but its contents have changed since
	ParseErr  error
	Endpoints []ModelEndpoint
}

// projectConfigPaths are the workspace-local candidates, in priority order.
func projectConfigPaths(workingDir string) []string {
	if workingDir == "" {
		return nil
	}
	return []string{
		filepath.Join(workingDir, ".fastllm", "config.json"),
		filepath.Join(workingDir, ".fastllm", "config", "settings.json"),
	}
}

// ReviewProjectConfig reports the project config in workingDir, or nil when
// there is none.
func ReviewProjectConfig(workingDir string) *ProjectConfigReview {
	for _, path := range projectConfigPaths(workingDir) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		review := &ProjectConfigReview{Path: path}
		review.Trusted, review.Changed = trustState(path, data)
		var s Settings
		if err := json.Unmarshal(data, &s); err != nil {
			review.ParseErr = err
		} else {
			review.Endpoints = s.Models
		}
		return review
	}
	return nil
}

// TrustProjectConfig records the project config in workingDir, as it is now,
// as trusted.
func TrustProjectConfig(workingDir string) (string, error) {
	review := ReviewProjectConfig(workingDir)
	if review == nil {
		return "", errors.New("this workspace has no .fastllm config")
	}
	data, err := os.ReadFile(review.Path)
	if err != nil {
		return "", err
	}
	store, err := loadTrustStore()
	if err != nil {
		return "", err
	}
	store[trustKey(review.Path)] = digest(data)
	return review.Path, saveTrustStore(store)
}

// UntrustProjectConfig forgets any trust recorded for workingDir's config.
func UntrustProjectConfig(workingDir string) error {
	store, err := loadTrustStore()
	if err != nil {
		return err
	}
	for _, path := range projectConfigPaths(workingDir) {
		delete(store, trustKey(path))
	}
	return saveTrustStore(store)
}

// trustState reports whether path's current contents are trusted, and
// whether a different version of it was.
func trustState(path string, data []byte) (trusted, changed bool) {
	if os.Getenv(TrustEnv) == "1" {
		return true, false
	}
	store, err := loadTrustStore()
	if err != nil {
		return false, false
	}
	recorded, ok := store[trustKey(path)]
	if !ok {
		return false, false
	}
	if recorded == digest(data) {
		return true, false
	}
	return false, true
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// trustKey names a config file independently of how it was reached: through
// a symlink, a relative path, or (on Windows) a different letter case.
func trustKey(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return strings.ToLower(filepath.ToSlash(path))
}

// trustHomeDir locates the trust store; tests relocate it on its own so they
// never write the user's real one.
var trustHomeDir = os.UserHomeDir

func trustStorePath() (string, error) {
	if path := os.Getenv("FASTLLM_TRUST_STORE"); path != "" {
		return path, nil
	}
	home, err := trustHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".fastllm", "trusted-configs.json"), nil
}

func loadTrustStore() (map[string]string, error) {
	path, err := trustStorePath()
	if err != nil {
		return nil, err
	}
	store := map[string]string{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	return store, nil
}

func saveTrustStore(store map[string]string) error {
	path, err := trustStorePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o600)
}
