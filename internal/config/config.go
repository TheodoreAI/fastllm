package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ModelEndpoint configures an LLM model and its inference endpoint.
type ModelEndpoint struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	URL        string                 `json:"url"`
	APIKey     string                 `json:"api_key,omitempty"`
	APIKeyFile string                 `json:"api_key_file,omitempty"`
	Provider   string                 `json:"provider,omitempty"`
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
		home, err := userHomeDir()
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

// DefaultSettings returns the endpoints a fresh install can actually reach.
//
// Everything here must be true for any user on any machine: canonical provider URLs
// and the stock local Ollama port. Personal or site-specific endpoints — cluster
// tunnels, lab hosts, forwarded ports — belong in ~/.fastllm/config.json on the
// machine that has them, never in the shipped defaults.
func DefaultSettings() *Settings {
	return &Settings{
		DefaultModel: "llama3.1",
		Models: []ModelEndpoint{
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
				Name: "Qwen 2.5 Coder (Local Ollama)",
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
	_, _ = SaveSettings(globalPath, s)
	return s, globalPath, nil
}

// userHomeDir is indirected so tests can relocate the key store.
var userHomeDir = os.UserHomeDir

// keyFileNamePattern matches characters that are unsafe in a filename. Model IDs
// routinely contain ':' and '/' (for example vendor/Model-Name:tag).
var keyFileNamePattern = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// writeKeyFile stores a secret outside config.json and returns a home-relative path
// to it. The path stays ~-relative so the config remains portable between machines.
func writeKeyFile(modelID, key string) (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	name := strings.Trim(keyFileNamePattern.ReplaceAllString(modelID, "_"), "_")
	if name == "" {
		name = "key"
	}
	dir := filepath.Join(home, ".fastllm", "keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(key+"\n"), 0o600); err != nil {
		return "", err
	}
	return "~/.fastllm/keys/" + name, nil
}

// SaveSettings writes the Settings structure as formatted JSON to target path.
//
// An inline api_key is never persisted. Any secret found on a model is relocated to
// a 0600 file under ~/.fastllm/keys/ and the entry is rewritten to reference it via
// api_key_file, preserving what the key resolved to before. The IDs of relocated
// models are returned so the caller can tell the user where the secret went.
//
// This is deliberately structural rather than advisory: config.json is the file most
// likely to be copied into a repo, a gist, or a bug report, so no code path — present
// or future — is able to write a credential into it.
func SaveSettings(path string, s *Settings) ([]string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	// Serialize a copy: the caller's in-memory Settings keeps its resolved keys for
	// the rest of the session.
	out := *s
	out.Models = append([]ModelEndpoint(nil), s.Models...)

	var relocated []string
	for i := range out.Models {
		key := strings.TrimSpace(out.Models[i].APIKey)
		out.Models[i].APIKey = ""
		if key == "" {
			continue
		}
		keyPath, err := writeKeyFile(out.Models[i].ID, key)
		if err != nil {
			return nil, err
		}
		// The inline key was winning in ResolveAPIKey, so point at its new home even
		// when an api_key_file was already set; otherwise resolution would change.
		out.Models[i].APIKeyFile = keyPath
		relocated = append(relocated, out.Models[i].ID)
	}

	data, err := json.MarshalIndent(&out, "", "  ")
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		return nil, err
	}
	return relocated, nil
}

// ProviderEndpointDefaults is where a named provider lives on this machine. A zero
// value means the provider is not configured, and callers must treat that as
// "unavailable" rather than substituting a host of their own.
type ProviderEndpointDefaults struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Configured reports whether the provider has somewhere to connect to.
func (p ProviderEndpointDefaults) Configured() bool {
	return strings.TrimSpace(p.BaseURL) != ""
}

// providerEnvVars lists the environment overrides honoured per provider, most
// specific first. Environment variable names are portable across machines; host
// addresses and key paths are not, which is why no default URL appears here.
var providerEnvVars = map[string]struct{ urls, keys []string }{
	"selfhosted": {
		urls: []string{"SELFHOSTED_LLM_BASE_URL", "LLM_SELFHOSTED_BASE_URL"},
		keys: []string{"SELFHOSTED_LLM_API_KEY", "LLM_SELFHOSTED_API_KEY"},
	},
}

func firstEnv(names []string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// FindByProvider returns the endpoint tagged with the given provider name, letting
// callers locate "the self-hosted cluster endpoint" without knowing its host or ID.
func (s *Settings) FindByProvider(provider string) *ModelEndpoint {
	if s == nil {
		return nil
	}
	target := strings.ToLower(strings.TrimSpace(provider))
	if target == "" {
		return nil
	}
	for i := range s.Models {
		if strings.ToLower(strings.TrimSpace(s.Models[i].Provider)) == target {
			return &s.Models[i]
		}
	}
	return nil
}

// ResolveProvider locates a provider's endpoint: environment variables first, then
// the config entry tagged with "provider": "<name>". The binary ships no host
// address and no key path, so a machine that has configured neither resolves to a
// zero value — an endpoint exists only because a user said where it is.
func ResolveProvider(s *Settings, provider string) ProviderEndpointDefaults {
	var resolved ProviderEndpointDefaults
	if env, ok := providerEnvVars[strings.ToLower(strings.TrimSpace(provider))]; ok {
		resolved.BaseURL = firstEnv(env.urls)
		resolved.APIKey = firstEnv(env.keys)
	}
	if endpoint := s.FindByProvider(provider); endpoint != nil {
		if resolved.BaseURL == "" {
			resolved.BaseURL = strings.TrimSpace(endpoint.URL)
		}
		if resolved.APIKey == "" {
			resolved.APIKey = endpoint.ResolveAPIKey()
		}
		resolved.Model = strings.TrimSpace(endpoint.ID)
	}
	return resolved
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
