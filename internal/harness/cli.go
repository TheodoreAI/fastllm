package harness

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"fastllm/internal/config"
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
	initConsole()

	fs := flag.NewFlagSet("harness", flag.ContinueOnError)

	taskFlag := fs.String("task", "", "Task instruction for the agent to execute")
	dirFlag := fs.String("dir", ".", "Target working directory (sandboxed root)")
	modelFlag := fs.String("model", "", "Model name (defaults to config default_model or llama3.1)")
	urlFlag := fs.String("url", "", "LLM API base URL (defaults to model endpoint URL or http://localhost:11434/v1)")
	keyFlag := fs.String("key", "", "LLM API key (defaults to LLM_API_KEY)")
	maxTurnsFlag := fs.Int("max-turns", 20, "Maximum tool execution turns")
	timeoutFlag := fs.Int("timeout", 60, "Command timeout in seconds for run_command")
	systemFlag := fs.String("system", "", "Custom system prompt override")
	thinkFlag := fs.String("think", "", "Reasoning effort level (e.g. low, medium, high)")
	noCmdsFlag := fs.Bool("no-commands", false, "Disable the run_command tool")
	jsonFlag := fs.Bool("json", false, "Output only the final RunResult JSON")
	quietFlag := fs.Bool("quiet", false, "Suppress turn-by-turn progress output")
	resumeFlag := fs.String("resume", "", "Resume an interactive session by ID, or 'last'")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	task := strings.TrimSpace(*taskFlag)
	if task == "" && fs.NArg() > 0 {
		task = strings.Join(fs.Args(), " ")
	}

	workDir, err := filepath.Abs(*dirFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving working dir %q: %v\n", *dirFlag, err)
		return 1
	}

	// Load settings from .fastllm/config.json or ~/.fastllm/config.json
	settings, _, _ := config.LoadSettings(workDir)

	model := strings.TrimSpace(*modelFlag)
	if model == "" {
		if env := os.Getenv("LLM_CHAT_MODEL"); env != "" {
			model = env
		} else if settings != nil && settings.DefaultModel != "" {
			model = settings.DefaultModel
		} else {
			model = "llama3.1"
		}
	}

	baseURL := strings.TrimSpace(*urlFlag)
	apiKey := strings.TrimSpace(*keyFlag)

	// If model matches a configured endpoint, apply its URL and APIKey if not explicitly passed
	if settings != nil {
		if matched := settings.FindModel(model); matched != nil {
			if baseURL == "" && os.Getenv("LLM_BASE_URL") == "" && matched.URL != "" {
				baseURL = matched.URL
			}
			if apiKey == "" && os.Getenv("LLM_API_KEY") == "" && matched.APIKey != "" {
				apiKey = matched.APIKey
			}
		}
	}

	if baseURL == "" {
		baseURL = getenv("LLM_BASE_URL", "http://localhost:11434/v1")
	}
	if apiKey == "" {
		apiKey = getenv("LLM_API_KEY", "")
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

	client := llm.New(baseURL, apiKey, model, "")
	if strings.Contains(baseURL, "localhost:11434") || strings.Contains(baseURL, "127.0.0.1:11434") {
		client.SendThink = true
	}
	if settings != nil {
		if matched := settings.FindModel(model); matched != nil {
			applyModelParameters(client, matched.Parameters)
		}
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
		ResumeSession:  strings.TrimSpace(*resumeFlag),
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
		cmdStr := ColorGreen("enabled")
		if *noCmdsFlag {
			cmdStr = ColorYellow("disabled")
		}
		lines := []string{
			"",
			FormatKV("task", task, 10),
			FormatKV("directory", workDir, 10),
			FormatKV("model", model, 10),
			FormatKV("endpoint", baseURL, 10),
			FormatKV("commands", cmdStr, 10),
			"",
		}
		fmt.Println(FormatCard("fastllm "+SymDot+" autonomous agent task", lines, 74))
	}

	onEvent := func(ev Event) {
		if *jsonFlag || *quietFlag {
			return
		}
		switch ev.Type {
		case EventTurnStart:
			// Turn started
		case EventToolCall:
			if ev.ToolCall != nil {
				fmt.Println(FormatToolCall(ev.ToolCall.Name, summarizeArgs(ev.ToolCall.Arguments)))
			}
		case EventToolResult:
			if ev.ToolCall != nil {
				fmt.Println(FormatToolResult(ev.ToolCall.Name, ev.ToolCall.Result, 4))
			}
		case EventTurnComplete:
			if ev.Metrics != nil {
				fmt.Println(FormatTurnSummary(*ev.Metrics))
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
		statusVal := ColorGreen(SymCheck + " SUCCESS")
		if result == nil || !result.Success {
			statusVal = ColorRed(SymCross + " FAILED")
		}

		var lines []string
		lines = append(lines, "")
		lines = append(lines, FormatKV("status", statusVal, 10))

		if result != nil {
			lines = append(lines, FormatKV("turns", fmt.Sprintf("%d", result.Turns), 10))
			lines = append(lines, FormatKV("duration", fmt.Sprintf("%.2fs", float64(result.DurationMS)/1000.0), 10))
			if result.Metrics != nil {
				costStr := "$0.0000"
				if result.Metrics.TotalCost > 0 {
					costStr = fmt.Sprintf("$%.4f", result.Metrics.TotalCost)
				}
				tokenSummary := fmt.Sprintf("%d total (%d prompt %s %d completion %s %s)",
					result.Metrics.TotalTokens, result.Metrics.TotalPromptTokens, SymDot,
					result.Metrics.TotalCompletionTokens, SymDot, costStr)
				lines = append(lines, FormatKV("tokens", tokenSummary, 10))
			}
			if result.Error != "" {
				lines = append(lines, FormatKV("error", ColorRed(result.Error), 10))
			}
			if result.FinalResponse != "" {
				lines = append(lines, "")
				lines = append(lines, ColorBrightWhite(StyleBold("Final Response:")))
				for _, respLine := range strings.Split(result.FinalResponse, "\n") {
					lines = append(lines, respLine)
				}
			}
		} else if err != nil {
			lines = append(lines, FormatKV("error", ColorRed(err.Error()), 10))
		}
		lines = append(lines, "")
		fmt.Println("\n" + FormatCard("Task Execution Summary", lines, 74))
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
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := m[k]
		if k == "content" || k == "replace" {
			str, _ := v.(string)
			parts = append(parts, fmt.Sprintf("%s=[%d bytes]", k, len(str)))
		} else {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return strings.Join(parts, " ")
}
