package canonical

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type CommandMode string

const (
	CommandModeDeterministic CommandMode = "deterministic"
	CommandModeAgent         CommandMode = "agent"
	CommandModeHybrid        CommandMode = "hybrid"
)

type GateRequirement string

const (
	GateStakeholdersConfirmed GateRequirement = "stakeholders-confirmed"
	GateProductScopeConfirmed GateRequirement = "product-scope-confirmed"
	GateArchitectureConfirmed GateRequirement = "architecture-confirmed"
)

// CommandSpec is the Backend-owned contract for an executable command. Agent
// commands can only return proposals; Backend remains responsible for all
// canonical writes and provider side effects.
type CommandSpec struct {
	ID             string
	Mode           CommandMode
	DomainOwner    string
	MinimumStage   string
	RequiredGates  []GateRequirement
	Timeout        time.Duration
	MaxAttempts    int
	ProviderWrites bool
	ValidateInput  func(json.RawMessage) error
	ValidateOutput func(json.RawMessage) error
}

type CommandRegistry struct {
	specs map[string]CommandSpec
}

func NewCommandRegistry(specs ...CommandSpec) (*CommandRegistry, error) {
	registry := &CommandRegistry{specs: make(map[string]CommandSpec, len(specs))}
	for _, spec := range specs {
		spec.ID = normalizeAgentCommand(spec.ID)
		if spec.ID == "" {
			return nil, fmt.Errorf("command spec id is required")
		}
		if spec.Mode != CommandModeDeterministic && spec.Mode != CommandModeAgent && spec.Mode != CommandModeHybrid {
			return nil, fmt.Errorf("command %q has invalid mode %q", spec.ID, spec.Mode)
		}
		if spec.DomainOwner == "" {
			return nil, fmt.Errorf("command %q has no domain owner", spec.ID)
		}
		if spec.MaxAttempts < 1 {
			return nil, fmt.Errorf("command %q must allow at least one attempt", spec.ID)
		}
		if spec.Mode != CommandModeDeterministic && spec.ProviderWrites {
			return nil, fmt.Errorf("agent command %q cannot enable provider writes", spec.ID)
		}
		if _, exists := registry.specs[spec.ID]; exists {
			return nil, fmt.Errorf("duplicate command spec %q", spec.ID)
		}
		registry.specs[spec.ID] = spec
	}
	return registry, nil
}

func (r *CommandRegistry) Lookup(command string) (CommandSpec, bool) {
	if r == nil {
		return CommandSpec{}, false
	}
	spec, ok := r.specs[normalizeAgentCommand(command)]
	return spec, ok
}

func (r *CommandRegistry) Specs() []CommandSpec {
	if r == nil {
		return nil
	}
	out := make([]CommandSpec, 0, len(r.specs))
	for _, spec := range r.specs {
		out = append(out, spec)
	}
	return out
}

func defaultCommandRegistry() *CommandRegistry {
	objectInput := func(raw json.RawMessage) error {
		if len(raw) == 0 || string(raw) == "null" {
			return nil
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil || body == nil {
			return fmt.Errorf("command payload must be a JSON object")
		}
		return nil
	}
	objectOutput := func(raw json.RawMessage) error {
		var body map[string]any
		if len(raw) == 0 || json.Unmarshal(raw, &body) != nil || body == nil {
			return fmt.Errorf("agent command returned an invalid structured result")
		}
		return nil
	}
	spec := func(id string, mode CommandMode, owner, stage string, gates ...GateRequirement) CommandSpec {
		return CommandSpec{
			ID: id, Mode: mode, DomainOwner: owner, MinimumStage: stage,
			RequiredGates: gates, Timeout: 60 * time.Second, MaxAttempts: 1,
			ProviderWrites: false, ValidateInput: objectInput, ValidateOutput: objectOutput,
		}
	}
	registry, err := NewCommandRegistry(
		spec("refresh-eligibility", CommandModeDeterministic, "delivery", ""),
		spec("sync-wizard-draft", CommandModeDeterministic, "delivery", ""),
		spec("classify-work", CommandModeAgent, "product", "requirements"),
		spec("create-spec", CommandModeAgent, "product", "requirements"),
		spec("technical-plan", CommandModeAgent, "planning", "sdlc-plan", GateProductScopeConfirmed),
		spec("sdlc-start", CommandModeHybrid, "delivery", "requirements", GateProductScopeConfirmed),
		spec("sdlc-next", CommandModeAgent, "delivery", "sdlc-plan", GateProductScopeConfirmed),
		spec("confirm-product-scope", CommandModeHybrid, "product", "requirements", GateStakeholdersConfirmed),
		spec("confirm-stakeholders", CommandModeHybrid, "grooming", "project-stakeholders"),
		spec("configure-stakeholders", CommandModeAgent, "grooming", "project-stakeholders"),
		spec("grooming-stakeholder-pack", CommandModeAgent, "grooming", "stakeholder-qa", GateProductScopeConfirmed),
		spec("grooming-revision", CommandModeAgent, "grooming", "stakeholder-qa", GateProductScopeConfirmed),
		spec("grooming-sign-off-capture", CommandModeAgent, "grooming", "stakeholder-qa", GateProductScopeConfirmed),
		spec("grooming-questions", CommandModeAgent, "grooming", "stakeholder-qa", GateProductScopeConfirmed),
		spec("grooming-analysis", CommandModeAgent, "grooming", "stakeholder-qa", GateProductScopeConfirmed),
		spec("dependency-graph", CommandModeAgent, "planning", "sdlc-plan", GateProductScopeConfirmed),
		spec("propose-designs", CommandModeAgent, "architecture", "project-shape", GateProductScopeConfirmed),
		spec("architecture-proposal", CommandModeAgent, "architecture", "project-shape", GateProductScopeConfirmed),
		spec("implement-step", CommandModeAgent, "delivery", "ship", GateProductScopeConfirmed),
		spec("qa-validation", CommandModeAgent, "qa", "ship", GateProductScopeConfirmed),
	)
	if err != nil {
		panic(err)
	}
	return registry
}

func (s CommandSpec) eligible(eligibility Eligibility) error {
	if s.MinimumStage == "" {
		return nil
	}
	required := stageIndex(strings.TrimSpace(s.MinimumStage))
	if required < 0 {
		return fmt.Errorf("command %q has invalid minimum stage %q", s.ID, s.MinimumStage)
	}
	current := stageIndex(eligibility.CurrentStep)
	if current < required {
		return fmt.Errorf("command %q is not eligible until %s", s.ID, s.MinimumStage)
	}
	return nil
}

func (s *Service) validateCommand(ctx context.Context, projectID int64, req CommandRequest, snap *Snapshot) (CommandSpec, error) {
	spec, ok := s.registry.Lookup(req.Command)
	if !ok {
		return CommandSpec{}, fmt.Errorf("unknown command: %s", strings.TrimSpace(req.Command))
	}
	if req.ExpectedRevision != nil && *req.ExpectedRevision != snap.Revision {
		return CommandSpec{}, fmt.Errorf("stale revision: expected %d have %d", *req.ExpectedRevision, snap.Revision)
	}
	if err := spec.eligible(snap.Eligibility); err != nil {
		return CommandSpec{}, err
	}
	if err := s.gates.Require(ctx, projectID, spec.RequiredGates); err != nil {
		return CommandSpec{}, err
	}
	if spec.ValidateInput != nil {
		if err := spec.ValidateInput(req.Payload); err != nil {
			return CommandSpec{}, err
		}
	}
	return spec, nil
}
