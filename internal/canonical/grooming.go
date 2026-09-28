package canonical

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

type GroomingSnapshot struct {
	QuestionCount      int  `json:"questionCount"`
	MandatoryPending   int  `json:"mandatoryPending"`
	AnsweredMandatory  int  `json:"answeredMandatory"`
	GroomAcknowledged  bool `json:"groomAcknowledged"`
	ReadyForGGroom     bool `json:"readyForGGroom"`
}

func DeriveGroomingReadiness(wizardState []byte) GroomingSnapshot {
	var root map[string]any
	if json.Unmarshal(wizardState, &root) != nil {
		return GroomingSnapshot{}
	}
	questions, _ := root["questions"].([]any)
	responses, _ := root["responses"].([]any)
	ack, _ := root["groomAcknowledged"].(bool)

	respByQ := map[string]map[string]any{}
	for _, item := range responses {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		qid, _ := m["questionId"].(string)
		if qid != "" {
			respByQ[qid] = m
		}
	}

	mandatoryPending := 0
	answeredMandatory := 0
	for _, item := range questions {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		mandatory, _ := m["mandatory"].(bool)
		if !mandatory {
			continue
		}
		qid, _ := m["id"].(string)
		resp := respByQ[qid]
		status, _ := resp["status"].(string)
		text, _ := resp["response"].(string)
		if status == "answered" && text != "" {
			answeredMandatory++
		} else {
			mandatoryPending++
		}
	}

	ready := mandatoryPending == 0 && len(questions) > 0
	return GroomingSnapshot{
		QuestionCount:     len(questions),
		MandatoryPending:  mandatoryPending,
		AnsweredMandatory: answeredMandatory,
		GroomAcknowledged: ack,
		ReadyForGGroom:    ready,
	}
}

func (s *Service) SyncGroomingSession(ctx context.Context, projectID int64, wizardState []byte) error {
	readiness := DeriveGroomingReadiness(wizardState)
	session := map[string]any{"readiness": readiness}
	sessionRaw, _ := json.Marshal(session)
	digest := DigestBytes(wizardState)
	readinessRaw, _ := json.Marshal(readiness)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO blink_grooming_session (project_id, revision, session_json, readiness_json, session_digest, updated_at)
		VALUES ($1, 1, $2, $3, $4, NOW())
		ON CONFLICT (project_id) DO UPDATE SET
			revision = blink_grooming_session.revision + 1,
			session_json = EXCLUDED.session_json,
			readiness_json = EXCLUDED.readiness_json,
			session_digest = EXCLUDED.session_digest,
			updated_at = NOW()
	`, projectID, sessionRaw, readinessRaw, digest)
	return err
}

func (s *Service) AppendRequirementRevision(ctx context.Context, projectID int64, viewKind, contentDigest string, payload map[string]any) error {
	var prevDigest string
	err := s.pool.QueryRow(ctx, `
		SELECT content_digest FROM blink_requirement_revision
		WHERE project_id=$1 AND view_kind=$2
		ORDER BY id DESC LIMIT 1
	`, projectID, viewKind).Scan(&prevDigest)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && prevDigest == contentDigest {
		return nil
	}
	var next int
	_ = s.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(revision_no), 0) + 1 FROM blink_requirement_revision WHERE project_id=$1 AND view_kind=$2
	`, projectID, viewKind).Scan(&next)
	raw, _ := json.Marshal(payload)
	_, err = s.pool.Exec(ctx, `
		INSERT INTO blink_requirement_revision (project_id, revision_no, view_kind, content_digest, payload_json)
		VALUES ($1,$2,$3,$4,$5)
	`, projectID, next, viewKind, contentDigest, raw)
	return err
}
