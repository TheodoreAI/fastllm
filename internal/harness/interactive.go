package harness

import (
	"bufio"
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
	workingDir := initialReq.WorkingDir
	if workingDir == "" {
		workingDir = r.DefaultWorkingDir
	}
	absWorkingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return fmt.Errorf("resolve working dir: %w", err)
	}

	// Load configuration (.fastllm/config.json or ~/.fastllm/config.json)
	settings, configPath, _ := config.LoadSettings(absWorkingDir)

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

	// Initialize Git checkpoint manager and background process manager
	checkpointMgr := NewCheckpointManager(absWorkingDir)
	processMgr := NewProcessManager()
	defer processMgr.KillAll()

	initConsole()

	var sessionMetrics SessionMetrics

	fmt.Println(FormatWelcomeBanner(absWorkingDir, model, configPath, checkpointMgr.IsGitRepo(), len(discoveredRules), allowCmds))

	fileReader := files.New(absWorkingDir, true)

	scanner := bufio.NewScanner(os.Stdin)
	permissions := NewPermissionController(permissionMode, scanner)
	tools := interactiveTools(allowCmds, permissionMode)

	sessionMessages := []llm.Message{
		{Role: "system", Content: systemPrompt + rulesPrompt},
	}
	sessionStore, sessionStoreErr := DefaultSessionStore()
	var activeSession *InteractiveSession
	if sessionStoreErr == nil {
		activeSession = sessionStore.New(absWorkingDir, model, InteractiveRuntime{
			MaxTurns: maxTurns, CommandTimeout: cmdTimeout, ThinkLevel: thinkLevel, AllowCommands: allowCmds, PermissionMode: permissionMode,
		})
	}

	saveSession := func() {
		if sessionStore == nil || activeSession == nil {
			return
		}
		activeSession.WorkingDir = absWorkingDir
		activeSession.Model = model
		activeSession.Runtime = InteractiveRuntime{
			MaxTurns: maxTurns, CommandTimeout: cmdTimeout, ThinkLevel: thinkLevel, AllowCommands: allowCmds, PermissionMode: permissionMode,
		}
		activeSession.Messages = append([]llm.Message(nil), sessionMessages[1:]...)
		activeSession.Title = sessionTitle(activeSession.Messages)
		if err := sessionStore.Save(activeSession); err != nil {
			fmt.Println(ColorYellow(fmt.Sprintf("  %s Session autosave failed: %v", SymCross, err)))
		}
	}
	if sessionStoreErr != nil {
		fmt.Println(ColorYellow(fmt.Sprintf("  %s Session persistence unavailable: %v", SymCross, sessionStoreErr)))
	} else {
		saveSession()
	}

	switchWorkspace := func(newDir string) error {
		resolvedDir, err := resolveInteractiveDirectory(absWorkingDir, newDir)
		if err != nil {
			return err
		}
		absWorkingDir = resolvedDir
		fileReader = files.New(absWorkingDir, true)
		checkpointMgr = NewCheckpointManager(absWorkingDir)
		discoveredRules = DiscoverWorkspaceRules(absWorkingDir)
		rulesPrompt = FormatRulesForPrompt(discoveredRules)
		settings, configPath, _ = config.LoadSettings(absWorkingDir)
		sessionMessages = []llm.Message{
			{Role: "system", Content: systemPrompt + rulesPrompt},
		}
		fmt.Println(FormatWelcomeBanner(absWorkingDir, model, configPath, checkpointMgr.IsGitRepo(), len(discoveredRules), allowCmds))
		return nil
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
		if shellMode {
			fmt.Print(FormatShellPrompt(absWorkingDir))
		} else {
			fmt.Print(FormatPrompt(model))
		}
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

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
				fmt.Println(FormatStatusCard(absWorkingDir, model, len(discoveredRules), sessionMetrics, processMgr))
				fmt.Println(FormatRuntimeCard(maxTurns, cmdTimeout, thinkLevel, allowCmds, permissionMode, activeSessionID(activeSession)))
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
				resolvedDir, err := resolveInteractiveDirectory(absWorkingDir, loaded.WorkingDir)
				if err != nil {
					fmt.Println(ColorRed(fmt.Sprintf("  %s Cannot restore workspace: %v", SymCross, err)))
					continue
				}
				saveSession()
				model = loaded.Model
				maxTurns = loaded.Runtime.MaxTurns
				if maxTurns <= 0 {
					maxTurns = 20
				}
				cmdTimeout = loaded.Runtime.CommandTimeout
				if cmdTimeout <= 0 {
					cmdTimeout = 60 * time.Second
				}
				thinkLevel = loaded.Runtime.ThinkLevel
				allowCmds = loaded.Runtime.AllowCommands
				permissionMode = loaded.Runtime.PermissionMode
				if permissionMode == "" {
					permissionMode = PermissionAsk
				}
				permissions.SetMode(permissionMode)
				tools = interactiveTools(allowCmds, permissionMode)
				if err := switchWorkspace(resolvedDir); err != nil {
					fmt.Println(ColorRed(fmt.Sprintf("  %s Cannot restore workspace: %v", SymCross, err)))
					continue
				}
				sessionMessages = append([]llm.Message{{Role: "system", Content: systemPrompt + rulesPrompt}}, loaded.Messages...)
				activeSession = loaded
				fmt.Println(ColorGreen(fmt.Sprintf("  %s Resumed %s — %s", SymCheck, loaded.ID, loaded.Title)))
				continue

			case "/new":
				saveSession()
				sessionMessages = []llm.Message{{Role: "system", Content: systemPrompt + rulesPrompt}}
				sessionMetrics = SessionMetrics{}
				if sessionStore != nil {
					activeSession = sessionStore.New(absWorkingDir, model, InteractiveRuntime{
						MaxTurns: maxTurns, CommandTimeout: cmdTimeout, ThinkLevel: thinkLevel, AllowCommands: allowCmds, PermissionMode: permissionMode,
					})
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
					fmt.Println(ColorYellow("  Usage: /set <turns|timeout|think|commands|permissions> <value>"))
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
					tools = interactiveTools(allowCmds, permissionMode)
				case "permissions", "permission":
					var parsed PermissionMode
					parsed, setErr = ParsePermissionMode(value)
					if setErr == nil {
						permissionMode = parsed
						permissions.SetMode(parsed)
						tools = interactiveTools(allowCmds, permissionMode)
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
					if err := config.SaveSettings(configPath, settings); err != nil {
						fmt.Println(ColorRed(fmt.Sprintf("  %s Error saving to %s: %v", SymCross, configPath, err)))
					} else {
						fmt.Println(ColorGreen(fmt.Sprintf("  %s Added model %q (%s) to %s", SymCheck, newID, newURL, configPath)))
					}
					continue
				}

				targetModel := parts[1]
				model = targetModel
				if settings != nil {
					if matched := settings.FindModel(targetModel); matched != nil {
						fmt.Println(ColorGreen(fmt.Sprintf("  %s Switched model to: %s (%s, URL: %s)", SymCheck, matched.ID, matched.Name, matched.URL)))
						if client, ok := r.LLM.(*llm.Client); ok {
							if matched.URL != "" {
								client.BaseURL = matched.URL
							}
							if matched.APIKey != "" {
								client.APIKey = matched.APIKey
							}
							applyModelParameters(client, matched.Parameters)
						}
						saveSession()
						continue
					}
				}
				fmt.Println(ColorGreen(fmt.Sprintf("  %s Switched model to: %s", SymCheck, model)))
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

			default:
				fmt.Printf("Unknown command %q. Type /help for available commands.\n", cmd)
				continue
			}
		}

		if line == "exit" || line == "quit" {
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

		// Add user turn
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
		)
		saveSession()
		cancel()
		signal.Stop(sigChan)
	}

	saveSession()
	return scanner.Err()
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
) {
	for turn := 1; turn <= maxTurns; turn++ {
		if ctx.Err() != nil {
			fmt.Println("Turn canceled.")
			return
		}

		// Context compaction to prevent context window explosion
		*sessionMessages, _ = CompactMessages(*sessionMessages, DefaultCompactionConfig())

		turnStart := time.Now()
		promptTokens := countApproxTokens(*sessionMessages)

		reply, err := r.LLM.Chat(ctx, model, *sessionMessages, tools, thinkLevel)
		turnDuration := time.Since(turnStart)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Println(ColorRed(fmt.Sprintf("\n%s Error: %v", SymCross, err)))
			}
			return
		}

		compTokens := countApproxTokens([]llm.Message{reply})
		metrics := ComputeTurnMetrics(turn, model, promptTokens, compTokens, turnDuration)
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
			if ctx.Err() != nil {
				fmt.Println(ColorYellow("\nTurn canceled."))
				return
			}

			fmt.Println(FormatToolCall(call.Function.Name, summarizeArgs(call.Function.Arguments)))
			if requiresPermission(call.Function.Name) && !permissions.Authorize(call.Function.Name, summarizeArgs(call.Function.Arguments)) {
				toolResult := "Permission denied by user. Do not retry this action unless the user explicitly asks."
				fmt.Println(FormatToolResult(call.Function.Name, toolResult, 8))
				*sessionMessages = append(*sessionMessages, llm.Message{Role: "tool", Content: toolResult, ToolCallID: call.ID})
				continue
			}

			var toolResult string
			switch call.Function.Name {
			case "read_file":
				var args struct {
					Path string `json:"path"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				toolResult = r.executeReadFile(fileReader, args.Path)

			case "write_file":
				var args struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				toolResult = r.executeWriteFile(fileReader, args.Path, args.Content)

			case "edit_file":
				var args struct {
					Path    string `json:"path"`
					Search  string `json:"search"`
					Replace string `json:"replace"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				toolResult = r.executeEditFile(fileReader, args.Path, args.Search, args.Replace)

			case "patch_file":
				var args struct {
					Path string `json:"path"`
					Diff string `json:"diff"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				toolResult = r.executePatchFile(fileReader, args.Path, args.Diff)

			case "list_files":
				var args struct {
					Path string `json:"path"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				toolResult = r.executeListFiles(ctx, absWorkingDir, args.Path)

			case "search_files":
				var args struct {
					Pattern string `json:"pattern"`
					Path    string `json:"path"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				toolResult = r.executeSearchFiles(ctx, absWorkingDir, args.Pattern, args.Path)

			case "web_search":
				var args struct {
					Query      string `json:"query"`
					MaxResults int    `json:"max_results"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				toolResult = webtools.SearchFormatted(ctx, args.Query, args.MaxResults)

			case "web_fetch":
				var args struct {
					URL      string `json:"url"`
					MaxBytes int    `json:"max_bytes"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				content, err := webtools.Fetch(ctx, args.URL, args.MaxBytes)
				if err != nil {
					toolResult = fmt.Sprintf("Error fetching %s: %v", args.URL, err)
				} else {
					toolResult = content
				}

			case "run_command":
				if !allowCmds {
					toolResult = "Error: run_command is disabled."
				} else {
					var args struct {
						Command        string `json:"command"`
						TimeoutSeconds int    `json:"timeout_seconds"`
						Background     bool   `json:"background"`
					}
					_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
					if args.Background {
						proc, err := processMgr.Start(args.Command, absWorkingDir)
						if err != nil {
							toolResult = fmt.Sprintf("Error starting background process: %v", err)
						} else {
							toolResult = fmt.Sprintf("Process started in background with ID %s (PID %d). Inspect with process_status.", proc.ID, proc.PID)
						}
					} else {
						perCallTimeout := cmdTimeout
						if args.TimeoutSeconds > 0 {
							perCallTimeout = time.Duration(args.TimeoutSeconds) * time.Second
						}
						toolResult = r.executeRunCommand(ctx, absWorkingDir, args.Command, perCallTimeout)
					}
				}

			case "process_status":
				if !allowCmds {
					toolResult = "Error: process management is disabled."
				} else {
					var args struct {
						ProcessID string `json:"process_id"`
					}
					_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
					if args.ProcessID == "" {
						toolResult = processMgr.FormatProcessTable()
					} else {
						proc, out, err := processMgr.Status(args.ProcessID)
						if err != nil {
							toolResult = err.Error()
						} else {
							status := "RUNNING"
							if proc.Exited {
								status = fmt.Sprintf("EXITED (%d)", proc.ExitCode)
							}
							toolResult = fmt.Sprintf("Process %s (PID %d): %s\nCommand: %s\nOutput:\n%s",
								proc.ID, proc.PID, status, proc.Command, out)
						}
					}
				}

			case "kill_process":
				if !allowCmds {
					toolResult = "Error: process management is disabled."
				} else {
					var args struct {
						ProcessID string `json:"process_id"`
					}
					_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
					if err := processMgr.Kill(args.ProcessID); err != nil {
						toolResult = fmt.Sprintf("Error killing process %s: %v", args.ProcessID, err)
					} else {
						toolResult = fmt.Sprintf("Process %s terminated.", args.ProcessID)
					}
				}

			case "finish_task":
				var args struct {
					Summary string `json:"summary"`
					Answer  string `json:"answer"`
				}
				_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
				summary := args.Summary
				if args.Answer != "" {
					summary = fmt.Sprintf("%s\n\nAnswer: %s", summary, args.Answer)
				}
				finishSummary = summary
				toolResult = "Task completed successfully."
				taskFinished = true

			default:
				toolResult = fmt.Sprintf("Error: unknown tool %q", call.Function.Name)
			}

			fmt.Println(FormatToolResult(call.Function.Name, toolResult, 8))

			*sessionMessages = append(*sessionMessages, llm.Message{
				Role:       "tool",
				Content:    toolResult,
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

func interactiveTools(allowCommands bool, permissionMode PermissionMode) []llm.Tool {
	tools := []llm.Tool{
		readFileTool,
		listFilesTool,
		searchFilesTool,
		webSearchTool,
		webFetchTool,
		finishTaskTool,
	}
	if permissionMode != PermissionReadOnly {
		tools = append(tools, writeFileTool, editFileTool, patchFileTool)
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

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", trimmed)
	} else {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "sh"
		}
		cmd = exec.Command(shell, "-c", trimmed)
	}
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
