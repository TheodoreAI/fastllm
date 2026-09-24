package harness

import (
	"fmt"
	"strings"
	"time"
)

func (p *PermissionController) HandleCommand(args []string) string {
	action := "list"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}
	switch action {
	case "clear", "reset":
		p.ClearGrants()
		return "Cleared all session capability grants."
	case "revoke":
		if len(args) != 2 {
			return "Usage: /permissions revoke <grant-id|tool-name>"
		}
		if n := p.RevokeGrant(args[1]); n > 0 {
			return fmt.Sprintf("Revoked %d capability grant(s) for %q.", n, args[1])
		}
		return fmt.Sprintf("No active capability grant found matching %q.", args[1])
	case "list":
		var out strings.Builder
		fmt.Fprintf(&out, "Session Capabilities & Permissions:\nmode: %s\nworkspace: %s\n", p.Mode, p.Workspace)
		active := p.ActiveGrants()
		if len(active) == 0 {
			out.WriteString("No active capability grants for this session.")
		} else {
			for _, g := range active {
				fmt.Fprintf(&out, "  %s  %s  (%s ago)\n", g.ID, sanitizeUntrusted(g.Scope.Describe()), time.Since(g.GrantedAt).Truncate(time.Second))
			}
			out.WriteString("Use /permissions revoke <id|tool> or /permissions clear to revoke grants.")
		}
		return out.String()
	default:
		return "Usage: /permissions [list|revoke <grant-id|tool-name>|clear]"
	}
}

// Only the UI goroutine accesses the controller. Workers request permission
// through permissionChan, so grant inspection and revocation stay serialized.
func (m *teaModel) permissionController() *PermissionController {
	if m.permissions == nil {
		m.permissions = NewPermissionController(m.permissionMode, nil)
		m.permissions.SetWorkspace(m.workingDir)
	}
	return m.permissions
}
