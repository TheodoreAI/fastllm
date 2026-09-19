package harness

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fastllm/internal/llm"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// RunCLI parses CLI flags and executes an autonomous harness task.
// Returns the exit code (0 for success, 1 for failure).
func RunCLI(args []string) int {
	fs := flag.NewFlagSet("harness", flag.ContinueOnError)

	taskFlag := fs.String("task", "", "Task instruction for the agent to execute")
	dirFlag := fs.String("dir", ".", "Target working directory (sandboxed root)")
	modelFlag := fs.String("model", "", "Model name (defaults to LLM_CHAT_MODEL or llama3.1)")
	urlFlag := fs.String("url", "", "LLM API base URL (defaults to LLM_BASE_URL or http://localhost:11434/v1)")
	keyFlag := fs.String("key", "", "LLM API key (defaults to LLM_API_KEY)")
	maxTurnsFlag := fs.Int("max-turns", 20, "Maximum tool execution turns")
	timeoutFlag := fs.Int("timeout", 60, "Command timeout in seconds for run_command")
	systemFlag := fs.String("system", "", "Custom system prompt override")
	thinkFlag := fs.String("think", "", "Reasoning effort level (e.g. low, medium, high)")
	noCmdsFlag := fs.Bool("no-commands", false, "Disable the run_command tool")
	jsonFlag := fs.Bool("json", false, "Output only the final RunResult JSON")
	quietFlag := fs.Bool("quiet", false, "Suppress turn-by-turn progress output")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	task := strings.TrimSpace(*taskFlag)
	if task == "" && fs.NArg() > 0 {
		task = strings.Join(fs.Args(), " ")
	}

	baseURL := strings.TrimSpace(*urlFlag)
	if baseURL == "" {
		baseURL = getenv("LLM_BASE_URL", "http://localhost:11434/v1")
	}

	apiKey := strings.TrimSpace(*keyFlag)
	if apiKey == "" {
		apiKey = getenv("LLM_API_KEY", "")
	}

	model := strings.TrimSpace(*modelFlag)
	if model == "" {
		model = getenv("LLM_CHAT_MODEL", "llama3.1")
	}

	// Auto-route Muse Glimmer or OSU cluster models to the cluster tunnel (port 8010)
	isOSU := strings.HasPrefix(model, "osu:") || strings.HasPrefix(model, "cluster:") ||
		model == "muse-glimmer" || model == "muse-glimmer-30b" || model == "meta-models/Muse-Glimmer-30B"

	if isOSU {
		if strings.TrimSpace(*urlFlag) == "" && os.Getenv("LLM_BASE_URL") == "" {
			if env := os.Getenv("OSU_LLM_BASE_URL"); env != "" {
				baseURL = env
			} else {
				baseURL = "http://127.0.0.1:8010/v1"
			}
		}
		if apiKey == "" {
			if env := os.Getenv("OSU_LLM_API_KEY"); env != "" {
				apiKey = env
			} else if home, err := os.UserHomeDir(); err == nil {
				keyFile := filepath.Join(home, ".osu-llm", "vllm-api-key")
				if data, err := os.ReadFile(keyFile); err == nil {
					apiKey = strings.TrimSpace(string(data))
				}
			}
		}
		model = strings.TrimPrefix(strings.TrimPrefix(model, "osu:"), "cluster:")
	}

	workDir, err := filepath.Abs(*dirFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving working dir %q: %v\n", *dirFlag, err)
		return 1
	}

	client := llm.New(baseURL, apiKey, model, "")
	if strings.Contains(baseURL, "localhost:11434") || strings.Contains(baseURL, "127.0.0.1:11434") {
		client.SendThink = true
	}

	runner := NewRunner(client, workDir, model)

	req := RunRequest{
		Task:           task,
		WorkingDir:     workDir,
		Model:          model,
		SystemPrompt:   *systemFlag,
		MaxTurns:       *maxTurnsFlag,
		AllowCommands:  !*noCmdsFlag,
		CommandTimeout: time.Duration(*timeoutFlag) * time.Second,
		ThinkLevel:     *thinkFlag,
	}

	// If no task was specified, launch the interactive TUI REPL
	if task == "" || task == "tui" || task == "interactive" {
		if err := runner.RunInteractive(req); err != nil {
			fmt.Fprintf(os.Stderr, "Interactive session error: %v\n", err)
			return 1
		}
		return 0
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Fprintln(os.Stderr, "\nReceived interrupt signal. Canceling task...")
		cancel()
	}()

	if !*jsonFlag && !*quietFlag {
		fmt.Println("==================================================")
		fmt.Println(" fastllm Autonomous Agent Harness")
		fmt.Printf(" Task:        %s\n", task)
		fmt.Printf(" Directory:   %s\n", workDir)
		fmt.Printf(" Model:       %s\n", model)
		fmt.Printf(" Endpoint:    %s\n", baseURL)
		fmt.Printf(" Commands:    %v\n", !*noCmdsFlag)
		fmt.Println("==================================================")
	}

	onEvent := func(ev Event) {
		if *jsonFlag || *quietFlag {
			return
		}
		switch ev.Type {
		case EventTurnStart:
			fmt.Printf("\n[Turn %d]\n", ev.Turn)
		case EventToolCall:
			if ev.ToolCall != nil {
				fmt.Printf("  -> tool: %s %s\n", ev.ToolCall.Name, summarizeArgs(ev.ToolCall.Arguments))
			}
		case EventToolResult:
			if ev.ToolCall != nil {
				lines := strings.Split(strings.TrimSpace(ev.ToolCall.Result), "\n")
				firstLine := ""
				if len(lines) > 0 {
					firstLine = lines[0]
				}
				if len(lines) > 1 {
					fmt.Printf("     result: %s (... %d more lines)\n", firstLine, len(lines)-1)
				} else {
					fmt.Printf("     result: %s\n", firstLine)
				}
			}
		case EventTurnComplete:
			if ev.Metrics != nil {
				fmt.Printf("  [%s]\n", ev.Metrics.FormatTurnSummary())
			}
		}
	}

	result, err := runner.Run(ctx, req, onEvent)

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		if err != nil || (result != nil && !result.Success) {
			return 1
		}
		return 0
	}

	if !*quietFlag {
		fmt.Println("\n==================================================")
		if result != nil && result.Success {
			fmt.Println(" Status:   SUCCESS")
		} else {
			fmt.Println(" Status:   FAILED")
		}
		if result != nil {
			fmt.Printf(" Turns:    %d\n", result.Turns)
			fmt.Printf(" Duration: %.2fs\n", float64(result.DurationMS)/1000.0)
			if result.Metrics != nil {
				fmt.Printf(" Metrics:  %s\n", result.Metrics.FormatSessionSummary())
			}
			if result.Error != "" {
				fmt.Printf(" Error:    %s\n", result.Error)
			}
			if result.FinalResponse != "" {
				fmt.Printf("\nResponse:\n%s\n", result.FinalResponse)
			}
		} else if err != nil {
			fmt.Printf(" Error:    %v\n", err)
		}
		fmt.Println("==================================================")
	}

	if err != nil || (result != nil && !result.Success) {
		return 1
	}
	return 0
}

func summarizeArgs(rawJSON string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &m); err != nil {
		if len(rawJSON) > 60 {
			return rawJSON[:60] + "..."
		}
		return rawJSON
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		if k == "content" || k == "replace" {
			str, _ := v.(string)
			parts = append(parts, fmt.Sprintf("%s=[%d bytes]", k, len(str)))
		} else {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return strings.Join(parts, " ")
}
