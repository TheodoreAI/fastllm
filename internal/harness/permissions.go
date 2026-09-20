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
	Input         interactiveInput
}

func NewPermissionController(mode PermissionMode, source any) *PermissionController {
	if mode == "" {
		mode = PermissionAsk
	}
	var input interactiveInput
	switch value := source.(type) {
	case interactiveInput:
		input = value
	case *bufio.Scanner:
		input = &scannerInput{scanner: value}
	}
	return &PermissionController{Mode: mode, SessionGrants: make(map[string]bool), Input: input}
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

	// Preferred path: an inline menu where enter accepts the highlighted option.
	// Deny is highlighted first so an accidental enter is never destructive.
	choices := []Choice{
		{Key: 'n', Label: "No", Value: "n"},
		{Key: 'y', Label: "Yes, once", Value: "y"},
		{Key: 'a', Label: "Yes, all this session", Value: "a"},
	}
	if answer, ok := Choose(ColorYellow("Allow?"), choices, 0); ok {
		switch answer {
		case "y":
			return true
		case "a":
			p.SessionGrants[toolName] = true
			return true
		}
		return false
	}

	// Fallback for pipes and dumb terminals: type the answer instead.
	for {
		if p.Input == nil {
			fmt.Println()
			return false
		}
		answer, err := p.Input.ReadLine(ColorYellow("  Allow? [y] once  [a] this tool for session  [n] deny: "))
		if err != nil {
			fmt.Println()
			return false
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
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
