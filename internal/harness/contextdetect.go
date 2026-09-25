package harness

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fastllm/internal/config"
	"fastllm/internal/llm"
)

// Asking a provider for a model's context window is the only place fastllm goes
// to the network for one, and only on an explicit command (/models detect, or
// right after /models add): startup never probes, because endpoints here are
// often tunnels that are down.

// contextProbeResult is one model's answer.
type contextProbeResult struct {
	ID     string
	Tokens int
	Source string
	Err    error
}

// contextProbeTargets copies the endpoints to ask about; "all" or no ids names
// every configured model. The copies let probing run off the UI goroutine.
func contextProbeTargets(settings *config.Settings, ids []string) (targets []config.ModelEndpoint, missing []string) {
	if settings == nil {
		return nil, ids
	}
	if len(ids) == 0 || (len(ids) == 1 && strings.EqualFold(ids[0], "all")) {
		return append([]config.ModelEndpoint(nil), settings.Models...), nil
	}
	for _, id := range ids {
		if endpoint := settings.FindModel(id); endpoint != nil {
			targets = append(targets, *endpoint)
		} else {
			missing = append(missing, id)
		}
	}
	return targets, missing
}

// probeContextWindows asks each endpoint's provider for its window. It only
// reads its arguments.
func probeContextWindows(targets []config.ModelEndpoint) []contextProbeResult {
	results := make([]contextProbeResult, 0, len(targets))
	for _, endpoint := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		tokens, source, err := llm.DetectContextWindow(ctx, llm.ContextProbe{
			Provider: endpoint.Provider, BaseURL: endpoint.URL, APIKey: endpoint.ResolveAPIKey(), Model: endpoint.ID,
		})
		cancel()
		results = append(results, contextProbeResult{ID: endpoint.ID, Tokens: tokens, Source: source, Err: err})
	}
	return results
}

// applyContextWindows saves each detected window as context_window and returns
// one line per model and whether anything changed. A failed probe keeps what
// fastllm had.
func applyContextWindows(settings *config.Settings, configPath string, results []contextProbeResult, missing []string) ([]string, bool, error) {
	var lines []string
	for _, id := range missing {
		lines = append(lines, fmt.Sprintf("%s: not configured", id))
	}
	changed := false
	for _, result := range results {
		endpoint := settings.FindModel(result.ID)
		if endpoint == nil {
			continue
		}
		previous := describeWindow(settings, endpoint)
		switch {
		case result.Err != nil:
			lines = append(lines, fmt.Sprintf("%s: kept %s (%v)", result.ID, previous, result.Err))
		case endpoint.ContextWindow == result.Tokens:
			lines = append(lines, fmt.Sprintf("%s: %d tokens, already set (%s)", result.ID, result.Tokens, result.Source))
		default:
			endpoint.ContextWindow = result.Tokens
			changed = true
			lines = append(lines, fmt.Sprintf("%s: %d tokens from %s, was %s", result.ID, result.Tokens, result.Source, previous))
		}
	}
	if !changed {
		return lines, false, nil
	}
	if _, err := config.SaveSettings(configPath, settings); err != nil {
		return lines, false, fmt.Errorf("saving %s: %w", configPath, err)
	}
	return lines, true, nil
}

// describeWindow says what fastllm assumes for an endpoint before detection.
func describeWindow(settings *config.Settings, endpoint *config.ModelEndpoint) string {
	if endpoint.ContextWindow > 0 {
		return fmt.Sprintf("%d (configured)", endpoint.ContextWindow)
	}
	if known := lookupKnownContextWindow(endpoint.ID); known > 0 {
		return fmt.Sprintf("%d (built-in table)", known)
	}
	return fmt.Sprintf("%d (default)", ResolveContextWindow(settings, endpoint.ID))
}

// printContextDetection shows detection results in line mode.
func printContextDetection(lines []string, err error) {
	for _, line := range lines {
		fmt.Println(ColorGray("  " + line))
	}
	if err != nil {
		fmt.Println(ColorRed("  " + err.Error()))
	}
}

// contextDetectedMsg carries probe results back to the TUI's goroutine.
type contextDetectedMsg struct {
	results []contextProbeResult
	missing []string
}
