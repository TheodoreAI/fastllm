package harness

import (
	"fmt"
	"slices"
	"strings"
)

type capabilityPolicy struct {
	write, network, commands, delegate bool
}

func policyForRequest(req RunRequest) capabilityPolicy {
	readOnly := req.PermissionMode == PermissionReadOnly || (req.AgentDepth > 0 && req.PermissionMode == PermissionAsk)
	networkPolicy, err := normalizeNetworkPolicy(req.NetworkPolicy)
	p := capabilityPolicy{
		write:    !readOnly && allowsCapability(req.Capabilities, "write"),
		network:  err == nil && allowsCapability(req.Capabilities, "network") && networkPolicy != "none",
		delegate: !readOnly && allowsCapability(req.Capabilities, "delegate"),
	}
	// Shells are not OS-sandboxed. They can both write files and access the
	// network, so granting them under either restriction would bypass it.
	p.commands = p.write && p.network && allowsCapability(req.Capabilities, "commands")
	return p
}

func canonicalCapability(value string) string {
	switch value = strings.ToLower(strings.TrimSpace(value)); value {
	case "filesystem_write":
		return "write"
	case "network_fetch", "web":
		return "network"
	case "run_command", "shell":
		return "commands"
	case "spawn_agent":
		return "delegate"
	default:
		return value
	}
}

func allowsCapability(caps []string, name string) bool {
	if caps == nil {
		return true
	}
	for _, c := range caps {
		if canonicalCapability(c) == name {
			return true
		}
	}
	return false
}

func attenuateCapabilities(parent, requested []string) []string {
	if requested == nil {
		return slices.Clone(parent)
	}
	if parent == nil {
		return slices.Clone(requested)
	}
	// Preserve an explicit empty slice: nil means unrestricted/inherited.
	result := make([]string, 0)
	for _, name := range []string{"read", "write", "network", "commands", "delegate"} {
		if allowsCapability(parent, name) && allowsCapability(requested, name) {
			result = append(result, name)
		}
	}
	return result
}

func normalizeNetworkPolicy(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "none", "public":
		return value, nil
	default:
		return "", fmt.Errorf("network_policy must be none or public")
	}
}

func attenuateNetworkPolicy(parent, requested string) (string, error) {
	parent, err := normalizeNetworkPolicy(parent)
	if err != nil {
		return "", err
	}
	requested, err = normalizeNetworkPolicy(requested)
	if err != nil {
		return "", err
	}
	if parent == "none" || requested == "" {
		return parent, nil
	}
	return requested, nil
}
