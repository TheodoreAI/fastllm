package harness

import (
	"fmt"
	"os"
	"strings"
	"time"

	"fastllm/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

// untrustedConfigNotice describes a project config that is being ignored
// because the user has not trusted it, or "" when there is nothing to say.
// Its contents come from the repository, so every field is sanitized.
func untrustedConfigNotice(workingDir, action string) string {
	review := config.ReviewProjectConfig(workingDir)
	if review == nil || review.Trusted {
		return ""
	}
	return FormatUntrustedConfigNotice(review, action)
}

// FormatUntrustedConfigNotice shows what a project config would change, so
// trusting it is an informed decision.
func FormatUntrustedConfigNotice(review *config.ProjectConfigReview, action string) string {
	lines := []string{"", FormatKV("file", sanitizeUntrusted(abbreviateHome(review.Path)), 10)}
	if review.Changed {
		lines = append(lines, ColorYellow("It has changed since you trusted it."))
	}
	switch {
	case review.ParseErr != nil:
		lines = append(lines, FormatKV("error", sanitizeUntrusted(review.ParseErr.Error()), 10))
	case len(review.Endpoints) == 0:
		lines = append(lines, FormatKV("models", "none", 10))
	default:
		for _, ep := range review.Endpoints {
			detail := sanitizeUntrusted(ep.ID) + " → " + sanitizeUntrusted(ep.URL)
			if ep.APIKeyFile != "" {
				detail += "  (key: " + sanitizeUntrusted(ep.APIKeyFile) + ")"
			}
			if ep.APIKey != "" {
				detail += "  (inline key)"
			}
			lines = append(lines, FormatKV("model", detail, 10))
		}
	}
	lines = append(lines, "",
		ColorGray("It came with this workspace, so it is not used until you trust it:"),
		ColorGray("an endpoint here receives your conversation and any key it names."),
		ColorGray("Your global config is used instead. ")+ColorCyan(action), "")
	return FormatCard("Workspace config not trusted", lines, 86)
}

// handleTrustSlash runs /trust and /untrust, then reloads the settings so the
// change applies immediately.
func (m *teaModel) handleTrustSlash(command string) tea.Cmd {
	m.appendHistory(styleUserPrompt.Render("❯ "+command) + "\n")
	var err error
	var verb string
	if command == "/trust" {
		if review := config.ReviewProjectConfig(m.workingDir); review == nil {
			m.appendHistory(styleMuted.Render("This workspace has no .fastllm config to trust.") + "\n\n")
			return nil
		} else if review.Trusted {
			m.appendHistory(styleMuted.Render("This workspace's config is already trusted.") + "\n\n")
			return nil
		}
		_, err = config.TrustProjectConfig(m.workingDir)
		verb = "Trusted"
	} else {
		err = config.UntrustProjectConfig(m.workingDir)
		verb = "Stopped using"
	}
	if err != nil {
		m.appendHistory(styleDiffDel.Render(fmt.Sprintf("%s failed: %v", strings.TrimPrefix(command, "/"), err)) + "\n\n")
		return nil
	}

	settings, configPath, err := config.LoadSettings(m.workingDir)
	if err != nil {
		m.appendHistory(styleDiffDel.Render("Reloading settings failed: "+err.Error()) + "\n\n")
		return nil
	}
	m.settings, m.configPath = settings, configPath
	msg := fmt.Sprintf("%s this workspace's config. Settings now come from %s.", verb, abbreviateHome(configPath))
	if endpoint := m.settings.FindModel(m.modelName); endpoint != nil {
		if m.runner != nil {
			if err := m.runner.SwitchModel(endpoint); err != nil {
				msg += " Could not switch the active model: " + err.Error()
			}
		}
	} else {
		msg += fmt.Sprintf(" %s is not defined there; use /models to pick one.", m.modelName)
	}
	m.appendHistory(styleStatusNotice.Render(msg) + "\n\n")
	m.statusNotice = verb + " workspace config"
	return m.clearStatusAfter(3 * time.Second)
}

// headlessConfigNotice is the one-line warning for runs that cannot prompt.
func headlessConfigNotice(workingDir string) {
	if review := config.ReviewProjectConfig(workingDir); review != nil && !review.Trusted {
		fmt.Fprintf(os.Stderr, "Ignoring untrusted %s (set %s=1 to use it, or /trust it in the TUI).\n",
			sanitizeUntrusted(review.Path), config.TrustEnv)
	}
}
