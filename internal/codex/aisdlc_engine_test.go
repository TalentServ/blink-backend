package codex

import (
	"strings"
	"testing"

	"github.com/ai-sdlc-framework/ai-sdlc/sdk-go/core"
)

func TestBuildImplementGovernanceAndHardGateBlocks(t *testing.T) {
	bundle, err := BuildImplementGovernance("Demo App", "DEMO-1", "go")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Pipeline == nil || bundle.AgentRole == nil || bundle.QualityGate == nil {
		t.Fatal("missing resources")
	}
	if core.APIVersion != "ai-sdlc.io/v1alpha1" {
		t.Fatalf("unexpected api version %s", core.APIVersion)
	}

	files := map[string]string{
		"src/main.go": "package main",
		".ai-sdlc/pipeline.yaml": "kind: Pipeline",
		"AGENTS.md": "# no",
	}
	env := map[string]any{"summary": "changed too many things", "commitMessage": "feat: x"}
	out := EvaluateHarvestedTurn(bundle, WorkflowImplementStep, "gpt-test", "prompt", "output", files, env, false)
	if out == nil || !out.Blocked {
		t.Fatalf("expected blocked gate, got %#v", out)
	}
}

func TestEvaluateHarvestedTurnPassesCleanProductFiles(t *testing.T) {
	bundle, err := BuildImplementGovernance("Demo App", "DEMO-1", "go")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"src/main.go":      "package main",
		"src/main_test.go": "package main",
	}
	env := map[string]any{"summary": "ok", "commitMessage": "feat: add main"}
	out := EvaluateHarvestedTurn(bundle, WorkflowImplementStep, "gpt-test", "prompt", "output", files, env, false)
	if out == nil || out.Blocked {
		t.Fatalf("expected pass, got %#v", out)
	}
	if out.Provenance == nil || out.Provenance.Tool != "blink-codex-cloud" {
		t.Fatalf("missing provenance: %#v", out.Provenance)
	}
}

func TestPartitionDropsGovernance(t *testing.T) {
	product, blocked := PartitionProductFiles(map[string]string{
		"workspace/repos/backend/src/app.go": "package app",
		".ai-sdlc/pipeline.yaml":             "no",
		"AGENTS.md":                          "no",
		"outputs/blink-result.json":          "{}",
		".env":                               "SECRET=1",
		"certs/key.pem":                      "pem",
	})
	if _, ok := product["src/app.go"]; !ok {
		t.Fatalf("expected product file, got %#v", product)
	}
	if len(blocked) < 4 {
		t.Fatalf("expected governance rejects, got %v", blocked)
	}
	if !IsGovernancePath(".ai-sdlc/pipeline.yaml") || !IsGovernancePath("AGENTS.md") {
		t.Fatal("governance denylist failed")
	}
}

func TestValidateHandoffEnvelope(t *testing.T) {
	if err := ValidateHandoffEnvelope(WorkflowImplementStep, map[string]any{"summary": "x", "commitMessage": "feat"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateHandoffEnvelope(WorkflowPRReview, map[string]any{"summary": "x"}); err == nil {
		t.Fatal("review should require findings")
	}
}

func TestBlinkHarnessOverridesLocalCLI(t *testing.T) {
	out, err := BuildInstructions("", PromptImplementStep, PromptVars{"ISSUE": "DEMO-1", "PROJECT_CONTEXT": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "DEMO-1") || !strings.Contains(out, "blink-result.json") {
		t.Fatalf("missing contract: %s", out)
	}
	if strings.Contains(out, "make ai-sdlc-validate-stage") {
		t.Fatal("harness still asks the agent to run Makefile targets")
	}
	if !strings.Contains(out, "Network access: DISABLED") {
		t.Fatal("missing offline rule")
	}
}
