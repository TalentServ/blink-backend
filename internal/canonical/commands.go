package canonical

import "strings"

func normalizeAgentCommand(command string) string {
	c := strings.TrimSpace(command)
	c = strings.TrimPrefix(c, "/")
	return strings.ToLower(c)
}

func agentCommandSupported(command string) bool {
	switch normalizeAgentCommand(command) {
	case "classify-work",
		"create-spec",
		"technical-plan",
		"sdlc-start",
		"sdlc-next",
		"confirm-product-scope",
		"confirm-stakeholders",
		"configure-stakeholders",
		"grooming-stakeholder-pack",
		"grooming-revision",
		"grooming-sign-off-capture",
		"propose-designs",
		"implement-step",
		"qa-validation":
		return true
	default:
		return false
	}
}
