package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nisha-ts-40599/blink-backend/internal/canonical"
)

func (s *Server) bindCanonicalCommandExecutor() {
	if s.canonical != nil {
		s.canonical.SetCommandExecutor(s)
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
		if err == nil {
			s.afterProductScopeConfirm(ctx, projectID, actorEmail, body, raw)
		}
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
	case "implement-step":
		raw, err = s.agent.ImplementStep(ctx, body)
	case "qa-validation":
		raw, err = s.agent.QaValidation(ctx, body)
	default:
		return nil, fmt.Errorf("unsupported agent command: %s", command)
	}
	if err != nil {
		return nil, err
	}
	s.persistAgentOverlaysCtx(ctx, p.ProjectName, projectID, raw)
	inDigest := canonical.DigestBytes(raw)
	_ = s.canonical.RecordAIRun(ctx, projectID, command, "", inDigest, inDigest, correlationID)
	return raw, nil
}

func (s *Server) persistAgentOverlaysCtx(ctx context.Context, projectName string, id int64, raw json.RawMessage) {
	files := overlaysFromAgentJSON(raw)
	if len(files) == 0 {
		return
	}
	pid := id
	_, _ = s.s3.PutOverlayFiles(ctx, projectName, &pid, files)
}
