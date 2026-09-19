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

	model := initialReq.Model
	if model == "" {
		model = r.DefaultModel
	}

	systemPrompt := initialReq.SystemPrompt
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = DefaultSystemPrompt
	}

	allowCmds := r.AllowCommands
	cmdTimeout := initialReq.CommandTimeout
	if cmdTimeout <= 0 {
		cmdTimeout = r.CommandTimeout
	}
	if cmdTimeout <= 0 {
		cmdTimeout = 60 * time.Second
	}

	fmt.Println("==================================================")
	fmt.Println(" fastllm Interactive Agent TUI")
	fmt.Printf(" Working Dir: %s\n", absWorkingDir)
	fmt.Printf(" Model:       %s\n", model)
	fmt.Printf(" Commands:    %v\n", allowCmds)
	fmt.Println(" Type 'exit' to quit, '/clear' to reset context,")
	fmt.Println(" '/model <name>' to switch model, '/help' for info.")
	fmt.Println("==================================================")

	fileReader := files.New(absWorkingDir, true)

	tools := []llm.Tool{
		readFileTool,
		writeFileTool,
		editFileTool,
		listFilesTool,
		searchFilesTool,
		finishTaskTool,
	}
	if allowCmds {
		tools = append(tools, runCommandTool)
	}

	sessionMessages := []llm.Message{
		{Role: "system", Content: systemPrompt},
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
			case "/clear":
				sessionMessages = []llm.Message{
					{Role: "system", Content: systemPrompt},
				}
				fmt.Println("Conversation context cleared.")
				continue
			case "/model":
				if len(parts) < 2 {
					fmt.Printf("Current model: %s\nUsage: /model <name>\n", model)
				} else {
					model = parts[1]
					fmt.Printf("Switched model to: %s\n", model)
				}
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
						fmt.Printf("Switched working directory to: %s\n", absWorkingDir)
					}
				}
				continue
			case "/help":
				fmt.Println("Interactive TUI Commands:")
				fmt.Println("  /clear         - Reset conversation context")
				fmt.Println("  /model <name>  - Switch model on the fly")
				fmt.Println("  /dir <path>    - Switch working directory")
				fmt.Println("  /help          - Show this help message")
				fmt.Println("  exit, quit     - Exit interactive session")
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

		r.runInteractiveTurn(ctx, model, tools, fileReader, absWorkingDir, allowCmds, cmdTimeout, initialReq.ThinkLevel, &sessionMessages)
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

		reply, err := r.LLM.Chat(ctx, model, *sessionMessages, tools, thinkLevel)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Printf("\n[Error: %v]\n", err)
			}
			return
		}

		// If no tools called, print reply and return control to the prompt
		if len(reply.ToolCalls) == 0 {
			if reply.Content != "" {
				fmt.Printf("\n%s\n", reply.Content)
			}
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
					}
					_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
					perCallTimeout := cmdTimeout
					if args.TimeoutSeconds > 0 {
						perCallTimeout = time.Duration(args.TimeoutSeconds) * time.Second
					}
					toolResult = r.executeRunCommand(ctx, absWorkingDir, args.Command, perCallTimeout)
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

		if taskFinished {
			return
		}
	}

	fmt.Println("\n[Max turns limit reached for this turn]")
}
