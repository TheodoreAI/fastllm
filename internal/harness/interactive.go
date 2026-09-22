package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"fastllm/internal/config"
	"fastllm/internal/files"
	"fastllm/internal/llm"
	"fastllm/internal/webtools"
)

// RunInteractive starts an interactive terminal TUI / REPL session.
func (r *Runner) RunInteractive(initialReq RunRequest) error {
	if os.Getenv("FASTLLM_SIMPLE_TUI") != "1" {
		return r.RunBubbleTea(initialReq)
	}
	return r.runSimpleInteractive(initialReq)
}

func (r *Runner) runSimpleInteractive(initialReq RunRequest) error {
	workingDir := initialReq.WorkingDir
	if workingDir == "" {
		workingDir = r.DefaultWorkingDir
	}
	absWorkingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return fmt.Errorf("resolve working dir: %w", err)
	}

	// Load configuration (.fastllm/config.json or ~/.fastllm/config.json)
	settings, configPath, err := config.LoadSettings(absWorkingDir)
	if err != nil {
		return err
	}

	model := initialReq.Model
	if model == "" {
		if settings != nil && settings.DefaultModel != "" {
			model = settings.DefaultModel
		} else {
			model = r.DefaultModel
		}
	}

	systemPrompt := initialReq.SystemPrompt
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = DefaultSystemPrompt
	}

	// Auto-discover workspace rules (AGENTS.md, CLAUDE.md, etc.)
	discoveredRules := DiscoverWorkspaceRules(absWorkingDir)
	rulesPrompt := FormatRulesForPrompt(discoveredRules)
	discoveredSkills := DiscoverWorkspaceSkills(absWorkingDir)

	allowCmds := r.AllowCommands
	maxTurns := initialReq.MaxTurns
	if maxTurns <= 0 {
		maxTurns = r.DefaultMaxTurns
	}
	if maxTurns <= 0 {
		maxTurns = 20
	}
	cmdTimeout := initialReq.CommandTimeout
	if cmdTimeout <= 0 {
		cmdTimeout = r.CommandTimeout
	}
	if cmdTimeout <= 0 {
		cmdTimeout = 60 * time.Second
	}
	thinkLevel := initialReq.ThinkLevel
	permissionMode := PermissionAsk
	expandedTools := false

	// Initialize Git checkpoint manager and background process manager
	checkpointMgr := NewCheckpointManager(absWorkingDir)
	processMgr := NewProcessManager()
	defer processMgr.KillAll()

	initConsole()

	var sessionMetrics SessionMetrics

	fmt.Println(FormatWelcomeBanner(absWorkingDir, model, configPath, checkpointMgr.IsGitRepo(), len(discoveredRules), allowCmds))

	fileReader := files.New(absWorkingDir, true)

	sessionMessages := []llm.Message{
		{Role: "system", Content: systemPrompt + rulesPrompt},
	}
	sessionStore, sessionStoreErr := DefaultSessionStore()
	var completionModels, completionSessions []string
	refreshCompletions := func() {
		completionModels = completionModels[:0]
		if settings != nil {
			for _, item := range settings.Models {
				completionModels = append(completionModels, item.ID)
			}
		}
		completionSessions = completionSessions[:0]
		if sessionStore != nil {
			if items, err := sessionStore.List(); err == nil {
				for _, item := range items {
					completionSessions = append(completionSessions, item.ID)
				}
			}
		}
	}
	refreshCompletions()
	historyPath := filepath.Join(os.TempDir(), "fastllm-history")
	if sessionStore != nil {
		historyPath = filepath.Join(filepath.Dir(sessionStore.Dir), "history")
	}
	input := newInteractiveInput(historyPath, func(line string, cursor int) []string {
		return interactiveCompletions(line, cursor, absWorkingDir, completionModels, completionSessions)
	})
	defer input.Close()
	permissions := NewPermissionController(permissionMode, input)
	tools := interactiveTools(allowCmds, permissionMode, r.EnableObservations)
	var activeSession *InteractiveSession
	if sessionStoreErr == nil {
		activeSession = sessionStore.New(absWorkingDir, model, InteractiveRuntime{
			MaxTurns: maxTurns, CommandTimeout: cmdTimeout, ThinkLevel: thinkLevel, AllowCommands: allowCmds, PermissionMode: permissionMode,
		})
	}
	observationSessionID := "interactive"
	if activeSession != nil {
		observationSessionID = activeSession.ID
	}
	var observationStore *ObservationStore
	if r.EnableObservations {
		observationStore, _ = DefaultObservationStore(observationSessionID)
	}
	observations := &ObservationManager{Store: observationStore}

	saveSession := func() {
		if sessionStore == nil || activeSession == nil {
			return
		}
		activeSession.WorkingDir = absWorkingDir
		activeSession.Model = model
		activeSession.Runtime = InteractiveRuntime{
			MaxTurns: maxTurns, CommandTimeout: cmdTimeout, ThinkLevel: thinkLevel, AllowCommands: allowCmds, PermissionMode: permissionMode, ExpandedTools: expandedTools,
		}
		activeSession.Messages = append([]llm.Message(nil), sessionMessages[1:]...)
		if !activeSession.CustomTitle {
			activeSession.Title = sessionTitle(activeSession.Messages)
		}
		if err := sessionStore.Save(activeSession); err != nil {
			fmt.Println(ColorYellow(fmt.Sprintf("  %s Session autosave failed: %v", SymCross, err)))
		} else {
			refreshCompletions()
		}
	}
	if sessionStoreErr != nil {
		fmt.Println(ColorYellow(fmt.Sprintf("  %s Session persistence unavailable: %v", SymCross, sessionStoreErr)))
	}

	switchWorkspace := func(newDir string) error {
		resolvedDir, err := resolveInteractiveDirectory(absWorkingDir, newDir)
		if err != nil {
			return err
		}
		newSettings, newConfigPath, err := config.LoadSettings(resolvedDir)
		if err != nil {
			return err
		}
		absWorkingDir = resolvedDir
		fileReader = files.New(absWorkingDir, true)
		checkpointMgr = NewCheckpointManager(absWorkingDir)
		discoveredRules = DiscoverWorkspaceRules(absWorkingDir)
		rulesPrompt = FormatRulesForPrompt(discoveredRules)
		discoveredSkills = DiscoverWorkspaceSkills(absWorkingDir)
		settings, configPath = newSettings, newConfigPath
		sessionMessages = []llm.Message{
			{Role: "system", Content: systemPrompt + rulesPrompt},
		}
		fmt.Println(FormatWelcomeBanner(absWorkingDir, model, configPath, checkpointMgr.IsGitRepo(), len(discoveredRules), allowCmds))
		return nil
	}

	loadSession := func(loaded *InteractiveSession) error {
		resolvedDir, err := resolveInteractiveDirectory(absWorkingDir, loaded.WorkingDir)
		if err != nil {
			return err
		}
		model = loaded.Model
		maxTurns = loaded.Runtime.MaxTurns
		if maxTurns <= 0 {
			maxTurns = 20
		}
		cmdTimeout = loaded.Runtime.CommandTimeout
		if cmdTimeout <= 0 {
			cmdTimeout = 60 * time.Second
		}
		thinkLevel, allowCmds = loaded.Runtime.ThinkLevel, loaded.Runtime.AllowCommands
		permissionMode = loaded.Runtime.PermissionMode
		if permissionMode == "" {
			permissionMode = PermissionAsk
		}
		expandedTools = loaded.Runtime.ExpandedTools
		permissions.SetMode(permissionMode)
		tools = interactiveTools(allowCmds, permissionMode, r.EnableObservations)
		if err := switchWorkspace(resolvedDir); err != nil {
			return err
		}
		sessionMessages = append([]llm.Message{{Role: "system", Content: systemPrompt + rulesPrompt}}, loaded.Messages...)
		loaded.ClosedAt = nil
		activeSession = loaded
		if observationStore != nil {
			observationStore.SetSession(loaded.ID)
		}
		return nil
	}

	if sessionStore != nil {
		_, _ = sessionStore.Prune(time.Now().UTC(), 100)
		var recovery *InteractiveSession
		if initialReq.ResumeSession != "" {
			if strings.EqualFold(initialReq.ResumeSession, "last") {
				recovery, err = sessionStore.Latest()
			} else {
				recovery, err = sessionStore.Load(initialReq.ResumeSession)
			}
		} else if latest, latestErr := sessionStore.Latest(); latestErr == nil && latest.ClosedAt == nil && len(latest.Messages) > 0 {
			recovery = latest
		}
		if err == nil && recovery != nil {
			err = loadSession(recovery)
			if err == nil {
				fmt.Println(ColorGreen(fmt.Sprintf("  %s Recovered session %s — %s", SymCheck, recovery.ID, recovery.Title)))
			}
		}
		if err != nil {
			fmt.Println(ColorYellow(fmt.Sprintf("  %s Could not resume session: %v", SymCross, err)))
		}
		saveSession()
	}

	runShellCommand := func(command string) {
		path, isDirectoryChange := parseDirectoryChange(command)
		if isDirectoryChange {
			if err := switchWorkspace(path); err != nil {
				fmt.Println(ColorRed(fmt.Sprintf("  %s Cannot change directory: %v", SymCross, err)))
			} else {
				saveSession()
			}
			return
		}
		_ = runInteractiveCommand(absWorkingDir, command)
	}

	var shellMode bool

	for {
		// Rebuilt every prompt so it reflects the turn that just finished.
		status := FormatStatusLine(StatusLine{
			Model:          model,
			PermissionMode: permissionMode,
			ShellMode:      shellMode,
			ContextChars:   messageCharacterCount(sessionMessages),
			ContextBudget:  DefaultCompactionConfig().MaxTotalChars,
			Turns:          sessionMetrics.TotalTurns,
			TotalTokens:    sessionMetrics.TotalTokens,
			Cost:           sessionMetrics.TotalCost,
			WorkingDir:     absWorkingDir,
			// Re-read each prompt: /dir can move the session to another repo.
			Branch: currentBranch(absWorkingDir),
		})
		// The status line already names the model, so the prompt stays bare.
		prompt := status + FormatPrompt("")
		if shellMode {
			prompt = status + FormatShellPrompt(absWorkingDir)
		}
		rawLine, readErr := input.ReadLine(prompt)
		if readErr != nil {
			break
		}
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		skillPrompt := ""

		// If in shell mode, execute shell commands directly
		if shellMode {
			if line == "exit" || line == "quit" || line == "/exit" || line == "/quit" || line == "/shell" || line == "/sh" {
				shellMode = false
				fmt.Println(ColorGreen("  " + SymCheck + " Returned to Agent Mode."))
				continue
			}
			if line == "/c" || line == "/clear" {
				ClearScreen()
				sessionMessages = []llm.Message{
					{Role: "system", Content: systemPrompt + rulesPrompt},
				}
				fmt.Println(ColorGreen("  " + SymCheck + " Conversation and screen cleared."))
				saveSession()
				continue
			}
			if line == "/cls" {
				ClearScreen()
				fmt.Println(ColorGreen("  " + SymCheck + " Screen cleared."))
				continue
			}
			if line == "/help" {
				fmt.Println(FormatHelp())
				continue
			}
			runShellCommand(line)
			continue
		}

		// In Agent mode: instant shell command execution via !<cmd> or $ <cmd>
		if strings.HasPrefix(line, "!") {
			cmdStr := strings.TrimSpace(line[1:])
			if cmdStr != "" {
				runShellCommand(cmdStr)
			}
			continue
		}
		if strings.HasPrefix(line, "$ ") {
			cmdStr := strings.TrimSpace(line[2:])
			if cmdStr != "" {
				runShellCommand(cmdStr)
			}
			continue
		}

		// Handle slash commands
		if strings.HasPrefix(line, "/") {
			parts := strings.Fields(line)
			cmd := strings.ToLower(parts[0])
			switch cmd {
			case "/exit", "/quit":
				if activeSession != nil {
					now := time.Now().UTC()
					activeSession.ClosedAt = &now
				}
				saveSession()
				fmt.Println(ColorGray("\nSession ended. Goodbye!"))
				return nil

			case "/help":
				fmt.Println(FormatHelp())
				continue

			case "/shell", "/sh":
				if len(parts) > 1 {
					cmdStr := strings.TrimSpace(line[len(parts[0]):])
					runShellCommand(cmdStr)
					continue
				}
				shellMode = true
				fmt.Println(ColorYellow("  " + SymBranch + " Engaged Shell Mode. Type shell commands directly, or 'exit' / '/shell' to return to agent."))
				continue

			case "/c", "/clear":
				ClearScreen()
				sessionMessages = []llm.Message{
					{Role: "system", Content: systemPrompt + rulesPrompt},
				}
				fmt.Println(FormatWelcomeBanner(absWorkingDir, model, configPath, checkpointMgr.IsGitRepo(), len(discoveredRules), allowCmds))
				fmt.Println(ColorGreen("  " + SymCheck + " Conversation and screen cleared."))
				saveSession()
				continue

			case "/cls":
				ClearScreen()
				fmt.Println(ColorGreen("  " + SymCheck + " Screen cleared."))
				continue

			case "/undo":
				if !checkpointMgr.IsGitRepo() {
					fmt.Println(ColorYellow("  " + SymCross + " Current directory is not a Git repository; checkpoints disabled."))
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err := checkpointMgr.Rollback(ctx, "")
				cancel()
				if err != nil {
					fmt.Println(ColorRed(fmt.Sprintf("  %s Undo failed: %v", SymCross, err)))
				} else {
					fmt.Println(ColorGreen(fmt.Sprintf("  %s Successfully rolled back working tree to pre-turn checkpoint.", SymCheck)))
				}
				continue

			case "/diff":
				if !checkpointMgr.IsGitRepo() {
					fmt.Println(ColorYellow("  " + SymCross + " Current directory is not a Git repository."))
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				diff, err := checkpointMgr.Diff(ctx)
				cancel()
				if err != nil {
					fmt.Println(ColorRed(fmt.Sprintf("  %s Diff failed: %v", SymCross, err)))
				} else if strings.TrimSpace(diff) == "" {
					fmt.Println(ColorGray("  Working tree clean (no uncommitted changes)."))
				} else {
					fmt.Println("\n" + HighlightDiff(diff))
				}
				continue

			case "/status":
				sessionMetrics.ObservationEfficiency = observations.Stats()
				fmt.Println(FormatStatusCard(absWorkingDir, model, len(discoveredRules), sessionMetrics, processMgr))
				fmt.Println(FormatRuntimeCard(maxTurns, cmdTimeout, thinkLevel, allowCmds, permissionMode, activeSessionID(activeSession)))
				if summary := r.agents.Summary(); summary.Total > 0 {
					fmt.Println(FormatCard("Child Agents", strings.Split(r.agents.Status(""), "\n"), 74))
				}
				continue

			case "/sessions":
				if sessionStore == nil {
					fmt.Println(ColorYellow("  Session persistence is unavailable."))
					continue
				}
				sessions, err := sessionStore.List()
				if err != nil {
					fmt.Println(ColorRed(fmt.Sprintf("  %s Cannot list sessions: %v", SymCross, err)))
				} else {
					fmt.Println(FormatSessionsTable(sessions, activeSessionID(activeSession)))
				}
				continue

			case "/session":
				target := activeSession
				var detailErr error
				if len(parts) > 1 && sessionStore != nil {
					target, detailErr = sessionStore.Load(parts[1])
				}
				if detailErr != nil || target == nil {
					fmt.Println(ColorRed(fmt.Sprintf("  %s Cannot load session details: %v", SymCross, detailErr)))
				} else {
					fmt.Printf("\n%s\n  ID: %s\n  Title: %s\n  Updated: %s\n  Directory: %s\n  Model: %s\n  Messages: %d\n\n", ColorCyan(StyleBold("Session Details")), target.ID, target.Title, target.UpdatedAt.Local().Format(time.RFC1123), target.WorkingDir, target.Model, len(target.Messages))
				}
				continue

			case "/rename":
				if activeSession == nil || sessionStore == nil || len(parts) < 2 {
					fmt.Println(ColorYellow("  Usage: /rename <new title>"))
					continue
				}
				title := strings.TrimSpace(line[len(parts[0]):])
				activeSession.Title, activeSession.CustomTitle = title, true
				saveSession()
				fmt.Println(ColorGreen("  " + SymCheck + " Session renamed."))
				continue

			case "/delete-session":
				if sessionStore == nil || len(parts) < 2 {
					fmt.Println(ColorYellow("  Usage: /delete-session <session-id>"))
					continue
				}
				if activeSession != nil && parts[1] == activeSession.ID {
					fmt.Println(ColorYellow("  Start or resume another session before deleting the active session."))
					continue
				}
				if err := sessionStore.Delete(parts[1]); err != nil {
					fmt.Println(ColorRed(fmt.Sprintf("  %s Delete failed: %v", SymCross, err)))
				} else {
					if observationStore != nil {
						_ = observationStore.DeleteSession(parts[1])
					}
					refreshCompletions()
					fmt.Println(ColorGreen("  " + SymCheck + " Session deleted."))
				}
				continue

			case "/resume":
				if sessionStore == nil || len(parts) < 2 {
					fmt.Println(ColorYellow("  Usage: /resume <session-id>"))
					continue
				}
				loaded, err := sessionStore.Load(parts[1])
				if err != nil {
					fmt.Println(ColorRed(fmt.Sprintf("  %s Cannot resume session: %v", SymCross, err)))
					continue
				}
				if activeSession != nil {
					now := time.Now().UTC()
					activeSession.ClosedAt = &now
				}
				saveSession()
				if err := loadSession(loaded); err != nil {
					fmt.Println(ColorRed(fmt.Sprintf("  %s Cannot restore workspace: %v", SymCross, err)))
					continue
				}
				fmt.Println(ColorGreen(fmt.Sprintf("  %s Resumed %s — %s", SymCheck, loaded.ID, loaded.Title)))
				continue

			case "/new":
				if activeSession != nil {
					now := time.Now().UTC()
					activeSession.ClosedAt = &now
				}
				saveSession()
				sessionMessages = []llm.Message{{Role: "system", Content: systemPrompt + rulesPrompt}}
				sessionMetrics = SessionMetrics{}
				if sessionStore != nil {
					activeSession = sessionStore.New(absWorkingDir, model, InteractiveRuntime{
						MaxTurns: maxTurns, CommandTimeout: cmdTimeout, ThinkLevel: thinkLevel, AllowCommands: allowCmds, PermissionMode: permissionMode, ExpandedTools: expandedTools,
					})
					if observationStore != nil {
						observationStore.SetSession(activeSession.ID)
					}
					saveSession()
				}
				fmt.Println(ColorGreen("  " + SymCheck + " Started a new session."))
				continue

			case "/set":
				if len(parts) == 1 {
					fmt.Println(FormatRuntimeCard(maxTurns, cmdTimeout, thinkLevel, allowCmds, permissionMode, activeSessionID(activeSession)))
					continue
				}
				if len(parts) < 3 {
					fmt.Println(ColorYellow("  Usage: /set <turns|timeout|think|commands|permissions|output> <value>"))
					continue
				}
				value := strings.ToLower(parts[2])
				var setErr error
				switch strings.ToLower(parts[1]) {
				case "turns":
					var parsed int
					parsed, setErr = strconv.Atoi(value)
					if setErr == nil && (parsed < 1 || parsed > 100) {
						setErr = fmt.Errorf("turns must be between 1 and 100")
					}
					if setErr == nil {
						maxTurns = parsed
					}
				case "timeout":
					var seconds int
					seconds, setErr = strconv.Atoi(value)
					if setErr == nil && (seconds < 1 || seconds > 3600) {
						setErr = fmt.Errorf("timeout must be between 1 and 3600 seconds")
					}
					if setErr == nil {
						cmdTimeout = time.Duration(seconds) * time.Second
					}
				case "think":
					if value == "off" {
						thinkLevel = ""
					} else if value == "low" || value == "medium" || value == "high" {
						thinkLevel = value
					} else {
						setErr = fmt.Errorf("think must be off, low, medium, or high")
					}
				case "commands":
					if value == "on" {
						allowCmds = true
					} else if value == "off" {
						allowCmds = false
					} else {
						setErr = fmt.Errorf("commands must be on or off")
					}
					tools = interactiveTools(allowCmds, permissionMode, r.EnableObservations)
				case "permissions", "permission":
					var parsed PermissionMode
					parsed, setErr = ParsePermissionMode(value)
					if setErr == nil {
						permissionMode = parsed
						permissions.SetMode(parsed)
						tools = interactiveTools(allowCmds, permissionMode, r.EnableObservations)
					}
				case "output":
					if value == "expanded" {
						expandedTools = true
					} else if value == "compact" {
						expandedTools = false
					} else {
						setErr = fmt.Errorf("output must be compact or expanded")
					}
				default:
					setErr = fmt.Errorf("unknown setting %q", parts[1])
				}
				if setErr != nil {
					fmt.Println(ColorRed(fmt.Sprintf("  %s %v", SymCross, setErr)))
				} else {
					saveSession()
					fmt.Println(FormatRuntimeCard(maxTurns, cmdTimeout, thinkLevel, allowCmds, permissionMode, activeSessionID(activeSession)))
				}
				continue

			case "/rules":
				if len(discoveredRules) == 0 {
					fmt.Println(ColorGray("  No workspace instruction files discovered in this directory or parents."))
				} else {
					fmt.Println(ColorCyan(StyleBold(fmt.Sprintf("\nDiscovered %d workspace rule file(s):", len(discoveredRules)))))
					for _, r := range discoveredRules {
						lines := []string{
							"",
							FormatKV("file", r.Filename, 8),
							FormatKV("path", r.Path, 8),
							"",
							ColorGray(r.Content),
							"",
						}
						fmt.Println(FormatCard(r.Filename, lines, 74))
					}
				}
				continue

			case "/skills":
				name, task := parseSkillInvocation(line)
				if name == "" {
					fmt.Println(formatSkillList(discoveredSkills))
					continue
				}
				skill, ok := findSkill(discoveredSkills, name)
				if !ok {
					fmt.Println(ColorRed(fmt.Sprintf("  %s Unknown skill %q. Use /skills to list project skills.", SymCross, name)))
					continue
				}
				if task == "" {
					fmt.Println(formatSkillDetails(skill, absWorkingDir))
					continue
				}
				line = task
				skillPrompt = formatSkillPrompt(skill, absWorkingDir)
				fmt.Println(ColorGray("  Using skill " + skill.Name + " for this turn."))

			case "/ps":
				procs := processMgr.List()
				if len(procs) == 0 {
					fmt.Println(ColorGray("  No background processes running."))
				} else {
					fmt.Println(processMgr.FormatProcessTable())
				}
				continue

			case "/kill":
				if len(parts) < 2 {
					fmt.Println(ColorYellow("  Usage: /kill <process_id> (e.g. /kill proc-1)"))
				} else {
					err := processMgr.Kill(parts[1])
					if err != nil {
						fmt.Println(ColorRed(fmt.Sprintf("  %s Error killing process %s: %v", SymCross, parts[1], err)))
					} else {
						fmt.Println(ColorGreen(fmt.Sprintf("  %s Killed process %s.", SymCheck, parts[1])))
					}
				}
				continue

			case "/model", "/models":
				if len(parts) == 1 || (len(parts) == 2 && parts[1] == "list") {
					if settings != nil && len(settings.Models) > 0 {
						fmt.Println(FormatModelsTable(settings.Models, model, configPath))
					} else {
						fmt.Println(ColorGray("  (no models configured in " + configPath + ")"))
					}
					fmt.Println("\n" + ColorGray("Usage: /model <name> | /models add <id> <url>"))
					continue
				}

				if len(parts) >= 4 && strings.ToLower(parts[1]) == "add" {
					newID := parts[2]
					newURL := parts[3]
					if settings == nil {
						settings = config.DefaultSettings()
					}
					settings.AddOrUpdateModel(config.ModelEndpoint{
						ID:   newID,
						Name: newID,
						URL:  newURL,
					})
					relocated, err := config.SaveSettings(configPath, settings)
					if err != nil {
						fmt.Println(ColorRed(fmt.Sprintf("  %s Error saving to %s: %v", SymCross, configPath, err)))
					} else {
						fmt.Println(ColorGreen(fmt.Sprintf("  %s Added model %q (%s) to %s", SymCheck, newID, newURL, configPath)))
						if len(relocated) > 0 {
							fmt.Println(ColorYellow(fmt.Sprintf("  %s API keys for %s were moved out of %s into ~/.fastllm/keys/ and are now referenced by api_key_file.", SymDot, strings.Join(relocated, ", "), filepath.Base(configPath))))
						}
					}
					continue
				}

				targetModel := parts[1]
				matched := settings.FindModel(targetModel)
				if matched == nil {
					fmt.Println(ColorRed(fmt.Sprintf("  %s Model %q is not configured. Use /models to list available models.", SymCross, targetModel)))
					continue
				}
				if err := r.SwitchModel(matched); err != nil {
					fmt.Println(ColorRed(fmt.Sprintf("  %s Could not switch model: %v", SymCross, err)))
					continue
				}
				model = matched.ID
				fmt.Println(ColorGreen(fmt.Sprintf("  %s Switched model to: %s (%s, URL: %s)", SymCheck, matched.ID, matched.Name, matched.URL)))
				saveSession()
				continue

			case "/dir":
				if len(parts) < 2 {
					fmt.Printf("Current directory: %s\nUsage: /dir <path>\n", absWorkingDir)
				} else {
					if err := switchWorkspace(strings.TrimSpace(line[len(parts[0]):])); err != nil {
						fmt.Printf("Invalid directory %q: %v\n", parts[1], err)
					} else {
						saveSession()
					}
				}
				continue

			case "/search":
				if len(parts) < 2 {
					fmt.Println(ColorYellow("  Usage: /search <query> (e.g. /search golang context timeout)"))
				} else {
					q := strings.TrimSpace(line[len(parts[0]):])
					fmt.Println(ColorGray(fmt.Sprintf("  Searching the web for %q...", q)))
					ctxSearch, cancelSearch := context.WithTimeout(context.Background(), 15*time.Second)
					res := webtools.SearchFormatted(ctxSearch, q, 5)
					cancelSearch()
					fmt.Println("\n" + res)
				}
				continue

			case "/fetch":
				if len(parts) < 2 {
					fmt.Println(ColorYellow("  Usage: /fetch <url> (e.g. /fetch https://go.dev/blog/go1.25)"))
				} else {
					u := strings.TrimSpace(parts[1])
					fmt.Println(ColorGray(fmt.Sprintf("  Fetching %s...", u)))
					ctxFetch, cancelFetch := context.WithTimeout(context.Background(), 15*time.Second)
					content, err := webtools.Fetch(ctxFetch, u, 16384)
					cancelFetch()
					if err != nil {
						fmt.Println(ColorRed(fmt.Sprintf("  %s Fetch error: %v", SymCross, err)))
					} else {
						fmt.Println("\n" + content)
					}
				}
				continue

			case "/image":
				if len(parts) < 2 {
					fmt.Println(ColorYellow("  Usage: /image <prompt>"))
					continue
				}
				prompt := strings.TrimSpace(line[len(parts[0]):])
				endpoint := configuredImageEndpoint(settings)
				if endpoint == nil {
					fmt.Println(ColorRed("  No image model is configured. Add a model whose id or name contains \"image\"."))
					continue
				}
				fmt.Println(ColorGray(fmt.Sprintf("  Generating image with %s...", endpoint.Name)))
				ctxImage, cancelImage := context.WithTimeout(context.Background(), 15*time.Minute)
				started := time.Now()
				path, size, err := generateConfiguredImage(ctxImage, settings, absWorkingDir, prompt)
				cancelImage()
				if err != nil {
					fmt.Println(ColorRed(fmt.Sprintf("  %s Image generation failed: %v", SymCross, err)))
				} else {
					fmt.Println(ColorGreen(fmt.Sprintf("  %s Image saved: %s (%d bytes, %.1fs)", SymCheck, path, size, time.Since(started).Seconds())))
				}
				continue

			default:
				fmt.Printf("Unknown command %q. Type /help for available commands.\n", cmd)
				continue
			}
		}

		if line == "exit" || line == "quit" {
			if activeSession != nil {
				now := time.Now().UTC()
				activeSession.ClosedAt = &now
			}
			saveSession()
			fmt.Println("Goodbye!")
			return nil
		}

		// Save checkpoint before executing agent turns for this user prompt
		if checkpointMgr.IsGitRepo() {
			ctxCp, cancelCp := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = checkpointMgr.CreateCheckpoint(ctxCp, "turn: "+line)
			cancelCp()
		}

		// Add user turn. Skill instructions apply only to this turn and are
		// removed before the durable session transcript is saved.
		baseSystemPrompt := sessionMessages[0].Content
		if skillPrompt != "" {
			sessionMessages[0].Content += skillPrompt
		}
		sessionMessages = append(sessionMessages, llm.Message{
			Role:    "user",
			Content: line,
		})

		ctx, cancel := context.WithCancel(context.Background())
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		go func() {
			select {
			case <-sigChan:
				fmt.Println("\n[Canceling turn...]")
				cancel()
			case <-ctx.Done():
			}
		}()

		r.runInteractiveTurn(
			ctx,
			model,
			tools,
			fileReader,
			absWorkingDir,
			allowCmds,
			cmdTimeout,
			thinkLevel,
			maxTurns,
			permissions,
			processMgr,
			&sessionMetrics,
			&sessionMessages,
			expandedTools,
			observations,
			observationStore,
		)
		sessionMessages[0].Content = baseSystemPrompt
		saveSession()
		cancel()
		signal.Stop(sigChan)
	}

	if activeSession != nil {
		now := time.Now().UTC()
		activeSession.ClosedAt = &now
	}
	saveSession()
	return nil
}

func (r *Runner) runInteractiveTurn(
	ctx context.Context,
	model string,
	tools []llm.Tool,
	fileReader *files.Reader,
	absWorkingDir string,
	allowCmds bool,
	cmdTimeout time.Duration,
	thinkLevel string,
	maxTurns int,
	permissions *PermissionController,
	processMgr *ProcessManager,
	sessionMetrics *SessionMetrics,
	sessionMessages *[]llm.Message,
	expandedTools bool,
	observations *ObservationManager,
	observationStore *ObservationStore,
) {
	var planBoundary bool
	for turn := 1; turn <= maxTurns; turn++ {
		if ctx.Err() != nil {
			fmt.Println("Turn canceled.")
			return
		}

		contextChars := messageCharacterCount(*sessionMessages)
		cfg := planBoundaryCompactionConfig(DefaultCompactionConfig(), contextChars, planBoundary)

		var compacted bool
		*sessionMessages, compacted = OnlineCompactMessages(*sessionMessages, cfg)
		planBoundary = false

		// Report either the compaction or the approach to it, never both: warning
		// that context is "at 112% of the threshold" and then that it was already
		// compacted describes one event as if it were a problem plus a fix.
		switch {
		case compacted:
			after := messageCharacterCount(*sessionMessages)
			fmt.Println(ColorGray(fmt.Sprintf("  Context reached %s; compacted older tool output down to %s.",
				formatCharCount(contextChars), formatCharCount(after))))
		case contextChars >= cfg.MaxTotalChars*3/4:
			fmt.Println(ColorGray(fmt.Sprintf("  Context is at %d%% of the compaction budget (%s).",
				contextChars*100/cfg.MaxTotalChars, formatCharCount(cfg.MaxTotalChars))))
		}

		turnStart := time.Now()
		promptTokens := countApproxTokens(*sessionMessages)

		thinking := NewSpinner("Thinking")
		thinking.Start()
		modelMessages := observations.Project(*sessionMessages, turn)
		chatResult, err := chatWithRetry(ctx, r.LLM, model, modelMessages, tools, thinkLevel, func(attempt int, delay time.Duration, retryErr error) {
			thinking.Stop()
			fmt.Printf("%s\n", ColorYellow(fmt.Sprintf("  Model request failed; retry %d/3 in %s: %v", attempt, delay, retryErr)))
			thinking.Start()
		})
		thinking.Stop()
		turnDuration := time.Since(turnStart)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Println(ColorRed(fmt.Sprintf("\n%s Error: %v", SymCross, err)))
			}
			return
		}
		reply := chatResult.Message

		compTokens := countApproxTokens([]llm.Message{reply})
		promptTokens, compTokens, _ = resolveTurnTokens(chatResult.Usage, chatResult.HasUsage, promptTokens, compTokens)
		billable := false
		if direct, ok := localClient(r.LLM); ok {
			billable = billableEndpoint(direct.BaseURL)
		}
		metrics := ComputeTurnMetrics(turn, model, promptTokens, compTokens, turnDuration, billable)
		sessionMetrics.Add(metrics)

		// If no tools called, print reply and return control to the prompt
		if len(reply.ToolCalls) == 0 {
			if reply.Content != "" {
				fmt.Printf("\n%s\n", FormatMarkdown(reply.Content))
			}
			fmt.Println(FormatTurnSummary(metrics))
			*sessionMessages = append(*sessionMessages, reply)
			return
		}

		*sessionMessages = append(*sessionMessages, reply)

		var taskFinished bool
		var finishSummary string
		for _, call := range reply.ToolCalls {
			previewLines := 8
			if expandedTools {
				previewLines = 1000000
			}
			if ctx.Err() != nil {
				fmt.Println(ColorYellow("\nTurn canceled."))
				return
			}

			normalizedArgs, argErr := normalizeToolArguments(call.Function.Arguments)
			if argErr != nil {
				toolResult := argErr.Error()
				fmt.Println(FormatToolCall(call.Function.Name, summarizeArgs(call.Function.Arguments)))
				fmt.Println(FormatToolResult(call.Function.Name, toolResult, previewLines))
				*sessionMessages = append(*sessionMessages, llm.Message{Role: "tool", Content: toolResult, ToolCallID: call.ID})
				continue
			}
			call.Function.Arguments = normalizedArgs
			fmt.Println(FormatToolCall(call.Function.Name, summarizeArgs(call.Function.Arguments)))
			if requiresPermission(call.Function.Name) && !permissions.Authorize(call.Function.Name, summarizeArgs(call.Function.Arguments)) {
				toolResult := "Permission denied by user. Do not retry this action unless the user explicitly asks."
				fmt.Println(FormatToolResult(call.Function.Name, toolResult, previewLines))
				*sessionMessages = append(*sessionMessages, llm.Message{Role: "tool", Content: toolResult, ToolCallID: call.ID})
				continue
			}
			if followUp := followUpFromArguments(call.Function.Arguments); followUp != nil && !permissions.Authorize("run_command", "command="+followUp.Command) {
				toolResult := "Permission denied for the fused follow-up command. The file mutation was not attempted."
				fmt.Println(FormatToolResult(call.Function.Name, toolResult, previewLines))
				*sessionMessages = append(*sessionMessages, llm.Message{Role: "tool", Content: toolResult, ToolCallID: call.ID})
				continue
			}

			// A tool can block for a long time (a big search, a build, a network
			// fetch). Animate for the whole execution so the session never looks
			// frozen between the call card and its result.
			running := NewSpinner("Running " + call.Function.Name)
			running.Start()
			toolStart := time.Now()
			parent := RunRequest{
				WorkingDir: absWorkingDir, Model: model, MaxTurns: maxTurns,
				AllowCommands: allowCmds, CommandTimeout: cmdTimeout,
				ThinkLevel: thinkLevel, PermissionMode: permissions.Mode,
			}
			execution := r.executeTool(toolExecutionContext{
				ctx:               ctx,
				request:           parent,
				fileReader:        fileReader,
				workingDir:        absWorkingDir,
				allowCommands:     allowCmds,
				commandTimeout:    cmdTimeout,
				processManager:    processMgr,
				observationStore:  observationStore,
				liveCommandOutput: true,
			}, call.Function.Name, call.Function.Arguments)
			toolResult := execution.output
			if execution.planBoundary {
				planBoundary = true
			}
			if execution.taskFinished {
				finishSummary = execution.finalResponse
				taskFinished = true
			}

			running.Stop()
			outcome := observations.Process(call.Function.Name, call.Function.Arguments, toolResult, turn)
			fmt.Println(FormatToolState(call.Function.Name, "completed", time.Since(toolStart)))
			fmt.Println(FormatToolResult(call.Function.Name, outcome.DisplayView, previewLines))

			*sessionMessages = append(*sessionMessages, llm.Message{
				Role:       "tool",
				Content:    outcome.ModelView,
				ToolCallID: call.ID,
			})
		}

		fmt.Println(FormatTurnSummary(metrics))

		if taskFinished {
			lines := []string{
				"",
				FormatKV("status", ColorGreen(SymCheck+" completed"), 10),
				"",
			}
			fmt.Println("\n" + FormatCard("Task Complete", lines, 74))
			if strings.TrimSpace(finishSummary) != "" {
				fmt.Println("\n" + FormatMarkdown(finishSummary))
			}
			return
		}
	}

	fmt.Println(ColorYellow(fmt.Sprintf("\n%s Max turns limit reached for this prompt.", SymCross)))
}

func parseDirectoryChange(command string) (string, bool) {
	trimmed := strings.TrimSpace(command)
	lower := strings.ToLower(trimmed)
	for _, name := range []string{"set-location", "chdir", "cd", "sl"} {
		if lower == name {
			return "", true
		}
		prefix := name + " "
		if strings.HasPrefix(lower, prefix) {
			path := strings.TrimSpace(trimmed[len(prefix):])
			if strings.HasPrefix(path, "/d ") {
				path = strings.TrimSpace(path[3:])
			}
			if len(path) >= 2 && ((path[0] == '"' && path[len(path)-1] == '"') || (path[0] == '\'' && path[len(path)-1] == '\'')) {
				path = path[1 : len(path)-1]
			}
			return path, true
		}
	}
	return "", false
}

func followUpFromArguments(raw string) *FollowUpCommand {
	var arguments struct {
		ThenRun *FollowUpCommand `json:"then_run"`
	}
	if json.Unmarshal([]byte(raw), &arguments) != nil {
		return nil
	}
	if arguments.ThenRun == nil || strings.TrimSpace(arguments.ThenRun.Command) == "" {
		return nil
	}
	return arguments.ThenRun
}

func interactiveTools(allowCommands bool, permissionMode PermissionMode, enableObservations bool) []llm.Tool {
	tools := []llm.Tool{
		readFileTool,
		listFilesTool,
		searchFilesTool,
		globFilesTool,
		webSearchTool,
		webFetchTool,
		updatePlanTool,
		finishTaskTool,
		agentStatusTool,
		sendAgentMessageTool,
	}
	if enableObservations {
		tools = append(tools, readObservationTool)
	}
	if permissionMode != PermissionReadOnly {
		tools = append(tools, writeFileTool, editFileTool, patchFileTool, spawnAgentTool, cancelAgentTool)
	}
	if allowCommands {
		if permissionMode != PermissionReadOnly {
			tools = append(tools, runCommandTool, killProcessTool)
		}
		tools = append(tools, processStatusTool)
	}
	return tools
}

func activeSessionID(session *InteractiveSession) string {
	if session == nil {
		return "(not persisted)"
	}
	return session.ID
}

func resolveInteractiveDirectory(currentDir, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		requested = home
	} else if strings.HasPrefix(requested, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		requested = filepath.Join(home, strings.TrimPrefix(requested, "~"+string(filepath.Separator)))
	}

	if !filepath.IsAbs(requested) {
		requested = filepath.Join(currentDir, requested)
	}
	resolved, err := filepath.Abs(requested)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", requested, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", resolved)
	}
	return filepath.Clean(resolved), nil
}

func applyModelParameters(client *llm.Client, params map[string]interface{}) {
	if client == nil || params == nil {
		return
	}
	if v, ok := params["temperature"]; ok {
		if f, ok := v.(float64); ok {
			client.Temperature = &f
		}
	}
	if v, ok := params["top_p"]; ok {
		if f, ok := v.(float64); ok {
			client.TopP = &f
		}
	}
	if v, ok := params["max_tokens"]; ok {
		if f, ok := v.(float64); ok {
			i := int(f)
			client.MaxTokens = &i
		}
	}
}

// runInteractiveCommand executes a shell command with live terminal IO.
func runInteractiveCommand(dir, command string) error {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return nil
	}

	cmd := newInteractiveShellCommand(trimmed)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Intercept interrupt so Ctrl+C cancels the running child command without terminating the fastllm REPL
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	if err := cmd.Start(); err != nil {
		fmt.Println(ColorRed(fmt.Sprintf("  %s Command error: %v", SymCross, err)))
		return err
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-sigChan:
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		fmt.Println("\n" + ColorYellow("  [Command canceled]"))
		return nil
	case err := <-done:
		if err != nil {
			fmt.Println(ColorRed(fmt.Sprintf("  %s Command exited with error: %v", SymCross, err)))
		}
		return err
	}
}

func newInteractiveShellCommand(command string) *exec.Cmd {
	return newInteractiveShellCommandContext(context.Background(), command)
}

func newInteractiveShellCommandContext(ctx context.Context, command string) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
	} else {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "sh"
		}
		cmd = exec.CommandContext(ctx, shell, "-c", command)
	}
	configureCommandTreeCancellation(cmd)
	return cmd
}
