package canonical

import (
	"context"
	"encoding/json"
)

// CommandExecutor runs Framework agent commands for canonical commands/execute.
type CommandExecutor interface {
	ExecuteAgentCommand(ctx context.Context, projectID int64, command string, payload json.RawMessage, actorEmail, correlationID string) (json.RawMessage, error)
}

// CommandPostHook owns the Backend-side persistence that follows a successful
// hybrid proposal. It is deliberately separate from the agent executor so an
// agent never receives a canonical-state mutation capability.
type CommandPostHook interface {
	AfterCommand(ctx context.Context, projectID int64, command string, payload, result json.RawMessage, actorEmail, correlationID string) error
}

func (s *Service) SetCommandExecutor(ex CommandExecutor) {
	s.cmdExec = ex
}

func (s *Service) SetCommandPostHook(hook CommandPostHook) {
	s.cmdHook = hook
}
