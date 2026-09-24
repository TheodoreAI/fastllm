package config

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"
)

// ModelEndpoint configures an LLM model and its inference endpoint.
type ModelEndpoint struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	APIKey     string `json:"api_key,omitempty"`
	APIKeyFile string `json:"api_key_file,omitempty"`
	Provider   string `json:"provider,omitempty"`
	// SendThink overrides the automatic detection of Ollama's "think" extension.
	SendThink *bool `json:"send_think,omitempty"`
	// ContextWindow is the model's total input+output window in tokens. It sizes
	// the compaction budget, so a self-hosted endpoint serving a model the built-in
	// table cannot recognize (a fine-tune, a renamed checkpoint, a vLLM server
	// started with a reduced --max-model-len) should set it explicitly rather than
	// inherit a guess. Zero means "fall back to the table, then the default".
	ContextWindow int                    `json:"context_window,omitempty"`
	Parameters    map[string]interface{} `json:"parameters,omitempty"`
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
	return strings.TrimSpace(decodeKeyFile(data))
}

// decodeKeyFile normalizes a key file's bytes to plain text.
//
// On Windows a key written with PowerShell's ">" or Out-File lands as UTF-16
// with a BOM. TrimSpace cannot clean that up — NUL is not Unicode whitespace
// and neither is the BOM — so the key would be sent mangled and the provider
// rejects it as invalid, which reads like a bad key rather than an encoding
// problem. Decoding here means a key file written by any ordinary Windows
// command still works.
func decodeKeyFile(data []byte) string {
	switch {
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE:
		return decodeUTF16(data[2:], binary.LittleEndian)
	case len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF:
		return decodeUTF16(data[2:], binary.BigEndian)
	}
	// BOM-less UTF-16LE still shows up as ASCII interleaved with NULs; a plain
	// UTF-8 key never contains one, so any NUL means the file is not plain text.
	if bytes.IndexByte(data, 0) >= 0 {
		return decodeUTF16(data, binary.LittleEndian)
	}
	return strings.TrimPrefix(string(data), "\ufeff")
}

func decodeUTF16(data []byte, order binary.ByteOrder) string {
	if len(data)%2 != 0 {
		data = data[:len(data)-1]
	}
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i < len(data); i += 2 {
		units = append(units, order.Uint16(data[i:i+2]))
	}
	return strings.TrimPrefix(string(utf16.Decode(units)), "\ufeff")
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
//
// A project config is used only once the user has trusted it (trust.go);
// until then it is skipped and the global config applies. Callers show the
// skipped file with ReviewProjectConfig.
func LoadSettings(workingDir string) (*Settings, string, error) {
	project := map[string]bool{}
	for _, p := range projectConfigPaths(workingDir) {
		project[p] = true
	}
	for _, p := range CandidateConfigPaths(workingDir) {
		data, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return DefaultSettings(), p, fmt.Errorf("read settings %q: %w", p, err)
		}
		if project[p] {
			if trusted, _ := trustState(p, data); !trusted {
				continue
			}
		}
		var s Settings
		if err := json.Unmarshal(data, &s); err != nil {
			return DefaultSettings(), p, fmt.Errorf("parse settings %q: %w", p, err)
		}
		return &s, p, nil
	}

	// None found, initialize default global config file
	globalPath, err := GlobalConfigPath()
	if err != nil {
		return DefaultSettings(), "", nil
	}

	s := DefaultSettings()
	if _, err := SaveSettings(globalPath, s); err != nil {
		return s, globalPath, fmt.Errorf("initialize settings %q: %w", globalPath, err)
	}
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
	if err := writeFileAtomic(filepath.Join(dir, name), []byte(key+"\n"), 0o600); err != nil {
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

	if err := writeFileAtomic(path, append(data, '\n'), 0o644); err != nil {
		return nil, err
	}
	// The user asked fastllm to write this file, so its exact contents are
	// theirs; a project config written here is trusted as written. The model
	// cannot reach SaveSettings, and its own writes to .fastllm/ ask first.
	if isProjectConfigPath(path) {
		if store, err := loadTrustStore(); err == nil {
			store[trustKey(path)] = digest(append(data, '\n'))
			_ = saveTrustStore(store)
		}
	}
	return relocated, nil
}

// isProjectConfigPath reports a .fastllm config file other than the global one.
func isProjectConfigPath(path string) bool {
	dir := filepath.Dir(path)
	if filepath.Base(dir) == "config" {
		dir = filepath.Dir(dir)
	}
	if filepath.Base(dir) != ".fastllm" {
		return false
	}
	home, err := userHomeDir()
	return err != nil || trustKey(filepath.Dir(dir)) != trustKey(home)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		if temp != nil {
			_ = temp.Close()
		}
		if err != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err = temp.Chmod(mode); err != nil {
		return err
	}
	if _, err = temp.Write(data); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	temp = nil
	if err = os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
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

// DefaultOllamaPort is the port Ollama listens on out of the box. It is the one
// piece of host knowledge worth keeping: the "think" field is Ollama's own
// extension, and every other OpenAI-compatible server either ignores it or
// rejects the request outright.
const DefaultOllamaPort = "11434"

// IsOllamaEndpoint reports whether a base URL points at an Ollama server. It
// matches on the port rather than the hostname, so Ollama on another box or
// behind a forward is recognised just as well as one on loopback.
func IsOllamaEndpoint(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return parsed.Port() == DefaultOllamaPort
}

// ShouldSendThink decides whether to send Ollama's "think" field to an endpoint.
// An explicit send_think in config always wins; otherwise the endpoint is probed
// by port, so a non-default Ollama port only needs the field set once rather than
// silently dropping reasoning effort.
func ShouldSendThink(endpoint *ModelEndpoint, baseURL string) bool {
	if endpoint != nil && endpoint.SendThink != nil {
		return *endpoint.SendThink
	}
	return IsOllamaEndpoint(baseURL)
}

// FindModel looks up a model endpoint by ID (case-insensitive).
func (s *Settings) FindModel(id string) *ModelEndpoint {
	if s == nil {
		return nil
	}
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
