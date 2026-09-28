package canonical

import (
	"context"
	"encoding/json"
)

// CommandExecutor runs Framework agent commands for canonical commands/execute.
type CommandExecutor interface {
	ExecuteAgentCommand(ctx context.Context, projectID int64, command string, payload json.RawMessage, actorEmail, correlationID string) (json.RawMessage, error)
}

func (s *Service) SetCommandExecutor(ex CommandExecutor) {
	s.cmdExec = ex
}
