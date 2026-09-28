package harness

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"fastllm/internal/config"
	"fastllm/internal/execution"
	"fastllm/internal/gitrepo"
	"fastllm/internal/llm"
	"fastllm/internal/webtools"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
)

// TUI Modes
type tuiMode int

const (
	modeAgent tuiMode = iota
	modeShell
)

// Palette. ApplyTheme (theme.go) assigns every colour from the active theme;
// nothing here is a literal so a theme switch reaches all of the chrome.
var (
	tuiColorCyan    color.Color
	tuiColorBlue    color.Color
	tuiColorGreen   color.Color
	tuiColorYellow  color.Color
	tuiColorRed     color.Color
	tuiColorPurple  color.Color
	tuiColorMuted   color.Color
	tuiColorBg      color.Color
	tuiColorCardBg  color.Color
	tuiColorBorder  color.Color
	tuiColorTrack   color.Color
	tuiColorWhite   color.Color
	tuiColorBrandFg color.Color
)

// Lip Gloss Styles, rebuilt by buildStyles whenever the theme changes.
var (
	styleBrand        lipgloss.Style
	styleHeaderPill   lipgloss.Style
	styleAgentBadge   lipgloss.Style
	styleShellBadge   lipgloss.Style
	styleHeaderBox    lipgloss.Style
	styleCard         lipgloss.Style
	styleUserPrompt   lipgloss.Style
	styleAssistant    lipgloss.Style
	styleMuted        lipgloss.Style
	styleStatusBar    lipgloss.Style
	styleStatusNotice lipgloss.Style
	styleDiffAdd      lipgloss.Style
	styleDiffDel      lipgloss.Style
	styleDiffHdr      lipgloss.Style
	styleHoverRow     lipgloss.Style
)

func buildStyles() {
	styleHoverRow = lipgloss.NewStyle().
		Foreground(tuiColorCyan).
		Background(tuiColorCardBg)

	styleBrand = lipgloss.NewStyle().
		Bold(true).
		Foreground(tuiColorBrandFg).
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
}

func tuiToolBadge(name string) string {
	var c color.Color
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
	Scope     GrantScope // what "for this session" would grant
	Workspace string
	Reply     chan permissionDecision
}

// scope is the request's grant scope. A request built without one (older
// callers, tests) grants only its own tool label.
func (msg teaPermissionRequestMsg) scope() GrantScope {
	if msg.Scope.Tool == "" {
		return GrantScope{Tool: msg.ToolName, Kind: scopeTool}
	}
	return msg.Scope
}

func (msg teaPermissionRequestMsg) consent() ConsentRequest {
	return ConsentRequest{Tool: msg.ToolName, Summary: msg.Summary, Scope: msg.scope()}
}

type permissionDecision struct {
	Allow        bool
	GrantSession bool
	Via          string // how it was decided, for the audit log
}
type teaShellDoneMsg struct {
	Command      string
	Output       string
	Err          error
	ShellCommand bool
	Canceled     bool
	Duration     time.Duration
	ClearScreen  bool
	ProcID       string
}
type teaGitWatchMsg struct{}
type teaGitRefreshMsg struct {
	status gitrepo.RepoStatus
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

	// UI components
	viewport viewport.Model
	input    textarea.Model
	spinner  spinner.Model

	// Viewport content
	historyText strings.Builder

	// Execution state
	isExecuting         bool
	hasResponseTurn     bool
	stream              streamBuffer
	streamHeaderShown   bool
	agentWorkStarted    bool
	lastResponse        string
	activeTurn          int
	activeTool          string
	activeArgs          string
	cancelTurn          context.CancelFunc
	cancelShell         context.CancelFunc
	shellExecuting      bool
	currentShellProc    *BackgroundProcess
	currentShellExec    execution.Process
	shellBackgrounded   bool
	shellTracker        *ShellActivityTracker
	lastAgentSubmitTime time.Time
	eventChan           chan Event
	permissionChan      chan teaPermissionRequestMsg
	statusNotice        string
	latestMetrics       *TurnMetrics
	// overhead caches what every request carries besides the transcript (the
	// system prompt and tool schemas) and the settings it was measured under.
	overhead           requestOverhead
	pendingPrompt      string
	pendingAttachments []llm.Attachment
	taskStarted        time.Time

	// Persistent conversation and runtime state.
	sessionStore      *SessionStore
	activeSession     *InteractiveSession
	sessionMessages   []llm.Message
	sessionMetrics    SessionMetrics
	maxTurns          int
	commandTimeout    time.Duration
	thinkLevel        string
	allowCommands     bool
	sandbox           bool
	permissionMode    PermissionMode
	expandedTools     bool
	permissions       *PermissionController
	pendingPermission *teaPermissionRequestMsg
	// pendingPlan is a plan-mode run's submit_plan text awaiting the user's
	// decision; planCursor indexes planApprovals.
	pendingPlan string
	planCursor  int
	skillsModal bool
	skillCursor int
	modelsModal bool
	modelCursor int
	// sessionsModal is the open sessions menu; nil when closed.
	sessionsModal *sessionsPicker
	// themeModal is the open /theme picker; nil when closed.
	themeModal *themePicker
	// filesModal is the open /files browser modal; nil when closed.
	filesModal *filesPicker
	// editor is the open file editor (tui_editor.go); nil when closed.
	editor *editorSession
	// suggest is the slash-command dropdown above the input box.
	suggest suggestState
	// budget limits each prompt's run (budget.go); saved with the session.
	budget Budget
	// journal records the model's file writes for /undo (journal.go). It is
	// about files, not the conversation, so only /dir replaces it.
	journal *WriteJournal
	// taint records secret files this conversation has read (I10); it lives
	// as long as the messages that may hold them.
	taint   *SessionTaint
	changes sessionChanges
	// hits holds the click targets of the last frame drawn (tui_hits.go).
	hits hitMap
	// hover is the ID of the click target under the pointer, or "".
	hover        string
	gitWatchChan <-chan struct{}
	gitWatchStop func()

	// blurred is set while the terminal window is in the background; the
	// spinner stops ticking then. finishedAway records that a turn or shell
	// command ended meanwhile, for the window title.
	blurred      bool
	finishedAway bool
	// turnFailed marks the last agent turn as failed (not canceled) until the
	// next key press; the taskbar progress shows it in red.
	turnFailed bool
	// canceling is set when the user cancels, so the failure that the
	// cancellation produces is not reported as one.
	canceling bool
	// keyDisambiguation reports that the terminal tells Shift+Enter apart from
	// Enter, so the hints can offer it for newlines.
	keyDisambiguation bool
	// inputStylesTheme is the theme the textarea styles were last built for.
	inputStylesTheme string
	// inputRows is the input box height the viewport was last sized for.
	inputRows int
	// inputOrigin is where the textarea's first cell was drawn in the last
	// frame, for placing the terminal cursor; nil when the box showed
	// something else (a prompt, the find bar) or no frame was drawn.
	inputOrigin *tea.Position
	// search is the open find bar (tui_search.go); nil when closed.
	search *transcriptSearch
	// termColorsKnown is set once the terminal has reported its background
	// (or did not answer in time). Painting the theme's background before
	// then would make the terminal report the theme's colour back.
	termColorsKnown bool

	// Diff modal viewer state
	diffModal          bool
	diffConfirmDiscard bool
	diffCursor         int
	diffViewport       viewport.Model
	diffPath           string
	diffSource         diffSource
	diffReady          bool
	// diffUnified is the user's choice of the single-column view; otherwise
	// the modal is side by side when the terminal is wide enough.
	diffUnified bool
	// diffText is the unified diff on show, and diffShownSplit whether it
	// was laid out side by side (and so fetched with the whole file).
	diffText       string
	diffShownSplit bool

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
	// Sessions start in plan mode (read-only) unless -mode chose: nothing
	// changes until a plan is approved or the mode is raised with Shift+Tab.
	permissionMode := PermissionPlan
	if req.PermissionMode != "" {
		permissionMode = NormalizeMode(req.PermissionMode)
	}

	ta := newChatInput()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(tuiColorCyan)

	hist := loadPromptHistory()

	m := &teaModel{
		runner:              runner,
		workingDir:          absWorkingDir,
		modelName:           modelName,
		mode:                modeAgent,
		configPath:          configPath,
		checkpointMgr:       checkpointMgr,
		processMgr:          processMgr,
		rules:               rules,
		skills:              skills,
		settings:            settings,
		input:               ta,
		spinner:             sp,
		promptHistory:       hist,
		historyIdx:          -1,
		shellTracker:        NewShellActivityTracker(10),
		lastAgentSubmitTime: time.Now(),
		maxTurns:            maxTurns,
		commandTimeout:      commandTimeout,
		budget:              req.Budget,
		thinkLevel:          req.ThinkLevel,
		allowCommands:       req.AllowCommands,
		sandbox:             req.Sandbox,
		permissionMode:      permissionMode,
		permissionChan:      make(chan teaPermissionRequestMsg),
	}
	if err := m.initializeSession(req.ResumeSession); err != nil {
		m.statusNotice = "Session recovery failed: " + err.Error()
	}

	// Initial welcome message in history
	m.appendHistory(m.formatWelcome())
	m.appendSessionTranscript()
	m.initGitWatcher()
	m.updatePromptAndPlaceholder()

	return m, nil
}

// formatWelcome is a short greeting rather than the legacy REPL's settings
// card: a snapshot of the model and workspace in the transcript goes stale the
// moment either changes, so the live values live in the sidebar and header.
func (m *teaModel) formatWelcome() string {
	banner := "\n" + ColorCyan(StyleBold(SymBranch+" fastllm")) + ColorGray("  autonomous agent") + "\n" +
		ColorGray("  Type ") + ColorCyan("/help") + ColorGray(" for commands  "+SymDot+"  ") +
		ColorYellow("Tab") + ColorGray(" for shell mode  "+SymDot+"  ") +
		ColorYellow("Shift+Tab") + ColorGray(" to change permissions")
	if notice := untrustedConfigNotice(m.workingDir, "Run /trust to use it."); notice != "" {
		banner += "\n\n" + notice
	}
	return banner + "\n\n"
}

func (m *teaModel) appendHistory(text string) {
	m.historyText.WriteString(trimPaddedTail(text))
	if m.ready {
		m.syncTranscript(true)
	}
}

// trimPaddedTail drops the padding lipgloss leaves on the last, unfinished
// line of a chunk. A style rendered over text ending in blank lines pads every
// line to the widest one, so the final blank line is really a row of spaces; the next
// append continues on that row and starts indented -- a command typed after a
// run showed up pushed right by the width of "Completed in N turn(s)". Only a
// tail with nothing but spaces and escape codes is changed, and the escape
// codes are kept so styles still close; lines before it are left alone.
func trimPaddedTail(text string) string {
	cut := strings.LastIndexByte(text, '\n') + 1
	tail := text[cut:]
	if tail == "" || strings.TrimSpace(StripANSI(tail)) != "" {
		return text
	}
	return text[:cut] + strings.Join(ansiRegexp.FindAllString(tail, -1), "")
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

// showChangesColumn reports whether the terminal is wide enough to give the
// session's file changes their own column beside the conversation.
func (m *teaModel) showChangesColumn() bool {
	return m.frameWidth() >= changesColumnMinFrame
}

// conversationWidth is the width of the conversation viewport: the full frame,
// less the changes column when it is shown.
func (m *teaModel) conversationWidth() int {
	if m.showChangesColumn() {
		return m.frameWidth() - changesColumnWidth
	}
	return m.frameWidth()
}

// inputBoxWidth is the lipgloss Width of the input box and the suggestion
// dropdown. Beside the sidebar it stops one column short of the separator, so
// the box's right border never touches it.
func (m *teaModel) inputBoxWidth() int {
	if m.showChangesColumn() {
		return m.conversationWidth() - 3
	}
	return m.frameWidth() - 2
}

func (m *teaModel) contentWidth() int {
	w := m.conversationWidth() - 3
	if w < 40 {
		return 76
	}
	return w
}

func (m *teaModel) initGitWatcher() {
	if m.gitWatchStop != nil {
		m.gitWatchStop()
		m.gitWatchStop = nil
		m.gitWatchChan = nil
	}
	if m.workingDir == "" {
		return
	}
	ch, stop, err := gitrepo.Watch(context.Background(), m.workingDir)
	if err == nil {
		m.gitWatchChan = ch
		m.gitWatchStop = stop
	}
}

func (m *teaModel) refreshGitStatusCmd() tea.Cmd {
	root := m.workingDir
	return func() tea.Msg {
		if root == "" {
			return teaGitRefreshMsg{status: gitrepo.RepoStatus{IsRepo: false}}
		}
		status, err := gitrepo.GetRepoStatus(context.Background(), root)
		if err != nil {
			return teaGitRefreshMsg{status: gitrepo.RepoStatus{IsRepo: false}}
		}
		return teaGitRefreshMsg{status: status}
	}
}

func (m *teaModel) waitForGitWatch() tea.Cmd {
	ch := m.gitWatchChan
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		_, ok := <-ch
		if !ok {
			return nil
		}
		return teaGitWatchMsg{}
	}
}

// Init implements tea.Model
func (m *teaModel) Init() tea.Cmd {
	return tea.Batch(
		m.input.Focus(),
		m.spinner.Tick,
		tea.RequestBackgroundColor,
		tea.Tick(terminalColorWait, func(time.Time) tea.Msg { return teaTermColorsTimeoutMsg{} }),
		m.refreshGitStatusCmd(),
		m.waitForGitWatch(),
	)
}

// Update implements tea.Model
func (m *teaModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	// Keys can change the input on any of the many return paths below, so the
	// dropdown catches up once, after the key has been fully handled.
	if _, isKey := msg.(tea.KeyMsg); isKey {
		defer m.refreshSuggestions()
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		m.input.SetWidth(m.inputBoxWidth() - 3)
		// Width changed, so the text re-wraps: recompute the box height before
		// handing the remaining rows to the viewport.
		m.syncInputHeight()

		if !m.ready {
			m.viewport = viewport.New(viewport.WithWidth(m.conversationWidth()), viewport.WithHeight(3))
			m.syncTranscript(false)
			m.ready = true
			m.resizeViewport()
			m.viewport.GotoBottom()
		} else {
			m.viewport.SetWidth(m.conversationWidth())
			m.resizeViewport()
		}

	case tea.PasteMsg:
		// Terminals deliver a paste as one message, newlines included; it falls
		// through to the textarea below, which inserts it literally. Modals and
		// prompts have no text box for it, so drop it there rather than typing
		// into the chat input behind them. The editor takes it.
		if m.editorShown() {
			m.handleEditorPaste(msg.Content)
			return m, nil
		}
		if m.pendingPlan != "" || m.pendingPermission != nil || m.modalOpen() {
			return m, nil
		}
		if m.search != nil {
			m.addSearchText(msg.Content)
			return m, nil
		}

	case editorClipboardMsg:
		m.handleEditorClipboard(msg)
		return m, nil

	case teaTermColorsTimeoutMsg:
		m.termColorsKnown = true

	case tea.FocusMsg:
		m.blurred = false
		m.finishedAway = false
		// The spinner stopped ticking while the window was in the background.
		cmds = append(cmds, m.spinner.Tick)

	case tea.BlurMsg:
		m.blurred = true
		// Terminals report no event when the pointer leaves the window.
		m.hover = ""

	case tea.BackgroundColorMsg:
		m.termColorsKnown = true
		// Follow a light terminal unless the user picked a theme themselves.
		if !msg.IsDark() && !themeFromPreference && !currentTheme.Light {
			// The greeting was drawn in the dark default; redraw it if it is
			// still all the transcript holds.
			welcomeOnly := m.historyText.String() == trimPaddedTail(m.formatWelcome())
			if err := ApplyTheme(defaultLightThemeName); err == nil {
				if welcomeOnly {
					m.historyText.Reset()
					m.appendHistory(m.formatWelcome())
				}
				m.statusNotice = "Light terminal: using " + defaultLightThemeName + " (/theme to change)"
				cmds = append(cmds, m.clearStatusAfter(5*time.Second))
			}
		}

	case tea.KeyboardEnhancementsMsg:
		m.keyDisambiguation = msg.SupportsKeyDisambiguation()

	case tea.KeyPressMsg:
		m.turnFailed = false
		if m.pendingPlan != "" && !m.isExecuting {
			return m.updatePlanApproval(msg)
		}
		if m.pendingPermission != nil {
			switch msg.String() {
			case "ctrl+c":
				m.resolvePermission(false, false)
				if m.cancelTurn != nil {
					m.cancelTurn()
				}
				return m, m.waitForNextEvent()
			case "enter", "esc", "n", "N":
				m.resolvePermission(false, false)
				return m, m.waitForNextEvent()
			case "y", "Y":
				m.resolvePermission(true, false)
				return m, m.waitForNextEvent()
			case "a", "A":
				m.resolvePermission(true, true)
				return m, m.waitForNextEvent()
			}
			return m, nil
		}
		if m.editor != nil {
			return m, m.handleEditorKey(msg)
		}
		if m.skillsModal {
			switch msg.String() {
			case "esc", "ctrl+c", "q", "Q":
				m.closeSkillsModal()
			case "enter":
				m.selectSkillFromModal()
			case "up", "k":
				if m.skillCursor > 0 {
					m.skillCursor--
				}
			case "down", "j":
				if m.skillCursor < len(m.skills)-1 {
					m.skillCursor++
				}
			case "home":
				m.skillCursor = 0
			case "end":
				if len(m.skills) > 0 {
					m.skillCursor = len(m.skills) - 1
				}
			}
			return m, nil
		}
		if m.sessionsModal != nil {
			return m, m.handleSessionsModalKey(msg)
		}
		if m.themeModal != nil {
			return m, m.handleThemeModalKey(msg)
		}
		if m.filesModal != nil {
			return m, m.handleFilesModalKey(msg)
		}
		if m.search != nil && !m.modalOpen() {
			return m, m.handleSearchKey(msg)
		}
		if m.modelsModal {
			switch msg.String() {
			case "esc", "ctrl+c", "q", "Q":
				m.closeModelsModal()
			case "enter":
				m.selectModelFromModal()
			case "up", "k":
				if m.modelCursor > 0 {
					m.modelCursor--
				}
			case "down", "j":
				if m.settings != nil && m.modelCursor < len(m.settings.Models)-1 {
					m.modelCursor++
				}
			case "home":
				m.modelCursor = 0
			case "end":
				if m.settings != nil && len(m.settings.Models) > 0 {
					m.modelCursor = len(m.settings.Models) - 1
				}
			}
			return m, nil
		}
		if m.diffModal {
			if m.diffConfirmDiscard {
				switch msg.String() {
				case "enter", "y", "Y":
					m.diffConfirmDiscard = false
					m.discardCurrentFile()
				case "esc", "ctrl+c":
					m.diffConfirmDiscard = false
				default:
					// Any other typed character answers "no".
					if msg.Text != "" {
						m.diffConfirmDiscard = false
					}
				}
				return m, nil
			}

			switch msg.String() {
			case "esc", "ctrl+c", "q", "Q":
				m.closeDiffModal()
				return m, nil
			case "right", "n", "N":
				m.nextDiffFile()
				return m, nil
			case "left", "p", "P":
				m.prevDiffFile()
				return m, nil
			case "s", "S":
				m.toggleStageCurrentFile()
				return m, nil
			case "d", "D":
				m.cycleDiffSource()
				return m, nil
			case "v", "V":
				m.diffUnified = !m.diffUnified
				m.loadDiffText()
				return m, nil
			case "e", "E":
				if f, ok := m.currentDiffFile(); ok && f.Status != "D" {
					m.closeDiffModal()
					if err := m.openEditor(f.Path, true); err != nil {
						m.statusNotice = "Cannot edit: " + err.Error()
						return m, m.clearStatusAfter(4 * time.Second)
					}
				}
				return m, nil
			case "x", "X":
				m.diffConfirmDiscard = true
				return m, nil
			}
			var vpCmd tea.Cmd
			m.diffViewport, vpCmd = m.diffViewport.Update(msg)
			return m, vpCmd
		}

		if msg.Mod.Contains(tea.ModAlt) {
			switch unicode.ToLower(msg.Code) {
			case 'c':
				if len(m.changes.files) > 0 {
					targetIdx := 0
					if m.changes.cursor >= 0 && m.changes.cursor < len(m.changes.files) {
						targetIdx = m.changes.cursor
					}
					m.openDiffModal(targetIdx)
					return m, nil
				}
			case 'm':
				m.openModelsModal()
				return m, nil
			}
		}

		// While the slash-command dropdown shows, it owns ↑/↓, Tab, Enter and
		// Esc; every other key falls through to the input as usual.
		if handled, cmd := m.handleSuggestKey(msg); handled {
			return m, cmd
		}

		switch msg.String() {
		case "ctrl+c":
			// With text selected in the input, Ctrl+C copies it, as in an
			// editor. Windows Terminal keeps Ctrl+Shift+C for itself.
			if m.input.HasSelection() {
				text := m.input.SelectedText()
				m.input.ClearSelection()
				return m, m.copyText(text, "selection")
			}
			if m.isExecuting && m.cancelTurn != nil {
				m.cancelActiveOperation()
				cmds = append(cmds, m.clearStatusAfter(3*time.Second))
				return m, tea.Batch(cmds...)
			}
			if m.shellExecuting && (m.cancelShell != nil || m.currentShellExec != nil) {
				m.cancelActiveOperation()
				cmds = append(cmds, m.clearStatusAfter(3*time.Second))
				return m, tea.Batch(cmds...)
			}
			m.closeSession()
			return m, tea.Quit

		case "ctrl+b":
			if m.shellExecuting && m.currentShellProc != nil {
				bp := m.currentShellProc
				m.shellBackgrounded = true
				m.shellExecuting = false
				m.cancelShell = nil
				m.currentShellExec = nil
				m.currentShellProc = nil
				notice := fmt.Sprintf("✓ Sent %q to background (%s · PID: %d)\n  Inspect logs: /logs %s (or logs %s)  •  Stop: /kill %s\n\n",
					bp.Command, bp.ID, bp.PID, bp.ID, bp.ID, bp.ID)
				m.appendHistory(styleStatusNotice.Render(notice))
				m.statusNotice = fmt.Sprintf("Sent %s to background", bp.ID)
				return m, m.clearStatusAfter(3 * time.Second)
			}

		case "tab":
			// Toggle between Agent Mode and Shell Mode
			if m.mode == modeAgent {
				m.mode = modeShell
				m.statusNotice = "Engaged Shell Mode."
			} else {
				m.mode = modeAgent
				m.statusNotice = "Returned to chat input."
			}
			m.updatePromptAndPlaceholder()
			cmds = append(cmds, m.clearStatusAfter(2*time.Second))
			return m, tea.Batch(cmds...)

		case "shift+tab":
			cmds = append(cmds, m.cyclePermissionMode())
			return m, tea.Batch(cmds...)

		case "ctrl+v":
			return m, m.handlePasteCommand()

		case "ctrl+j":
			// Ctrl+J is universal terminal newline / linefeed
			m.input.InsertString("\n")
			return m, nil

		case "ctrl+f":
			m.openSearch("")
			return m, nil

		case "ctrl+o":
			if len(m.changes.files) > 0 {
				targetIdx := 0
				if m.changes.cursor >= 0 && m.changes.cursor < len(m.changes.files) {
					targetIdx = m.changes.cursor
				}
				m.openDiffModal(targetIdx)
				return m, nil
			}

		case "alt+enter", "shift+enter":
			m.input.InsertString("\n")
			return m, nil

		case "enter":
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

		case "up":
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

		case "down":
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

		case "esc":
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

		case "pgup":
			m.viewport.ScrollUp(5)
			return m, nil

		case "pgdown":
			m.viewport.ScrollDown(5)
			return m, nil
		}

	case tea.MouseMsg:
		click, isClick := msg.(tea.MouseClickMsg)
		isClick = isClick && click.Button == tea.MouseLeft
		if motion, ok := msg.(tea.MouseMotionMsg); ok {
			if m.modalOpen() {
				m.hover = ""
			} else {
				m.hover = m.hits.at(motion.X, motion.Y)
				if motion.Button == tea.MouseNone {
					return m, nil
				}
			}
		}
		if m.editorShown() {
			m.handleEditorMouse(msg)
			return m, nil
		}
		if m.modalOpen() {
			// A click outside the modal dismisses it, as Esc would.
			if isClick && m.hits.at(click.X, click.Y) != hitModal {
				return m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			}
			if m.diffModal {
				var vpCmd tea.Cmd
				m.diffViewport, vpCmd = m.diffViewport.Update(msg)
				return m, vpCmd
			}
			return m, nil
		}
		if isClick {
			switch id := m.hits.at(click.X, click.Y); {
			case id == hitModel:
				m.openModelsModal()
				return m, nil
			case id == hitMode:
				return m, m.cyclePermissionMode()
			case strings.HasPrefix(id, hitChangeFile):
				if idx, err := strconv.Atoi(strings.TrimPrefix(id, hitChangeFile)); err == nil {
					m.openDiffModal(idx)
					return m, nil
				}
			}
		}
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
				m.changes.Record(m.workingDir, ev.ToolCall.Name, ev.ToolCall.Arguments, ev.ToolCall.Result)
				if ev.ToolCall.Name == "run_command" {
					var runArgs struct {
						Command string `json:"command"`
					}
					_ = decodeToolArguments(ev.ToolCall.Arguments, &runArgs)
					cmdText := runArgs.Command
					if cmdText == "" {
						cmdText = m.activeArgs
					}
					isErr := strings.HasPrefix(strings.TrimSpace(ev.ToolCall.Result), "Error") ||
						strings.HasPrefix(strings.TrimSpace(ev.ToolCall.Result), "error")
					exitCode := 0
					if isErr {
						exitCode = 1
					}
					maxLines := 15
					if m.expandedTools {
						maxLines = 10000
					}
					box := FormatTerminalBox(TerminalBoxOptions{
						Command:     cmdText,
						Output:      ev.ToolCall.Result,
						Width:       m.contentWidth(),
						ExitCode:    exitCode,
						IsError:     isErr,
						MaxLines:    maxLines,
						AgentCalled: true,
					})
					m.appendHistory(box + "\n\n")
				} else {
					previewLines := 4
					if m.expandedTools {
						previewLines = 10000
					}
					m.appendHistory(FormatToolResult(ev.ToolCall.Name, ev.ToolCall.Result, previewLines) + "\n\n")
				}
				cmds = append(cmds, m.refreshGitStatusCmd())
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

		case EventPlanProposed:
			m.pendingPlan = ev.Response

		case EventNotice:
			m.appendHistory(styleStatusNotice.Render(ev.Response) + "\n\n")

		case EventTaskFinished:
			m.isExecuting = false
			m.activeTool = ""
			m.activeArgs = ""
			m.cancelTurn = nil
			m.turnFailed = ev.Error != "" && !m.canceling
			m.canceling = false
			m.finishedAway = m.blurred
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
			var transcript []llm.Message
			if ev.Result != nil {
				if strings.TrimSpace(ev.Result.FinalResponse) != "" {
					response = ev.Result.FinalResponse
				}
				transcript = ev.Result.Transcript
				// The exact figure from the run replaces the estimate.
				m.overhead = requestOverhead{chars: ev.Result.RequestOverheadChars, key: m.overheadKey()}
				if ev.Result.LearnedContextWindow > 0 {
					m.adoptContextWindow(ev.Result.LearnedContextWindow)
				}
			}
			m.recordCompletedPrompt(response, transcript)
			if m.pendingPlan != "" {
				m.planCursor = 0
				m.input.Blur()
				m.statusNotice = "Plan ready: choose how to carry it out"
			}
		}
		// Listen for next event
		if m.eventChan != nil {
			cmds = append(cmds, m.waitForNextEvent())
		}

	case contextDetectedMsg:
		lines, changed, err := applyContextWindows(m.settings, m.configPath, msg.results, msg.missing)
		for _, line := range lines {
			m.appendHistory(styleMuted.Render("  "+line) + "\n")
		}
		if err != nil {
			m.appendHistory(styleDiffDel.Render(err.Error()) + "\n")
		}
		m.appendHistory("\n")
		if changed {
			// The active model's budget follows at once; SwitchModel declines
			// while child agents run, and the next switch picks it up.
			if endpoint := m.settings.FindModel(m.modelName); endpoint != nil && m.runner != nil {
				_ = m.runner.SwitchModel(endpoint)
			}
		}

	case teaPermissionRequestMsg:
		if msg.Workspace != "" && msg.Workspace != m.workingDir {
			msg.Reply <- permissionDecision{Allow: false}
			cmds = append(cmds, m.waitForNextEvent())
		} else if g := m.permissionController().CoveringGrant(msg.consent()); g != nil {
			msg.Reply <- permissionDecision{Allow: true, Via: g.ID}
			cmds = append(cmds, m.waitForNextEvent())
		} else {
			m.pendingPermission = &msg
			m.input.Blur()
			m.statusNotice = "Permission required for " + msg.ToolName
		}

	case teaGitWatchMsg:
		cmds = append(cmds, m.refreshGitStatusCmd(), m.waitForGitWatch())

	case teaGitRefreshMsg:
		m.changes.UpdateFromGit(msg.status)

	case teaShellDoneMsg:
		m.finishedAway = m.blurred
		if msg.ShellCommand {
			if m.shellBackgrounded && msg.ProcID != "" {
				m.currentShellProc = nil
				m.currentShellExec = nil
				m.shellBackgrounded = false
				return m, nil
			}
			if msg.ProcID != "" && m.processMgr != nil {
				m.processMgr.Remove(msg.ProcID)
			}
			m.currentShellProc = nil
			m.currentShellExec = nil
			m.shellExecuting = false
			m.cancelShell = nil
			exitCode := 0
			isErr := msg.Err != nil
			output := msg.Output
			if isErr {
				exitCode = 1
				if output == "" {
					output = msg.Err.Error()
				}
			}
			cmdToRender := msg.Command
			if cmdToRender == "" {
				cmdToRender = "command"
			}
			if m.shellTracker != nil {
				m.shellTracker.Record(cmdToRender, output, exitCode, msg.Duration)
			}
			terminalBox := FormatTerminalBox(TerminalBoxOptions{
				Command:     cmdToRender,
				Output:      output,
				Width:       m.contentWidth(),
				ExitCode:    exitCode,
				Duration:    msg.Duration,
				IsError:     isErr,
				Canceled:    msg.Canceled,
				MaxLines:    500,
				AgentCalled: false,
			})
			m.appendHistory(terminalBox + "\n\n")
			cmds = append(cmds, m.refreshGitStatusCmd())
			if msg.ClearScreen {
				cmds = append(cmds, tea.ClearScreen)
			}
		} else {
			if msg.Output != "" {
				m.appendHistory(msg.Output + "\n")
			}
			if msg.Canceled {
				m.appendHistory(styleMuted.Render("[Command canceled]\n"))
			} else if msg.Err != nil {
				m.appendHistory(styleDiffDel.Render(fmt.Sprintf("$ %v\n", msg.Err)))
			}
			m.appendHistory("\n")
		}

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
		// Let the tick lapse while the window is in the background; FocusMsg
		// starts a new one.
		if m.blurred {
			break
		}
		var spCmd tea.Cmd
		m.spinner, spCmd = m.spinner.Update(msg)
		cmds = append(cmds, spCmd)
	}

	// Update viewport & input
	var vpCmd, inCmd tea.Cmd
	// Keystrokes belong to the input box. The viewport's default keymap binds
	// plain letters (j/k/d/u/f/b/space) to scrolling, so forwarding typed keys
	// here made the conversation jump up and down while typing. Scrolling by key
	// is handled explicitly above (PgUp/PgDn) and by the mouse-wheel branch.
	if _, isKey := msg.(tea.KeyMsg); !isKey {
		m.viewport, vpCmd = m.viewport.Update(msg)
	}
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

// newChatInput builds the chat input box. The textarea sizes itself to its
// content between minInputRows and maxInputRows, counting its own soft wraps,
// and leaves the caret to the terminal's real cursor (placed in View).
// MaxHeight only caps the Enter key's newline, which fastllm handles itself;
// pastes and Ctrl+J are limited by the textarea's own line cap and CharLimit.
func newChatInput() textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "Ask a question, enter a task, or type /help (Tab switches to Shell Mode)..."
	ta.Prompt = "❯ "
	ta.CharLimit = 8192
	ta.ShowLineNumbers = false
	ta.DynamicHeight = true
	ta.MinHeight = minInputRows
	ta.MaxHeight = maxInputRows
	ta.SetHeight(minInputRows)
	ta.SetVirtualCursor(false)
	ta.Focus()
	return ta
}

// syncInputHeight gives the viewport back exactly the rows the input box does
// not take whenever the box has grown or shrunk, so the frame height never
// changes.
func (m *teaModel) syncInputHeight() {
	if m.input.Height() == m.inputRows {
		return
	}
	m.inputRows = m.input.Height()
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
	vpHeight := m.height - headerRows - statusRows - boxBorderRows - spareRow - m.input.Height() - m.suggestionRows()
	if vpHeight < 3 {
		vpHeight = 3
	}
	m.viewport.SetHeight(vpHeight)
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
	via := "denied"
	switch {
	case allow && grant:
		via = "approved for session (" + m.permissionController().GrantScoped(request.scope()).ID + ")"
	case allow:
		via = "approved once"
	}
	request.Reply <- permissionDecision{Allow: allow, GrantSession: grant, Via: via}
	m.pendingPermission = nil
	m.statusNotice = ""
	m.input.Focus()
}

func (m *teaModel) cancelActiveOperation() {
	canceledShell := m.shellExecuting || m.cancelShell != nil
	// With a shell command running beside an agent turn, the first cancel
	// stops the command the user started last; the next stops the turn.
	canceledAgent := m.cancelTurn != nil && !canceledShell
	if canceledAgent {
		m.canceling = true
		m.cancelTurn()
	}
	if m.cancelShell != nil {
		m.cancelShell()
		m.cancelShell = nil
	}
	if m.currentShellExec != nil {
		_ = m.currentShellExec.Stop(context.Background())
		m.currentShellExec = nil
	}
	if m.currentShellProc != nil && m.processMgr != nil {
		m.processMgr.Remove(m.currentShellProc.ID)
		m.currentShellProc = nil
	}
	m.shellExecuting = false
	switch {
	case canceledShell && m.cancelTurn != nil:
		m.statusNotice = "Canceled shell command. Esc again cancels the agent turn."
	case canceledAgent:
		m.statusNotice = "Canceled active agent turn."
	case canceledShell:
		m.statusNotice = "Canceled active shell command."
	}
}

func (m *teaModel) handleShellSubmit(cmdStr string) tea.Cmd {
	if m.processMgr == nil {
		m.processMgr = NewProcessManager()
	}
	// A shell command may run beside an agent turn, but only one runs in the
	// foreground at a time. Keep the refused command in the box.
	if m.shellExecuting {
		if m.mode == modeShell {
			m.input.SetValue(cmdStr)
		}
		m.statusNotice = "A shell command is already running — Esc cancels it, Ctrl+B backgrounds it."
		return m.clearStatusAfter(3 * time.Second)
	}
	trimmed := strings.TrimSpace(cmdStr)
	if trimmed == "exit" || trimmed == "quit" || trimmed == "/exit" || trimmed == "/shell" {
		m.mode = modeAgent
		m.statusNotice = "Returned to Agent Mode."
		m.updatePromptAndPlaceholder()
		return m.clearStatusAfter(2 * time.Second)
	}
	if trimmed == "/c" || trimmed == "/clear" || trimmed == "clear" || trimmed == "cls" {
		m.historyText.Reset()
		m.appendHistory(m.formatWelcome())
		m.statusNotice = "History cleared."
		return m.clearStatusAfter(2 * time.Second)
	}
	if trimmed == "ps" || trimmed == "/ps" {
		m.appendHistory(styleUserPrompt.Render("❯ "+cmdStr) + "\n")
		m.appendHistory(m.processMgr.FormatProcessTable() + "\n\n")
		return nil
	}
	if strings.HasPrefix(trimmed, "logs ") || trimmed == "logs" || strings.HasPrefix(trimmed, "/logs ") || trimmed == "/logs" {
		m.appendHistory(styleUserPrompt.Render("❯ "+cmdStr) + "\n")
		return m.handleLogsCommand(cmdStr)
	}
	if strings.HasPrefix(trimmed, "kill ") || trimmed == "kill" || strings.HasPrefix(trimmed, "/kill ") || trimmed == "/kill" {
		m.appendHistory(styleUserPrompt.Render("❯ "+cmdStr) + "\n")
		return m.handleKillCommand(cmdStr)
	}

	// Direct background execution via trailing '&' or 'bg <cmd>'
	isBackground := false
	var bgCmd string
	if strings.HasSuffix(trimmed, "&") {
		isBackground = true
		bgCmd = strings.TrimSpace(strings.TrimSuffix(trimmed, "&"))
	} else if strings.HasPrefix(trimmed, "bg ") {
		isBackground = true
		bgCmd = strings.TrimSpace(strings.TrimPrefix(trimmed, "bg "))
	} else if trimmed == "bg" {
		m.appendHistory(styleUserPrompt.Render("❯ "+cmdStr) + "\n")
		m.appendHistory(styleMuted.Render("Usage: bg <command> (e.g. bg npm run dev, bg yarn serve)\n\n"))
		return nil
	}

	if isBackground {
		m.appendHistory(styleUserPrompt.Render("❯ "+cmdStr) + "\n")
		if bgCmd == "" {
			m.appendHistory(styleDiffDel.Render("Error: empty background command\n\n"))
			return nil
		}
		started := time.Now()
		bp, err := m.processMgr.Start(bgCmd, m.workingDir)
		if err != nil {
			box := FormatTerminalBox(TerminalBoxOptions{
				Command:     cmdStr,
				Output:      fmt.Sprintf("Failed to start background process: %v", err),
				Width:       m.contentWidth(),
				ExitCode:    1,
				Duration:    time.Since(started),
				IsError:     true,
				AgentCalled: false,
			})
			m.appendHistory(box + "\n\n")
			return nil
		}
		output := fmt.Sprintf("◈ Started background process: %s (PID: %d)\n  Command: %s\n  Inspect logs: /logs %s (or logs %s)  •  Stop: /kill %s",
			bp.ID, bp.PID, bp.Command, bp.ID, bp.ID, bp.ID)
		box := FormatTerminalBox(TerminalBoxOptions{
			Command:     cmdStr,
			Output:      output,
			Width:       m.contentWidth(),
			ExitCode:    0,
			Duration:    time.Since(started),
			IsError:     false,
			AgentCalled: false,
		})
		m.appendHistory(box + "\n\n")
		m.statusNotice = fmt.Sprintf("Started %s in background", bp.ID)
		return m.clearStatusAfter(3 * time.Second)
	}

	if path, ok := parseDirectoryChange(cmdStr); ok {
		started := time.Now()
		if err := m.changeWorkingDirectory(path); err != nil {
			box := FormatTerminalBox(TerminalBoxOptions{
				Command:     cmdStr,
				Output:      fmt.Sprintf("Cannot change directory: %v", err),
				Width:       m.contentWidth(),
				ExitCode:    1,
				Duration:    time.Since(started),
				IsError:     true,
				AgentCalled: false,
			})
			m.appendHistory(box + "\n\n")
			return nil
		}
		box := FormatTerminalBox(TerminalBoxOptions{
			Command:     cmdStr,
			Output:      fmt.Sprintf("✓ Working directory changed to %s\n  (%d workspace rules discovered)", abbreviateHome(m.workingDir), len(m.rules)),
			Width:       m.contentWidth(),
			ExitCode:    0,
			Duration:    time.Since(started),
			IsError:     false,
			AgentCalled: false,
		})
		m.appendHistory(box + "\n\n")
		return tea.Batch(
			m.refreshGitStatusCmd(),
			m.clearStatusAfter(3*time.Second),
		)
	}

	// Check for interactive terminal editor/CLI (nano, vim, gh auth login, git commit) or desktop GUI editor (notepad, code)
	spec := ClassifyShellCommand(cmdStr, m.workingDir)
	if (spec.Kind == CmdKindTUIEditor || spec.Kind == CmdKindInteractive) && spec.Cmd != nil {
		started := time.Now()
		// Ensure standard terminal environment variables for full TTY interactive CLI tools
		env := os.Environ()
		hasTerm := false
		for _, e := range env {
			if strings.HasPrefix(e, "TERM=") {
				hasTerm = true
				break
			}
		}
		if !hasTerm {
			env = append(env, "TERM=xterm-256color")
		}
		env = append(env, "COLORTERM=truecolor")
		spec.Cmd.Env = env

		return tea.ExecProcess(spec.Cmd, func(err error) tea.Msg {
			var output string
			if spec.Kind == CmdKindTUIEditor {
				output = fmt.Sprintf("Session finished for %s", spec.BinaryName)
				if spec.TargetFile != "" {
					output = fmt.Sprintf("Finished editing %s (%s)", filepath.Base(spec.TargetFile), spec.BinaryName)
				}
			} else {
				output = EnrichInteractiveOutcome(cmdStr, spec, m.workingDir, err)
			}
			return teaShellDoneMsg{
				Command:      cmdStr,
				Output:       output,
				Err:          err,
				ShellCommand: true,
				Duration:     time.Since(started),
				ClearScreen:  true,
			}
		})
	}

	if spec.Kind == CmdKindGUIEditor && spec.Cmd != nil {
		started := time.Now()
		err := LaunchGUIEditor(spec.Cmd)
		if err != nil {
			return func() tea.Msg {
				return teaShellDoneMsg{
					Command:      cmdStr,
					Output:       fmt.Sprintf("Failed to launch %s: %v", spec.BinaryName, err),
					Err:          err,
					ShellCommand: true,
					Duration:     time.Since(started),
				}
			}
		}
		notice := spec.Notice
		if notice == "" {
			notice = fmt.Sprintf("Launched %s", spec.BinaryName)
		}
		m.statusNotice = notice
		return tea.Batch(
			func() tea.Msg {
				return teaShellDoneMsg{
					Command:      cmdStr,
					Output:       notice,
					ShellCommand: true,
					Duration:     time.Since(started),
				}
			},
			m.clearStatusAfter(3*time.Second),
			m.refreshGitStatusCmd(),
		)
	}

	p, bp, err := m.processMgr.StartTracked(cmdStr, m.workingDir)
	if err != nil {
		started := time.Now()
		box := FormatTerminalBox(TerminalBoxOptions{
			Command:     cmdStr,
			Output:      fmt.Sprintf("Failed to run command: %v", err),
			Width:       m.contentWidth(),
			ExitCode:    1,
			Duration:    time.Since(started),
			IsError:     true,
			AgentCalled: false,
		})
		m.appendHistory(box + "\n\n")
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelShell = cancel
	m.shellExecuting = true
	m.currentShellProc = bp
	m.currentShellExec = p
	m.shellBackgrounded = false
	started := time.Now()

	return func() tea.Msg {
		res, err := p.Wait(ctx)
		out := res.Output
		if res.Truncated {
			out = "[earlier output truncated]\n" + out
		}
		return teaShellDoneMsg{
			Command:      cmdStr,
			Output:       strings.TrimRight(string(out), "\r\n"),
			Err:          err,
			ShellCommand: true,
			Canceled:     ctx.Err() != nil || res.Reason == "canceled" || errors.Is(err, context.Canceled),
			Duration:     time.Since(started),
			ProcID:       bp.ID,
		}
	}
}

func (m *teaModel) handleLogsCommand(inputVal string) tea.Cmd {
	parts := strings.Fields(inputVal)
	if len(parts) < 2 {
		m.appendHistory(styleMuted.Render("Usage: /logs <process_id> [max_lines] (e.g. /logs proc-1, logs 1 50)\n\n"))
		return nil
	}
	targetID := parts[1]
	maxLines := 50
	if len(parts) >= 3 {
		if n, err := strconv.Atoi(parts[2]); err == nil && n > 0 {
			maxLines = n
		}
	}
	bp, out, err := m.processMgr.Logs(targetID, maxLines)
	if err != nil {
		m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Logs error: %v\n\n", err)))
		return nil
	}
	if strings.TrimSpace(out) == "" {
		out = "[no output recorded yet]"
	}
	status := "RUNNING"
	if bp.Exited {
		status = fmt.Sprintf("EXIT %d", bp.ExitCode)
	}
	box := FormatTerminalBox(TerminalBoxOptions{
		Command:     fmt.Sprintf("logs %s (%s · PID: %d · %s)", bp.ID, bp.Command, bp.PID, status),
		Output:      out,
		Width:       m.contentWidth(),
		ExitCode:    bp.ExitCode,
		IsError:     bp.Exited && bp.ExitCode != 0,
		MaxLines:    maxLines,
		AgentCalled: false,
	})
	m.appendHistory(box + "\n\n")
	return nil
}

func (m *teaModel) handleKillCommand(inputVal string) tea.Cmd {
	parts := strings.Fields(inputVal)
	if len(parts) < 2 {
		m.appendHistory(styleMuted.Render("Usage: /kill <process_id|all> (e.g. /kill proc-1, /kill all)\n\n"))
		return nil
	}
	target := parts[1]
	if err := m.processMgr.Kill(target); err != nil {
		m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Kill error: %v\n\n", err)))
		return nil
	}
	if target == "all" || target == "--all" || target == "-a" {
		m.appendHistory(styleStatusNotice.Render("✓ Killed all background processes.\n\n"))
		m.statusNotice = "Killed all background processes."
	} else {
		m.appendHistory(styleStatusNotice.Render(fmt.Sprintf("✓ Killed process %s\n\n", target)))
		m.statusNotice = fmt.Sprintf("Killed process %s", target)
	}
	return m.clearStatusAfter(3 * time.Second)
}

func (m *teaModel) handleBgSlashCommand(inputVal string) tea.Cmd {
	parts := strings.SplitN(inputVal, " ", 2)
	if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
		m.appendHistory(styleMuted.Render("Usage: /bg <command> (e.g. /bg npm run dev, /bg python -m http.server 8000)\n\n"))
		return nil
	}
	cmdToRun := strings.TrimSpace(parts[1])
	started := time.Now()
	bp, err := m.processMgr.Start(cmdToRun, m.workingDir)
	if err != nil {
		m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Failed to start background process: %v\n\n", err)))
		return nil
	}
	output := fmt.Sprintf("◈ Started background process: %s (PID: %d)\n  Command: %s\n  Inspect logs: /logs %s (or logs %s)  •  Stop: /kill %s",
		bp.ID, bp.PID, bp.Command, bp.ID, bp.ID, bp.ID)
	box := FormatTerminalBox(TerminalBoxOptions{
		Command:     inputVal,
		Output:      output,
		Width:       m.contentWidth(),
		ExitCode:    0,
		Duration:    time.Since(started),
		IsError:     false,
		AgentCalled: false,
	})
	m.appendHistory(box + "\n\n")
	m.statusNotice = fmt.Sprintf("Started %s in background", bp.ID)
	return m.clearStatusAfter(3 * time.Second)
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
	m.initGitWatcher()
	m.permissionController().SetWorkspace(newDir)
	m.checkpointMgr = NewCheckpointManager(newDir)
	m.journal = nil // a new workspace starts with nothing to undo
	m.rules = DiscoverWorkspaceRules(newDir)
	m.skills = DiscoverWorkspaceSkills(newDir)
	m.settings, m.configPath = settings, configPath
	if notice := untrustedConfigNotice(newDir, "Run /trust to use it."); notice != "" {
		m.appendHistory(notice + "\n\n")
	}
	m.statusNotice = "Directory changed to " + filepath.Base(newDir)
	m.updatePromptAndPlaceholder()
	_ = m.saveSession()
	return nil
}

func (m *teaModel) updatePromptAndPlaceholder() {
	if m.mode == modeShell {
		dirBase := filepath.Base(m.workingDir)
		if dirBase == "" || dirBase == "." {
			dirBase = m.workingDir
		}
		m.input.Prompt = dirBase + " ❯ "
		m.input.Placeholder = fmt.Sprintf("Shell (%s) — enter command ($ go test, ls)...", abbreviateHome(m.workingDir))
	} else {
		m.input.Prompt = "❯ "
		m.input.Placeholder = "Ask a question, enter a task, or type /help (Tab switches to Shell Mode)..."
	}
	setFirstLinePrompt(&m.input, m.input.Prompt)
}

// setFirstLinePrompt shows the prompt on the input's first row only and
// indents the rest to match, instead of repeating it down the box.
func setFirstLinePrompt(ta *textarea.Model, prompt string) {
	width := lipgloss.Width(prompt)
	indent := strings.Repeat(" ", width)
	ta.SetPromptFunc(width, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return prompt
		}
		return indent
	})
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

		case "/theme", "/themes":
			return m.handleThemeSlash(parts)

		case "/trust", "/untrust":
			return m.handleTrustSlash(cmd)

		case "/audit":
			m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
			m.appendHistory(m.formatAuditTail(parts) + "\n\n")
			return nil

		case "/help":
			m.appendHistory(styleUserPrompt.Render("❯ /help") + "\n")
			m.appendHistory(FormatHelp() + "\n\n")
			return nil

		case "/c", "/clear":
			m.sessionMessages = nil
			m.taint = nil
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
			m.updatePromptAndPlaceholder()
			m.statusNotice = "Engaged Shell Mode."
			return m.clearStatusAfter(2 * time.Second)

		case "/changes":
			if len(m.changes.files) == 0 {
				m.statusNotice = "No modified files in session or working tree."
				return m.clearStatusAfter(2 * time.Second)
			}
			targetIdx := 0
			if len(parts) > 1 {
				arg := strings.TrimSpace(inputVal[len(parts[0]):])
				for i, f := range m.changes.files {
					if f.Path == arg || strings.HasSuffix(f.Path, arg) {
						targetIdx = i
						break
					}
				}
			} else if m.changes.cursor >= 0 && m.changes.cursor < len(m.changes.files) {
				targetIdx = m.changes.cursor
			}
			m.openDiffModal(targetIdx)
			return nil

		case "/edit", "/nano", "/vim":
			if len(parts) < 2 {
				m.statusNotice = fmt.Sprintf("Usage: %s <filepath>", cmd)
				return m.clearStatusAfter(3 * time.Second)
			}
			if cmd == "/edit" {
				if err := m.openEditor(inputVal[len(parts[0]):], false); err != nil {
					m.statusNotice = "Cannot edit: " + err.Error()
					return m.clearStatusAfter(4 * time.Second)
				}
				return nil
			}
			cmdName := cmd[1:]
			cmdStr := cmdName + " " + strings.TrimSpace(inputVal[len(parts[0]):])
			return m.handleShellSubmit(cmdStr)

		case "/discard", "/revert":
			m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
			if !m.checkpointMgr.IsGitRepo() {
				m.appendHistory(styleMuted.Render("Not a git repository.\n\n"))
				return nil
			}
			if len(parts) < 2 {
				m.appendHistory(styleMuted.Render("Usage: /discard <file|all> (or press 'x' in the diff modal to discard individual files)\n\n"))
				return nil
			}
			arg := strings.TrimSpace(inputVal[len(parts[0]):])
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if arg == "all" || arg == "." {
				if err := gitrepo.DiscardAll(ctx, m.workingDir); err != nil {
					m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Discard error: %v\n\n", err)))
				} else {
					m.statusNotice = "✓ Discarded all uncommitted changes."
					m.appendHistory(styleStatusNotice.Render("✓ Discarded all uncommitted changes across working tree.\n\n"))
				}
				return m.refreshGitStatusCmd()
			}

			matchedPath := arg
			isUntracked := false
			for _, f := range m.changes.files {
				if f.Path == arg || strings.HasSuffix(f.Path, arg) {
					matchedPath = f.Path
					isUntracked = f.Status == "?"
					break
				}
			}
			if err := gitrepo.RemoveFileChanges(ctx, m.workingDir, matchedPath, isUntracked); err != nil {
				m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Discard error: %v\n\n", err)))
			} else {
				m.statusNotice = fmt.Sprintf("✓ Discarded %s.", filepath.Base(matchedPath))
				m.appendHistory(styleStatusNotice.Render(fmt.Sprintf("✓ Discarded changes in %s.\n\n", matchedPath)))
			}
			return m.refreshGitStatusCmd()

		case "/diff":
			if len(parts) > 1 {
				arg := strings.TrimSpace(inputVal[len(parts[0]):])
				for i, f := range m.changes.files {
					if f.Path == arg || strings.HasSuffix(f.Path, arg) {
						m.openDiffModal(i)
						return nil
					}
				}
			}
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
			m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
			if m.isExecuting {
				m.appendHistory(styleMuted.Render("Finish or cancel the current run first.") + "\n\n")
				return nil
			}
			report, err := m.writeJournal().Undo(len(parts) > 1 && strings.EqualFold(parts[1], "force"))
			if err != nil {
				m.appendHistory(styleMuted.Render(err.Error()) + "\n\n")
				return nil
			}
			m.appendHistory(FormatUndoReport(report) + "\n\n")
			return m.refreshGitStatusCmd()

		case "/compact":
			m.appendHistory(styleUserPrompt.Render("❯ /compact") + "\n")
			// The same budget automatic compaction uses: the system prompt and
			// tool schemas go with every request, so they come out of it.
			budget := m.compactionConfig()
			budget.MaxTotalChars -= m.requestOverheadChars()
			if budget.MaxTotalChars < minTranscriptBudget {
				budget.MaxTotalChars = minTranscriptBudget
			}
			before, window := m.contextUsage()
			compacted, didCompact := ForceCompactMessages(m.sessionMessages, budget)
			if !didCompact {
				m.appendHistory(styleMuted.Render("Nothing to compact yet; the transcript has no completed older turns to collapse.\n\n"))
				return nil
			}
			m.sessionMessages = compacted
			after, _ := m.contextUsage()
			if err := m.saveSession(); err != nil {
				m.appendHistory(styleMuted.Render("Session autosave failed: "+err.Error()) + "\n\n")
			}
			// In the gauge's terms: the next request, in tokens, against the window.
			m.appendHistory(styleMuted.Render(fmt.Sprintf("Compacted: the next request drops from %s to %s tokens (%d%% of the %s-token window).",
				compactCount(before), compactCount(after), after*100/window, compactCount(window))) + "\n\n")
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
			m.appendHistory(FormatRuntimeCard(m.runtimeSettings(), m.activeSessionID()) + "\n\n")
			if summary := m.runner.agents.Summary(); summary.Total > 0 {
				m.appendHistory(FormatCard("Child Agents", strings.Split(m.runner.agents.Status(""), "\n"), 74) + "\n\n")
			}
			return nil

		case "/model", "/models":
			if len(parts) > 1 && strings.EqualFold(parts[1], "detect") {
				m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
				m.appendHistory(styleMuted.Render("Asking each provider for its model's context window...") + "\n")
				// Probing is network work, so it runs off the UI goroutine on
				// copies; the result is applied to settings back on it.
				targets, missing := contextProbeTargets(m.settings, parts[2:])
				return func() tea.Msg {
					return contextDetectedMsg{results: probeContextWindows(targets), missing: missing}
				}
			}
			if len(parts) > 1 && parts[1] != "list" {
				m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
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
			m.openModelsModal()
			return nil

		case "/ps":
			m.appendHistory(styleUserPrompt.Render("❯ /ps") + "\n")
			m.appendHistory(m.processMgr.FormatProcessTable() + "\n\n")
			return nil

		case "/kill":
			m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
			return m.handleKillCommand(inputVal)

		case "/logs":
			m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
			return m.handleLogsCommand(inputVal)

		case "/bg":
			return m.handleBgSlashCommand(inputVal)

		case "/find":
			m.openSearch(strings.TrimSpace(strings.TrimPrefix(inputVal, parts[0])))
			return nil

		case "/copy", "/yank":
			m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
			target, what := m.lastResponse, "response"
			if len(parts) > 1 {
				switch parts[1] {
				case "all":
					target, what = m.historyText.String(), "transcript"
				case "code":
					target, what = lastCodeBlock(m.lastResponse), "code block"
				}
			}
			if strings.TrimSpace(target) == "" {
				m.statusNotice = "Nothing to copy yet."
				m.appendHistory(styleMuted.Render("Nothing to copy yet.\n\n"))
				return m.clearStatusAfter(3 * time.Second)
			}
			cmd := m.copyText(StripANSI(target), what)
			m.appendHistory(styleStatusNotice.Render(m.statusNotice) + "\n\n")
			return cmd

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
			return tea.Batch(m.clearStatusAfter(2*time.Second), m.refreshGitStatusCmd(), m.waitForGitWatch())

		case "/paste":
			m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
			return m.handlePasteCommand()

		case "/attach":
			m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
			return m.handleAttachCommand(parts)

		case "/add":
			m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
			return m.handleAddFileCommand(parts)

		case "/detach":
			m.appendHistory(styleUserPrompt.Render("❯ "+inputVal) + "\n")
			m.pendingAttachments = nil
			m.statusNotice = "Cleared pending attachments."
			m.appendHistory(styleStatusNotice.Render("Cleared pending attachments.\n\n"))
			return m.clearStatusAfter(2 * time.Second)

		case "/files":
			targetDir := ""
			if len(parts) > 1 {
				targetDir = strings.Join(parts[1:], " ")
			}
			m.openFilesModal(targetDir)
			return nil
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
	detectedPrompt, inlineAtts := m.detectInlineAttachments(inputVal)
	allAttachments := append([]llm.Attachment(nil), m.pendingAttachments...)
	allAttachments = append(allAttachments, inlineAtts...)
	m.pendingAttachments = nil

	m.appendHistory(formatSubmittedPrompt(inputVal))
	if len(allAttachments) > 0 {
		var names []string
		for _, a := range allAttachments {
			names = append(names, fmt.Sprintf("[%s %s]", a.Type, a.Name))
		}
		m.appendHistory(styleMuted.Render(fmt.Sprintf("  Attached: %s\n\n", strings.Join(names, ", "))))
	}
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

	promptExtra := skillPrompt
	if m.shellTracker != nil {
		if recentShell := m.shellTracker.FormatRecentSince(m.lastAgentSubmitTime); recentShell != "" {
			if promptExtra != "" {
				promptExtra += "\n\n" + recentShell
			} else {
				promptExtra = recentShell
			}
		}
		m.lastAgentSubmitTime = time.Now()
	}

	// Capture run settings before the worker starts; directory and runtime
	// commands on the UI goroutine may change them while the model is running.
	req := RunRequest{
		Task:               detectedPrompt,
		Attachments:        allAttachments,
		WorkingDir:         m.workingDir,
		Model:              m.modelName,
		Audit:              OpenAuditLog(auditNameFor(m.activeSession)),
		Taint:              m.conversationTaint(),
		Budget:             m.budget,
		Journal:            m.writeJournal(),
		PromptExtra:        promptExtra,
		MaxTurns:           m.maxTurns,
		CommandTimeout:     m.commandTimeout,
		ThinkLevel:         m.thinkLevel,
		AllowCommands:      m.allowCommands,
		CommandsConfigured: true,
		Sandbox:            m.sandbox,
		PermissionMode:     m.permissionMode,
		InitialMessages:    priorMessages,
		StreamTokens:       true,
	}
	permissionChan := m.permissionChan
	// Launch background task
	go func() {
		defer close(events)
		// The monitor calls this only for its Ask decisions, which exist only in
		// agent mode; every other mode decides without asking.
		{
			req.Authorize = func(consent ConsentRequest) bool {
				reply := make(chan permissionDecision, 1)
				request := teaPermissionRequestMsg{ToolName: consent.Tool, Summary: consent.Summary, Scope: consent.Scope, Workspace: req.WorkingDir, Reply: reply}
				select {
				case permissionChan <- request:
				case <-ctx.Done():
					return false
				}
				select {
				case decision := <-reply:
					consent.note(decision.Via)
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

func (m *teaModel) openModelsModal() {
	if m.settings == nil || len(m.settings.Models) == 0 {
		m.statusNotice = "No models configured in config.json."
		return
	}
	m.modelsModal = true
	m.modelCursor = 0
	for i, mdl := range m.settings.Models {
		if strings.EqualFold(mdl.ID, m.modelName) {
			m.modelCursor = i
			break
		}
	}
	m.input.Blur()
}

func (m *teaModel) closeModelsModal() {
	m.modelsModal = false
	m.input.Focus()
}

func (m *teaModel) selectModelFromModal() {
	if m.settings == nil || m.modelCursor < 0 || m.modelCursor >= len(m.settings.Models) {
		m.closeModelsModal()
		return
	}
	selected := &m.settings.Models[m.modelCursor]
	m.closeModelsModal()

	if strings.EqualFold(selected.ID, m.modelName) {
		m.statusNotice = fmt.Sprintf("Model is already active: %s", selected.ID)
		return
	}

	if err := m.runner.SwitchModel(selected); err != nil {
		m.statusNotice = fmt.Sprintf("Could not switch model: %v", err)
		m.appendHistory(styleDiffDel.Render(fmt.Sprintf("Could not switch model: %v\n\n", err)))
		return
	}

	m.modelName = selected.ID
	_ = m.saveSession()
	// No status notice: the transcript line below and the header pill already
	// say it, and a third copy in the header read as noise.
	m.appendHistory(styleStatusNotice.Render(fmt.Sprintf("✓ Switched active model to %s (%s)\nEndpoint: %s",
		selected.ID, selected.Name, selected.URL)) + "\n\n")
}

// compactionConfig sizes compaction against the active model's context window.
// Read through this rather than caching: /model and /dir both change the answer.
func (m *teaModel) compactionConfig() CompactionConfig {
	return CompactionConfigForModel(m.settings, m.modelName)
}

// contextUsage estimates the next request, in tokens, against the model's
// window. A request is the transcript plus the system prompt and tool schemas
// sent with every one; the compaction gate in Runner.Run counts all three, so
// the gauge does too, converted at the gate's rate. Compaction fires when the
// estimate reaches the window less the reply reserve, near 87%. The unsent
// draft is not counted: it is not part of any request until submitted.
func (m *teaModel) contextUsage() (int, int) {
	chars := messageCharacterCount(m.sessionMessages) + m.requestOverheadChars()
	return chars * 10 / budgetCharsPerTokenTenths, ResolveContextWindow(m.settings, m.modelName)
}

// adoptContextWindow holds a window a provider stated for the rest of the
// session: the gauge and between-turn compaction read it from settings, and
// the runner's budget follows through SwitchModel, which declines while child
// agents run (a later run then recovers again). /models detect saves it.
func (m *teaModel) adoptContextWindow(window int) {
	endpoint := m.settings.FindModel(m.modelName)
	if endpoint == nil {
		return
	}
	endpoint.ContextWindow = window
	if m.runner != nil {
		_ = m.runner.SwitchModel(endpoint)
	}
}

type requestOverhead struct {
	chars int
	key   string
}

// overheadKey names the settings that change the system prompt or the tools.
func (m *teaModel) overheadKey() string {
	return fmt.Sprintf("%s|%s|%t|%s|%t", m.workingDir, m.modelName, m.allowCommands, m.permissionMode, m.sandbox)
}

// requestOverheadChars returns the system prompt and tool schemas a request
// carries: the exact figure from the last run, or an estimate once the
// directory, model, commands, mode, or sandbox setting has changed since.
func (m *teaModel) requestOverheadChars() int {
	key := m.overheadKey()
	if m.overhead.key != key && m.runner != nil {
		m.overhead = requestOverhead{key: key, chars: m.runner.EstimateRequestOverheadChars(RunRequest{
			WorkingDir: m.workingDir, Model: m.modelName, AllowCommands: m.allowCommands,
			CommandsConfigured: true, Sandbox: m.sandbox, PermissionMode: m.permissionMode,
		})}
	}
	return m.overhead.chars
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
	color := tuiColorGreen
	if percent >= 90 {
		color = tuiColorRed
	} else if percent >= 75 {
		color = tuiColorYellow
	}
	styleFill := lipgloss.NewStyle().Foreground(color)
	styleTrack := lipgloss.NewStyle().Foreground(tuiColorTrack)
	stylePct := lipgloss.NewStyle().Bold(true).Foreground(color)

	if barWidth <= 0 {
		return styleMuted.Render("ctx ") + stylePct.Render(fmt.Sprintf("%d%%", percent))
	}

	filled := 0
	if percent > 0 {
		filled = (percent*barWidth + 99) / 100
		if filled > barWidth {
			filled = barWidth
		}
	}
	meter := styleFill.Render(strings.Repeat("▰", filled)) + styleTrack.Render(strings.Repeat("▱", barWidth-filled))
	return fmt.Sprintf("%s %s %s%s",
		styleMuted.Render("ctx"),
		meter,
		stylePct.Render(fmt.Sprintf("%d%%", percent)),
		styleMuted.Render(fmt.Sprintf(" · %s/%s tok", compactCount(used), compactCount(budget))),
	)
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
				row = lipgloss.NewStyle().Bold(true).Foreground(tuiColorBrandFg).Background(tuiColorCyan).Width(contentWidth).Render("› " + strings.TrimPrefix(row, "  "))
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
	return box
}

func (m *teaModel) renderModelsModal() string {
	width, height := m.width, m.height
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	contentWidth := width - 8
	if contentWidth > 110 {
		contentWidth = 110
	}
	if contentWidth < 40 {
		contentWidth = 40
	}

	models := []config.ModelEndpoint{}
	if m.settings != nil {
		models = m.settings.Models
	}

	header := fmt.Sprintf("MODELS · %d configured", len(models))
	lines := []string{styleAgentBadge.Render(header), ""}

	if len(models) == 0 {
		lines = append(lines, styleMuted.Render("No models configured in config.json."))
	} else {
		visibleRows := (height - 8) / 2
		if visibleRows < 1 {
			visibleRows = 1
		}
		start := m.modelCursor - visibleRows/2
		if start < 0 {
			start = 0
		}
		if maxStart := len(models) - visibleRows; start > maxStart && maxStart > 0 {
			start = maxStart
		}
		end := start + visibleRows
		if end > len(models) {
			end = len(models)
		}

		for i := start; i < end; i++ {
			mdl := models[i]
			isActive := strings.EqualFold(mdl.ID, m.modelName)
			isSelected := i == m.modelCursor

			activeIndicator := styleMuted.Render("○ ")
			if isActive {
				activeIndicator = ColorGreen("● ")
			}

			idStr := ColorBrightWhite(StyleBold(mdl.ID))
			if isSelected {
				idStr = ColorCyan(StyleBold(mdl.ID))
			}

			activeTag := ""
			if isActive {
				activeTag = " " + styleStatusNotice.Render("(Active)")
			}

			nameStr := ""
			if mdl.Name != "" && !strings.EqualFold(mdl.Name, mdl.ID) {
				nameStr = styleMuted.Render(" · " + mdl.Name)
			}

			prefix := "  "
			if isSelected {
				prefix = ColorCyan(StyleBold("› "))
			}

			row1 := prefix + activeIndicator + idStr + nameStr + activeTag

			var details []string
			if mdl.Provider != "" {
				details = append(details, fmt.Sprintf("Provider: %s", mdl.Provider))
			}
			if mdl.URL != "" {
				details = append(details, fmt.Sprintf("URL: %s", mdl.URL))
			}
			if mdl.ContextWindow > 0 {
				details = append(details, fmt.Sprintf("Ctx: %s tok", compactCount(mdl.ContextWindow)))
			}
			detailStr := "    " + styleMuted.Render(strings.Join(details, " · "))

			lines = append(lines, clampToWidth(row1, contentWidth-2))
			if len(details) > 0 {
				lines = append(lines, clampToWidth(detailStr, contentWidth-2))
			}
			if i < end-1 {
				lines = append(lines, "")
			}
		}
	}

	footer := "↑/↓ or j/k: Navigate · Enter: Select & Switch · Esc/q: Cancel"
	if len(models) == 0 {
		footer = "Esc/q: Close"
	}
	lines = append(lines, "", styleMuted.Render(footer))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(tuiColorCyan).
		Background(tuiColorCardBg).
		Padding(0, 1).
		Width(contentWidth).
		Render(strings.Join(lines, "\n"))

	return box
}

func (m *teaModel) openDiffModal(fileIdx int) {
	if len(m.changes.files) == 0 {
		m.statusNotice = "No modified files to inspect."
		return
	}
	if fileIdx < 0 {
		fileIdx = 0
	}
	if fileIdx >= len(m.changes.files) {
		fileIdx = len(m.changes.files) - 1
	}

	if !m.diffModal {
		// Moving between files keeps what the modal compares; opening it
		// starts from HEAD against the working tree.
		m.diffSource = diffSourceHead
	}
	m.diffModal = true
	m.diffConfirmDiscard = false
	m.diffCursor = fileIdx
	m.changes.SetCursor(fileIdx)
	m.diffPath = m.changes.files[fileIdx].Path
	m.diffViewport = viewport.New()
	m.loadDiffText()
	m.diffReady = true
	m.input.Blur()
}

func (m *teaModel) closeDiffModal() {
	m.diffModal = false
	m.diffReady = false
	m.diffConfirmDiscard = false
	m.changes.SetCursor(-1)
	m.input.Focus()
}

func (m *teaModel) nextDiffFile() {
	if len(m.changes.files) <= 1 {
		return
	}
	next := (m.diffCursor + 1) % len(m.changes.files)
	m.openDiffModal(next)
}

func (m *teaModel) prevDiffFile() {
	if len(m.changes.files) <= 1 {
		return
	}
	prev := (m.diffCursor - 1 + len(m.changes.files)) % len(m.changes.files)
	m.openDiffModal(prev)
}

func (m *teaModel) toggleStageCurrentFile() {
	if !m.checkpointMgr.IsGitRepo() || m.diffCursor < 0 || m.diffCursor >= len(m.changes.files) {
		return
	}
	f := m.changes.files[m.diffCursor]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if f.Staged {
		_ = gitrepo.Unstage(ctx, m.workingDir, []string{f.Path})
		m.statusNotice = "Unstaged " + filepath.Base(f.Path)
	} else {
		_ = gitrepo.Stage(ctx, m.workingDir, []string{f.Path})
		m.statusNotice = "Staged " + filepath.Base(f.Path)
	}

	if status, err := gitrepo.GetRepoStatus(ctx, m.workingDir); err == nil {
		m.changes.UpdateFromGit(status)
	}
	if m.diffCursor >= len(m.changes.files) {
		m.diffCursor = len(m.changes.files) - 1
	}
	if m.diffCursor >= 0 {
		m.openDiffModal(m.diffCursor)
	} else {
		m.closeDiffModal()
	}
}

// cycleDiffSource steps the diff modal through what it compares: HEAD
// with the working tree, then the unstaged changes, then the staged ones.
func (m *teaModel) cycleDiffSource() {
	m.diffSource = (m.diffSource + 1) % diffSourceCount
	m.loadDiffText()
}

func (m *teaModel) discardCurrentFile() {
	if m.diffCursor < 0 || m.diffCursor >= len(m.changes.files) {
		return
	}
	f := m.changes.files[m.diffCursor]
	fileName := filepath.Base(f.Path)

	if m.checkpointMgr.IsGitRepo() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		isUntracked := f.Status == "?"
		if err := gitrepo.RemoveFileChanges(ctx, m.workingDir, f.Path, isUntracked); err != nil {
			m.statusNotice = fmt.Sprintf("Discard failed: %v", err)
			return
		}
		if status, err := gitrepo.GetRepoStatus(ctx, m.workingDir); err == nil {
			m.changes.UpdateFromGit(status)
		} else {
			m.changes.RemoveFile(f.Path)
		}
	} else {
		fullPath := filepath.Join(m.workingDir, filepath.FromSlash(f.Path))
		_ = os.Remove(fullPath)
		m.changes.RemoveFile(f.Path)
	}

	if len(m.changes.files) == 0 {
		m.closeDiffModal()
		m.statusNotice = fmt.Sprintf("✓ Discarded %s. Working tree clean.", fileName)
		return
	}

	if m.diffCursor >= len(m.changes.files) {
		m.diffCursor = len(m.changes.files) - 1
	}
	m.openDiffModal(m.diffCursor)
	m.statusNotice = fmt.Sprintf("✓ Discarded %s.", fileName)
}

func (m *teaModel) renderDiffModal() string {
	modalWidth, vpWidth, vpHeight, split := m.diffModalGeometry()
	if m.diffViewport.Width() != vpWidth || m.diffViewport.Height() != vpHeight || split != m.diffShownSplit {
		m.diffViewport.SetWidth(vpWidth)
		m.diffViewport.SetHeight(vpHeight)
		m.layoutDiffContent()
	}

	badge := styleDiffHdr.Render("[Modified]")
	if m.diffCursor >= 0 && m.diffCursor < len(m.changes.files) {
		f := m.changes.files[m.diffCursor]
		if f.Staged {
			badge = styleDiffAdd.Render("[Staged]")
		} else if f.Status == "?" {
			badge = styleMuted.Render("[Untracked]")
		} else if f.Status == "D" {
			badge = styleDiffDel.Render("[Deleted]")
		}
	}

	counter := ""
	if total := len(m.changes.files); total > 0 {
		counter = fmt.Sprintf(" · File %d of %d", m.diffCursor+1, total)
	}

	langBadge := ""
	if lang := DetectLanguage(m.diffPath); lang != "" {
		langBadge = " " + styleHeaderPill.Render(strings.ToUpper(lang))
	}

	pathHeader := ColorBrightWhite(StyleBold(m.diffPath))
	titleRow := fmt.Sprintf("%s%s  %s%s", badge, langBadge, pathHeader, styleMuted.Render(counter))

	var queueItems []string
	for i, cf := range m.changes.files {
		marker := "M"
		if cf.Staged {
			marker = "S"
		} else if cf.Status == "?" {
			marker = "?"
		} else if cf.Status == "D" {
			marker = "D"
		}
		item := fmt.Sprintf("[%s] %s", marker, filepath.Base(cf.Path))
		if i == m.diffCursor {
			queueItems = append(queueItems, ColorCyan(StyleBold("› "+item)))
		} else {
			queueItems = append(queueItems, ColorGray(item))
		}
	}
	queueRow := styleMuted.Render("Queue: ") + strings.Join(queueItems, "  ")
	queueRow = clampToWidth(queueRow, vpWidth)

	view := "v: Unified"
	if !split {
		view = "v: Side by side"
	}
	legend := "n/p: Next/Prev  •  d: " + m.diffSource.label() + "  •  " + view + "  •  e: Edit  •  s: Stage/Unstage  •  x: Discard  •  Esc/q: Close"
	if m.diffConfirmDiscard {
		legend = ColorRed(StyleBold("⚠️  Discard all changes in " + filepath.Base(m.diffPath) + "?  [y] Confirm   [n/Esc] Cancel"))
	} else {
		legend = styleMuted.Render(legend)
	}

	lines := []string{
		titleRow,
		queueRow,
		clampToWidth(legend, vpWidth),
		styleMuted.Render(strings.Repeat("─", vpWidth)),
	}
	if split {
		before, after := m.diffSource.sides()
		if f, ok := m.currentDiffFile(); ok && f.Status == "?" {
			before = "(new file)"
		}
		lines = append(lines, SplitHeader(before, after, vpWidth))
	}
	lines = append(lines, m.diffViewport.View())

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(tuiColorCyan).
		Background(tuiColorCardBg).
		Padding(0, 1).
		Width(modalWidth).
		Render(strings.Join(lines, "\n"))

	return box
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
func (m *teaModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true // Clean full-screen TUI buffer
	v.MouseMode = tea.MouseModeAllMotion // motion without a button held drives hover
	v.ReportFocus = true
	v.WindowTitle = m.windowTitle()
	v.ProgressBar = m.progressBar()
	v.Cursor = m.terminalCursor()
	v.BackgroundColor, v.ForegroundColor = m.terminalColors()
	return v
}

// terminalColorWait is how long to wait for the terminal to report its
// background before painting the theme's over it anyway.
const terminalColorWait = 700 * time.Millisecond

// teaTermColorsTimeoutMsg ends the wait for the terminal's background colour.
type teaTermColorsTimeoutMsg struct{}

// terminalColors paints the whole terminal in the theme's background and text
// colours, so a theme looks the same whatever the terminal's own scheme;
// Bubble Tea restores the terminal's colours on exit. The terminal theme is
// left alone: following the terminal's scheme is its point.
func (m *teaModel) terminalColors() (bg, fg color.Color) {
	if !m.termColorsKnown || currentTheme.ANSI16 {
		return nil, nil
	}
	return tuiColorBg, lipgloss.Color(currentTheme.Text)
}

// terminalCursor places the terminal's own cursor at the textarea's caret.
func (m *teaModel) terminalCursor() *tea.Cursor {
	if m.inputOrigin == nil || m.modalOpen() {
		return nil
	}
	c := m.input.Cursor()
	if c == nil {
		return nil
	}
	c.Position.X += m.inputOrigin.X
	c.Position.Y += m.inputOrigin.Y
	return c
}

// windowTitle names the workspace in the terminal tab and says what fastllm
// is doing, so a background tab still shows when it needs attention.
func (m *teaModel) windowTitle() string {
	suffix := "fastllm " + SymDot + " " + filepath.Base(m.workingDir)
	switch {
	case m.pendingPermission != nil:
		return "! approval needed " + SymDot + " " + suffix
	case m.pendingPlan != "" && !m.isExecuting:
		return "! plan ready " + SymDot + " " + suffix
	case m.isExecuting:
		status := fmt.Sprintf("turn %d", m.activeTurn)
		if m.activeTool != "" {
			status += " " + SymDot + " " + m.activeTool
		}
		return status + " " + SymDot + " " + suffix
	case m.shellExecuting:
		return "shell " + SymDot + " " + suffix
	case m.turnFailed:
		return "failed " + SymDot + " " + suffix
	case m.finishedAway:
		return "done " + SymDot + " " + suffix
	}
	return suffix
}

// progressBar drives the terminal's native progress indicator (the tab and,
// on Windows, the taskbar button): busy while working, yellow when waiting on
// the user, red after a failed turn.
func (m *teaModel) progressBar() *tea.ProgressBar {
	switch {
	case m.pendingPermission != nil, m.pendingPlan != "" && !m.isExecuting:
		return tea.NewProgressBar(tea.ProgressBarWarning, 100)
	case m.isExecuting, m.shellExecuting:
		return tea.NewProgressBar(tea.ProgressBarIndeterminate, 0)
	case m.turnFailed:
		return tea.NewProgressBar(tea.ProgressBarError, 100)
	}
	return nil
}

// newlineKey names the key that inserts a newline in the input. Shift+Enter
// only reaches the program when the terminal disambiguates keys; Ctrl+J works
// everywhere (Alt+Enter is Windows Terminal's full-screen toggle).
func (m *teaModel) newlineKey() string {
	if m.keyDisambiguation {
		return "Shift+Enter"
	}
	return "Ctrl+J"
}

// syncInputStyles rebuilds the textarea's styles when the theme changes, so its
// selection highlight uses the active palette.
func (m *teaModel) syncInputStyles() {
	if m.inputStylesTheme == currentTheme.Name {
		return
	}
	m.inputStylesTheme = currentTheme.Name
	styles := m.input.Styles()
	styles.Focused.Selection = lipgloss.NewStyle().Background(tuiColorTrack).Foreground(tuiColorWhite)
	styles.Blurred.Selection = styles.Focused.Selection
	styles.Cursor.Color = tuiColorCyan
	styles.Cursor.Shape = tea.CursorBar
	styles.Cursor.Blink = true
	m.input.SetStyles(styles)
}

// lastCodeBlock returns the body of the last fenced code block in text, or ""
// when it has none.
func lastCodeBlock(text string) string {
	var block []string
	var last string
	inside := false
	for _, line := range strings.Split(text, "\n") {
		fence := strings.HasPrefix(strings.TrimSpace(line), "```")
		switch {
		case fence && !inside:
			inside, block = true, nil
		case fence && inside:
			inside, last = false, strings.Join(block, "\n")
		case inside:
			block = append(block, line)
		}
	}
	return last
}

// writeClipboard writes the local clipboard; tests replace it.
var writeClipboard = clipboard.WriteAll

// copyText puts text on the clipboard twice over: the local clipboard, and the
// terminal's own (OSC 52), which reaches the user's machine even over SSH,
// where the local clipboard belongs to the remote host or does not exist.
func (m *teaModel) copyText(text, what string) tea.Cmd {
	if err := writeClipboard(text); err != nil {
		m.statusNotice = "✓ Copied " + what + " through the terminal."
	} else {
		m.statusNotice = "✓ Copied " + what + " to the clipboard."
	}
	return tea.Batch(tea.SetClipboard(text), m.clearStatusAfter(3*time.Second))
}

// render draws the whole frame: the active modal, or the chat layout.
func (m *teaModel) render() string {
	if !m.ready {
		return "Initializing fastllm..."
	}
	m.hits.reset()
	if m.editorShown() {
		m.hits.add(hitModal, 0, 0, m.width, m.height)
		return m.renderEditor()
	}
	m.syncInputStyles()
	frame := m.renderChat()
	if box := m.renderModal(); box != "" {
		return m.overlayModal(frame, box)
	}
	return frame
}

// modalOpen reports whether a modal owns the keyboard and mouse.
func (m *teaModel) modalOpen() bool {
	return m.diffModal || m.modelsModal || m.skillsModal || m.editor != nil ||
		m.sessionsModal != nil || m.themeModal != nil || m.filesModal != nil
}

// editorShown reports whether the editor has the screen: it gives way to a
// permission or plan prompt, which owns the keyboard until answered.
func (m *teaModel) editorShown() bool {
	return m.editor != nil && m.pendingPermission == nil && (m.pendingPlan == "" || m.isExecuting)
}

// renderModal draws the open modal's box, or returns "" when none is open.
func (m *teaModel) renderModal() string {
	switch {
	case m.diffModal:
		return m.renderDiffModal()
	case m.modelsModal:
		return m.renderModelsModal()
	case m.sessionsModal != nil:
		return m.renderSessionsModal()
	case m.themeModal != nil:
		return m.renderThemeModal()
	case m.skillsModal:
		return m.renderSkillsModal()
	case m.filesModal != nil:
		return m.renderFilesModal()
	}
	return ""
}

// overlayModal composites box over the centre of frame so the conversation
// stays visible around it. While it is open the box is the only click target.
func (m *teaModel) overlayModal(frame, box string) string {
	w, h := lipgloss.Width(box), lipgloss.Height(box)
	x := max((max(m.width, lipgloss.Width(frame))-w)/2, 0)
	y := max((max(m.height, lipgloss.Height(frame))-h)/2, 0)
	m.hits.reset()
	m.hits.add(hitModal, x, y, w, h)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(frame),
		lipgloss.NewLayer(box).X(x).Y(y).Z(1),
	).Render()
}

// renderChat draws the conversation layout: header, transcript, input box,
// sidebar and status bar.
func (m *teaModel) renderChat() string {
	var sb strings.Builder

	// 1. Top Header Bar
	var modeBadge string
	if m.mode == modeAgent {
		modeBadge = styleAgentBadge.Foreground(permissionModeColor(m.permissionMode)).
			Underline(m.hover == hitMode).
			Render("◈ " + strings.ToUpper(m.permissionMode.Label()))
	} else {
		modeBadge = styleShellBadge.Render("❯_ SHELL")
	}

	brand := styleBrand.Render("fastllm")
	dirBase := filepath.Base(m.workingDir)
	if dirBase == "" || dirBase == "." {
		dirBase = m.workingDir
	}
	metaStyle := styleHeaderPill
	if m.hover == hitModel {
		metaStyle = metaStyle.Foreground(tuiColorWhite).Underline(true)
	}
	metaBadge := metaStyle.Render(dirBase + " " + SymDot + " " + m.modelName)
	agentBadge := ""
	if m.runner != nil && m.runner.agents != nil {
		if summary := m.runner.agents.Summary(); summary.Total > 0 {
			agentBadge = " " + styleHeaderPill.Render(fmt.Sprintf("agents %d/%d · %s tok", summary.Pending+summary.Running, summary.Total, compactCount(summary.TotalTokens)))
		}
	}
	serversBadge := ""
	if m.processMgr != nil {
		if active := m.processMgr.ActiveCount(); active > 0 {
			label := "server"
			if active > 1 {
				label = "servers"
			}
			serversBadge = " " + styleHeaderPill.Render(fmt.Sprintf("◈ %d %s", active, label))
		}
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
		rightInfo = styleMuted.Render(fmt.Sprintf("%.1fs %s %.0f tok/s",
			m.latestMetrics.Duration.Seconds(), SymDot, m.latestMetrics.TokensPerSecond))
	}

	headerLeft := lipgloss.JoinHorizontal(lipgloss.Center, brand, " ", modeBadge, " ", metaBadge, serversBadge, agentBadge)
	modeX := lipgloss.Width(brand) + 1
	if m.mode == modeAgent {
		m.hits.add(hitMode, modeX, 0, lipgloss.Width(modeBadge), 1)
	}
	m.hits.add(hitModel, modeX+lipgloss.Width(modeBadge)+1, 0, lipgloss.Width(metaBadge), 1)
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
	// The rule tees into the sidebar's separator so the two read as one frame.
	rule := strings.Repeat(SymHLine, m.frameWidth())
	if m.showChangesColumn() {
		convW := m.conversationWidth()
		rule = strings.Repeat(SymHLine, convW) + "┬" + strings.Repeat(SymHLine, m.frameWidth()-convW-1)
	}
	sb.WriteString(lipgloss.NewStyle().Foreground(tuiColorBorder).Render(rule) + "\n")

	// 2. Left column: the conversation, then slash-command suggestions (the
	// viewport already gave up their rows in resizeViewport), then the input
	// box. On wide terminals the sidebar runs beside all three, down to the
	// status bar.
	left := []string{m.viewport.View()}
	if dropdown := m.renderSuggestions(); dropdown != "" {
		left = append(left, dropdown)
	}

	// 3. Bottom Input Box with Rounded Border
	var borderCol color.Color = tuiColorBorder
	if m.mode == modeShell {
		borderCol = tuiColorYellow
	}
	inputContent := m.input.View()
	if len(m.pendingAttachments) > 0 {
		var pills []string
		for _, att := range m.pendingAttachments {
			icon := "📎"
			if att.Type == "image" || strings.HasPrefix(att.MimeType, "image/") {
				icon = "📷"
			}
			pills = append(pills, lipgloss.NewStyle().Foreground(tuiColorCyan).Bold(true).Render(fmt.Sprintf("[%s %s]", icon, att.Name)))
		}
		inputContent = styleMuted.Render("Attached: ") + strings.Join(pills, " ") + "\n" + inputContent
	}
	// The textarea's first cell sits inside the box border, below the
	// header, rule, transcript, dropdown and any attachment line.
	textareaShown := true
	if m.pendingPermission != nil {
		inputContent = FormatPermissionPrompt(m.pendingPermission.ToolName, m.pendingPermission.Summary) +
			"\n" + FormatPermissionKeyLegend(m.pendingPermission.scope().Describe())
		borderCol = tuiColorYellow
		textareaShown = false
	} else if m.pendingPlan != "" && !m.isExecuting {
		inputContent = m.renderPlanApproval()
		borderCol = tuiColorCyan
		textareaShown = false
	} else if m.search != nil {
		inputContent = m.renderSearchBar()
		borderCol = tuiColorCyan
		textareaShown = false
	}
	m.inputOrigin = nil
	if textareaShown {
		y := 2 + lipgloss.Height(strings.Join(left, "\n")) + 1
		if len(m.pendingAttachments) > 0 {
			y++
		}
		m.inputOrigin = &tea.Position{X: 1, Y: y}
	}
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderCol).
		Width(m.inputBoxWidth()).
		Render(inputContent)
	left = append(left, inputBox)
	body := strings.Join(left, "\n")
	if m.showChangesColumn() {
		// Pad the left column to its full width so the sidebar lines up on
		// every row, including the input box's.
		body = lipgloss.NewStyle().Width(m.conversationWidth()).Render(body)
		// The header and its rule take the first two rows.
		sidebar := m.renderSidebar(m.conversationWidth(), 2, lipgloss.Height(body))
		body = lipgloss.JoinHorizontal(lipgloss.Top, body, sidebar)
	}
	sb.WriteString(body + "\n")

	// 4. Status Bar / Keymap Hints
	// Hints drop to a short form rather than wrapping: the full string is ~97
	// columns, so on a narrower terminal it silently became a second row and
	// pushed the frame past the terminal height.
	hints := "Tab: Shell  " + SymDot + "  Enter: Send  " + SymDot + "  " + m.newlineKey() + ": Newline  " + SymDot + "  Shift+Tab: Permissions  " + SymDot + "  /help"
	shortHints := "Tab: Shell  " + SymDot + "  Enter: Send  " + SymDot + "  /help"
	if m.mode == modeShell {
		hints = "Tab: Agent  " + SymDot + "  Enter: Run  " + SymDot + "  exit: Leave Shell  " + SymDot + "  Ctrl+B: Bg"
		shortHints = "Tab: Agent  " + SymDot + "  Enter: Run  " + SymDot + "  exit"
	}
	if m.search != nil {
		hints = "Enter/↓: Next  " + SymDot + "  ↑: Previous  " + SymDot + "  Esc: Close find"
		shortHints = "Enter: Next  " + SymDot + "  ↑: Prev  " + SymDot + "  Esc"
	} else if m.isExecuting {
		hints = "Esc / Ctrl+C: Cancel"
		shortHints = hints
	} else if m.shellExecuting {
		hints = "Ctrl+B: Background  " + SymDot + "  Esc / Ctrl+C: Cancel"
		shortHints = "Ctrl+B: Bg  " + SymDot + "  Esc: Cancel"
	} else if suggestHints, ok := m.suggestionHints(); ok {
		hints, shortHints = suggestHints, suggestHints
	}
	used, budget := m.contextUsage()
	gauge := formatContextGauge(used, budget, 8)
	if m.showChangesColumn() {
		// The sidebar's Context section carries the full meter.
		gauge = formatContextGauge(used, budget, 0)
	}
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
	LoadThemePreference()

	model, err := newTeaModel(r, req)
	if err != nil {
		return err
	}

	p := tea.NewProgram(model)
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
