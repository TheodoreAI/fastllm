package harness

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fastllm/internal/llm"
)

// The monitor is the harness's reference monitor. The model is an untrusted
// process whose only system calls are tool calls, and every one of them is
// decided here: the tool list the model is offered, dispatch in executeTool,
// the execution broker's policy, and the file writer's policy are all views of
// authorize. Nothing else encodes which mode permits what.
//
// Invariants (docs in README "Permission modes"; tests in monitor_test.go):
//
//	I1 no self-escalation: no tool changes the mode, a grant, or a policy.
//	I2 complete mediation: every effect is decided by authorize.
//	I3 fail closed: an empty or unknown mode is plan; an unknown tool is denied.
//	I4 the mode table below is an upper bound on what a run can do.
//	I5 delegation only narrows: a child of agent runs as plan.
//	I6 a run's mode is fixed when it starts.
//	I7 an Ask shows the full effect being approved.
//	I8 the harness's own configuration is not model-writable: no mode writes
//	   inside .git, and writes to fastllm settings, rule files, and skills
//	   always ask (protected.go).
//	I9 a session grant covers what was approved, not the tool: a command
//	   prefix or exact command, a directory or file (grants.go).
//	I10 reading a secret file always asks, and once one is read every web
//	   request asks, for the rest of the conversation (secrets.go).

// Decision is the monitor's verdict on one tool call.
type Decision int

const (
	Deny Decision = iota
	Ask
	Allow
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	default:
		return "deny"
	}
}

// toolClass groups tools by the effect they can have.
type toolClass int

const (
	classUnknown  toolClass = iota
	classRead               // workspace reads and bookkeeping with no effect
	classPlan               // submit_plan: plan mode's terminal call
	classNetwork            // public web access
	classWrite              // workspace file mutation
	classCommand            // subprocess creation or termination
	classProcess            // inspecting processes this run started
	classDelegate           // creating, steering, or stopping a child agent
	classInspect            // reading child-agent state
)

var toolClasses = map[string]toolClass{
	"read_file":          classRead,
	"list_files":         classRead,
	"search_files":       classRead,
	"glob_files":         classRead,
	"read_observation":   classRead,
	"update_plan":        classRead,
	"finish_task":        classRead,
	"submit_plan":        classPlan,
	"web_search":         classNetwork,
	"web_fetch":          classNetwork,
	"write_file":         classWrite,
	"edit_file":          classWrite,
	"patch_file":         classWrite,
	"run_command":        classCommand,
	"kill_process":       classCommand,
	"process_status":     classProcess,
	"spawn_agent":        classDelegate,
	"cancel_agent":       classDelegate,
	"send_agent_message": classDelegate,
	"agent_status":       classInspect,
}

// effectiveMode is the mode the table is evaluated under. A child cannot ask
// the user anything, so a child of an asking parent is read-only (I5).
func effectiveMode(req RunRequest) PermissionMode {
	mode := NormalizeMode(req.PermissionMode)
	if req.AgentDepth > 0 && mode == PermissionAgent {
		return PermissionPlan
	}
	return mode
}

// runCapabilities is the attenuation layer: explicit Capabilities and the
// NetworkPolicy can only remove what the mode would otherwise permit.
type runCapabilities struct {
	write, network, commands, delegate bool
}

func capabilitiesFor(req RunRequest) runCapabilities {
	networkPolicy, err := normalizeNetworkPolicy(req.NetworkPolicy)
	c := runCapabilities{
		write:    allowsCapability(req.Capabilities, "write"),
		network:  err == nil && networkPolicy != "none" && allowsCapability(req.Capabilities, "network"),
		delegate: allowsCapability(req.Capabilities, "delegate"),
	}
	// Local shells are not confined to the workspace or kept off the network,
	// so they are only granted where both of those are already granted.
	c.commands = c.write && c.network && allowsCapability(req.Capabilities, "commands") && req.AllowCommands
	return c
}

// decideTool is the mode table (I4). It ignores arguments.
func decideTool(req RunRequest, tool string) Decision {
	class, known := toolClasses[tool]
	if !known {
		return Deny
	}
	mode := effectiveMode(req)
	caps := capabilitiesFor(req)
	gate := func(granted bool, d Decision) Decision {
		if !granted {
			return Deny
		}
		return d
	}
	switch class {
	case classRead, classInspect:
		return Allow
	case classPlan:
		// A plan is a proposal to the user, so only a top-level plan run makes one.
		if NormalizeMode(req.PermissionMode) == PermissionPlan && req.AgentDepth == 0 {
			return Allow
		}
		return Deny
	case classNetwork:
		// Plan has no network: a fetch URL can carry workspace contents out.
		if mode == PermissionPlan {
			return Deny
		}
		return gate(caps.network, Allow)
	case classWrite:
		switch mode {
		case PermissionAgent:
			return gate(caps.write, Ask)
		case PermissionEdit, PermissionFull:
			return gate(caps.write, Allow)
		}
	case classCommand:
		switch mode {
		case PermissionAgent:
			return gate(caps.commands, Ask)
		case PermissionFull:
			return gate(caps.commands, Allow)
		}
	case classProcess:
		if mode == PermissionAgent || mode == PermissionFull {
			return gate(caps.commands, Allow)
		}
	case classDelegate:
		switch mode {
		case PermissionAgent:
			if tool == "send_agent_message" {
				return gate(caps.delegate, Allow)
			}
			return gate(caps.delegate, Ask)
		case PermissionFull:
			return gate(caps.delegate, Allow)
		}
	}
	return Deny
}

// authorize decides a call including its arguments: a mutation carrying a
// then_run is also a run_command, and is never looser than either part.
func authorize(req RunRequest, tool, args string) Decision {
	d := decideTool(req, tool)
	if toolClasses[tool] == classWrite && fusedFollowUp(args) != nil {
		d = min(d, decideTool(req, "run_command"))
	}
	switch writeProtection(req, tool, args) {
	case protectGit:
		d = Deny
	case protectConfig:
		d = min(d, Ask)
	}
	return d
}

// fusedFollowUp reports a then_run whether or not its command is blank: any
// then_run object asks for a subprocess.
func fusedFollowUp(raw string) *FollowUpCommand {
	var arguments struct {
		ThenRun *FollowUpCommand `json:"then_run"`
	}
	if json.Unmarshal([]byte(raw), &arguments) != nil {
		return nil
	}
	return arguments.ThenRun
}

// admit runs the whole decision for one call, including asking the user for
// an Ask, and records it in the run's audit log. A nil Authorize means nobody
// can be asked, so Ask becomes Deny. It returns the reason for a refusal,
// phrased for the model.
func admit(req RunRequest, tool, args string) (bool, string) {
	v := decide(req, tool, args)
	req.Audit.Record(AuditRecord{
		Time: time.Now().UTC(), AgentDepth: req.AgentDepth, Mode: effectiveMode(req),
		Tool: tool, Summary: consentSummary(tool, args), ArgsSHA256: argsDigest(args),
		Allowed: v.allowed, Layer: v.layer, Via: v.via,
	})
	return v.allowed, v.refusal
}

// verdict is one decision and the layer that made it, for the audit log.
type verdict struct {
	allowed bool
	refusal string
	layer   string
	via     string
}

func refused(layer, message string) verdict {
	return verdict{layer: layer, refusal: message}
}

func decide(req RunRequest, tool, args string) verdict {
	if decideTool(req, tool) == Deny {
		return refused(denialLayer(req, tool), fmt.Sprintf("Error: permission denied: %s is not available %s. Do not retry it; work within the tools you were given.", tool, denialSource(req, tool)))
	}
	followUp := fusedFollowUp(args)
	if toolClasses[tool] == classWrite && followUp != nil && decideTool(req, "run_command") == Deny {
		return refused(denialLayer(req, "run_command"), fmt.Sprintf("Error: permission denied: fused then_run commands are not available %s. The file mutation was not attempted; retry without then_run.", denialSource(req, "run_command")))
	}
	decision, askAs, summary := decideTool(req, tool), tool, consentSummary(tool, args)
	layer := "mode"
	switch writeProtection(req, tool, args) {
	case protectGit:
		return refused("protected-git", "Error: permission denied: file tools never write inside .git in any mode, because git runs commands named in its own files. Do not retry; ask the user to make git changes themselves.")
	case protectConfig:
		// Settings, rules, and skills shape every later session, so even edit
		// and full ask, under a label a session grant for ordinary writes
		// does not cover.
		decision = min(decision, Ask)
		askAs = tool + " (fastllm configuration)"
		summary += " — changes settings, rules, or skills that fastllm loads into future sessions"
		layer = "protected-config"
		if decision == Ask && req.Authorize == nil {
			return refused("protected-config", "Error: permission denied: this path holds fastllm settings, rules, or skills, and changing it needs the user's approval, which this run cannot ask for. Do not retry.")
		}
	}
	var scope *GrantScope
	if secretPath, secret := readsSecret(req, tool, args); secret {
		decision = min(decision, Ask)
		askAs = tool + " (secret file)"
		summary += " — a credentials file; reading it puts its contents in the conversation, and every web request after it will ask"
		layer = "secret-file"
		exact := GrantScope{Tool: askAs, Kind: scopeFile, Pattern: secretPath, Subject: secretPath}
		scope = &exact
		if req.Authorize == nil {
			return refused("secret-file", "Error: permission denied: "+secretPath+" looks like a credentials file, and reading it needs the user's approval, which this run cannot ask for. Do not retry; work without it.")
		}
	}
	if sources := req.Taint.Sources(); len(sources) > 0 && toolClasses[tool] == classNetwork {
		decision = min(decision, Ask)
		askAs = tool + " (after reading secrets)"
		summary += " — this conversation has read " + strings.Join(sources, ", ") + "; this request could carry their contents out"
		layer = "tainted-network"
		exact := GrantScope{Tool: askAs, Kind: scopeExact, Pattern: summary, Subject: summary}
		scope = &exact
		if req.Authorize == nil {
			return refused("tainted-network", "Error: permission denied: this conversation has read a credentials file, so web requests need the user's approval, which this run cannot ask for. Do not retry.")
		}
	}
	var via string
	if decision == Ask {
		if req.Authorize == nil {
			return refused("no-approver", "Permission denied by user for "+tool+". Do not retry this action unless the user explicitly asks.")
		}
		// Anything but the plain mode table asks in every mode.
		always := layer != "mode"
		layer = "user"
		consentScope := scopeFor(req, askAs, tool, args)
		if scope != nil {
			consentScope = *scope
		}
		consent := ConsentRequest{Tool: askAs, Summary: summary, Scope: consentScope, Via: &via, Always: always}
		if !askUser(req, consent) {
			v := refused("user", "Permission denied by user for "+tool+". Do not retry this action unless the user explicitly asks.")
			v.via = orDefault(via, "denied")
			return v
		}
	}
	if toolClasses[tool] == classWrite && followUp != nil && decideTool(req, "run_command") == Ask {
		var followVia string
		consent := ConsentRequest{Tool: "run_command", Summary: "command=" + followUp.Command, Scope: commandScope("run_command", followUp.Command), Via: &followVia}
		if !askUser(req, consent) {
			v := refused("user", "Permission denied by user for the fused follow-up command. The file mutation was not attempted.")
			v.via = "then_run " + orDefault(followVia, "denied")
			return v
		}
		layer, via = "user", strings.TrimSpace(via+"; then_run "+orDefault(followVia, "approved"))
	}
	if layer == "user" {
		via = orDefault(via, "approved")
	}
	if secretPath, secret := readsSecret(req, tool, args); secret {
		req.Taint.Mark(secretPath)
	}
	return verdict{allowed: true, layer: layer, via: via}
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// denialLayer is denialSource's category, for the audit log.
func denialLayer(req RunRequest, tool string) string {
	switch source := denialSource(req, tool); {
	case strings.HasPrefix(source, "in this harness"):
		return "unknown-tool"
	case strings.HasSuffix(source, " mode"):
		return "mode"
	case strings.Contains(source, "commands are turned off"):
		return "commands-off"
	default:
		return "capability"
	}
}

// denialSource names the layer that refused, so the model (and whoever reads
// the transcript) can tell a mode from a capability restriction.
func denialSource(req RunRequest, tool string) string {
	if _, known := toolClasses[tool]; !known {
		return "in this harness"
	}
	modeOnly := RunRequest{PermissionMode: req.PermissionMode, AgentDepth: req.AgentDepth, AllowCommands: true}
	if decideTool(modeOnly, tool) == Deny {
		return "in " + effectiveMode(req).Label() + " mode"
	}
	if cls := toolClasses[tool]; (cls == classCommand || cls == classProcess) && !req.AllowCommands {
		return "because commands are turned off for this session"
	}
	return "under this run's capability policy"
}

func askUser(req RunRequest, consent ConsentRequest) bool {
	return req.Authorize != nil && req.Authorize(consent)
}

// consentSummary states exactly what an approval would permit (I7). Commands
// and task text are never shortened; file bodies are sized rather than shown.
func consentSummary(tool, raw string) string {
	var args map[string]any
	if json.Unmarshal([]byte(raw), &args) != nil {
		return "arguments=" + raw
	}
	str := func(key string) string {
		value, _ := args[key].(string)
		return value
	}
	var parts []string
	switch tool {
	case "run_command":
		parts = append(parts, "command="+str("command"))
		if background, _ := args["background"].(bool); background {
			parts = append(parts, "background=true")
		}
	case "write_file":
		parts = append(parts, "path="+str("path"), fmt.Sprintf("content=[%d bytes]", len(str("content"))))
	case "edit_file":
		parts = append(parts, "path="+str("path"), fmt.Sprintf("search=[%d bytes] replace=[%d bytes]", len(str("search")), len(str("replace"))))
	case "patch_file":
		parts = append(parts, "path="+str("path"), fmt.Sprintf("diff=[%d lines]", strings.Count(str("diff"), "\n")+1))
	case "spawn_agent":
		parts = append(parts, "task="+str("task"))
		for _, key := range []string{"working_dir", "model"} {
			if value := str(key); value != "" {
				parts = append(parts, key+"="+value)
			}
		}
	default:
		return summarizeArgs(raw)
	}
	if followUp := fusedFollowUp(raw); followUp != nil {
		parts = append(parts, "then_run="+followUp.Command)
	}
	return strings.Join(parts, " ")
}

// allTools is every tool the harness can offer, in the order offered.
var allTools = []llm.Tool{
	readFileTool,
	listFilesTool,
	searchFilesTool,
	globFilesTool,
	updatePlanTool,
	submitPlanTool,
	finishTaskTool,
	webSearchTool,
	webFetchTool,
	writeFileTool,
	editFileTool,
	patchFileTool,
	spawnAgentTool,
	agentStatusTool,
	sendAgentMessageTool,
	cancelAgentTool,
	readObservationTool,
	runCommandTool,
	processStatusTool,
	killProcessTool,
}

// toolAvailability covers what exists in this runtime, as opposed to what is
// permitted: an observation store, and delegation depth left to spend.
type toolAvailability struct {
	observations bool
	delegation   bool
}

// toolsForRequest is the tool list offered to the model: exactly the tools
// the monitor would not deny, so an offered tool is never refused by mode and
// a denied tool is never advertised.
func toolsForRequest(req RunRequest, avail toolAvailability) []llm.Tool {
	tools := make([]llm.Tool, 0, len(allTools))
	for _, tool := range allTools {
		name := tool.Function.Name
		switch {
		case name == "read_observation" && !avail.observations:
			continue
		case name == "spawn_agent" && !avail.delegation:
			continue
		}
		if decideTool(req, name) == Deny {
			continue
		}
		// A parameter that would always be refused is not advertised either:
		// where commands are denied, mutations are offered without then_run.
		if toolClasses[name] == classWrite && decideTool(req, "run_command") == Deny {
			tool = withoutFollowUp(tool)
		}
		tools = append(tools, tool)
	}
	return tools
}

// withoutFollowUp returns a copy of a mutation tool whose schema has no
// then_run property. The shared tool definitions are never modified.
func withoutFollowUp(tool llm.Tool) llm.Tool {
	params, ok := tool.Function.Parameters.(map[string]any)
	if !ok {
		return tool
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		return tool
	}
	trimmedProps := make(map[string]any, len(props))
	for key, value := range props {
		if key != "then_run" {
			trimmedProps[key] = value
		}
	}
	trimmedParams := make(map[string]any, len(params))
	for key, value := range params {
		trimmedParams[key] = value
	}
	trimmedParams["properties"] = trimmedProps
	tool.Function.Parameters = trimmedParams
	return tool
}

// capabilityPolicy is the coarse view of the table that the execution broker
// and the file writer enforce independently of dispatch.
type capabilityPolicy struct {
	write, network, commands, delegate bool
}

func policyForRequest(req RunRequest) capabilityPolicy {
	return capabilityPolicy{
		write:    decideTool(req, "write_file") != Deny,
		network:  decideTool(req, "web_fetch") != Deny,
		commands: decideTool(req, "run_command") != Deny,
		delegate: decideTool(req, "spawn_agent") != Deny,
	}
}

// modePrompt is the advisory note for the run's mode, derived from the same
// decisions it describes.
func modePrompt(req RunRequest) string {
	switch {
	case decideTool(req, "submit_plan") == Allow:
		return planModePrompt
	case effectiveMode(req) == PermissionEdit:
		return editModePrompt
	}
	return ""
}
