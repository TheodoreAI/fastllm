package harness

import (
	"bufio"
	"fmt"
	"strings"
)

type PermissionMode string

const (
	PermissionAsk      PermissionMode = "ask"
	PermissionReadOnly PermissionMode = "read-only"
	PermissionAuto     PermissionMode = "auto"
)

func ParsePermissionMode(value string) (PermissionMode, error) {
	switch PermissionMode(strings.ToLower(strings.TrimSpace(value))) {
	case PermissionAsk:
		return PermissionAsk, nil
	case PermissionReadOnly:
		return PermissionReadOnly, nil
	case PermissionAuto:
		return PermissionAuto, nil
	default:
		return "", fmt.Errorf("permissions must be ask, read-only, or auto")
	}
}

type PermissionController struct {
	Mode          PermissionMode
	SessionGrants map[string]bool
	Scanner       *bufio.Scanner
}

func NewPermissionController(mode PermissionMode, scanner *bufio.Scanner) *PermissionController {
	if mode == "" {
		mode = PermissionAsk
	}
	return &PermissionController{Mode: mode, SessionGrants: make(map[string]bool), Scanner: scanner}
}

func (p *PermissionController) SetMode(mode PermissionMode) {
	p.Mode = mode
	p.SessionGrants = make(map[string]bool)
}

func (p *PermissionController) Authorize(toolName, summary string) bool {
	if !requiresPermission(toolName) {
		return true
	}
	switch p.Mode {
	case PermissionAuto:
		return true
	case PermissionReadOnly:
		return false
	case PermissionAsk:
		if p.SessionGrants[toolName] {
			return true
		}
	default:
		return false
	}

	fmt.Println(FormatPermissionPrompt(toolName, summary))
	for {
		fmt.Print(ColorYellow("  Allow? [y] once  [a] this tool for session  [n] deny: "))
		if p.Scanner == nil || !p.Scanner.Scan() {
			fmt.Println()
			return false
		}
		switch strings.ToLower(strings.TrimSpace(p.Scanner.Text())) {
		case "y", "yes":
			return true
		case "a", "always", "session":
			p.SessionGrants[toolName] = true
			return true
		case "n", "no", "deny", "":
			return false
		default:
			fmt.Println(ColorGray("  Enter y, a, or n."))
		}
	}
}

func requiresPermission(toolName string) bool {
	switch toolName {
	case "write_file", "edit_file", "patch_file", "run_command", "kill_process":
		return true
	default:
		return false
	}
}
