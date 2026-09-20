package harness

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"fastllm/internal/llm"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *teaModel) runtimeSettings() InteractiveRuntime {
	return InteractiveRuntime{
		MaxTurns:       m.maxTurns,
		CommandTimeout: m.commandTimeout,
		ThinkLevel:     m.thinkLevel,
		AllowCommands:  m.allowCommands,
		PermissionMode: m.permissionMode,
		ExpandedTools:  m.expandedTools,
	}
}

func (m *teaModel) initializeSession(resumeID string) error {
	store, err := DefaultSessionStore()
	if err != nil {
		return err
	}
	m.sessionStore = store
	_, _ = store.Prune(time.Now().UTC(), 100)

	var loaded *InteractiveSession
	if strings.TrimSpace(resumeID) != "" {
		if strings.EqualFold(resumeID, "last") {
			loaded, err = store.Latest()
		} else {
			loaded, err = store.Load(resumeID)
		}
	} else if latest, latestErr := store.Latest(); latestErr == nil && latest.ClosedAt == nil && len(latest.Messages) > 0 {
		loaded = latest
	}
	if err != nil {
		return err
	}
	if loaded != nil {
		return m.loadSession(loaded)
	}
	m.activeSession = store.New(m.workingDir, m.modelName, m.runtimeSettings())
	return m.saveSession()
}

func (m *teaModel) loadSession(session *InteractiveSession) error {
	if session == nil {
		return fmt.Errorf("session is unavailable")
	}
	if strings.TrimSpace(session.WorkingDir) != "" {
		if err := m.changeWorkingDirectory(session.WorkingDir); err != nil {
			return err
		}
	}
	if session.Model != "" {
		if endpoint := m.settings.FindModel(session.Model); endpoint != nil {
			if err := m.runner.SwitchModel(endpoint); err != nil {
				return err
			}
		}
		m.modelName = session.Model
	}
	if session.Runtime.MaxTurns > 0 {
		m.maxTurns = session.Runtime.MaxTurns
	}
	if session.Runtime.CommandTimeout > 0 {
		m.commandTimeout = session.Runtime.CommandTimeout
	}
	m.thinkLevel = session.Runtime.ThinkLevel
	m.allowCommands = session.Runtime.AllowCommands
	if session.Runtime.PermissionMode != "" {
		m.permissionMode = session.Runtime.PermissionMode
	}
	m.expandedTools = session.Runtime.ExpandedTools
	m.sessionMessages = append([]llm.Message(nil), session.Messages...)
	m.sessionMetrics = session.Metrics
	session.ClosedAt = nil
	m.activeSession = session
	m.sessionGrants = make(map[string]bool)
	return m.saveSession()
}

func (m *teaModel) saveSession() error {
	if m.sessionStore == nil || m.activeSession == nil {
		return nil
	}
	m.activeSession.WorkingDir = m.workingDir
	m.activeSession.Model = m.modelName
	m.activeSession.Runtime = m.runtimeSettings()
	m.activeSession.Messages = append([]llm.Message(nil), m.sessionMessages...)
	m.activeSession.Metrics = m.sessionMetrics
	if !m.activeSession.CustomTitle {
		m.activeSession.Title = sessionTitle(m.activeSession.Messages)
	}
	return m.sessionStore.Save(m.activeSession)
}

func (m *teaModel) closeSession() {
	if m.activeSession == nil {
		return
	}
	now := time.Now().UTC()
	m.activeSession.ClosedAt = &now
	_ = m.saveSession()
}

func (m *teaModel) startNewSession() error {
	if m.activeSession != nil {
		now := time.Now().UTC()
		m.activeSession.ClosedAt = &now
		if err := m.saveSession(); err != nil {
			return err
		}
	}
	m.sessionMessages = nil
	m.sessionMetrics = SessionMetrics{}
	m.latestMetrics = nil
	m.lastResponse = ""
	m.sessionGrants = make(map[string]bool)
	if m.sessionStore != nil {
		m.activeSession = m.sessionStore.New(m.workingDir, m.modelName, m.runtimeSettings())
		return m.saveSession()
	}
	return nil
}

func (m *teaModel) initialMessages() []InitialMessage {
	messages := make([]InitialMessage, 0, len(m.sessionMessages))
	for _, message := range m.sessionMessages {
		if (message.Role == "user" || message.Role == "assistant") && strings.TrimSpace(message.Content) != "" {
			messages = append(messages, InitialMessage{Role: message.Role, Content: message.Content})
		}
	}
	return messages
}

// compactSessionContext trims the accumulated cross-turn transcript before it is
// replayed as InitialMessages. Runner.Run compacts only within a single run, so
// without this the TUI's own message list grows unbounded across turns. Returns a
// notice to surface, or "" when the context is comfortably inside budget.
func (m *teaModel) compactSessionContext() string {
	cfg := DefaultCompactionConfig()
	if cfg.MaxTotalChars <= 0 {
		return ""
	}
	before := messageCharacterCount(m.sessionMessages)

	compacted, didCompact := OnlineCompactMessages(m.sessionMessages, cfg)
	if didCompact {
		m.sessionMessages = compacted
		if err := m.saveSession(); err != nil {
			return "Session autosave failed: " + err.Error()
		}
		return fmt.Sprintf("Context reached %s; compacted older turns down to %s.",
			formatCharCount(before), formatCharCount(messageCharacterCount(m.sessionMessages)))
	}

	// Report either the compaction or the approach to it, never both.
	if before >= cfg.MaxTotalChars*3/4 {
		return fmt.Sprintf("Context is at %d%% of the compaction budget (%s).",
			before*100/cfg.MaxTotalChars, formatCharCount(cfg.MaxTotalChars))
	}
	return ""
}

func (m *teaModel) recordCompletedPrompt(response string) {
	if strings.TrimSpace(m.pendingPrompt) == "" {
		return
	}
	m.sessionMessages = append(m.sessionMessages, llm.Message{Role: "user", Content: m.pendingPrompt})
	if strings.TrimSpace(response) != "" {
		m.sessionMessages = append(m.sessionMessages, llm.Message{Role: "assistant", Content: response})
	}
	m.pendingPrompt = ""
	if err := m.saveSession(); err != nil {
		m.statusNotice = "Session autosave failed: " + err.Error()
	}
}

func (m *teaModel) appendSessionTranscript() {
	if len(m.sessionMessages) == 0 || m.activeSession == nil {
		return
	}
	m.appendHistory(styleMuted.Render("Recovered session "+m.activeSession.ID+" · "+m.activeSession.Title) + "\n")
	for _, message := range m.sessionMessages {
		switch message.Role {
		case "user":
			m.appendHistory(formatSubmittedPrompt(message.Content))
		case "assistant":
			m.appendHistory(formatAssistantAnswer(message.Content, m.contentWidth()))
		}
	}
}

func (m *teaModel) activeSessionID() string {
	if m.activeSession == nil {
		return ""
	}
	return m.activeSession.ID
}

func (m *teaModel) handleSessionSlash(input string, parts []string, command string) (bool, tea.Cmd) {
	switch command {
	case "/sessions":
		if m.sessionStore == nil {
			m.appendHistory(styleDiffDel.Render("Session persistence is unavailable.\n\n"))
			return true, nil
		}
		sessions, err := m.sessionStore.List()
		if err != nil {
			m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Cannot list sessions: %v\n\n", err)))
		} else {
			m.appendHistory(FormatSessionsTable(sessions, m.activeSessionID()) + "\n\n")
		}
		return true, nil

	case "/session":
		target := m.activeSession
		var err error
		if len(parts) > 1 && m.sessionStore != nil {
			target, err = m.sessionStore.Load(parts[1])
		}
		if err != nil || target == nil {
			m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Cannot load session details: %v\n\n", err)))
		} else {
			lines := []string{
				FormatKV("ID", target.ID, 11),
				FormatKV("title", target.Title, 11),
				FormatKV("updated", target.UpdatedAt.Local().Format(time.RFC1123), 11),
				FormatKV("directory", target.WorkingDir, 11),
				FormatKV("model", target.Model, 11),
				FormatKV("messages", strconv.Itoa(len(target.Messages)), 11),
				FormatKV("tokens", strconv.Itoa(target.Metrics.TotalTokens), 11),
			}
			m.appendHistory(FormatCard("Session Details", lines, 86) + "\n\n")
		}
		return true, nil

	case "/rename":
		if m.activeSession == nil || m.sessionStore == nil || len(parts) < 2 {
			m.appendHistory(styleMuted.Render("Usage: /rename <new title>\n\n"))
			return true, nil
		}
		m.activeSession.Title = strings.TrimSpace(input[len(parts[0]):])
		m.activeSession.CustomTitle = true
		if err := m.saveSession(); err != nil {
			m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Rename failed: %v\n\n", err)))
		} else {
			m.appendHistory(styleStatusNotice.Render("Session renamed.\n\n"))
		}
		return true, nil

	case "/delete-session":
		if m.sessionStore == nil || len(parts) < 2 {
			m.appendHistory(styleMuted.Render("Usage: /delete-session <session-id>\n\n"))
			return true, nil
		}
		if parts[1] == m.activeSessionID() {
			m.appendHistory(styleDiffDel.Render("Start or resume another session before deleting the active session.\n\n"))
			return true, nil
		}
		if err := m.sessionStore.Delete(parts[1]); err != nil {
			m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Delete failed: %v\n\n", err)))
		} else {
			m.appendHistory(styleStatusNotice.Render("Session deleted.\n\n"))
		}
		return true, nil

	case "/resume":
		if m.sessionStore == nil || len(parts) < 2 {
			m.appendHistory(styleMuted.Render("Usage: /resume <session-id|last>\n\n"))
			return true, nil
		}
		var loaded *InteractiveSession
		var err error
		if strings.EqualFold(parts[1], "last") {
			loaded, err = m.sessionStore.Latest()
		} else {
			loaded, err = m.sessionStore.Load(parts[1])
		}
		if err == nil {
			m.closeSession()
			err = m.loadSession(loaded)
		}
		if err != nil {
			m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Cannot resume session: %v\n\n", err)))
		} else {
			m.historyText.Reset()
			m.appendHistory(m.formatWelcome())
			m.appendSessionTranscript()
			m.statusNotice = "Resumed " + loaded.Title
		}
		return true, m.clearStatusAfter(3 * time.Second)

	case "/new":
		if err := m.startNewSession(); err != nil {
			m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Cannot start session: %v\n\n", err)))
			return true, nil
		}
		m.historyText.Reset()
		m.appendHistory(m.formatWelcome())
		m.statusNotice = "Started a new session."
		return true, m.clearStatusAfter(2 * time.Second)

	case "/set":
		if len(parts) == 1 {
			m.appendHistory(FormatRuntimeCard(m.maxTurns, m.commandTimeout, m.thinkLevel, m.allowCommands, m.permissionMode, m.activeSessionID()) + "\n\n")
			return true, nil
		}
		if len(parts) < 3 {
			m.appendHistory(styleMuted.Render("Usage: /set <turns|timeout|think|commands|permissions|output> <value>\n\n"))
			return true, nil
		}
		if err := m.setRuntimeValue(parts[1], parts[2]); err != nil {
			m.appendHistory(styleDiffDel.Render(err.Error() + "\n\n"))
		} else {
			_ = m.saveSession()
			m.appendHistory(FormatRuntimeCard(m.maxTurns, m.commandTimeout, m.thinkLevel, m.allowCommands, m.permissionMode, m.activeSessionID()) + "\n\n")
		}
		return true, nil
	}
	return false, nil
}

func (m *teaModel) setRuntimeValue(name, value string) error {
	name, value = strings.ToLower(strings.TrimSpace(name)), strings.ToLower(strings.TrimSpace(value))
	switch name {
	case "turns":
		turns, err := strconv.Atoi(value)
		if err != nil || turns < 1 || turns > 100 {
			return fmt.Errorf("turns must be between 1 and 100")
		}
		m.maxTurns = turns
	case "timeout":
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds < 1 || seconds > 3600 {
			return fmt.Errorf("timeout must be between 1 and 3600 seconds")
		}
		m.commandTimeout = time.Duration(seconds) * time.Second
	case "think":
		if value == "off" {
			m.thinkLevel = ""
		} else if value == "low" || value == "medium" || value == "high" {
			m.thinkLevel = value
		} else {
			return fmt.Errorf("think must be off, low, medium, or high")
		}
	case "commands":
		if value == "on" {
			m.allowCommands = true
		} else if value == "off" {
			m.allowCommands = false
		} else {
			return fmt.Errorf("commands must be on or off")
		}
	case "permissions", "permission":
		mode, err := ParsePermissionMode(value)
		if err != nil {
			return err
		}
		m.permissionMode = mode
		m.sessionGrants = make(map[string]bool)
	case "output":
		if value == "expanded" {
			m.expandedTools = true
		} else if value == "compact" {
			m.expandedTools = false
		} else {
			return fmt.Errorf("output must be compact or expanded")
		}
	default:
		return fmt.Errorf("unknown setting %q", name)
	}
	return nil
}
