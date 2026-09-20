package harness

import (
	"strings"

	"fastllm/internal/config"
	"fastllm/internal/llm"
)

// cloudProviderPrefixes maps a config entry's "provider" tag to the routing
// prefix llm.Router dispatches on. Only providers that need a non-OpenAI wire
// format (or a provider-specific base URL) belong here: an ordinary
// OpenAI-compatible endpoint is reached with its own URL and does not need the
// router at all.
//
// This mapping lives in harness rather than config because it is the point
// where the two packages meet — config must not learn the llm package's
// routing vocabulary just to describe an endpoint.
var cloudProviderPrefixes = map[string]string{
	"anthropic":  llm.AnthropicPrefix,
	"openai":     llm.OpenAIPrefix,
	"gemini":     llm.GeminiPrefix,
	"nvidia":     llm.NvidiaPrefix,
	"cloudflare": llm.CloudflarePrefix,
}

// routedModelID returns the prefixed model name the router needs for a
// provider-tagged endpoint, and false for endpoints that are plain
// OpenAI-compatible URLs.
func routedModelID(endpoint *config.ModelEndpoint) (string, bool) {
	if endpoint == nil {
		return "", false
	}
	prefix, ok := cloudProviderPrefixes[strings.ToLower(strings.TrimSpace(endpoint.Provider))]
	if !ok {
		return "", false
	}
	return prefix + endpoint.ID, true
}

// CloudConfigFromSettings collects provider credentials out of the user's
// config so the terminal UI can reach the same native clients the HTTP server
// has always used. Keys come from the provider-tagged entries themselves
// (api_key / api_key_file), so nothing new has to be configured: tagging an
// endpoint is what enables its provider.
func CloudConfigFromSettings(settings *config.Settings) llm.CloudProviderConfig {
	var cloud llm.CloudProviderConfig
	if settings == nil {
		return cloud
	}
	for i := range settings.Models {
		endpoint := &settings.Models[i]
		key := endpoint.ResolveAPIKey()
		if key == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(endpoint.Provider)) {
		case "anthropic":
			cloud.AnthropicAPIKey = key
		case "openai":
			cloud.OpenAIAPIKey = key
		case "gemini":
			cloud.GeminiAPIKey = key
		case "nvidia":
			cloud.NvidiaAPIKey = key
		case "cloudflare":
			// Workers AI is scoped under a per-account URL, so the key alone is
			// not enough. There is no dedicated config field, so the account id
			// rides in the generic parameters map rather than forcing a schema
			// change on every other provider.
			cloud.CloudflareAPIKey = key
			cloud.CloudflareAccountID = stringParameter(endpoint.Parameters, "account_id")
		}
	}

	// Deliberately no SelfHosted* fields. A self-hosted endpoint is an ordinary
	// OpenAI-compatible URL, which the direct client already serves — and it is
	// the client SwitchModel configures, so it is the one carrying the
	// endpoint's parameters. Populating these would make the router claim bare
	// self-hosted model names (see isConfiguredSelfHostedModel) and silently
	// divert a tunnel to a second client with none of those parameters applied.
	return cloud
}

// stringParameter reads a string out of an endpoint's free-form parameters.
func stringParameter(params map[string]interface{}, name string) string {
	if params == nil {
		return ""
	}
	value, ok := params[name].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}
