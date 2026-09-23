package harness

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fastllm/internal/llm"
)

func drainAgentInbox(messages *[]llm.Message, inbox <-chan string) {
	if inbox == nil {
		return
	}
	for {
		select {
		case message := <-inbox:
			if strings.TrimSpace(message) != "" {
				*messages = append(*messages, llm.Message{Role: "user", Content: "Parent agent guidance: " + message})
			}
		default:
			return
		}
	}
}

const defaultAgentStatusWait = 60 * time.Second

func (r *Runner) executeAgentTool(ctx context.Context, parent RunRequest, name, rawArgs string) string {
	switch name {
	case "spawn_agent":
		var args struct {
			Task          string   `json:"task"`
			WorkingDir    string   `json:"working_dir"`
			Model         string   `json:"model"`
			MaxTurns      int      `json:"max_turns"`
			Capabilities  []string `json:"capabilities"`
			NetworkPolicy string   `json:"network_policy"`
		}
		if err := decodeToolArguments(rawArgs, &args); err != nil {
			return "Error: invalid spawn_agent arguments: " + err.Error()
		}
		id, err := r.agents.Spawn(parent, args.Task, args.WorkingDir, args.Model, args.MaxTurns, SpawnOptions{
			Capabilities:  args.Capabilities,
			NetworkPolicy: args.NetworkPolicy,
		})
		if err != nil {
			return "Error spawning agent: " + err.Error()
		}
		return fmt.Sprintf("Spawned %s. Use agent_status with agent_id=%q to inspect its result.", id, id)
	case "agent_status":
		var args struct {
			AgentID     string `json:"agent_id"`
			WaitSeconds *int   `json:"wait_seconds"`
		}
		if err := decodeToolArguments(rawArgs, &args); err != nil {
			return "Error: invalid agent_status arguments: " + err.Error()
		}
		if strings.TrimSpace(args.AgentID) == "" {
			return r.agents.Status("")
		}
		wait := defaultAgentStatusWait
		if args.WaitSeconds != nil {
			seconds := max(0, min(*args.WaitSeconds, 300))
			wait = time.Duration(seconds) * time.Second
		}
		return r.agents.WaitStatus(ctx, args.AgentID, wait)
	case "send_agent_message":
		var args struct {
			AgentID string `json:"agent_id"`
			Message string `json:"message"`
		}
		if err := decodeToolArguments(rawArgs, &args); err != nil {
			return "Error: invalid send_agent_message arguments: " + err.Error()
		}
		if err := r.agents.Send(args.AgentID, args.Message); err != nil {
			return "Error sending agent message: " + err.Error()
		}
		return "Message queued for " + args.AgentID + "."
	case "cancel_agent":
		var args struct {
			AgentID string `json:"agent_id"`
		}
		if err := decodeToolArguments(rawArgs, &args); err != nil {
			return "Error: invalid cancel_agent arguments: " + err.Error()
		}
		if err := r.agents.Cancel(args.AgentID); err != nil {
			return "Error canceling agent: " + err.Error()
		}
		return "Cancellation requested for " + args.AgentID + "."
	default:
		return fmt.Sprintf("Error: unknown agent tool %q", name)
	}
}

type AgentState string

const (
	AgentPending   AgentState = "pending"
	AgentRunning   AgentState = "running"
	AgentCompleted AgentState = "completed"
	AgentFailed    AgentState = "failed"
	AgentCanceled  AgentState = "canceled"
)

type AgentRecord struct {
	ID            string
	Task          string
	WorkingDir    string
	Model         string
	State         AgentState
	Capabilities  []string
	NetworkPolicy string
	StartedAt     time.Time
	FinishedAt    time.Time
	Result        *RunResult
	Error         string
	Metrics       SessionMetrics

	cancel context.CancelFunc
	inbox  chan string
	done   chan struct{}
}

type SpawnOptions struct {
	Capabilities  []string
	NetworkPolicy string
}

type AgentManager struct {
	mu            sync.RWMutex
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	closed        bool
	runner        *Runner
	agents        map[string]*AgentRecord
	nextID        int
	maxConcurrent int
	maxDepth      int
}

func NewAgentManager(runner *Runner, maxConcurrent, maxDepth int) *AgentManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &AgentManager{
		ctx: ctx, cancel: cancel,
		runner: runner, agents: make(map[string]*AgentRecord),
		maxConcurrent: maxConcurrent, maxDepth: maxDepth,
	}
}

func (m *AgentManager) Tools(depth int) []llm.Tool {
	if m == nil || depth >= m.maxDepth {
		return nil
	}
	return []llm.Tool{spawnAgentTool, agentStatusTool, sendAgentMessageTool, cancelAgentTool}
}

func (m *AgentManager) Spawn(parent RunRequest, task, requestedDir, model string, maxTurns int, opts ...SpawnOptions) (string, error) {
	if m == nil {
		return "", fmt.Errorf("agent manager is unavailable")
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return "", fmt.Errorf("task is required")
	}
	if parent.AgentDepth >= m.maxDepth {
		return "", fmt.Errorf("maximum delegation depth (%d) reached", m.maxDepth)
	}

	var opt SpawnOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	opt.Capabilities = attenuateCapabilities(parent.Capabilities, opt.Capabilities)
	networkPolicy, err := attenuateNetworkPolicy(parent.NetworkPolicy, opt.NetworkPolicy)
	if err != nil {
		return "", err
	}
	opt.NetworkPolicy = networkPolicy

	workingDir, err := childWorkingDirectory(parent.WorkingDir, requestedDir)
	if err != nil {
		return "", err
	}
	if model == "" {
		model = parent.Model
	}
	if _, directClient := m.runner.LLM.(*llm.Client); directClient && model != parent.Model {
		return "", fmt.Errorf("model override %q cannot use the active endpoint for %q; switch the parent model first or use a routed client", model, parent.Model)
	}
	if maxTurns <= 0 {
		maxTurns = 10
	}
	if maxTurns > 50 {
		maxTurns = 50
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", fmt.Errorf("agent manager is closed")
	}
	if m.activeCountLocked() >= m.maxConcurrent {
		m.mu.Unlock()
		return "", fmt.Errorf("agent concurrency limit (%d) reached", m.maxConcurrent)
	}
	m.nextID++
	id := fmt.Sprintf("agent-%d", m.nextID)
	ctx, cancel := context.WithCancel(m.ctx)
	record := &AgentRecord{
		ID: id, Task: task, WorkingDir: workingDir, Model: model,
		Capabilities: opt.Capabilities, NetworkPolicy: opt.NetworkPolicy,
		State: AgentPending, cancel: cancel, inbox: make(chan string, 16), done: make(chan struct{}),
	}
	m.agents[id] = record
	m.wg.Add(1)
	m.mu.Unlock()

	childReq := RunRequest{
		Task: task, WorkingDir: workingDir, Model: model, MaxTurns: maxTurns,
		AllowCommands: parent.AllowCommands, CommandsConfigured: true, CommandTimeout: parent.CommandTimeout,
		// A child is sandboxed whenever its parent is; it cannot opt out.
		Sandbox:    parent.Sandbox,
		ThinkLevel: parent.ThinkLevel, AgentDepth: parent.AgentDepth + 1,
		AgentInbox: record.inbox, PermissionMode: parent.PermissionMode,
		Capabilities: opt.Capabilities, NetworkPolicy: opt.NetworkPolicy,
	}
	go m.run(ctx, record, childReq)
	return id, nil
}

func childWorkingDirectory(parentDir, requested string) (string, error) {
	root, err := filepath.Abs(parentDir)
	if err != nil {
		return "", fmt.Errorf("resolve parent workspace: %w", err)
	}
	if strings.TrimSpace(requested) == "" {
		return root, nil
	}
	child, err := resolveInteractiveDirectory(root, requested)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, child)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("child working directory must stay within %q", root)
	}
	return child, nil
}

func (m *AgentManager) run(ctx context.Context, record *AgentRecord, req RunRequest) {
	defer m.wg.Done()
	m.mu.Lock()
	record.State = AgentRunning
	record.StartedAt = time.Now()
	m.mu.Unlock()

	childRunner := m.runner.childRunner()
	result, err := childRunner.Run(ctx, req, func(ev Event) {
		if ev.Type == EventTurnComplete && ev.Metrics != nil {
			m.mu.Lock()
			record.Metrics.Add(*ev.Metrics)
			m.mu.Unlock()
		}
	})

	m.mu.Lock()
	record.FinishedAt = time.Now()
	record.Result = result
	switch {
	case ctx.Err() != nil:
		record.State = AgentCanceled
		record.Error = ctx.Err().Error()
	case err != nil:
		record.State = AgentFailed
		record.Error = err.Error()
	case result != nil && result.Error != "":
		record.State = AgentFailed
		record.Error = result.Error
	default:
		record.State = AgentCompleted
	}
	m.mu.Unlock()
	close(record.done)
}

func (m *AgentManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (r *Runner) childRunner() *Runner {
	child := *r
	if client, ok := r.LLM.(*llm.Client); ok {
		cloned := llm.New(client.BaseURL, client.APIKey, client.ChatModel, client.EmbedModel)
		cloned.HTTPClient = client.HTTPClient
		cloned.ChannelFraming = client.ChannelFraming
		cloned.SendThink = client.SendThink
		cloned.Temperature = client.Temperature
		cloned.TopP = client.TopP
		cloned.MaxTokens = client.MaxTokens
		child.LLM = cloned
	}
	return &child
}

func (m *AgentManager) activeCountLocked() int {
	count := 0
	for _, agent := range m.agents {
		if agent.State == AgentPending || agent.State == AgentRunning {
			count++
		}
	}
	return count
}

func (m *AgentManager) ActiveCount() int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeCountLocked()
}

func (m *AgentManager) Status(id string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if id != "" {
		agent, ok := m.agents[id]
		if !ok {
			return fmt.Sprintf("Error: unknown agent %q.", id)
		}
		return formatAgentRecord(agent)
	}
	summary := m.summaryLocked()
	header := formatAgentSummary(summary)
	if len(m.agents) == 0 {
		return header
	}
	ids := make([]string, 0, len(m.agents))
	for agentID := range m.agents {
		ids = append(ids, agentID)
	}
	sort.Strings(ids)
	lines := make([]string, 0, len(ids))
	for _, agentID := range ids {
		a := m.agents[agentID]
		lines = append(lines, fmt.Sprintf("%s | %-9s | %d tok | %s", a.ID, a.State, a.Metrics.TotalTokens, oneLine(a.Task, 70)))
	}
	return header + "\n" + strings.Join(lines, "\n")
}

// WaitStatus waits for one active child to finish, or until the caller's
// context or wait limit expires. Keeping the wait inside the harness prevents
// an LLM from spending a model turn on every instantaneous status poll.
func (m *AgentManager) WaitStatus(ctx context.Context, id string, wait time.Duration) string {
	m.mu.RLock()
	agent, ok := m.agents[id]
	if !ok {
		m.mu.RUnlock()
		return fmt.Sprintf("Error: unknown agent %q.", id)
	}
	done := agent.done
	active := agent.State == AgentPending || agent.State == AgentRunning
	m.mu.RUnlock()

	if !active || wait <= 0 {
		return m.Status(id)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
		return m.Status(id)
	case <-ctx.Done():
		return m.Status(id) + "\nWait canceled: " + ctx.Err().Error()
	case <-timer.C:
		return m.Status(id) + fmt.Sprintf("\nStill active after waiting %s. Continue other useful work before checking again.", wait.Round(time.Second))
	}
}

func formatAgentRecord(a *AgentRecord) string {
	lines := []string{
		fmt.Sprintf("Agent: %s", a.ID), fmt.Sprintf("State: %s", a.State),
		fmt.Sprintf("Task: %s", a.Task), fmt.Sprintf("Directory: %s", a.WorkingDir),
		fmt.Sprintf("Model: %s", a.Model),
		fmt.Sprintf("Tokens: %d total (%d in / %d out)", a.Metrics.TotalTokens, a.Metrics.TotalPromptTokens, a.Metrics.TotalCompletionTokens),
		fmt.Sprintf("Turns: %d", a.Metrics.TotalTurns),
	}
	if a.Error != "" {
		lines = append(lines, "Error: "+a.Error)
	}
	if a.Result != nil && strings.TrimSpace(a.Result.FinalResponse) != "" {
		lines = append(lines, "Result:\n"+a.Result.FinalResponse)
	}
	return strings.Join(lines, "\n")
}

type AgentSummary struct {
	Total, Pending, Running, Completed, Failed, Canceled int
	PromptTokens, CompletionTokens, TotalTokens          int
}

func (m *AgentManager) Summary() AgentSummary {
	if m == nil {
		return AgentSummary{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.summaryLocked()
}

func (m *AgentManager) summaryLocked() AgentSummary {
	var summary AgentSummary
	for _, agent := range m.agents {
		summary.Total++
		switch agent.State {
		case AgentPending:
			summary.Pending++
		case AgentRunning:
			summary.Running++
		case AgentCompleted:
			summary.Completed++
		case AgentFailed:
			summary.Failed++
		case AgentCanceled:
			summary.Canceled++
		}
		summary.PromptTokens += agent.Metrics.TotalPromptTokens
		summary.CompletionTokens += agent.Metrics.TotalCompletionTokens
		summary.TotalTokens += agent.Metrics.TotalTokens
	}
	return summary
}

func formatAgentSummary(s AgentSummary) string {
	return fmt.Sprintf(
		"Agents: %d known | %d active (%d pending, %d running) | %d completed | %d failed | %d canceled\nTokens: %d total (%d in / %d out)",
		s.Total, s.Pending+s.Running, s.Pending, s.Running, s.Completed, s.Failed, s.Canceled,
		s.TotalTokens, s.PromptTokens, s.CompletionTokens,
	)
}

func oneLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > limit {
		return value[:limit-3] + "..."
	}
	return value
}

func (m *AgentManager) Send(id, message string) error {
	m.mu.RLock()
	agent, ok := m.agents[id]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("unknown agent %q", id)
	}
	if agent.State != AgentPending && agent.State != AgentRunning {
		m.mu.RUnlock()
		return fmt.Errorf("agent %s is %s", id, agent.State)
	}
	inbox := agent.inbox
	m.mu.RUnlock()
	select {
	case inbox <- strings.TrimSpace(message):
		return nil
	default:
		return fmt.Errorf("agent %s inbox is full", id)
	}
}

func (m *AgentManager) Cancel(id string) error {
	m.mu.RLock()
	agent, ok := m.agents[id]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("unknown agent %q", id)
	}
	if agent.State != AgentPending && agent.State != AgentRunning {
		m.mu.RUnlock()
		return fmt.Errorf("agent %s is already %s", id, agent.State)
	}
	cancel := agent.cancel
	m.mu.RUnlock()
	cancel()
	return nil
}
