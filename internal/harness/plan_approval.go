package harness

import (
	"fmt"
	"strings"
)

// planApproval is one answer to a proposed plan. Approving is a user action
// that chooses the mode the plan runs under; submit_plan itself grants nothing.
type planApproval struct {
	Key   rune
	Label string
	Mode  PermissionMode // empty: keep planning
}

var planApprovals = []planApproval{
	{Key: 'n', Label: "Keep planning"},
	{Key: 'a', Label: "Approve " + SymArrowR + " run in Agent mode (ask before each change)", Mode: PermissionAgent},
	{Key: 'e', Label: "Approve " + SymArrowR + " run in Edit mode (edit files, no commands)", Mode: PermissionEdit},
	{Key: 'f', Label: "Approve " + SymArrowR + " run in Full-Access mode", Mode: PermissionFull},
}

// implementPlanTask is the turn that follows an approval.
func implementPlanTask(plan string) string {
	return "Implement the approved plan:\n\n" + strings.TrimSpace(plan)
}

// askPlanApproval is the line-mode approval menu. It returns the chosen mode,
// or "" to keep planning.
func askPlanApproval(input interactiveInput) PermissionMode {
	choices := make([]Choice, 0, len(planApprovals))
	for _, approval := range planApprovals {
		choices = append(choices, Choice{Key: approval.Key, Label: approval.Label, Value: string(approval.Mode)})
	}
	if answer, ok := Choose(ColorCyan("Plan ready. Carry it out?"), choices, 0); ok {
		return PermissionMode(answer)
	}
	if input == nil {
		return ""
	}
	answer, err := input.ReadLine(ColorCyan("  Plan ready. [a] agent  [e] edit  [f] full-access  [n] keep planning: "))
	if err != nil {
		return ""
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	for _, approval := range planApprovals {
		if answer == string(approval.Key) {
			return approval.Mode
		}
	}
	fmt.Println(ColorGray("  Kept planning."))
	return ""
}
