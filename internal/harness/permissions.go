package harness

import (
	"bufio"
	"fmt"
	"strings"
	"time"
)

// PermissionMode is the user's standing authorization for model-initiated
// effects. It is chosen only by user input (Shift+Tab, /set, plan approval,
// -mode); no tool call can change it. The table it selects lives in monitor.go.
type PermissionMode string

const (
	// PermissionPlan is read-only with no network: the model explores and
	// proposes a plan with submit_plan.
	PermissionPlan PermissionMode = "plan"
	// PermissionAgent asks the user before every mutating call.
	PermissionAgent PermissionMode = "agent"
	// PermissionEdit permits workspace file edits without asking, but never a
	// subprocess or a child agent.
	PermissionEdit PermissionMode = "edit"
	// PermissionFull permits everything the run's capabilities allow, unasked.
	PermissionFull PermissionMode = "full"
)

// permissionCycle is the Shift+Tab order, which is also least to most
// authority: a headless agent run cannot ask, so it sits below edit.
var permissionCycle = []PermissionMode{PermissionPlan, PermissionAgent, PermissionEdit, PermissionFull}

// ModeRank orders modes by authority, plan lowest. An unknown mode ranks as
// plan, matching NormalizeMode's fail-closed default.
func ModeRank(mode PermissionMode) int {
	mode = NormalizeMode(mode)
	for i, m := range permissionCycle {
		if m == mode {
			return i
		}
	}
	return 0
}

// ParsePermissionMode accepts the four mode names and the legacy names that
// older sessions and API callers still send.
func ParsePermissionMode(value string) (PermissionMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "plan", "read-only", "readonly":
		return PermissionPlan, nil
	case "agent", "ask":
		return PermissionAgent, nil
	case "edit", "edit-only":
		return PermissionEdit, nil
	case "full", "full-access", "auto":
		return PermissionFull, nil
	default:
		return "", fmt.Errorf("permissions must be plan, agent, edit, or full")
	}
}

// NormalizeMode fails closed: an empty, unknown, or legacy-misspelled mode is
// treated as plan, the least-privileged mode.
func NormalizeMode(mode PermissionMode) PermissionMode {
	parsed, err := ParsePermissionMode(string(mode))
	if err != nil {
		return PermissionPlan
	}
	return parsed
}

// Next is the mode Shift+Tab switches to.
func (m PermissionMode) Next() PermissionMode {
	current := NormalizeMode(m)
	for i, mode := range permissionCycle {
		if mode == current {
			return permissionCycle[(i+1)%len(permissionCycle)]
		}
	}
	return PermissionPlan
}

// Label is the display name.
func (m PermissionMode) Label() string {
	switch NormalizeMode(m) {
	case PermissionAgent:
		return "Agent"
	case PermissionEdit:
		return "Edit"
	case PermissionFull:
		return "Full-Access"
	default:
		return "Plan"
	}
}

// CapabilityGrant is one "for this session" approval. It covers its Scope in
// the workspace it was granted in, until revoked or the session changes (I9).
type CapabilityGrant struct {
	ID        string     `json:"id"`
	Tool      string     `json:"tool"`
	Scope     GrantScope `json:"scope"`
	Workspace string     `json:"workspace,omitempty"`
	GrantedAt time.Time  `json:"granted_at"`
	Revoked   bool       `json:"revoked"`
}

type PermissionController struct {
	Mode        PermissionMode
	Grants      []*CapabilityGrant
	Input       interactiveInput
	Workspace   string
	nextGrantID int
}

func NewPermissionController(mode PermissionMode, source any) *PermissionController {
	if mode == "" {
		mode = PermissionPlan
	}
	var input interactiveInput
	switch value := source.(type) {
	case interactiveInput:
		input = value
	case *bufio.Scanner:
		input = &scannerInput{scanner: value}
	}
	return &PermissionController{
		Mode:   mode,
		Grants: make([]*CapabilityGrant, 0),
		Input:  input,
	}
}

func (p *PermissionController) SetWorkspace(workspace string) {
	p.Workspace = workspace
	p.ClearGrants()
}

func (p *PermissionController) SetMode(mode PermissionMode) {
	p.Mode = mode
	p.ClearGrants()
}

func (p *PermissionController) ClearGrants() {
	if p == nil {
		return
	}
	for _, g := range p.Grants {
		g.Revoked = true
	}
}

// Grant records a session grant for every call of a tool. Scoped grants,
// which is what approvals make, go through GrantScoped.
func (p *PermissionController) Grant(toolName string) *CapabilityGrant {
	return p.GrantScoped(GrantScope{Tool: toolName, Kind: scopeTool})
}

// GrantScoped records a session grant covering scope.
func (p *PermissionController) GrantScoped(scope GrantScope) *CapabilityGrant {
	if p == nil {
		return nil
	}
	p.nextGrantID++
	scope.Subject = ""
	grant := &CapabilityGrant{
		ID:        fmt.Sprintf("grant-%d", p.nextGrantID),
		Tool:      scope.Tool,
		Scope:     scope,
		Workspace: p.Workspace,
		GrantedAt: time.Now(),
	}
	p.Grants = append(p.Grants, grant)
	return grant
}

// Covers reports whether an active grant in this workspace permits request.
func (p *PermissionController) Covers(request ConsentRequest) bool {
	return p.CoveringGrant(request) != nil
}

// CoveringGrant returns the active grant that permits request, if any.
func (p *PermissionController) CoveringGrant(request ConsentRequest) *CapabilityGrant {
	for _, g := range p.ActiveGrants() {
		if g.Scope.Covers(request.Scope) {
			return g
		}
	}
	return nil
}

// HasGrant reports whether any active grant exists for a tool label, whatever
// its scope. It is for display and tests; authorization uses Covers.
func (p *PermissionController) HasGrant(toolName string) bool {
	for _, g := range p.ActiveGrants() {
		if g.Tool == toolName {
			return true
		}
	}
	return false
}

func (p *PermissionController) RevokeGrant(target string) int {
	if p == nil {
		return 0
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return 0
	}
	count := 0
	for _, g := range p.Grants {
		if !g.Revoked && (strings.EqualFold(g.ID, target) || strings.EqualFold(g.Tool, target)) {
			g.Revoked = true
			count++
		}
	}
	return count
}

func (p *PermissionController) ActiveGrants() []*CapabilityGrant {
	if p == nil {
		return nil
	}
	var active []*CapabilityGrant
	for _, g := range p.Grants {
		if !g.Revoked {
			if g.Workspace == "" || p.Workspace == "" || g.Workspace == p.Workspace {
				active = append(active, g)
			}
		}
	}
	return active
}

// Authorize is the interactive half of an Ask decision from the monitor: it
// consults session grants, then prompts. It decides nothing about which tools
// need asking; the monitor calls it only for calls the mode table marks Ask.
func (p *PermissionController) Authorize(request ConsentRequest) bool {
	// Outside agent mode the monitor asks only for the always-ask classes;
	// anything else arriving here is refused rather than approved.
	if p == nil || (NormalizeMode(p.Mode) != PermissionAgent && !request.Always) {
		return false
	}
	if g := p.CoveringGrant(request); g != nil {
		request.note(g.ID)
		return true
	}

	fmt.Println(FormatPermissionPrompt(request.Tool, request.Summary))
	session := "Yes, and allow " + request.Scope.Describe() + " this session"

	// Preferred path: an inline menu where enter accepts the highlighted option.
	// Deny is highlighted first so an accidental enter is never destructive.
	choices := []Choice{
		{Key: 'n', Label: "No", Value: "n"},
		{Key: 'y', Label: "Yes, once", Value: "y"},
		{Key: 'a', Label: session, Value: "a"},
	}
	if answer, ok := Choose(ColorYellow("Allow?"), choices, 0); ok {
		switch answer {
		case "y":
			request.note("approved once")
			return true
		case "a":
			request.note("approved for session (" + p.GrantScoped(request.Scope).ID + ")")
			return true
		}
		request.note("denied")
		return false
	}

	// Fallback for pipes and dumb terminals: type the answer instead.
	for {
		if p.Input == nil {
			fmt.Println()
			return false
		}
		answer, err := p.Input.ReadLine(ColorYellow("  Allow? [y] once  [a] " + request.Scope.Describe() + " for session  [n] deny: "))
		if err != nil {
			fmt.Println()
			return false
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
			request.note("approved once")
			return true
		case "a", "always", "session":
			request.note("approved for session (" + p.GrantScoped(request.Scope).ID + ")")
			return true
		case "n", "no", "deny", "":
			request.note("denied")
			return false
		default:
			fmt.Println(ColorGray("  Enter y, a, or n."))
		}
	}
}
