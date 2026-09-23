package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fastllm/internal/config"
	"fastllm/internal/llm"
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
type teaPermissionRequestMsg struct {
	ToolName  string
	Summary   string
	Workspace string
	Reply     chan permissionDecision
}
type permissionDecision struct {
	Allow        bool
	GrantSession bool
}
type teaShellDoneMsg struct {
	Output       string
	Err          error
	ShellCommand bool
	Canceled     bool
}
type teaImageDoneMsg struct {
	Path     string
	Bytes    int
	Duration time.Duration
	Err      error
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
	skills        []Skill
	settings      *config.Settings
	systemPrompt  string

	// UI components
	viewport viewport.Model
	input    textarea.Model
	spinner  spinner.Model

	// Viewport content
	historyText strings.Builder

	// Execution state
	isExecuting       bool
	hasResponseTurn   bool
	stream            streamBuffer
	streamHeaderShown bool
	agentWorkStarted  bool
	lastResponse      string
	activeTurn        int
	activeTool        string
	activeArgs        string
	cancelTurn        context.CancelFunc
	cancelShell       context.CancelFunc
	shellExecuting    bool
	eventChan         chan Event
	permissionChan    chan teaPermissionRequestMsg
	statusNotice      string
	latestMetrics     *TurnMetrics
	pendingPrompt     string
	taskStarted       time.Time

	// Persistent conversation and runtime state.
	sessionStore      *SessionStore
	activeSession     *InteractiveSession
	sessionMessages   []llm.Message
	sessionMetrics    SessionMetrics
	maxTurns          int
	commandTimeout    time.Duration
	thinkLevel        string
	allowCommands     bool
	permissionMode    PermissionMode
	expandedTools     bool
	permissions       *PermissionController
	pendingPermission *teaPermissionRequestMsg
	skillsModal       bool
	skillCursor       int

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
	skills := DiscoverWorkspaceSkills(absWorkingDir)

	checkpointMgr := NewCheckpointManager(absWorkingDir)
	processMgr := NewProcessManager()
	settings, configPath, err := config.LoadSettings(absWorkingDir)
	if err != nil {
		return nil, err
	}
	maxTurns := req.MaxTurns
	if maxTurns <= 0 {
		maxTurns = runner.DefaultMaxTurns
	}
	if maxTurns <= 0 {
		maxTurns = 50
	}
	commandTimeout := req.CommandTimeout
	if commandTimeout <= 0 {
		commandTimeout = runner.CommandTimeout
	}
	if commandTimeout <= 0 {
		commandTimeout = 60 * time.Second
	}
	permissionMode := req.PermissionMode
	if permissionMode == "" {
		permissionMode = PermissionAsk
	}

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
		runner:         runner,
		workingDir:     absWorkingDir,
		modelName:      modelName,
		mode:           modeAgent,
		configPath:     configPath,
		checkpointMgr:  checkpointMgr,
		processMgr:     processMgr,
		rules:          rules,
		skills:         skills,
		settings:       settings,
		systemPrompt:   DefaultSystemPrompt + rulesPrompt + formatSkillCatalogPrompt(skills),
		input:          ta,
		spinner:        sp,
		promptHistory:  hist,
		historyIdx:     -1,
		maxTurns:       maxTurns,
		commandTimeout: commandTimeout,
		thinkLevel:     req.ThinkLevel,
		allowCommands:  req.AllowCommands,
		permissionMode: permissionMode,
		permissionChan: make(chan teaPermissionRequestMsg),
	}
	if err := m.initializeSession(req.ResumeSession); err != nil {
		m.statusNotice = "Session recovery failed: " + err.Error()
	}

	// Initial welcome message in history
	m.appendHistory(m.formatWelcome())
	m.appendSessionTranscript()

	return m, nil
}

func (m *teaModel) formatWelcome() string {
	banner := FormatWelcomeBanner(
		m.workingDir,
		m.modelName,
		m.configPath,
		m.checkpointMgr.IsGitRepo(),
		len(m.rules),
		m.allowCommands,
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

// frameWidth is the width every full-width row is built to.
//
// It stops one column short of the terminal. A row that fills the final column
// leaves the cursor in a wrap-pending state, and many terminals (Windows Terminal
// among them) then emit an extra row, which changes the frame height and shifts
// the whole UI up or down as content changes. Giving the frame one spare column
// makes every row unambiguously one row.
func (m *teaModel) frameWidth() int {
	if m.width < 2 {
		return 1
	}
	return m.width - 1
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

		m.input.SetWidth(msg.Width - 6)
		// Width changed, so the text re-wraps: recompute the box height before
		// handing the remaining rows to the viewport.
		m.syncInputHeight()

		if !m.ready {
			m.viewport = viewport.New(m.frameWidth(), 3)
			m.viewport.SetContent(m.historyText.String())
			m.ready = true
			m.resizeViewport()
			m.viewport.GotoBottom()
		} else {
			m.viewport.Width = m.frameWidth()
			m.resizeViewport()
		}

	case tea.KeyMsg:
		if m.pendingPermission != nil {
			if msg.Type == tea.KeyCtrlC {
				m.resolvePermission(false, false)
				if m.cancelTurn != nil {
					m.cancelTurn()
				}
				return m, m.waitForNextEvent()
			}
			if msg.Type == tea.KeyEnter || msg.Type == tea.KeyEsc {
				m.resolvePermission(false, false)
				return m, m.waitForNextEvent()
			}
			if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
				switch msg.Runes[0] {
				case 'y', 'Y':
					m.resolvePermission(true, false)
					return m, m.waitForNextEvent()
				case 'a', 'A':
					m.resolvePermission(true, true)
					return m, m.waitForNextEvent()
				case 'n', 'N':
					m.resolvePermission(false, false)
					return m, m.waitForNextEvent()
				}
			}
			return m, nil
		}
		if m.skillsModal {
			switch msg.Type {
			case tea.KeyEsc, tea.KeyCtrlC:
				m.closeSkillsModal()
			case tea.KeyEnter:
				m.selectSkillFromModal()
			case tea.KeyUp:
				if m.skillCursor > 0 {
					m.skillCursor--
				}
			case tea.KeyDown:
				if m.skillCursor < len(m.skills)-1 {
					m.skillCursor++
				}
			case tea.KeyHome:
				m.skillCursor = 0
			case tea.KeyEnd:
				if len(m.skills) > 0 {
					m.skillCursor = len(m.skills) - 1
				}
			case tea.KeyRunes:
				if len(msg.Runes) > 0 {
					switch msg.Runes[0] {
					case 'q', 'Q':
						m.closeSkillsModal()
					case 'j':
						if m.skillCursor < len(m.skills)-1 {
							m.skillCursor++
						}
					case 'k':
						if m.skillCursor > 0 {
							m.skillCursor--
						}
					}
				}
			}
			return m, nil
		}
		// A bracketed paste arrives as one key event carrying the whole
		// clipboard, newlines included. Insert it literally -- treating those
		// newlines as Enter submits the paste one line at a time, which also
		// starts one agent turn per line.
		if msg.Paste {
			m.input.InsertString(string(msg.Runes))
			return m, nil
		}

		switch msg.Type {
		case tea.KeyCtrlC:
			if m.isExecuting && m.cancelTurn != nil {
				m.cancelActiveOperation()
				cmds = append(cmds, m.clearStatusAfter(3*time.Second))
				return m, tea.Batch(cmds...)
			}
			if m.shellExecuting && m.cancelShell != nil {
				m.cancelActiveOperation()
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
			if m.isExecuting || m.shellExecuting {
				m.cancelActiveOperation()
				cmds = append(cmds, m.clearStatusAfter(3*time.Second))
				return m, tea.Batch(cmds...)
			}
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
				previewLines := 4
				if m.expandedTools {
					previewLines = 10000
				}
				m.appendHistory(FormatToolResult(ev.ToolCall.Name, ev.ToolCall.Result, previewLines) + "\n\n")
			}

		case EventTokenDelta:
			if ready := m.stream.Add(ev.Response); ready != "" {
				if !m.streamHeaderShown {
					m.appendHistory(streamAnswerHeader())
					m.streamHeaderShown = true
				}
				m.appendHistory(FormatMarkdownWidth(ready, m.contentWidth()) + "\n")
			}

		case EventTokenDiscard:
			m.retractStreamedText()

		case EventTurnComplete:
			// Whether this turn streamed is decided by what actually reached the
			// transcript, never by stream.Active(): the buffer is marked active at
			// turn start, before any token arrives, so a turn that streamed nothing
			// (a provider that cannot stream, or a reply sent whole) would take the
			// streaming branch and render no answer at all.
			tail := m.stream.Flush()
			streamedAnything := m.streamHeaderShown || tail != ""
			switch {
			case streamedAnything:
				if strings.TrimSpace(tail) != "" {
					// The final line usually arrives without a trailing newline, and
					// a one-line answer never showed a header, so write it here.
					if !m.streamHeaderShown {
						m.appendHistory(streamAnswerHeader())
						m.streamHeaderShown = true
					}
					m.appendHistory(FormatMarkdownWidth(tail, m.contentWidth()) + "\n")
				}
				if ev.Response != "" {
					m.lastResponse = ev.Response
					m.hasResponseTurn = true
				}
				m.appendHistory("\n")
				m.streamHeaderShown = false
			case ev.Response != "":
				m.lastResponse = ev.Response
				m.hasResponseTurn = true
				m.appendHistory(formatAssistantAnswer(ev.Response, m.contentWidth()))
			}
			if ev.Metrics != nil {
				m.latestMetrics = ev.Metrics
				m.sessionMetrics.Add(*ev.Metrics)
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
			response := m.lastResponse
			if ev.Result != nil && strings.TrimSpace(ev.Result.FinalResponse) != "" {
				response = ev.Result.FinalResponse
			}
			m.recordCompletedPrompt(response)
		}
		// Listen for next event
		if m.eventChan != nil {
			cmds = append(cmds, m.waitForNextEvent())
		}

	case teaPermissionRequestMsg:
		if msg.Workspace != "" && msg.Workspace != m.workingDir {
			msg.Reply <- permissionDecision{Allow: false}
			cmds = append(cmds, m.waitForNextEvent())
		} else if m.permissionController().HasGrant(msg.ToolName) {
			msg.Reply <- permissionDecision{Allow: true}
			cmds = append(cmds, m.waitForNextEvent())
		} else {
			m.pendingPermission = &msg
			m.input.Blur()
			m.statusNotice = "Permission required for " + msg.ToolName
		}

	case teaShellDoneMsg:
		if msg.ShellCommand {
			m.shellExecuting = false
			m.cancelShell = nil
		}
		if msg.Output != "" {
			m.appendHistory(msg.Output + "\n")
		}
		if msg.Canceled {
			m.appendHistory(styleMuted.Render("[Command canceled]\n"))
		} else if msg.Err != nil {
			m.appendHistory(styleDiffDel.Render(fmt.Sprintf("$ %v\n", msg.Err)))
		}
		m.appendHistory("\n")

	case teaImageDoneMsg:
		if msg.Err != nil {
			m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Image generation failed: %v\n\n", msg.Err)))
		} else {
			m.appendHistory(styleStatusNotice.Render(fmt.Sprintf("Image saved: %s", msg.Path)) + "\n")
			m.appendHistory(styleMuted.Render(fmt.Sprintf("  %d bytes · %.1fs\n\n", msg.Bytes, msg.Duration.Seconds())))
			m.statusNotice = "Image generated."
		}

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
	m.syncInputHeight()

	return m, tea.Batch(cmds...)
}

// Input box bounds. It starts at two rows to match the resting layout and grows
// with the text rather than scrolling it: a fixed two-row box pushes the line
// you are typing out of sight once the text wraps past it.
const (
	minInputRows = 2
	maxInputRows = 10
)

// inputDisplayRows reports how many terminal rows the textarea's content needs
// once soft-wrapped at its content width.
func inputDisplayRows(ta textarea.Model) int {
	width := ta.Width()
	if width < 1 {
		width = 1
	}
	rows := 0
	for _, line := range strings.Split(ta.Value(), "\n") {
		needed := (VisualLen(line) + width - 1) / width
		if needed < 1 {
			needed = 1
		}
		rows += needed
	}
	if rows < 1 {
		rows = 1
	}
	return rows
}

// syncInputHeight grows or shrinks the input box to fit its content and gives
// the viewport back exactly the rows the box did not take, so the frame height
// never changes.
func (m *teaModel) syncInputHeight() {
	desired := inputDisplayRows(m.input)
	if desired < minInputRows {
		desired = minInputRows
	}
	if desired > maxInputRows {
		desired = maxInputRows
	}
	if desired == m.input.Height() {
		return
	}
	m.input.SetHeight(desired)
	m.resizeViewport()
}

// resizeViewport recomputes the conversation height from the current input box
// size. Both the resize path and the initial layout go through here so they can
// never disagree about how many rows the frame uses.
func (m *teaModel) resizeViewport() {
	// One spare row is deliberate: it absorbs a history line that happens to be
	// wider than the viewport and wraps, so an over-wide line costs a row of
	// slack instead of pushing the frame past the bottom of the terminal.
	const headerRows, statusRows, boxBorderRows, spareRow = 2, 1, 2, 1
	vpHeight := m.height - headerRows - statusRows - boxBorderRows - spareRow - m.input.Height()
	if vpHeight < 3 {
		vpHeight = 3
	}
	m.viewport.Height = vpHeight
}

func (m *teaModel) clearStatusAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return teaStatusClearMsg{}
	})
}

func (m *teaModel) waitForNextEvent() tea.Cmd {
	return func() tea.Msg {
		select {
		case ev, ok := <-m.eventChan:
			if !ok {
				return nil
			}
			return teaAgentEventMsg(ev)
		case request := <-m.permissionChan:
			return request
		}
	}
}

func (m *teaModel) resolvePermission(allow, grant bool) {
	request := m.pendingPermission
	if request == nil {
		return
	}
	if allow && grant {
		m.permissionController().Grant(request.ToolName)
	}
	request.Reply <- permissionDecision{Allow: allow, GrantSession: grant}
	m.pendingPermission = nil
	m.statusNotice = ""
	m.input.Focus()
}

func (m *teaModel) cancelActiveOperation() {
	canceledAgent := m.cancelTurn != nil
	if m.cancelTurn != nil {
		m.cancelTurn()
	}
	canceledShell := m.cancelShell != nil
	if m.cancelShell != nil {
		m.cancelShell()
	}
	switch {
	case canceledAgent && canceledShell:
		m.statusNotice = "Canceled active operations."
	case canceledAgent:
		m.statusNotice = "Canceled active agent turn."
	case canceledShell:
		m.statusNotice = "Canceled active shell command."
	}
}

func (m *teaModel) handleShellSubmit(cmdStr string) tea.Cmd {
	if m.isExecuting || m.shellExecuting {
		m.statusNotice = "An operation is already running — Esc cancels it."
		return m.clearStatusAfter(3 * time.Second)
	}
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
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelShell = cancel
	m.shellExecuting = true

	root := m.workingDir
	return func() tea.Msg {
		result, err := runUserCommand(ctx, root, cmdStr)
		out := result.Output
		if result.Truncated {
			out = "[earlier output truncated]\n" + out
		}
		return teaShellDoneMsg{
			Output:       strings.TrimRight(string(out), "\r\n"),
			Err:          err,
			ShellCommand: true,
			Canceled:     ctx.Err() != nil,
		}
	}
}

func (m *teaModel) changeWorkingDirectory(path string) error {
	newDir, err := resolveInteractiveDirectory(m.workingDir, path)
	if err != nil {
		return err
	}
	settings, configPath, err := config.LoadSettings(newDir)
	if err != nil {
		return err
	}
	m.workingDir = newDir
	m.permissionController().SetWorkspace(newDir)
	m.checkpointMgr = NewCheckpointManager(newDir)
	m.rules = DiscoverWorkspaceRules(newDir)
	m.skills = DiscoverWorkspaceSkills(newDir)
	m.systemPrompt = DefaultSystemPrompt + FormatRulesForPrompt(m.rules) + formatSkillCatalogPrompt(m.skills)
	m.settings, m.configPath = settings, configPath
	m.statusNotice = "Directory changed to " + filepath.Base(newDir)
	_ = m.saveSession()
	return nil
}

func (m *teaModel) handleAgentSubmit(inputVal string) tea.Cmd {
	skillPrompt := ""
	// 1. Check for slash commands
	if strings.HasPrefix(inputVal, "/") {
		parts := strings.Fields(inputVal)
		cmd := strings.ToLower(parts[0])
		if handled, result := m.handleSessionSlash(inputVal, parts, cmd); handled {
			return result
		}

		switch cmd {
		case "/exit", "/quit":
			m.closeSession()
			return tea.Quit

		case "/help":
			m.appendHistory(styleUserPrompt.Render("❯ /help") + "\n")
			m.appendHistory(FormatHelp() + "\n\n")
			return nil

		case "/c", "/clear":
			m.sessionMessages = nil
			m.lastResponse = ""
			_ = m.saveSession()
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

		case "/compact":
			m.appendHistory(styleUserPrompt.Render("❯ /compact") + "\n")
			budget := m.compactionConfig()
			before := messageCharacterCount(m.sessionMessages)
			compacted, didCompact := ForceCompactMessages(m.sessionMessages, budget)
			if !didCompact {
				m.appendHistory(styleMuted.Render("Nothing to compact yet; the transcript has no completed older turns to collapse.\n\n"))
				return nil
			}
			m.sessionMessages = compacted
			after := messageCharacterCount(m.sessionMessages)
			if err := m.saveSession(); err != nil {
				m.appendHistory(styleMuted.Render("Session autosave failed: "+err.Error()) + "\n\n")
			}
			m.appendHistory(styleMuted.Render(fmt.Sprintf("Compacted context from %s to %s (%d%% of the %s budget).",
				formatCharCount(before), formatCharCount(after),
				after*100/budget.MaxTotalChars,
				formatCharCount(budget.MaxTotalChars))) + "\n\n")
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

		case "/skills":
			name, task := parseSkillInvocation(inputVal)
			if name == "" || (strings.EqualFold(name, "list") && task == "") {
				m.openSkillsModal()
				return nil
			}
			skill, ok := findSkill(m.skills, name)
			if !ok {
				m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Unknown skill %q. Use /skills to list project skills.\n\n", name)))
				return nil
			}
			if task == "" {
				m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
				m.appendHistory(formatSkillDetails(skill, m.workingDir) + "\n\n")
				return nil
			}
			inputVal = task
			skillPrompt = formatSkillPrompt(skill, m.workingDir)
			m.appendHistory(styleMuted.Render("Using skill "+skill.Name+" for this turn.") + "\n")

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

		case "/image":
			if len(parts) < 2 {
				m.appendHistory(styleMuted.Render("Usage: /image <prompt>\n\n"))
				return nil
			}
			prompt := strings.TrimSpace(inputVal[len(parts[0]):])
			endpoint := configuredImageEndpoint(m.settings)
			if endpoint == nil {
				m.appendHistory(styleDiffDel.Render("No image model is configured. Add a model whose id or name contains \"image\".\n\n"))
				return nil
			}
			m.appendHistory(styleUserPrompt.Render("❯ /image "+prompt) + "\n")
			m.appendHistory(styleMuted.Render("Generating image with "+endpoint.Name+"...") + "\n")
			workingDir := m.workingDir
			settings := m.settings
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
				defer cancel()
				started := time.Now()
				path, size, err := generateConfiguredImage(ctx, settings, workingDir, prompt)
				return teaImageDoneMsg{Path: path, Bytes: size, Duration: time.Since(started), Err: err}
			}

		case "/status":
			m.appendHistory(styleUserPrompt.Render("❯ /status") + "\n")
			card := FormatStatusCard(m.workingDir, m.modelName, len(m.rules), m.sessionMetrics, m.processMgr)
			m.appendHistory(card + "\n\n")
			m.appendHistory(FormatRuntimeCard(m.maxTurns, m.commandTimeout, m.thinkLevel, m.allowCommands, m.permissionMode, m.activeSessionID()) + "\n\n")
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
				_ = m.saveSession()
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
	//
	// One turn at a time. Without this a multi-line paste submits once per
	// pasted line, and the resulting workers race over the event channel.
	if m.isExecuting || m.shellExecuting {
		m.input.SetValue(inputVal)
		m.statusNotice = "An operation is already running — Esc cancels it."
		return m.clearStatusAfter(3 * time.Second)
	}
	m.appendHistory(formatSubmittedPrompt(inputVal))
	if notice := m.compactSessionContext(); notice != "" {
		m.appendHistory(styleMuted.Render("  "+notice) + "\n\n")
	}
	m.pendingPrompt = inputVal
	m.taskStarted = time.Now()
	m.isExecuting = true
	m.hasResponseTurn = false
	m.stream.Start()
	m.streamHeaderShown = false
	m.agentWorkStarted = false
	m.activeTurn = 1

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	// Capture the channel in a local: the worker below must send on *this*
	// turn's channel, not on whatever m.eventChan happens to hold when the
	// send runs. Reading the field at send time let one worker send into a
	// later turn's channel after that turn had closed it -- a panic.
	events := make(chan Event, 64)
	m.eventChan = events

	// Snapshot the transcript on the UI goroutine; the worker below must not read
	// m.sessionMessages while Update may be appending to it.
	priorMessages := m.initialMessages()
	systemPrompt := m.systemPrompt + skillPrompt

	// Capture run settings before the worker starts; directory and runtime
	// commands on the UI goroutine may change them while the model is running.
	req := RunRequest{
		Task:               inputVal,
		WorkingDir:         m.workingDir,
		Model:              m.modelName,
		SystemPrompt:       systemPrompt,
		MaxTurns:           m.maxTurns,
		CommandTimeout:     m.commandTimeout,
		ThinkLevel:         m.thinkLevel,
		AllowCommands:      m.allowCommands,
		CommandsConfigured: true,
		PermissionMode:     m.permissionMode,
		InitialMessages:    priorMessages,
		StreamTokens:       true,
	}
	permissionChan := m.permissionChan
	// Launch background task
	go func() {
		defer close(events)
		if req.PermissionMode == PermissionAsk {
			req.Authorize = func(toolName, summary string) bool {
				reply := make(chan permissionDecision, 1)
				request := teaPermissionRequestMsg{ToolName: toolName, Summary: summary, Workspace: req.WorkingDir, Reply: reply}
				select {
				case permissionChan <- request:
				case <-ctx.Done():
					return false
				}
				select {
				case decision := <-reply:
					return decision.Allow
				case <-ctx.Done():
					return false
				}
			}
		}
		// Run reports a failed turn by emitting EventTaskFinished and returning
		// the same error, so synthesizing one unconditionally printed every
		// failure twice — once with the runner's "LLM chat error on turn N"
		// wording and once with the bare error. The fallback still matters for
		// failures that happen before the loop starts (an unresolvable working
		// directory, say), which return without emitting anything. emit is
		// synchronous on this goroutine, so a plain flag is enough.
		finished := false
		_, err := m.runner.Run(ctx, req, func(ev Event) {
			if ev.Type == EventTaskFinished {
				finished = true
			}
			events <- ev
		})
		if err != nil && !finished {
			events <- Event{
				Type:  EventTaskFinished,
				Error: err.Error(),
			}
		}
	}()

	return m.waitForNextEvent()
}

func formatSubmittedPrompt(input string) string {
	label := lipgloss.NewStyle().Bold(true).Foreground(tuiColorCyan).Render(SymBullet + " YOU")
	return "\n" + label + "  " + styleUserPrompt.Render(input) + "\n"
}

func formatAssistantAnswer(response string, width int) string {
	label := lipgloss.NewStyle().Bold(true).Foreground(tuiColorGreen).Render(SymBullet + " ASSISTANT")
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

func (m *teaModel) openSkillsModal() {
	m.skillsModal = true
	if m.skillCursor >= len(m.skills) {
		m.skillCursor = 0
	}
	m.input.Blur()
}

func (m *teaModel) closeSkillsModal() {
	m.skillsModal = false
	m.input.Focus()
}

// selectSkillFromModal closes the picker and stages the highlighted skill as a
// /skills invocation, leaving the cursor after the trailing space so the task
// can be typed straight away. Staging rather than submitting keeps the skill
// from running against an empty task on a stray Enter.
func (m *teaModel) selectSkillFromModal() {
	if m.skillCursor < 0 || m.skillCursor >= len(m.skills) {
		m.closeSkillsModal()
		return
	}
	skill := m.skills[m.skillCursor]
	m.closeSkillsModal()
	m.input.SetValue("/skills " + skill.Name + " ")
	m.input.CursorEnd()
}

// compactionConfig sizes compaction against the active model's context window.
// Read through this rather than caching: /model and /dir both change the answer.
func (m *teaModel) compactionConfig() CompactionConfig {
	return CompactionConfigForModel(m.settings, m.modelName)
}

// contextUsage reports progress toward automatic compaction. The numerator must
// stay exactly what OnlineCompactMessages measures -- sessionMessages alone.
// systemPrompt travels as RunRequest.SystemPrompt and is never an element of
// sessionMessages, so counting it here (or the unsent draft) inflated the gauge
// by the whole prompt: ~18% of budget in a repo with a large skill catalog,
// enough to show red while compaction was still far from firing.
func (m *teaModel) contextUsage() (int, int) {
	return messageCharacterCount(m.sessionMessages), m.compactionConfig().MaxTotalChars
}

func formatContextGauge(used, budget, barWidth int) string {
	if budget <= 0 {
		return ""
	}
	if used < 0 {
		used = 0
	}
	percent := used * 100 / budget
	if percent > 100 {
		percent = 100
	}
	label := fmt.Sprintf("ctx %d%%", percent)
	if barWidth > 0 {
		filled := (percent*barWidth + 99) / 100
		if filled > barWidth {
			filled = barWidth
		}
		label = fmt.Sprintf("ctx [%s%s] %s/%s", strings.Repeat("█", filled), strings.Repeat("░", barWidth-filled), compactCount(used), compactCount(budget))
	}
	color := tuiColorGreen
	if percent >= 90 {
		color = tuiColorRed
	} else if percent >= 75 {
		color = tuiColorYellow
	}
	return lipgloss.NewStyle().Foreground(color).Render(label)
}

func (m *teaModel) renderSkillsModal() string {
	width, height := m.width, m.height
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	contentWidth := width - 8
	if contentWidth > 100 {
		contentWidth = 100
	}
	if contentWidth < 32 {
		contentWidth = 32
	}
	visibleRows := height - 8
	if visibleRows < 1 {
		visibleRows = 1
	}

	start := m.skillCursor - visibleRows/2
	if start < 0 {
		start = 0
	}
	if maxStart := len(m.skills) - visibleRows; start > maxStart && maxStart > 0 {
		start = maxStart
	}
	end := start + visibleRows
	if end > len(m.skills) {
		end = len(m.skills)
	}

	lines := []string{styleAgentBadge.Render(fmt.Sprintf("SKILLS · %d available", len(m.skills))), ""}
	if len(m.skills) == 0 {
		lines = append(lines, styleMuted.Render("No project or global skills found."))
	} else {
		nameWidth := 24
		if contentWidth < 52 {
			nameWidth = contentWidth - 6
		}
		for i := start; i < end; i++ {
			skill := m.skills[i]
			name := truncateText(skill.Name, nameWidth)
			descriptionWidth := contentWidth - nameWidth - 6
			row := fmt.Sprintf("  %-*s", nameWidth, name)
			if descriptionWidth >= 8 {
				row += "  " + truncateText(skillSummary(skill.Description, 160), descriptionWidth)
			}
			if i == m.skillCursor {
				row = lipgloss.NewStyle().Bold(true).Foreground(tuiColorDarkBg).Background(tuiColorCyan).Width(contentWidth).Render("› " + strings.TrimPrefix(row, "  "))
			}
			lines = append(lines, row)
		}
	}
	footer := "↑/↓ or j/k navigate · Enter select · Esc/q close"
	if len(m.skills) == 0 {
		footer = "Esc/q close"
	}
	lines = append(lines, "", styleMuted.Render(footer))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(tuiColorCyan).
		Background(tuiColorCardBg).
		Padding(0, 1).
		Width(contentWidth).
		Render(strings.Join(lines, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceBackground(tuiColorDarkBg))
}

func truncateText(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

// View implements tea.Model
func (m *teaModel) View() string {
	if !m.ready {
		return "Initializing fastllm..."
	}
	if m.skillsModal {
		return m.renderSkillsModal()
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
		rightInfo = fmt.Sprintf("%s Turn %d · %s", m.spinner.View(), m.activeTurn, time.Since(m.taskStarted).Round(time.Second))
		if m.activeTool != "" {
			rightInfo += " " + tuiToolBadge(m.activeTool)
		}
	} else if m.shellExecuting {
		rightInfo = fmt.Sprintf("%s Shell command", m.spinner.View())
	} else if m.statusNotice != "" {
		rightInfo = styleStatusNotice.Render(m.statusNotice)
	} else if m.latestMetrics != nil {
		rightInfo = styleMuted.Render(fmt.Sprintf("%.1fs • %.0f tok/s",
			m.latestMetrics.Duration.Seconds(), m.latestMetrics.TokensPerSecond))
	}

	headerLeft := lipgloss.JoinHorizontal(lipgloss.Center, brand, " ", modeBadge, " ", modelBadge, " ", dirBadge, agentBadge)
	leftWidth := lipgloss.Width(headerLeft)
	rightWidth := lipgloss.Width(rightInfo)
	gap := m.frameWidth() - leftWidth - rightWidth - 2
	if gap < 1 {
		gap = 1
	}
	// Every full-width row must be clamped to the terminal width. A row even one
	// column too wide is wrapped by the terminal into two, which shortens the
	// frame and shifts everything below it. rightInfo changes on every spinner
	// tick and whenever a status notice appears, so an unclamped header visibly
	// jitters the whole UI up and down while a turn runs.
	headerRow := clampToWidth(headerLeft+strings.Repeat(" ", gap)+rightInfo, m.frameWidth())
	sb.WriteString(headerRow + "\n")
	sb.WriteString(styleMuted.Render(strings.Repeat("─", m.frameWidth())) + "\n")

	// 2. Viewport (Conversation & Tool Call History)
	sb.WriteString(m.viewport.View() + "\n")

	// 3. Bottom Input Box with Rounded Border
	var borderCol lipgloss.Color = tuiColorBorder
	if m.mode == modeShell {
		borderCol = tuiColorYellow
	}
	inputContent := m.input.View()
	if m.pendingPermission != nil {
		inputContent = FormatPermissionPrompt(m.pendingPermission.ToolName, m.pendingPermission.Summary) +
			"\n" + FormatPermissionKeyLegend()
		borderCol = tuiColorYellow
	}
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderCol).
		Width(m.frameWidth() - 2).
		Render(inputContent)
	sb.WriteString(inputBox + "\n")

	// 4. Status Bar / Keymap Hints
	// Hints drop to a short form rather than wrapping: the full string is ~97
	// columns, so on a narrower terminal it silently became a second row and
	// pushed the frame past the terminal height.
	hints := "Tab: Mode  •  Enter: Send  •  ↑/↓: History  •  Ctrl+J: Newline  •  /copy: Copy  •  Ctrl+C: Quit"
	shortHints := "Tab: Mode  •  Enter: Send  •  Ctrl+C: Quit"
	if m.mode == modeShell {
		hints = "Tab: Agent  •  Enter: Run  •  ↑/↓: History  •  Ctrl+V: Paste  •  exit: Leave Shell"
		shortHints = "Tab: Agent  •  Enter: Run  •  exit: Leave Shell"
	}
	if m.isExecuting || m.shellExecuting {
		hints = "Esc: Cancel  •  Ctrl+C: Cancel"
		shortHints = hints
	}
	used, budget := m.contextUsage()
	gauge := formatContextGauge(used, budget, 10)
	if VisualLen(hints)+VisualLen(gauge)+2 > m.frameWidth() {
		hints = shortHints
	}
	if VisualLen(hints)+VisualLen(gauge)+2 > m.frameWidth() {
		gauge = formatContextGauge(used, budget, 0)
	}
	gap = m.frameWidth() - VisualLen(hints) - VisualLen(gauge) - 2
	if gap < 1 {
		gap = 1
	}
	status := hints + strings.Repeat(" ", gap) + gauge
	status = PadRight(clampToWidth(status, m.frameWidth()-2), m.frameWidth()-2)
	sb.WriteString(styleStatusBar.Render(status))

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

// clampToWidth truncates a rendered row to at most width display columns,
// preserving ANSI styling. A row wider than the terminal is wrapped by the
// terminal rather than clipped, which silently adds a line to the frame.
func clampToWidth(row string, width int) string {
	if width <= 0 || VisualLen(row) <= width {
		return row
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(row)
}
