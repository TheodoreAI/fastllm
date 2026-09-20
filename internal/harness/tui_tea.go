package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fastllm/internal/config"
	"fastllm/internal/webtools"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TUI Modes
type tuiMode int

const (
	modeAgent tuiMode = iota
	modeShell
)

// Lip Gloss Palette (matching FastLLM Zinc/Cyan dark aesthetic, zero emojis)
var (
	tuiColorCyan   = lipgloss.Color("#06B6D4") // Cyan 500
	tuiColorBlue   = lipgloss.Color("#38BDF8") // Sky 400
	tuiColorGreen  = lipgloss.Color("#10B981") // Emerald 500
	tuiColorYellow = lipgloss.Color("#F59E0B") // Amber 500
	tuiColorRed    = lipgloss.Color("#EF4444") // Red 500
	tuiColorPurple = lipgloss.Color("#A855F7") // Violet 500
	tuiColorMuted  = lipgloss.Color("#71717A") // Zinc 500
	tuiColorDarkBg = lipgloss.Color("#09090B") // Zinc 950
	tuiColorCardBg = lipgloss.Color("#18181B") // Zinc 900
	tuiColorBorder = lipgloss.Color("#27272A") // Zinc 800
	tuiColorWhite  = lipgloss.Color("#FAFAFA") // Zinc 50
)

// Lip Gloss Styles
var (
	styleBrand = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#000000")).
			Background(tuiColorCyan).
			Padding(0, 1)

	styleHeaderPill = lipgloss.NewStyle().
			Foreground(tuiColorMuted).
			Background(tuiColorCardBg).
			Padding(0, 1)

	styleAgentBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(tuiColorCyan).
			Background(tuiColorCardBg).
			Padding(0, 1)

	styleShellBadge = lipgloss.NewStyle().
			Bold(true).
			Foreground(tuiColorYellow).
			Background(tuiColorCardBg).
			Padding(0, 1)

	styleHeaderBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(tuiColorBorder).
			Padding(0, 1).
			MarginBottom(0)

	styleCard = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(tuiColorBorder).
			Background(tuiColorCardBg).
			Padding(0, 1).
			MarginBottom(1)

	styleUserPrompt = lipgloss.NewStyle().
			Bold(true).
			Foreground(tuiColorWhite)

	styleAssistant = lipgloss.NewStyle().
			Foreground(tuiColorWhite)

	styleMuted = lipgloss.NewStyle().
			Foreground(tuiColorMuted)

	styleStatusBar = lipgloss.NewStyle().
			Background(tuiColorCardBg).
			Foreground(tuiColorMuted).
			Padding(0, 1)

	styleStatusNotice = lipgloss.NewStyle().
				Foreground(tuiColorGreen).
				Bold(true)

	styleDiffAdd = lipgloss.NewStyle().Foreground(tuiColorGreen)
	styleDiffDel = lipgloss.NewStyle().Foreground(tuiColorRed)
	styleDiffHdr = lipgloss.NewStyle().Foreground(tuiColorCyan).Bold(true)
)

func tuiToolBadge(name string) string {
	var c lipgloss.Color
	switch name {
	case "read_file", "list_files", "search_files", "glob_files":
		c = tuiColorCyan
	case "write_file", "edit_file", "patch_file":
		c = tuiColorYellow
	case "web_search", "web_fetch":
		c = tuiColorBlue
	case "run_command", "process_status", "kill_process", "spawn_agent", "agent_status", "send_agent_message", "cancel_agent":
		c = tuiColorPurple
	case "finish_task":
		c = tuiColorGreen
	default:
		c = tuiColorWhite
	}
	return lipgloss.NewStyle().Bold(true).Foreground(c).Render("[" + name + "]")
}

// Bubble Tea Messages
type teaAgentEventMsg Event
type teaStatusClearMsg struct{}
type teaShellDoneMsg struct {
	Output string
	Err    error
}

// teaModel holds the Bubble Tea state
type teaModel struct {
	runner        *Runner
	workingDir    string
	modelName     string
	mode          tuiMode
	configPath    string
	checkpointMgr *CheckpointManager
	processMgr    *ProcessManager
	rules         []RuleFile
	settings      *config.Settings
	systemPrompt  string

	// UI components
	viewport viewport.Model
	input    textarea.Model
	spinner  spinner.Model

	// Viewport content
	historyText strings.Builder

	// Execution state
	isExecuting      bool
	hasResponseTurn  bool
	agentWorkStarted bool
	lastResponse     string
	activeTurn       int
	activeTool       string
	activeArgs       string
	cancelTurn       context.CancelFunc
	eventChan        chan Event
	statusNotice     string
	latestMetrics    *TurnMetrics

	// Prompt history navigation
	promptHistory []string
	historyIdx    int
	historyDraft  string

	// Geometry
	width  int
	height int
	ready  bool
}

// newTeaModel constructs the initial Bubble Tea model
func newTeaModel(runner *Runner, req RunRequest) (*teaModel, error) {
	workingDir := req.WorkingDir
	if workingDir == "" {
		workingDir = runner.DefaultWorkingDir
	}
	absWorkingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return nil, err
	}

	modelName := req.Model
	if modelName == "" {
		modelName = runner.DefaultModel
	}

	rules := DiscoverWorkspaceRules(absWorkingDir)
	rulesPrompt := FormatRulesForPrompt(rules)

	checkpointMgr := NewCheckpointManager(absWorkingDir)
	processMgr := NewProcessManager()
	settings, configPath, _ := config.LoadSettings(absWorkingDir)

	ta := textarea.New()
	ta.Placeholder = "Ask a question, enter a task, or type /help (Tab switches to Shell Mode)..."
	ta.Focus()
	ta.Prompt = "❯ "
	ta.CharLimit = 8192
	ta.SetHeight(2)
	ta.ShowLineNumbers = false

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(tuiColorCyan)

	hist := loadPromptHistory()

	m := &teaModel{
		runner:        runner,
		workingDir:    absWorkingDir,
		modelName:     modelName,
		mode:          modeAgent,
		configPath:    configPath,
		checkpointMgr: checkpointMgr,
		processMgr:    processMgr,
		rules:         rules,
		settings:      settings,
		systemPrompt:  DefaultSystemPrompt + rulesPrompt,
		input:         ta,
		spinner:       sp,
		promptHistory: hist,
		historyIdx:    -1,
	}

	// Initial welcome message in history
	m.appendHistory(m.formatWelcome())

	return m, nil
}

func (m *teaModel) formatWelcome() string {
	banner := FormatWelcomeBanner(
		m.workingDir,
		m.modelName,
		m.configPath,
		m.checkpointMgr.IsGitRepo(),
		len(m.rules),
		m.runner.AllowCommands,
	)
	return banner + "\n\n"
}

func (m *teaModel) appendHistory(text string) {
	m.historyText.WriteString(text)
	if m.ready {
		m.viewport.SetContent(m.historyText.String())
		m.viewport.GotoBottom()
	}
}

func (m *teaModel) contentWidth() int {
	w := m.width - 4
	if w < 40 {
		return 76
	}
	return w
}

// Init implements tea.Model
func (m *teaModel) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.spinner.Tick,
	)
}

// Update implements tea.Model
func (m *teaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		headerHeight := 2
		inputHeight := m.input.Height() + 3
		statusHeight := 1
		vpHeight := m.height - headerHeight - inputHeight - statusHeight
		if vpHeight < 5 {
			vpHeight = 5
		}

		if !m.ready {
			m.viewport = viewport.New(msg.Width, vpHeight)
			m.viewport.SetContent(m.historyText.String())
			m.viewport.GotoBottom()
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = vpHeight
		}
		m.input.SetWidth(msg.Width - 6)

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			if m.isExecuting && m.cancelTurn != nil {
				m.cancelTurn()
				m.statusNotice = "Canceled active agent turn."
				cmds = append(cmds, m.clearStatusAfter(3*time.Second))
				return m, tea.Batch(cmds...)
			}
			return m, tea.Quit

		case tea.KeyTab:
			// Toggle between Agent Mode and Shell Mode
			if m.mode == modeAgent {
				m.mode = modeShell
				m.input.Placeholder = "Enter shell command ($ go test, !git status, ls)..."
				m.statusNotice = "Engaged Shell Mode."
			} else {
				m.mode = modeAgent
				m.input.Placeholder = "Ask a question, enter a task, or type /help..."
				m.statusNotice = "Returned to Agent Mode."
			}
			cmds = append(cmds, m.clearStatusAfter(2*time.Second))
			return m, tea.Batch(cmds...)

		case tea.KeyCtrlV:
			clipText, err := clipboard.ReadAll()
			if err == nil && clipText != "" {
				m.input.InsertString(clipText)
				return m, nil
			}

		case tea.KeyCtrlJ:
			// Ctrl+J is universal terminal newline / linefeed
			m.input.InsertString("\n")
			return m, nil

		case tea.KeyEnter:
			if msg.Alt {
				m.input.InsertString("\n")
				return m, nil
			}
			val := m.input.Value()
			// If ends with backslash, allow multiline continuation
			if strings.HasSuffix(val, "\\") {
				m.input.SetValue(strings.TrimSuffix(val, "\\") + "\n")
				return m, nil
			}
			inputVal := strings.TrimSpace(val)
			if inputVal != "" {
				m.addPromptHistory(inputVal)
				m.historyIdx = -1
				m.historyDraft = ""
				m.input.Reset()
				if m.mode == modeShell {
					cmds = append(cmds, m.handleShellSubmit(inputVal))
				} else {
					cmds = append(cmds, m.handleAgentSubmit(inputVal))
				}
				return m, tea.Batch(cmds...)
			}

		case tea.KeyUp:
			if m.input.Line() == 0 && len(m.promptHistory) > 0 {
				if m.historyIdx == -1 {
					m.historyDraft = m.input.Value()
					m.historyIdx = len(m.promptHistory) - 1
				} else if m.historyIdx > 0 {
					m.historyIdx--
				}
				m.input.SetValue(m.promptHistory[m.historyIdx])
				m.input.CursorEnd()
				return m, nil
			}
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			return m, inputCmd

		case tea.KeyDown:
			if m.historyIdx != -1 && m.input.Line() >= m.input.LineCount()-1 {
				if m.historyIdx < len(m.promptHistory)-1 {
					m.historyIdx++
					m.input.SetValue(m.promptHistory[m.historyIdx])
					m.input.CursorEnd()
				} else {
					m.historyIdx = -1
					m.input.SetValue(m.historyDraft)
					m.input.CursorEnd()
				}
				return m, nil
			}
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			return m, inputCmd

		case tea.KeyEsc:
			if m.historyIdx != -1 {
				m.historyIdx = -1
				m.input.SetValue(m.historyDraft)
				m.input.CursorEnd()
				return m, nil
			}

		case tea.KeyPgUp:
			m.viewport.LineUp(5)
			return m, nil

		case tea.KeyPgDown:
			m.viewport.LineDown(5)
			return m, nil
		}

	case tea.MouseMsg:
		// Mouse-wheel events belong exclusively to the conversation viewport.
		// Never let them reach the textarea or prompt-history navigation.
		var viewportCmd tea.Cmd
		m.viewport, viewportCmd = m.viewport.Update(msg)
		return m, viewportCmd

	case teaAgentEventMsg:
		ev := Event(msg)
		switch ev.Type {
		case EventTurnStart:
			m.activeTurn = ev.Turn

		case EventToolCall:
			if ev.ToolCall != nil {
				m.startAgentWork(ev.Turn)
				m.activeTool = ev.ToolCall.Name
				m.activeArgs = summarizeArgs(ev.ToolCall.Arguments)
				m.appendHistory(FormatToolCall(ev.ToolCall.Name, m.activeArgs) + "\n")
			}

		case EventToolResult:
			if ev.ToolCall != nil {
				m.startAgentWork(ev.Turn)
				m.activeTool = ""
				m.activeArgs = ""
				m.appendHistory(FormatToolResult(ev.ToolCall.Name, ev.ToolCall.Result, 4) + "\n\n")
			}

		case EventTurnComplete:
			if ev.Response != "" {
				m.lastResponse = ev.Response
				m.hasResponseTurn = true
				m.appendHistory(formatAssistantAnswer(ev.Response, m.contentWidth()))
			}
			if ev.Metrics != nil {
				m.latestMetrics = ev.Metrics
				m.appendHistory(FormatTurnSummary(*ev.Metrics) + "\n\n")
			}

		case EventTaskFinished:
			m.isExecuting = false
			m.activeTool = ""
			m.activeArgs = ""
			m.cancelTurn = nil
			if ev.Error != "" {
				m.appendHistory(styleDiffDel.Render(fmt.Sprintf("\nTask failed: %s\n\n", ev.Error)))
			} else if ev.Result != nil {
				if !m.hasResponseTurn && strings.TrimSpace(ev.Result.FinalResponse) != "" {
					m.lastResponse = ev.Result.FinalResponse
					m.hasResponseTurn = true
					m.appendHistory(formatAssistantAnswer(ev.Result.FinalResponse, m.contentWidth()))
				}
				m.appendHistory(styleMuted.Render(fmt.Sprintf("─ Completed in %d turn(s) (%.1fs) ─\n\n",
					ev.Result.Turns, float64(ev.Result.DurationMS)/1000.0)))
			}
		}
		// Listen for next event
		if m.eventChan != nil {
			cmds = append(cmds, m.waitForNextEvent())
		}

	case teaShellDoneMsg:
		if msg.Output != "" {
			m.appendHistory(msg.Output + "\n")
		}
		if msg.Err != nil {
			m.appendHistory(styleDiffDel.Render(fmt.Sprintf("$ %v\n", msg.Err)))
		}
		m.appendHistory("\n")

	case teaStatusClearMsg:
		m.statusNotice = ""

	case spinner.TickMsg:
		var spCmd tea.Cmd
		m.spinner, spCmd = m.spinner.Update(msg)
		cmds = append(cmds, spCmd)
	}

	// Update viewport & input
	var vpCmd, inCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	m.input, inCmd = m.input.Update(msg)
	cmds = append(cmds, vpCmd, inCmd)

	return m, tea.Batch(cmds...)
}

func (m *teaModel) clearStatusAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return teaStatusClearMsg{}
	})
}

func (m *teaModel) waitForNextEvent() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-m.eventChan
		if !ok {
			return nil
		}
		return teaAgentEventMsg(ev)
	}
}

func (m *teaModel) handleShellSubmit(cmdStr string) tea.Cmd {
	if cmdStr == "exit" || cmdStr == "quit" || cmdStr == "/exit" || cmdStr == "/shell" {
		m.mode = modeAgent
		m.statusNotice = "Returned to Agent Mode."
		return m.clearStatusAfter(2 * time.Second)
	}
	if cmdStr == "/c" || cmdStr == "/clear" || cmdStr == "clear" || cmdStr == "cls" {
		m.historyText.Reset()
		m.appendHistory(m.formatWelcome())
		m.statusNotice = "History cleared."
		return m.clearStatusAfter(2 * time.Second)
	}
	if path, ok := parseDirectoryChange(cmdStr); ok {
		if err := m.changeWorkingDirectory(path); err != nil {
			m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Cannot change directory: %v\n\n", err)))
		} else {
			m.appendHistory(styleStatusNotice.Render(fmt.Sprintf("Working directory changed to %s\n\n", m.workingDir)))
		}
		return nil
	}

	m.appendHistory(styleShellBadge.Render("$ "+cmdStr) + "\n")

	return func() tea.Msg {
		cmd := newInteractiveShellCommand(cmdStr)
		cmd.Dir = m.workingDir
		out, err := cmd.CombinedOutput()
		return teaShellDoneMsg{
			Output: strings.TrimRight(string(out), "\r\n"),
			Err:    err,
		}
	}
}

func (m *teaModel) changeWorkingDirectory(path string) error {
	newDir, err := resolveInteractiveDirectory(m.workingDir, path)
	if err != nil {
		return err
	}
	m.workingDir = newDir
	m.checkpointMgr = NewCheckpointManager(newDir)
	m.rules = DiscoverWorkspaceRules(newDir)
	m.systemPrompt = DefaultSystemPrompt + FormatRulesForPrompt(m.rules)
	m.statusNotice = "Directory changed to " + filepath.Base(newDir)
	return nil
}

func (m *teaModel) handleAgentSubmit(inputVal string) tea.Cmd {
	// 1. Check for slash commands
	if strings.HasPrefix(inputVal, "/") {
		parts := strings.Fields(inputVal)
		cmd := strings.ToLower(parts[0])

		switch cmd {
		case "/exit", "/quit":
			return tea.Quit

		case "/help":
			m.appendHistory(styleUserPrompt.Render("❯ /help") + "\n")
			m.appendHistory(FormatHelp() + "\n\n")
			return nil

		case "/c", "/clear":
			m.historyText.Reset()
			m.appendHistory(m.formatWelcome())
			m.statusNotice = "Screen and conversation cleared."
			return m.clearStatusAfter(2 * time.Second)

		case "/cls":
			m.historyText.Reset()
			m.appendHistory(m.formatWelcome())
			return nil

		case "/shell", "/sh":
			if len(parts) > 1 {
				cmdStr := strings.TrimSpace(inputVal[len(parts[0]):])
				return m.handleShellSubmit(cmdStr)
			}
			m.mode = modeShell
			m.input.Placeholder = "Enter shell command ($ go test, !git status, ls)..."
			m.statusNotice = "Engaged Shell Mode."
			return m.clearStatusAfter(2 * time.Second)

		case "/diff":
			m.appendHistory(styleUserPrompt.Render("❯ /diff") + "\n")
			if !m.checkpointMgr.IsGitRepo() {
				m.appendHistory(styleMuted.Render("Not a git repository.\n\n"))
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			diff, err := m.checkpointMgr.Diff(ctx)
			cancel()
			if err != nil {
				m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Diff error: %v\n\n", err)))
			} else if strings.TrimSpace(diff) == "" {
				m.appendHistory(styleMuted.Render("Working tree clean (no uncommitted changes).\n\n"))
			} else {
				m.appendHistory(HighlightDiff(diff) + "\n\n")
			}
			return nil

		case "/undo":
			m.appendHistory(styleUserPrompt.Render("❯ /undo") + "\n")
			if !m.checkpointMgr.IsGitRepo() {
				m.appendHistory(styleMuted.Render("Not a git repository.\n\n"))
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := m.checkpointMgr.Rollback(ctx, "")
			cancel()
			if err != nil {
				m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Undo failed: %v\n\n", err)))
			} else {
				m.appendHistory(styleStatusNotice.Render("Successfully rolled back working tree to pre-turn checkpoint.\n\n"))
			}
			return nil

		case "/rules":
			m.appendHistory(styleUserPrompt.Render("❯ /rules") + "\n")
			if len(m.rules) == 0 {
				m.appendHistory(styleMuted.Render("No workspace rules found.\n\n"))
			} else {
				for _, r := range m.rules {
					m.appendHistory(fmt.Sprintf("File: %s (%s)\n%s\n\n", r.Filename, r.Path, styleMuted.Render(r.Content)))
				}
			}
			return nil

		case "/search":
			if len(parts) < 2 {
				m.appendHistory(styleMuted.Render("Usage: /search <query>\n\n"))
				return nil
			}
			q := strings.TrimSpace(inputVal[len(parts[0]):])
			m.appendHistory(styleUserPrompt.Render("❯ /search "+q) + "\n")
			m.appendHistory(styleMuted.Render("Searching web...") + "\n")
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				res := webtools.SearchFormatted(ctx, q, 5)
				cancel()
				return teaShellDoneMsg{Output: res}
			}

		case "/fetch":
			if len(parts) < 2 {
				m.appendHistory(styleMuted.Render("Usage: /fetch <url>\n\n"))
				return nil
			}
			u := strings.TrimSpace(parts[1])
			m.appendHistory(styleUserPrompt.Render("❯ /fetch "+u) + "\n")
			m.appendHistory(styleMuted.Render("Fetching web page...") + "\n")
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				content, err := webtools.Fetch(ctx, u, 16384)
				cancel()
				return teaShellDoneMsg{Output: content, Err: err}
			}

		case "/status":
			m.appendHistory(styleUserPrompt.Render("❯ /status") + "\n")
			card := FormatStatusCard(m.workingDir, m.modelName, len(m.rules), SessionMetrics{}, m.processMgr)
			m.appendHistory(card + "\n\n")
			if summary := m.runner.agents.Summary(); summary.Total > 0 {
				m.appendHistory(FormatCard("Child Agents", strings.Split(m.runner.agents.Status(""), "\n"), 74) + "\n\n")
			}
			return nil

		case "/model", "/models":
			m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
			if len(parts) > 1 && parts[1] != "list" {
				matched := m.settings.FindModel(parts[1])
				if matched == nil {
					m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Model %q is not configured. Use /models to list available models.\n\n", parts[1])))
					return nil
				}
				if err := m.runner.SwitchModel(matched); err != nil {
					m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Could not switch model: %v\n\n", err)))
					return nil
				}
				m.modelName = matched.ID
				m.statusNotice = fmt.Sprintf("Switched model to %s", matched.ID)
				m.appendHistory(styleStatusNotice.Render(fmt.Sprintf("Active model: %s (%s)\nEndpoint: %s", matched.ID, matched.Name, matched.URL)) + "\n\n")
				return m.clearStatusAfter(2 * time.Second)
			}
			if m.settings != nil && len(m.settings.Models) > 0 {
				m.appendHistory(FormatModelsTable(m.settings.Models, m.modelName, m.configPath) + "\n\n")
			} else {
				m.appendHistory(styleMuted.Render("No models configured. Usage: /model <name>\n\n"))
			}
			return nil

		case "/ps":
			m.appendHistory(styleUserPrompt.Render("❯ /ps") + "\n")
			m.appendHistory(m.processMgr.FormatProcessTable() + "\n\n")
			return nil

		case "/kill":
			m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
			if len(parts) < 2 {
				m.appendHistory(styleMuted.Render("Usage: /kill <process_id>\n\n"))
				return nil
			}
			if err := m.processMgr.Kill(parts[1]); err != nil {
				m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Kill error: %v\n\n", err)))
			} else {
				m.appendHistory(styleStatusNotice.Render(fmt.Sprintf("Killed process %s\n\n", parts[1])))
			}
			return nil

		case "/copy", "/yank":
			m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
			target := m.lastResponse
			if len(parts) > 1 && parts[1] == "all" {
				target = m.historyText.String()
			}
			if strings.TrimSpace(target) == "" {
				m.statusNotice = "Nothing to copy yet."
				m.appendHistory(styleMuted.Render("Nothing to copy yet.\n\n"))
			} else {
				err := clipboard.WriteAll(StripANSI(target))
				if err != nil {
					m.statusNotice = "Copy error: " + err.Error()
					m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Copy error: %v\n\n", err)))
				} else {
					m.statusNotice = "✓ Copied to clipboard."
					m.appendHistory(styleStatusNotice.Render("✓ Copied response to system clipboard.\n\n"))
				}
			}
			return m.clearStatusAfter(3 * time.Second)

		case "/dir":
			m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
			if len(parts) < 2 {
				m.appendHistory(styleMuted.Render("Usage: /dir <path>\n\n"))
				return nil
			}
			if err := m.changeWorkingDirectory(strings.Join(parts[1:], " ")); err != nil {
				m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Invalid path: %v\n\n", err)))
				return nil
			}
			m.appendHistory(styleStatusNotice.Render(fmt.Sprintf("Working directory changed to %s (%d rules discovered)\n\n", m.workingDir, len(m.rules))))
			return m.clearStatusAfter(2 * time.Second)
		}
	}

	// 2. Direct instant shell execution with !<cmd> or $ <cmd>
	if strings.HasPrefix(inputVal, "!") || strings.HasPrefix(inputVal, "$ ") {
		cmdStr := strings.TrimSpace(inputVal[1:])
		if strings.HasPrefix(inputVal, "$ ") {
			cmdStr = strings.TrimSpace(inputVal[2:])
		}
		return m.handleShellSubmit(cmdStr)
	}

	// 3. Autonomous Agent Turn Submission
	m.appendHistory(formatSubmittedPrompt(inputVal))
	m.isExecuting = true
	m.hasResponseTurn = false
	m.agentWorkStarted = false
	m.activeTurn = 1

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	m.eventChan = make(chan Event, 64)

	// Launch background task
	go func() {
		defer close(m.eventChan)
		req := RunRequest{
			Task:           inputVal,
			WorkingDir:     m.workingDir,
			Model:          m.modelName,
			SystemPrompt:   m.systemPrompt,
			AllowCommands:  m.runner.AllowCommands,
			PermissionMode: PermissionAuto,
		}
		_, err := m.runner.Run(ctx, req, func(ev Event) {
			m.eventChan <- ev
		})
		if err != nil {
			m.eventChan <- Event{
				Type:  EventTaskFinished,
				Error: err.Error(),
			}
		}
	}()

	return m.waitForNextEvent()
}

func formatSubmittedPrompt(input string) string {
	label := lipgloss.NewStyle().Bold(true).Foreground(tuiColorCyan).Render("YOU")
	return "\n" + label + "  " + styleUserPrompt.Render(input) + "\n"
}

func formatAssistantAnswer(response string, width int) string {
	label := lipgloss.NewStyle().Bold(true).Foreground(tuiColorGreen).Render("ASSISTANT")
	return "\n" + label + "\n" + FormatMarkdownWidth(response, width) + "\n\n"
}

func (m *teaModel) startAgentWork(turn int) {
	if m.agentWorkStarted {
		return
	}
	m.agentWorkStarted = true
	label := fmt.Sprintf("AGENT WORK · TURN %d", turn)
	m.appendHistory("\n" + styleMuted.Bold(true).Render(label) + "\n")
}

// View implements tea.Model
func (m *teaModel) View() string {
	if !m.ready {
		return "Initializing fastllm..."
	}

	var sb strings.Builder

	// 1. Top Header Bar
	var modeBadge string
	if m.mode == modeAgent {
		modeBadge = styleAgentBadge.Render("◈ AGENT")
	} else {
		modeBadge = styleShellBadge.Render("❯_ SHELL")
	}

	brand := styleBrand.Render("fastllm")
	modelBadge := styleHeaderPill.Render(m.modelName)
	dirBase := filepath.Base(m.workingDir)
	if dirBase == "" || dirBase == "." {
		dirBase = m.workingDir
	}
	dirBadge := styleHeaderPill.Render(dirBase)
	agentBadge := ""
	if summary := m.runner.agents.Summary(); summary.Total > 0 {
		agentBadge = " " + styleHeaderPill.Render(fmt.Sprintf("agents %d/%d · %s tok", summary.Pending+summary.Running, summary.Total, compactCount(summary.TotalTokens)))
	}

	var rightInfo string
	if m.isExecuting {
		rightInfo = fmt.Sprintf("%s Turn %d", m.spinner.View(), m.activeTurn)
		if m.activeTool != "" {
			rightInfo += " " + tuiToolBadge(m.activeTool)
		}
	} else if m.statusNotice != "" {
		rightInfo = styleStatusNotice.Render(m.statusNotice)
	} else if m.latestMetrics != nil {
		rightInfo = styleMuted.Render(fmt.Sprintf("%.1fs • %.0f tok/s",
			m.latestMetrics.Duration.Seconds(), m.latestMetrics.TokensPerSecond))
	}

	headerLeft := lipgloss.JoinHorizontal(lipgloss.Center, brand, " ", modeBadge, " ", modelBadge, " ", dirBadge, agentBadge)
	leftWidth := lipgloss.Width(headerLeft)
	rightWidth := lipgloss.Width(rightInfo)
	gap := m.width - leftWidth - rightWidth - 2
	if gap < 1 {
		gap = 1
	}
	headerRow := headerLeft + strings.Repeat(" ", gap) + rightInfo
	sb.WriteString(headerRow + "\n")
	sb.WriteString(styleMuted.Render(strings.Repeat("─", m.width)) + "\n")

	// 2. Viewport (Conversation & Tool Call History)
	sb.WriteString(m.viewport.View() + "\n")

	// 3. Bottom Input Box with Rounded Border
	var borderCol lipgloss.Color = tuiColorBorder
	if m.mode == modeShell {
		borderCol = tuiColorYellow
	}
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderCol).
		Width(m.width - 2).
		Render(m.input.View())
	sb.WriteString(inputBox + "\n")

	// 4. Status Bar / Keymap Hints
	hints := "Tab: Mode  •  Enter: Send  •  ↑/↓: History  •  Ctrl+J: Newline  •  /copy: Copy  •  Ctrl+C: Quit"
	if m.mode == modeShell {
		hints = "Tab: Agent  •  Enter: Run  •  ↑/↓: History  •  Ctrl+V: Paste  •  exit: Leave Shell"
	}
	sb.WriteString(styleStatusBar.Width(m.width).Render(hints))

	return sb.String()
}

// Global program reference for async event dispatches
var teaModelProg *tea.Program

// RunBubbleTea launches the full-screen Bubble Tea TUI
func (r *Runner) RunBubbleTea(req RunRequest) error {
	initConsole()

	model, err := newTeaModel(r, req)
	if err != nil {
		return err
	}

	p := tea.NewProgram(
		model,
		tea.WithAltScreen(), // Clean full-screen TUI buffer
		tea.WithMouseCellMotion(),
	)
	teaModelProg = p

	_, err = p.Run()
	return err
}

func promptHistoryPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".fastllm", "history")
}

func loadPromptHistory() []string {
	p := promptHistoryPath()
	if p == "" {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	raw := strings.Split(string(data), "\n")
	var lines []string
	for _, l := range raw {
		trimmed := strings.TrimSpace(strings.TrimRight(l, "\r"))
		if trimmed != "" {
			restored := strings.ReplaceAll(trimmed, "\\n", "\n")
			lines = append(lines, restored)
		}
	}
	if len(lines) > 500 {
		lines = lines[len(lines)-500:]
	}
	return lines
}

func savePromptHistoryEntry(entry string) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return
	}
	p := promptHistoryPath()
	if p == "" {
		return
	}
	dir := filepath.Dir(p)
	_ = os.MkdirAll(dir, 0o755)

	singleLine := strings.ReplaceAll(entry, "\r\n", "\\n")
	singleLine = strings.ReplaceAll(singleLine, "\n", "\\n")

	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(singleLine + "\n")
}

func (m *teaModel) addPromptHistory(val string) {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return
	}
	if n := len(m.promptHistory); n > 0 && m.promptHistory[n-1] == trimmed {
		return
	}
	m.promptHistory = append(m.promptHistory, trimmed)
	savePromptHistoryEntry(trimmed)
}
