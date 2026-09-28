package httpapi

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nisha-ts-40599/blink-backend/internal/canonical"
)

func agentStringField(raw json.RawMessage, key string) string {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return ""
	}
	v, _ := root[key].(string)
	return strings.TrimSpace(v)
}

func (s *Server) afterStakeholderConfirm(ctx context.Context, projectID int64, actor string, stakes []map[string]string, raw json.RawMessage) {
	if s.canonical == nil {
		return
	}
	digest := agentStringField(raw, "confirmationDigest")
	assignmentsDigest := canonical.DigestJSON(stakes)
	if digest == "" {
		digest = assignmentsDigest
	}
	_ = assignmentsDigest
	_ = s.canonical.RecordStakeholderConfirmation(ctx, projectID, stakes, digest, actor)
	_ = s.canonical.RefreshEligibility(ctx, projectID, actor)
}

func (s *Server) afterProductScopeConfirm(ctx context.Context, projectID int64, actor string, body map[string]any, raw json.RawMessage) {
	if s.canonical == nil {
		return
	}
	confirmed := agentStringField(raw, "confirmationDigest")
	expected, _ := body["expectedDigest"].(string)
	source, _ := body["sourceDigest"].(string)
	if source == "" {
		source = expected
	}
	scope := expected
	if scope == "" {
		scope = canonical.DigestJSON(body["productScope"])
	}
	if confirmed == "" {
		confirmed = scope
	}
	_ = s.canonical.RecordProductScopeConfirmation(ctx, projectID, source, scope, confirmed, actor, "")
	_ = s.canonical.RefreshEligibility(ctx, projectID, actor)
}

func (s *Server) afterSdlcStart(ctx context.Context, projectID int64, raw json.RawMessage) {
	if s.canonical == nil {
		return
	}
	issue := agentStringField(raw, "issueId")
	if issue == "" {
		issue = agentStringField(raw, "issueKey")
	}
	if issue != "" {
		_ = s.canonical.RecordSDLStartIssue(ctx, projectID, issue)
	}
}
