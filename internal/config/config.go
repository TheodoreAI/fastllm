package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ModelEndpoint configures an LLM model and its inference endpoint.
type ModelEndpoint struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	URL        string                 `json:"url"`
	APIKey     string                 `json:"api_key,omitempty"`
	APIKeyFile string                 `json:"api_key_file,omitempty"`
	Parameters map[string]interface{} `json:"parameters,omitempty"`
}

// ResolveAPIKey returns the endpoint's API key. An inline APIKey wins; otherwise the
// key is read from APIKeyFile. The indirection keeps the secret out of config.json,
// which is otherwise safe to copy between machines or paste into a bug report.
// A missing or unreadable file yields an empty key rather than an error, so an
// endpoint that needs no auth still works when the field is left set.
func (m *ModelEndpoint) ResolveAPIKey() string {
	if key := strings.TrimSpace(m.APIKey); key != "" {
		return key
	}
	path := strings.TrimSpace(m.APIKeyFile)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		path = filepath.Join(home, strings.TrimLeft(strings.TrimPrefix(path, "~"), `/\`))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Settings represents the overall fastllm configuration file structure.
type Settings struct {
	DefaultModel string          `json:"default_model"`
	Models       []ModelEndpoint `json:"models"`
}

// DefaultSettings returns a well-structured set of default models.
func DefaultSettings() *Settings {
	return &Settings{
		DefaultModel: "muse-glimmer",
		Models: []ModelEndpoint{
			{
				ID:   "muse-glimmer",
				Name: "Muse Glimmer (OSU Cluster Tunnel)",
				URL:  "http://127.0.0.1:8010/v1",
				Parameters: map[string]interface{}{
					"temperature": 0.2,
					"max_tokens":  4096,
				},
			},
			{
				ID:   "llama3.1",
				Name: "Llama 3.1 8B (Local Ollama)",
				URL:  "http://localhost:11434/v1",
				Parameters: map[string]interface{}{
					"temperature": 0.7,
				},
			},
			{
				ID:   "qwen2.5-coder",
				Name: "Qwen 2.5 Coder 32B (Ollama / Local)",
				URL:  "http://localhost:11434/v1",
				Parameters: map[string]interface{}{
					"temperature": 0.2,
				},
			},
			{
				ID:   "claude-3-5-sonnet",
				Name: "Claude 3.5 Sonnet (Anthropic Cloud)",
				URL:  "https://api.anthropic.com/v1",
			},
			{
				ID:   "gpt-4o",
				Name: "GPT-4o (OpenAI Cloud)",
				URL:  "https://api.openai.com/v1",
			},
		},
	}
}

// CandidateConfigPaths returns potential configuration file paths in priority order:
// 1. ./.fastllm/config.json
// 2. ./.fastllm/config/settings.json
// 3. ~/.fastllm/config.json
// 4. ~/.fastllm/config/settings.json
func CandidateConfigPaths(workingDir string) []string {
	var paths []string
	if workingDir != "" {
		paths = append(paths,
			filepath.Join(workingDir, ".fastllm", "config.json"),
			filepath.Join(workingDir, ".fastllm", "config", "settings.json"),
		)
	}

	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths,
			filepath.Join(home, ".fastllm", "config.json"),
			filepath.Join(home, ".fastllm", "config", "settings.json"),
		)
	}

	return paths
}

// GlobalConfigPath returns the primary user global config path ~/.fastllm/config.json.
func GlobalConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".fastllm", "config.json"), nil
}

// LoadSettings searches candidate locations for a config file.
// If found, it parses and returns it along with the path.
// If not found, it creates the default ~/.fastllm/config.json and returns it.
func LoadSettings(workingDir string) (*Settings, string, error) {
	for _, p := range CandidateConfigPaths(workingDir) {
		if data, err := os.ReadFile(p); err == nil {
			var s Settings
			if err := json.Unmarshal(data, &s); err == nil {
				return &s, p, nil
			}
		}
	}

	// None found, initialize default global config file
	globalPath, err := GlobalConfigPath()
	if err != nil {
		return DefaultSettings(), "", nil
	}

	s := DefaultSettings()
	_ = SaveSettings(globalPath, s)
	return s, globalPath, nil
}

// SaveSettings writes the Settings structure as formatted JSON to target path.
func SaveSettings(path string, s *Settings) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, append(data, '\n'), 0644)
}

// FindModel looks up a model endpoint by ID (case-insensitive).
func (s *Settings) FindModel(id string) *ModelEndpoint {
	target := strings.ToLower(strings.TrimSpace(id))
	for i := range s.Models {
		if strings.ToLower(s.Models[i].ID) == target {
			return &s.Models[i]
		}
	}
	return nil
}

// AddOrUpdateModel adds a new model endpoint or updates an existing one.
func (s *Settings) AddOrUpdateModel(m ModelEndpoint) {
	for i := range s.Models {
		if strings.EqualFold(s.Models[i].ID, m.ID) {
			s.Models[i] = m
			return
		}
	}
	s.Models = append(s.Models, m)
}
