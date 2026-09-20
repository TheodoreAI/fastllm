package harness

import (
	"testing"

	"fastllm/internal/config"
	"fastllm/internal/llm"
)

func TestRoutedModelIDUsesProviderTag(t *testing.T) {
	cases := []struct {
		provider string
		want     string
		routed   bool
	}{
		{"gemini", "gemini:g-1", true},
		{"Gemini", "gemini:g-1", true},
		{"anthropic", "anthropic:g-1", true},
		{"openai", "openai:g-1", true},
		{"selfhosted", "", false}, // reached by URL, not by a native client
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := routedModelID(&config.ModelEndpoint{ID: "g-1", Provider: tc.provider})
		if ok != tc.routed || got != tc.want {
			t.Fatalf("provider %q -> (%q,%v); want (%q,%v)", tc.provider, got, ok, tc.want, tc.routed)
		}
	}
}

// A provider-tagged endpoint has no URL of its own: the native client builds
// one. Rejecting it for a missing URL is what made Gemini unreachable.
func TestSwitchModelAcceptsProviderEndpointWithoutURL(t *testing.T) {
	router := llm.NewRouter(llm.New("http://localhost:11434/v1", "", "llama3.1", ""),
		llm.CloudProviderConfig{GeminiAPIKey: "test-key"})
	r := NewRunner(router, t.TempDir(), "llama3.1")

	endpoint := &config.ModelEndpoint{ID: "gemini-3.7-flash", Provider: "gemini"}
	if err := r.SwitchModel(endpoint); err != nil {
		t.Fatalf("switch failed: %v", err)
	}
	if r.DefaultModel != "gemini-3.7-flash" {
		t.Fatalf("display name = %q; the user-visible id must not gain a prefix", r.DefaultModel)
	}
	if r.routeModel != "gemini:gemini-3.7-flash" {
		t.Fatalf("route = %q; want the prefixed name", r.routeModel)
	}
}

func TestSwitchModelRejectsProviderEndpointWithoutRouter(t *testing.T) {
	r := NewRunner(llm.New("http://x/v1", "", "m", ""), t.TempDir(), "m")
	err := r.SwitchModel(&config.ModelEndpoint{ID: "gemini-3.7-flash", Provider: "gemini"})
	if err == nil {
		t.Fatal("a plain client cannot reach a native provider; switching must fail loudly")
	}
}

// Switching back to a plain endpoint must clear the route, or the old
// provider prefix would keep hijacking every later turn.
func TestSwitchModelClearsRouteForPlainEndpoint(t *testing.T) {
	router := llm.NewRouter(llm.New("http://localhost:11434/v1", "", "llama3.1", ""),
		llm.CloudProviderConfig{GeminiAPIKey: "k"})
	r := NewRunner(router, t.TempDir(), "llama3.1")

	if err := r.SwitchModel(&config.ModelEndpoint{ID: "gemini-3.7-flash", Provider: "gemini"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SwitchModel(&config.ModelEndpoint{ID: "llama3.1", URL: "http://localhost:11434/v1"}); err != nil {
		t.Fatal(err)
	}
	if r.routeModel != "" {
		t.Fatalf("route = %q; want cleared", r.routeModel)
	}
}

func TestSwitchModelStillRequiresURLForPlainEndpoint(t *testing.T) {
	r := NewRunner(llm.New("http://x/v1", "", "m", ""), t.TempDir(), "m")
	if err := r.SwitchModel(&config.ModelEndpoint{ID: "no-url"}); err == nil {
		t.Fatal("an untagged endpoint with no URL is unreachable and must still error")
	}
}

func TestCloudConfigFromSettingsCollectsKeysByProviderTag(t *testing.T) {
	settings := &config.Settings{Models: []config.ModelEndpoint{
		{ID: "g", Provider: "gemini", APIKey: "gem-key"},
		{ID: "a", Provider: "anthropic", APIKey: "ant-key"},
		{ID: "local", URL: "http://localhost:11434/v1"},
		{ID: "cf", Provider: "cloudflare", APIKey: "cf-key",
			Parameters: map[string]interface{}{"account_id": "acct-123"}},
	}}
	cloud := CloudConfigFromSettings(settings)
	if cloud.GeminiAPIKey != "gem-key" || cloud.AnthropicAPIKey != "ant-key" {
		t.Fatalf("keys not collected: %+v", cloud)
	}
	if cloud.CloudflareAPIKey != "cf-key" || cloud.CloudflareAccountID != "acct-123" {
		t.Fatalf("cloudflare needs both key and account id: %+v", cloud)
	}
	if cloud.OpenAIAPIKey != "" {
		t.Fatalf("untagged endpoint leaked into a provider slot: %+v", cloud)
	}
}

// Self-hosted tunnels are reached through the direct client that SwitchModel
// configures. If the router were also given them it would claim bare tunnel
// model names and route around the endpoint's parameters.
func TestCloudConfigLeavesSelfHostedToTheDirectClient(t *testing.T) {
	settings := &config.Settings{Models: []config.ModelEndpoint{
		{ID: "muse-glimmer", Provider: "selfhosted", URL: "http://127.0.0.1:8010/v1", APIKey: "k"},
	}}
	cloud := CloudConfigFromSettings(settings)
	if cloud.SelfHostedBaseURL != "" || cloud.SelfHostedAPIKey != "" || cloud.SelfHostedModel != "" {
		t.Fatalf("self-hosted must stay with the direct client: %+v", cloud)
	}
}

func TestSelfHostedEndpointStillRoutesThroughDirectClient(t *testing.T) {
	local := llm.New("http://localhost:11434/v1", "", "llama3.1", "")
	router := llm.NewRouter(local, CloudConfigFromSettings(&config.Settings{}))
	r := NewRunner(router, t.TempDir(), "llama3.1")

	endpoint := &config.ModelEndpoint{ID: "muse-glimmer", Provider: "selfhosted", URL: "http://127.0.0.1:8010/v1"}
	if err := r.SwitchModel(endpoint); err != nil {
		t.Fatal(err)
	}
	if r.routeModel != "" {
		t.Fatalf("self-hosted gained a router prefix: %q", r.routeModel)
	}
	if local.BaseURL != "http://127.0.0.1:8010/v1" {
		t.Fatalf("direct client was not repointed: %q", local.BaseURL)
	}
}
