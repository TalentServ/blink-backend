package canonical

import (
	"context"
	"encoding/json"
	"testing"
)

func TestDefaultCommandRegistryIsCompleteAndFailsClosed(t *testing.T) {
	registry := defaultCommandRegistry()
	for _, command := range []string{
		"refresh-eligibility", "sync-wizard-draft", "classify-work", "create-spec",
		"technical-plan", "sdlc-start", "sdlc-next", "confirm-product-scope",
		"confirm-stakeholders", "configure-stakeholders", "grooming-stakeholder-pack",
		"grooming-revision", "grooming-sign-off-capture", "propose-designs",
		"grooming-questions", "grooming-analysis", "dependency-graph",
		"architecture-proposal", "confirm-topology", "implement-step", "qa-validation",
	} {
		if _, ok := registry.Lookup(command); !ok {
			t.Fatalf("command %q is missing from the registry", command)
		}
	}
	if _, ok := registry.Lookup("not-a-command"); ok {
		t.Fatal("unknown command must not be resolved")
	}
}

func TestGovernedProposalCommandsRemainAgentOnly(t *testing.T) {
	registry := defaultCommandRegistry()
	for _, command := range []string{
		"grooming-questions", "grooming-analysis", "dependency-graph", "architecture-proposal",
	} {
		spec, ok := registry.Lookup(command)
		if !ok {
			t.Fatalf("%s is not registered", command)
		}
		if spec.Mode != CommandModeAgent || spec.ProviderWrites {
			t.Fatalf("%s must return a proposal without provider write authority", command)
		}
	}
}

func TestCommandRegistryIsolatesAgentModesFromProviders(t *testing.T) {
	if _, err := NewCommandRegistry(CommandSpec{
		ID: "unsafe-agent", Mode: CommandModeAgent, DomainOwner: "qa",
		MaxAttempts: 1, ProviderWrites: true,
	}); err == nil {
		t.Fatal("agent specs must reject provider write authority")
	}

	registry := defaultCommandRegistry()
	for _, spec := range registry.Specs() {
		if (spec.Mode == CommandModeAgent || spec.Mode == CommandModeHybrid) && spec.ProviderWrites {
			t.Fatalf("%s permits provider writes in %s mode", spec.ID, spec.Mode)
		}
	}
}

func TestCommandSpecEnforcesEligibilityAndStructuredPayloads(t *testing.T) {
	registry := defaultCommandRegistry()
	spec, ok := registry.Lookup("confirm-product-scope")
	if !ok {
		t.Fatal("confirm-product-scope spec missing")
	}
	if err := spec.eligible(Eligibility{CurrentStep: "project-stakeholders"}); err == nil {
		t.Fatal("product scope confirmation must reject an ineligible stage")
	}
	if err := spec.eligible(Eligibility{CurrentStep: "requirements"}); err != nil {
		t.Fatalf("requirements stage should be eligible: %v", err)
	}
	if err := spec.ValidateInput(json.RawMessage(`[]`)); err == nil {
		t.Fatal("array payload must be rejected")
	}
	if err := spec.ValidateOutput(json.RawMessage(`{"confirmationDigest":"abc"}`)); err != nil {
		t.Fatalf("structured output should be accepted: %v", err)
	}
	if err := spec.ValidateOutput(json.RawMessage(`[]`)); err == nil {
		t.Fatal("array agent output must be rejected")
	}
}

func TestServiceValidationFailsClosedBeforeAnyMutation(t *testing.T) {
	service := &Service{registry: defaultCommandRegistry()}
	snapshot := &Snapshot{Revision: 4, Eligibility: Eligibility{CurrentStep: "requirements"}}
	if _, err := service.validateCommand(context.Background(), 1, CommandRequest{Command: "unknown-command"}, snapshot); err == nil {
		t.Fatal("unknown command must be rejected before a command run is created")
	}
	expected := int64(3)
	if _, err := service.validateCommand(context.Background(), 1, CommandRequest{
		Command: "classify-work", ExpectedRevision: &expected,
	}, snapshot); err == nil {
		t.Fatal("stale revision must be rejected before a command run is created")
	}
	// Catch-up commands ignore ExpectedRevision so wizard autosave races do not block.
	if _, err := service.validateCommand(context.Background(), 1, CommandRequest{
		Command: "sync-wizard-draft", ExpectedRevision: &expected,
	}, snapshot); err != nil {
		t.Fatalf("sync-wizard-draft must ignore stale expectedRevision: %v", err)
	}
}
