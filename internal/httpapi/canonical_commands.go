package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func (s *Server) bindCanonicalCommandExecutor() {
	if s.canonical != nil {
		s.canonical.SetCommandExecutor(s)
		s.canonical.SetCommandPostHook(s)
	}
}

func (s *Server) ExecuteAgentCommand(ctx context.Context, projectID int64, command string, payload json.RawMessage, actorEmail, correlationID string) (json.RawMessage, error) {
	if s.agent == nil {
		return nil, fmt.Errorf("agent runtime not configured")
	}
	p, err := s.proj.RequireOwned(ctx, projectID, actorEmail)
	if err != nil {
		return nil, err
	}
	pid := projectID
	s.s3.EnsureProvisioned(ctx, p.ProjectName, &pid)

	body := map[string]any{}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &body)
	}
	if body == nil {
		body = map[string]any{}
	}
	body["projectName"] = p.ProjectName
	body["projectId"] = strconv.FormatInt(projectID, 10)
	// The runtime is a proposal engine. It is never granted provider credentials
	// or canonical mutation authority; Backend applies any accepted proposal.
	body["providerWrites"] = false
	body["canonicalMutation"] = false
	if _, ok := body["actor"]; !ok || strings.TrimSpace(fmt.Sprint(body["actor"])) == "" {
		body["actor"] = actorEmail
	}

	var raw json.RawMessage
	switch command {
	case "classify-work":
		raw, err = s.agent.ClassifyWork(ctx, body)
	case "create-spec":
		raw, err = s.agent.CreateSpec(ctx, body)
	case "technical-plan":
		raw, err = s.agent.TechnicalPlan(ctx, body)
	case "sdlc-start":
		raw, err = s.agent.SdlcStart(ctx, body)
	case "sdlc-next":
		raw, err = s.agent.SdlcNext(ctx, body)
	case "confirm-product-scope":
		raw, err = s.agent.ConfirmProductScope(ctx, body)
	case "confirm-stakeholders":
		raw, err = s.agent.ConfirmStakeholders(ctx, body)
	case "configure-stakeholders":
		stakes := make([]map[string]string, 0, len(p.Stakeholders))
		for _, st := range p.Stakeholders {
			stakes = append(stakes, map[string]string{"role_id": st.RoleCode, "name": st.Name, "email": st.Email})
		}
		raw, err = s.agent.ConfigureStakeholders(ctx, p.ProjectName, strconv.FormatInt(projectID, 10), stakes)
	case "propose-designs":
		raw, err = s.agent.ProposeDesigns(ctx, body)
	case "grooming-stakeholder-pack":
		raw, err = s.agent.GroomingStakeholderPack(ctx, body)
	case "grooming-revision":
		raw, err = s.agent.GroomingRevision(ctx, body)
	case "grooming-sign-off-capture":
		raw, err = s.agent.GroomingSignOffCapture(ctx, body)
	case "grooming-questions":
		raw, err = s.agent.GroomingQuestions(ctx, body)
	case "grooming-analysis":
		raw, err = s.agent.GroomingAnalysis(ctx, body)
	case "dependency-graph":
		raw, err = s.agent.DependencyGraph(ctx, body)
	case "implement-step":
		raw, err = s.agent.ImplementStep(ctx, body)
	case "qa-validation":
		raw, err = s.agent.QaValidation(ctx, body)
	case "architecture-proposal":
		raw, err = s.agent.ArchitectureProposal(ctx, body)
	case "confirm-topology":
		raw, err = s.agent.ConfirmTopology(ctx, body)
	default:
		return nil, fmt.Errorf("unsupported agent command: %s", command)
	}
	if err != nil {
		return nil, err
	}
	s.persistAgentOverlaysCtx(ctx, p.ProjectName, projectID, raw)
	return raw, nil
}

// AfterCommand is invoked by the canonical service only after a hybrid agent
// proposal passes structured-output validation and its revision is rechecked.
// This keeps confirmation side effects identical for the canonical facade and
// the older compatibility routes.
func (s *Server) AfterCommand(ctx context.Context, projectID int64, command string, payload, result json.RawMessage, actorEmail, correlationID string) error {
	var body map[string]any
	if len(payload) > 0 && json.Unmarshal(payload, &body) != nil {
		return fmt.Errorf("invalid command payload")
	}
	switch command {
	case "confirm-stakeholders":
		stakeholders, err := stakeholderAssignments(body["stakeholders"])
		if err != nil {
			return err
		}
		return s.afterStakeholderConfirm(ctx, projectID, actorEmail, stakeholders, result)
	case "confirm-product-scope":
		return s.afterProductScopeConfirm(ctx, projectID, actorEmail, body, result)
	case "confirm-topology":
		return s.afterTopologyConfirm(ctx, projectID, actorEmail, body, result)
	case "sdlc-start":
		return s.afterSdlcStart(ctx, projectID, result)
	default:
		return nil
	}
}

func stakeholderAssignments(value any) ([]map[string]string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var stakeholders []map[string]string
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &stakeholders) != nil {
		return nil, fmt.Errorf("confirm-stakeholders requires stakeholder assignments")
	}
	return stakeholders, nil
}

func (s *Server) persistAgentOverlaysCtx(ctx context.Context, projectName string, id int64, raw json.RawMessage) {
	files := overlaysFromAgentJSON(raw)
	if len(files) == 0 {
		return
	}
	pid := id
	_, _ = s.s3.PutOverlayFiles(ctx, projectName, &pid, files)
}
