package canonical

import "strings"

func normalizeAgentCommand(command string) string {
	c := strings.TrimSpace(command)
	c = strings.TrimPrefix(c, "/")
	return strings.ToLower(c)
}
