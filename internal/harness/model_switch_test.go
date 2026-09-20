package harness

import (
	"strings"
	"testing"

	"fastllm/internal/config"
	"fastllm/internal/llm"
)

func TestRunnerSwitchModelUpdatesEndpointAndClearsStaleSettings(t *testing.T) {
	oldTemperature := 0.9
	oldTopP := 0.8
	oldMaxTokens := 4096
	client := llm.New("http://localhost:8010/v1", "old-key", "old-model", "")
	client.SendThink = true
	client.Temperature = &oldTemperature
	client.TopP = &oldTopP
	client.MaxTokens = &oldMaxTokens
	runner := NewRunner(client, t.TempDir(), "old-model")

	endpoint := &config.ModelEndpoint{
		ID:   "gemma-4-31b",
		Name: "Gemma 4 31B",
		URL:  "http://localhost:8003/v1/",
		Parameters: map[string]interface{}{
			"temperature": 0.2,
		},
	}
	if err := runner.SwitchModel(endpoint); err != nil {
		t.Fatalf("SwitchModel() error = %v", err)
	}

	if client.BaseURL != "http://localhost:8003/v1" {
		t.Errorf("BaseURL = %q, want Gemma endpoint", client.BaseURL)
	}
	if client.ChatModel != "gemma-4-31b" || runner.DefaultModel != "gemma-4-31b" {
		t.Errorf("model was not updated: client=%q runner=%q", client.ChatModel, runner.DefaultModel)
	}
	if client.APIKey != "" {
		t.Errorf("APIKey = %q, want stale key cleared", client.APIKey)
	}
	if client.SendThink {
		t.Error("SendThink remained enabled for non-Ollama endpoint")
	}
	if client.Temperature == nil || *client.Temperature != 0.2 {
		t.Errorf("Temperature = %v, want 0.2", client.Temperature)
	}
	if client.TopP != nil || client.MaxTokens != nil {
		t.Errorf("stale parameters remained: TopP=%v MaxTokens=%v", client.TopP, client.MaxTokens)
	}
}

func TestTeaModelCommandSwitchesModelEndpoint(t *testing.T) {
	client := llm.New("http://localhost:8010/v1", "", "muse-glimmer", "")
	runner := NewRunner(client, t.TempDir(), "muse-glimmer")
	m := &teaModel{
		runner:    runner,
		modelName: "muse-glimmer",
		settings: &config.Settings{Models: []config.ModelEndpoint{
			{ID: "muse-glimmer", Name: "Muse Glimmer", URL: "http://localhost:8010/v1"},
			{ID: "gemma-4-31b", Name: "Gemma 4 31B", URL: "http://localhost:8003/v1"},
		}},
	}

	m.handleAgentSubmit("/model gemma-4-31b")

	if m.modelName != "gemma-4-31b" {
		t.Errorf("modelName = %q, want gemma-4-31b", m.modelName)
	}
	if client.BaseURL != "http://localhost:8003/v1" {
		t.Errorf("BaseURL = %q, want port 8003", client.BaseURL)
	}
	if !strings.Contains(m.historyText.String(), "Endpoint: http://localhost:8003/v1") {
		t.Errorf("switch confirmation did not show endpoint: %q", m.historyText.String())
	}
}

func TestTeaModelRejectsUnknownModelWithoutChangingEndpoint(t *testing.T) {
	client := llm.New("http://localhost:8010/v1", "", "muse-glimmer", "")
	runner := NewRunner(client, t.TempDir(), "muse-glimmer")
	m := &teaModel{
		runner:    runner,
		modelName: "muse-glimmer",
		settings:  &config.Settings{},
	}

	m.handleAgentSubmit("/model typo")

	if m.modelName != "muse-glimmer" || client.BaseURL != "http://localhost:8010/v1" {
		t.Errorf("unknown model changed runtime: model=%q endpoint=%q", m.modelName, client.BaseURL)
	}
}
