package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fastllm/internal/config"
	"fastllm/internal/files"
	"fastllm/internal/llm"
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
	cmdTimeout := initialReq.CommandTimeout
	if cmdTimeout <= 0 {
		cmdTimeout = r.CommandTimeout
	}
	if cmdTimeout <= 0 {
		cmdTimeout = 60 * time.Second
	}

	// Initialize Git checkpoint manager and background process manager
	checkpointMgr := NewCheckpointManager(absWorkingDir)
	processMgr := NewProcessManager()
	defer processMgr.KillAll()

	var sessionMetrics SessionMetrics

	fmt.Println("================================================================")
	fmt.Println("  fastllm Interactive Agent REPL")
	fmt.Printf("  Directory:   %s\n", absWorkingDir)
	fmt.Printf("  Model:       %s\n", model)
	if configPath != "" {
		fmt.Printf("  Config:      %s\n", configPath)
	}
	fmt.Printf("  Git Repo:    %v\n", checkpointMgr.IsGitRepo())
	fmt.Printf("  Rules:       %d rule file(s) discovered\n", len(discoveredRules))
	fmt.Printf("  Commands:    %v\n", allowCmds)
	fmt.Println("----------------------------------------------------------------")
	fmt.Println("  Slash Commands:")
	fmt.Println("    /help                 - Show command reference")
	fmt.Println("    /models, /model       - List available models & endpoints")
	fmt.Println("    /model <name>         - Switch active model")
	fmt.Println("    /models add <id> <url>- Add a new model endpoint to config")
	fmt.Println("    /undo                 - Rollback working tree to pre-turn checkpoint")
	fmt.Println("    /diff                 - Inspect git diff of current changes")
	fmt.Println("    /status               - View session metrics, tokens, cost, and procs")
	fmt.Println("    /dir <path>           - Switch working directory")
	fmt.Println("    /rules                - Inspect loaded workspace rules")
	fmt.Println("    /ps                   - List active background processes")
	fmt.Println("    /kill <id>            - Kill a background process")
	fmt.Println("    /clear                - Reset conversation context")
	fmt.Println("    exit, quit            - Exit interactive session")
	fmt.Println("================================================================")

	fileReader := files.New(absWorkingDir, true)

	tools := []llm.Tool{
		readFileTool,
		writeFileTool,
		editFileTool,
		patchFileTool,
		listFilesTool,
		searchFilesTool,
		finishTaskTool,
	}
	if allowCmds {
		tools = append(tools, runCommandTool, processStatusTool, killProcessTool)
	}

	sessionMessages := []llm.Message{
		{Role: "system", Content: systemPrompt + rulesPrompt},
	}

	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print("\nfastllm> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Handle slash commands
		if strings.HasPrefix(line, "/") {
			parts := strings.Fields(line)
			cmd := strings.ToLower(parts[0])
			switch cmd {
			case "/exit", "/quit":
				fmt.Println("Goodbye!")
				return nil

			case "/help":
				fmt.Println("\nAvailable Slash Commands:")
				fmt.Println("  /undo          - Rollback working directory to checkpoint before latest turn")
				fmt.Println("  /diff          - Show git diff of uncommitted changes")
				fmt.Println("  /status        - Display session tokens, cost, latency, and background jobs")
				fmt.Println("  /model <name>  - Switch active model (e.g. /model muse-glimmer, /model llama3.1)")
				fmt.Println("  /dir <path>    - Switch working directory and reload rules")
				fmt.Println("  /rules         - View discovered workspace instruction files")
				fmt.Println("  /ps            - List running background processes")
				fmt.Println("  /kill <id>     - Kill background process by ID (e.g. /kill proc-1)")
				fmt.Println("  /clear         - Clear conversation history and reset context")
				fmt.Println("  /exit, /quit   - Quit the REPL")
				continue

			case "/undo":
				if !checkpointMgr.IsGitRepo() {
					fmt.Println("[Undo] Current directory is not a Git repository; checkpoints disabled.")
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err := checkpointMgr.Rollback(ctx, "")
				cancel()
				if err != nil {
					fmt.Printf("[Undo Error] %v\n", err)
				} else {
					fmt.Println("[Undo] Successfully rolled back working tree to previous checkpoint.")
				}
				continue

			case "/diff":
				if !checkpointMgr.IsGitRepo() {
					fmt.Println("[Diff] Current directory is not a Git repository.")
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				diff, err := checkpointMgr.Diff(ctx)
				cancel()
				if err != nil {
					fmt.Printf("[Diff Error] %v\n", err)
				} else {
					fmt.Println(diff)
				}
				continue

			case "/status":
				fmt.Println("\n=== Session Status ===")
				fmt.Printf("  Working Directory: %s\n", absWorkingDir)
				fmt.Printf("  Active Model:      %s\n", model)
				fmt.Printf("  Rules Loaded:      %d\n", len(discoveredRules))
				fmt.Printf("  %s\n", sessionMetrics.FormatSessionSummary())
				fmt.Println("\n=== Background Processes ===")
				fmt.Println(processMgr.FormatProcessTable())
				continue

			case "/rules":
				if len(discoveredRules) == 0 {
					fmt.Println("No workspace rules discovered in this directory or its parents.")
				} else {
					fmt.Printf("Discovered %d workspace rule file(s):\n", len(discoveredRules))
					for _, r := range discoveredRules {
						fmt.Printf("\n--- [%s] (%s) ---\n%s\n", r.Filename, r.Path, r.Content)
					}
				}
				continue

			case "/ps":
				fmt.Println(processMgr.FormatProcessTable())
				continue

			case "/kill":
				if len(parts) < 2 {
					fmt.Println("Usage: /kill <process_id> (e.g. /kill proc-1)")
				} else {
					err := processMgr.Kill(parts[1])
					if err != nil {
						fmt.Printf("Error killing process %s: %v\n", parts[1], err)
					} else {
						fmt.Printf("Killed process %s.\n", parts[1])
					}
				}
				continue

			case "/clear":
				sessionMessages = []llm.Message{
					{Role: "system", Content: systemPrompt + rulesPrompt},
				}
				fmt.Println("Conversation context cleared.")
				continue

			case "/model", "/models":
				if len(parts) == 1 || (len(parts) == 2 && parts[1] == "list") {
					fmt.Printf("\nConfigured Models (from %s):\n", configPath)
					if settings != nil && len(settings.Models) > 0 {
						for _, m := range settings.Models {
							marker := "  "
							if strings.EqualFold(m.ID, model) {
								marker = "* "
							}
							desc := m.Name
							if desc == "" {
								desc = m.ID
							}
							fmt.Printf("  %s%-18s %-32s [%s]\n", marker, m.ID, desc, m.URL)
						}
					} else {
						fmt.Println("  (no models configured)")
					}
					fmt.Println("\nUsage:")
					fmt.Println("  /model <name>              - Switch active model")
					fmt.Println("  /models add <id> <url>     - Add a new model endpoint")
					if configPath != "" {
						fmt.Printf("  Config file: %s\n", configPath)
					}
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
						fmt.Printf("Error saving to %s: %v\n", configPath, err)
					} else {
						fmt.Printf("Added model %q (%s) to %s\n", newID, newURL, configPath)
					}
					continue
				}

				targetModel := parts[1]
				model = targetModel
				if settings != nil {
					if matched := settings.FindModel(targetModel); matched != nil {
						fmt.Printf("Switched model to: %s (%s, URL: %s)\n", matched.ID, matched.Name, matched.URL)
						if client, ok := r.LLM.(*llm.Client); ok {
							if matched.URL != "" {
								client.BaseURL = matched.URL
							}
							if matched.APIKey != "" {
								client.APIKey = matched.APIKey
							}
							applyModelParameters(client, matched.Parameters)
						}
						continue
					}
				}
				fmt.Printf("Switched model to: %s\n", model)
				continue

			case "/dir":
				if len(parts) < 2 {
					fmt.Printf("Current directory: %s\nUsage: /dir <path>\n", absWorkingDir)
				} else {
					newDir, err := filepath.Abs(parts[1])
					if err != nil {
						fmt.Printf("Invalid directory %q: %v\n", parts[1], err)
					} else {
						absWorkingDir = newDir
						fileReader = files.New(absWorkingDir, true)
						checkpointMgr = NewCheckpointManager(absWorkingDir)
						discoveredRules = DiscoverWorkspaceRules(absWorkingDir)
						rulesPrompt = FormatRulesForPrompt(discoveredRules)
						settings, configPath, _ = config.LoadSettings(absWorkingDir)
						sessionMessages = []llm.Message{
							{Role: "system", Content: systemPrompt + rulesPrompt},
						}
						fmt.Printf("Switched working directory to: %s\n", absWorkingDir)
						fmt.Printf("Discovered %d rule file(s).\n", len(discoveredRules))
					}
				}
				continue

			default:
				fmt.Printf("Unknown command %q. Type /help for available commands.\n", cmd)
				continue
			}
		}

		if line == "exit" || line == "quit" {
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
			initialReq.ThinkLevel,
			processMgr,
			&sessionMetrics,
			&sessionMessages,
		)
		cancel()
		signal.Stop(sigChan)
	}

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
	processMgr *ProcessManager,
	sessionMetrics *SessionMetrics,
	sessionMessages *[]llm.Message,
) {
	maxTurns := r.DefaultMaxTurns
	if maxTurns <= 0 {
		maxTurns = 20
	}

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
				fmt.Printf("\n[Error: %v]\n", err)
			}
			return
		}

		compTokens := countApproxTokens([]llm.Message{reply})
		metrics := ComputeTurnMetrics(turn, model, promptTokens, compTokens, turnDuration)
		sessionMetrics.Add(metrics)

		// If no tools called, print reply and return control to the prompt
		if len(reply.ToolCalls) == 0 {
			if reply.Content != "" {
				fmt.Printf("\n%s\n", reply.Content)
			}
			fmt.Printf("\n[%s]\n", metrics.FormatTurnSummary())
			*sessionMessages = append(*sessionMessages, reply)
			return
		}

		*sessionMessages = append(*sessionMessages, reply)

		var taskFinished bool
		for _, call := range reply.ToolCalls {
			if ctx.Err() != nil {
				fmt.Println("Turn canceled.")
				return
			}

			fmt.Printf("\n  -> tool: %s %s\n", call.Function.Name, summarizeArgs(call.Function.Arguments))

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
				fmt.Printf("\n[Finished]: %s\n", summary)
				toolResult = "Task finished."
				taskFinished = true

			default:
				toolResult = fmt.Sprintf("Error: unknown tool %q", call.Function.Name)
			}

			lines := strings.Split(strings.TrimSpace(toolResult), "\n")
			firstLine := ""
			if len(lines) > 0 {
				firstLine = lines[0]
			}
			if len(lines) > 1 {
				fmt.Printf("     result: %s (... %d more lines)\n", firstLine, len(lines)-1)
			} else {
				fmt.Printf("     result: %s\n", firstLine)
			}

			*sessionMessages = append(*sessionMessages, llm.Message{
				Role:       "tool",
				Content:    toolResult,
				ToolCallID: call.ID,
			})
		}

		fmt.Printf("  [%s]\n", metrics.FormatTurnSummary())

		if taskFinished {
			return
		}
	}

	fmt.Println("\n[Max turns limit reached for this turn]")
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

