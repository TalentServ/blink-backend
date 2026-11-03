package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/ai-sdlc-framework/ai-sdlc/sdk-go/audit"
	"github.com/ai-sdlc-framework/ai-sdlc/sdk-go/builders"
	"github.com/ai-sdlc-framework/ai-sdlc/sdk-go/core"
	"github.com/ai-sdlc-framework/ai-sdlc/sdk-go/policy"
)

const (
	maxFilesPerChange     = 25
	metricFilesChanged    = "files-changed"
	metricBlockedPaths    = "blocked-paths"
	metricTestsPresent    = "tests-present"
	metricMajorFindings   = "critical-or-major-findings"
	metricHumanMerge      = "human-merge-approval"
)

// GovernanceBundle is the sdk-go resource set for one Codex turn.
type GovernanceBundle struct {
	Pipeline    *core.Pipeline
	AgentRole   *core.AgentRole
	QualityGate *core.QualityGate
}

// GateOutcome is the post-harvest enforcement result.
type GateOutcome struct {
	Passed     bool
	Blocked    bool
	Reason     string
	Results    []policy.GateResult
	Provenance *core.ProvenanceRecord
	Audit      []*audit.AuditEntry
}

func intPtr(v int) *int          { return &v }
func boolPtr(v bool) *bool       { return &v }
func floatPtr(v float64) *float64 { return &v }

// BuildImplementGovernance constructs implement/follow-up/fix resources.
func BuildImplementGovernance(projectName, issueID, techStack string) (*GovernanceBundle, error) {
	return buildGovernance(projectName, issueID, techStack, false)
}

// BuildReviewGovernance constructs review / merge-readiness resources.
func BuildReviewGovernance(projectName, issueID, techStack string) (*GovernanceBundle, error) {
	return buildGovernance(projectName, issueID, techStack, true)
}

func buildGovernance(projectName, issueID, techStack string, review bool) (*GovernanceBundle, error) {
	roleName := "implement-agent"
	roleTitle := "implement"
	goal := "Implement the approved story as product code and tests inside the staged repo. Do not push, open PRs, or write governance files into the repo."
	if review {
		roleName = "review-agent"
		roleTitle = "review"
		goal = "Review the staged PR diff for code, test, and security issues. Advisory only. Never claim human merge approval."
	}

	pipe, err := builders.NewPipelineBuilder("blink-" + roleTitle).
		Label("project", sanitizeResourceName(projectName)).
		Label("issue", sanitizeResourceName(issueID)).
		AddTrigger("blink.codex."+roleTitle, nil).
		AddProvider("github", "github", map[string]any{"owner": "blink"}).
		AddStage(roleTitle, roleName, &builders.StageOptions{
			QualityGates: []string{"blink-" + roleTitle + "-gates"},
			Timeout:      "10m",
		}).
		Build()
	if err != nil {
		return nil, err
	}

	role, err := builders.NewAgentRoleBuilder(roleName).
		Role(roleTitle).
		Goal(goal).
		Backstory("Blink Codex Cloud hosted sandbox. Offline. blink-backend is the control plane.").
		AddTool("edit").
		AddTool("read").
		AddTool("test").
		WithConstraints(&core.Constraints{
			MaxFilesPerChange: intPtr(maxFilesPerChange),
			RequireTests:      boolPtr(true),
			BlockedPaths:      []string{".env*", "*.pem", ".ai-sdlc/**", "AGENTS.md", "capabilities/**"},
		}).
		AddHandoff("blink-backend", "turn-complete", &core.HandoffContract{
			Schema:         "blink-result.json",
			RequiredFields: []string{"summary"},
		}).
		AddSkill("ai-sdlc-governance", "Official AI-SDLC governance skill", []string{"governance"}).
		Build()
	if err != nil {
		return nil, err
	}

	qg := builders.NewQualityGateBuilder("blink-" + roleTitle + "-gates")
	qg.AddGate("files-within-budget", core.EnforcementHardMandatory, core.GateRule{
		Metric: metricFilesChanged, Operator: "<=", Threshold: floatPtr(maxFilesPerChange),
	}, nil)
	qg.AddGate("no-blocked-paths", core.EnforcementHardMandatory, core.GateRule{
		Metric: metricBlockedPaths, Operator: "==", Threshold: floatPtr(0),
	}, nil)
	qg.AddGate("provenance-recorded", core.EnforcementHardMandatory, core.GateRule{
		RequireAttribution: boolPtr(true),
	}, nil)
	if review {
		qg.AddGate("review-findings", core.EnforcementSoftMandatory, core.GateRule{
			Metric: metricMajorFindings, Operator: "==", Threshold: floatPtr(0),
		}, nil)
	} else {
		qg.AddGate("tests-present", core.EnforcementSoftMandatory, core.GateRule{
			Metric: metricTestsPresent, Operator: ">=", Threshold: floatPtr(1),
		}, nil)
	}
	quality, err := qg.Build()
	if err != nil {
		return nil, err
	}

	bundle := &GovernanceBundle{Pipeline: pipe, AgentRole: role, QualityGate: quality}
	if err := admitBundle(bundle); err != nil {
		return nil, err
	}
	_ = techStack
	return bundle, nil
}

func admitBundle(b *GovernanceBundle) error {
	pipe := policy.NewAdmissionPipeline().WithValidators(func(r core.AnyResource) error {
		if r == nil || r.GetMetadata() == nil || strings.TrimSpace(r.GetMetadata().Name) == "" {
			return fmt.Errorf("%s missing name", r.GetKind())
		}
		_ = core.Validate(r.GetKind(), asMap(r))
		return nil
	})
	for _, res := range []core.AnyResource{b.Pipeline, b.AgentRole, b.QualityGate} {
		resp := pipe.AdmitResource(context.Background(), &policy.AdmissionRequest{
			Resource:  res,
			Operation: "create",
		})
		if resp == nil || resp.Allowed {
			continue
		}
		return fmt.Errorf("admit %s: %s", res.GetKind(), strings.Join(resp.Reasons, "; "))
	}
	return nil
}

func asMap(v any) map[string]any {
	raw, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// ValidateHandoffEnvelope checks the Blink/official JSON return envelope.
func ValidateHandoffEnvelope(kind WorkflowKind, env map[string]any) error {
	if env == nil {
		return fmt.Errorf("handoff envelope is empty")
	}
	if strings.TrimSpace(stringField(env, "summary")) == "" {
		return fmt.Errorf("handoff envelope missing summary")
	}
	switch kind {
	case WorkflowPRReview:
		if _, ok := env["findings"]; !ok {
			return fmt.Errorf("review envelope missing findings")
		}
	default:
		if strings.TrimSpace(stringField(env, "commitMessage")) == "" && len(fileEntries(env)) == 0 {
			return fmt.Errorf("implement envelope missing commitMessage and files")
		}
	}
	return nil
}

// EvaluateHarvestedTurn fills sdk-go EvaluationContext from observed artifacts and enforces gates.
func EvaluateHarvestedTurn(bundle *GovernanceBundle, kind WorkflowKind, model, prompt, output string, files map[string]string, envelope map[string]any, humanMergeApproved bool) *GateOutcome {
	product, blocked := PartitionProductFiles(files)
	tests := 0
	for p := range product {
		if isTestPath(p) {
			tests++
		}
	}
	major := countMajorFindings(envelope)
	prov := core.CreateProvenance(first(model, "gpt-6-luna"), "blink-codex-cloud", hashPrompt(prompt), core.WithReviewDecision(core.ReviewPending))
	provMap := map[string]string{
		"model":          prov.Model,
		"tool":           prov.Tool,
		"promptHash":     prov.PromptHash,
		"timestamp":      prov.Timestamp,
		"reviewDecision": string(prov.ReviewDecision),
	}
	human := 0.0
	if humanMergeApproved {
		human = 1
	}
	eval := &policy.EvaluationContext{
		Metrics: map[string]float64{
			metricFilesChanged:  float64(len(product)),
			metricBlockedPaths:  float64(len(blocked)),
			metricTestsPresent:  float64(tests),
			metricMajorFindings: float64(major),
			metricHumanMerge:    human,
		},
		AIAuthored: true,
		Provenance: provMap,
	}
	enforced := policy.Enforce(context.Background(), bundle.QualityGate, eval)
	sink := audit.NewInMemoryAuditSink()
	log := audit.NewAuditLog(sink)
	outcome := "passed"
	if enforced == nil || !enforced.Passed {
		outcome = "blocked"
	}
	_ = log.Log("codex-turn", "QualityGate", bundle.QualityGate.Metadata.Name, "blink-backend", outcome, map[string]any{
		"issueKind": string(kind),
		"output":    truncate(output, 240),
		"files":     len(product),
		"blocked":   len(blocked),
	})

	reason := ""
	if enforced != nil {
		for _, r := range enforced.Results {
			if !r.Passed && !r.Overridden {
				reason = first(reason, r.GateName+": "+r.Message)
			}
		}
	}
	passed := enforced != nil && enforced.Passed
	results := []policy.GateResult{}
	if enforced != nil {
		results = enforced.Results
	}
	return &GateOutcome{
		Passed:     passed,
		Blocked:    !passed,
		Reason:     first(reason, "quality gate failed"),
		Results:    results,
		Provenance: prov,
		Audit:      sink.Entries,
	}
}

func hashPrompt(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func countMajorFindings(env map[string]any) int {
	n := 0
	for _, f := range extractEnvelopeFindings(env) {
		sev := strings.ToLower(stringField(f, "severity"))
		if sev == "critical" || sev == "major" || sev == "must-fix" {
			n++
		}
	}
	return n
}

func extractEnvelopeFindings(env map[string]any) []map[string]any {
	out := []map[string]any{}
	if env == nil {
		return out
	}
	arr, _ := env["findings"].([]any)
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func fileEntries(env map[string]any) []map[string]any {
	out := []map[string]any{}
	if env == nil {
		return out
	}
	arr, _ := env["files"].([]any)
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func isTestPath(p string) bool {
	lower := strings.ToLower(p)
	base := path.Base(lower)
	return strings.Contains(lower, "/test/") ||
		strings.Contains(lower, "/tests/") ||
		strings.HasSuffix(base, "_test.go") ||
		strings.HasSuffix(base, ".test.ts") ||
		strings.HasSuffix(base, ".test.tsx") ||
		strings.HasSuffix(base, ".spec.ts") ||
		strings.HasSuffix(base, ".spec.tsx") ||
		strings.HasPrefix(base, "test_")
}

func sanitizeResourceName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r == '_' || r == ' ' || r == '/' {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "blink"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

func gateResultsJSON(results []policy.GateResult) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, r := range results {
		out = append(out, map[string]any{
			"gateName":    r.GateName,
			"passed":      r.Passed,
			"enforcement": string(r.Enforcement),
			"message":     r.Message,
			"overridden":  r.Overridden,
		})
	}
	return out
}
