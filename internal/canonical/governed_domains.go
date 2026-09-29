package canonical

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// GroomingQuestionInput is a Backend-owned question definition. Framework may
// suggest it, but only this service writes it into the canonical record.
type GroomingQuestionInput struct {
	Key            string `json:"key"`
	Prompt         string `json:"prompt"`
	Mandatory      bool   `json:"mandatory"`
	AssignedRoleID string `json:"assignedRoleId,omitempty"`
	SourceDigest   string `json:"sourceDigest,omitempty"`
}

type GroomingReadinessView struct {
	SessionDigest    string   `json:"sessionDigest"`
	Revision         int64    `json:"revision"`
	QuestionCount    int      `json:"questionCount"`
	MandatoryPending int      `json:"mandatoryPending"`
	OpenBlockers     int      `json:"openBlockers"`
	Ready            bool     `json:"ready"`
	Reasons          []string `json:"reasons"`
}

func (s *Service) UpsertGroomingQuestion(ctx context.Context, projectID int64, in GroomingQuestionInput, actor string) (string, error) {
	in.Key = strings.TrimSpace(in.Key)
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Key == "" || in.Prompt == "" {
		return "", fmt.Errorf("question key and prompt are required")
	}
	if in.SourceDigest == "" {
		in.SourceDigest = DigestBytes([]byte(in.Prompt))
	}
	id := uuid.NewString()
	err := s.pool.QueryRow(ctx, `
		INSERT INTO blink_grooming_question
			(id, project_id, question_key, prompt, mandatory, assigned_role_id, source_digest, created_by)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,NULLIF($8,''))
		ON CONFLICT (project_id, question_key) DO UPDATE SET
			prompt=EXCLUDED.prompt, mandatory=EXCLUDED.mandatory,
			assigned_role_id=EXCLUDED.assigned_role_id, source_digest=EXCLUDED.source_digest,
			status='open', closed_at=NULL, updated_at=NOW()
		RETURNING id::text
	`, id, projectID, in.Key, in.Prompt, in.Mandatory, in.AssignedRoleID, in.SourceDigest, actor).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, s.appendGroomingRevision(ctx, projectID, "question-upsert", actor)
}

func (s *Service) RecordGroomingAnswer(ctx context.Context, projectID int64, questionKey, text, status, actor string, evidence map[string]any) (string, error) {
	questionKey, text = strings.TrimSpace(questionKey), strings.TrimSpace(text)
	if questionKey == "" || text == "" {
		return "", fmt.Errorf("question key and answer text are required")
	}
	if status == "" {
		status = "answered"
	}
	var questionID string
	if err := s.pool.QueryRow(ctx, `SELECT id::text FROM blink_grooming_question WHERE project_id=$1 AND question_key=$2`, projectID, questionKey).Scan(&questionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("grooming question not found: %s", questionKey)
		}
		return "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE blink_grooming_answer SET superseded_at=NOW() WHERE question_id=$1 AND superseded_at IS NULL`, questionID); err != nil {
		return "", err
	}
	id := uuid.NewString()
	evidenceRaw, _ := json.Marshal(evidence)
	_, err = tx.Exec(ctx, `
		INSERT INTO blink_grooming_answer
			(id, question_id, project_id, answer_text, answer_status, evidence_json, answer_digest, answered_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''))
	`, id, questionID, projectID, text, status, evidenceRaw, DigestBytes([]byte(text)), actor)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, s.appendGroomingRevision(ctx, projectID, "answer-recorded", actor)
}

func (s *Service) RecordGroomingDecision(ctx context.Context, projectID int64, key string, decision map[string]any, actor string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("decision key is required")
	}
	raw, _ := json.Marshal(decision)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE blink_grooming_decision SET invalidated_at=NOW(), invalidation_reason='superseded' WHERE project_id=$1 AND decision_key=$2 AND invalidated_at IS NULL`, projectID, key); err != nil {
		return "", err
	}
	id := uuid.NewString()
	_, err = tx.Exec(ctx, `
		INSERT INTO blink_grooming_decision (id, project_id, decision_key, decision_json, decision_digest, decided_by)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''))
	`, id, projectID, key, raw, DigestBytes(raw), actor)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, s.appendGroomingRevision(ctx, projectID, "decision-recorded", actor)
}

func (s *Service) SetGroomingContext(ctx context.Context, projectID int64, key string, value map[string]any, inheritedFromProjectID *int64, inheritedRevision *int64, actor string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("context key is required")
	}
	raw, _ := json.Marshal(value)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO blink_grooming_context
			(id, project_id, context_key, context_json, context_digest, inherited_from_project_id, inherited_from_revision, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''))
		ON CONFLICT (project_id, context_key) WHERE invalidated_at IS NULL DO UPDATE SET
			context_json=EXCLUDED.context_json, context_digest=EXCLUDED.context_digest,
			inherited_from_project_id=EXCLUDED.inherited_from_project_id,
			inherited_from_revision=EXCLUDED.inherited_from_revision, created_by=EXCLUDED.created_by
	`, uuid.NewString(), projectID, key, raw, DigestBytes(raw), inheritedFromProjectID, inheritedRevision, actor)
	if err != nil {
		return err
	}
	return s.appendGroomingRevision(ctx, projectID, "context-updated", actor)
}

func (s *Service) SetGroomingBlocker(ctx context.Context, projectID int64, key, severity, message string, details map[string]any) error {
	key, message = strings.TrimSpace(key), strings.TrimSpace(message)
	if key == "" || message == "" {
		return fmt.Errorf("blocker key and message are required")
	}
	if severity == "" {
		severity = "blocking"
	}
	raw, _ := json.Marshal(details)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO blink_grooming_blocker (id, project_id, blocker_key, severity, message, source_digest, details_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (project_id, blocker_key) WHERE resolved_at IS NULL DO UPDATE SET
			severity=EXCLUDED.severity, message=EXCLUDED.message, source_digest=EXCLUDED.source_digest,
			details_json=EXCLUDED.details_json
	`, uuid.NewString(), projectID, key, severity, message, DigestBytes([]byte(message)), raw)
	return err
}

func (s *Service) ResolveGroomingBlocker(ctx context.Context, projectID int64, key string) error {
	_, err := s.pool.Exec(ctx, `UPDATE blink_grooming_blocker SET status='resolved', resolved_at=NOW() WHERE project_id=$1 AND blocker_key=$2 AND resolved_at IS NULL`, projectID, strings.TrimSpace(key))
	return err
}

func (s *Service) GroomingReadiness(ctx context.Context, projectID int64) (*GroomingReadinessView, error) {
	view := &GroomingReadinessView{Reasons: []string{}}
	var confirmed *string
	err := s.pool.QueryRow(ctx, `
		SELECT revision, session_digest, groom_confirmed_digest
		FROM blink_grooming_session WHERE project_id=$1
	`, projectID).Scan(&view.Revision, &view.SessionDigest, &confirmed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("grooming session not initialized")
	}
	if err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM blink_grooming_question WHERE project_id=$1`, projectID).Scan(&view.QuestionCount); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM blink_grooming_question q
		WHERE q.project_id=$1 AND q.mandatory AND NOT EXISTS (
			SELECT 1 FROM blink_grooming_answer a
			WHERE a.question_id=q.id AND a.superseded_at IS NULL
				AND a.answer_status='answered' AND btrim(a.answer_text) <> ''
		)
	`, projectID).Scan(&view.MandatoryPending); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM blink_grooming_blocker WHERE project_id=$1 AND resolved_at IS NULL AND severity='blocking'`, projectID).Scan(&view.OpenBlockers); err != nil {
		return nil, err
	}
	if view.QuestionCount == 0 {
		view.Reasons = append(view.Reasons, "no-questions")
	}
	if view.MandatoryPending > 0 {
		view.Reasons = append(view.Reasons, "mandatory-answers-pending")
	}
	if view.OpenBlockers > 0 {
		view.Reasons = append(view.Reasons, "open-blockers")
	}
	view.Ready = len(view.Reasons) == 0
	return view, nil
}

func (s *Service) appendGroomingRevision(ctx context.Context, projectID int64, reason, actor string) error {
	view, err := s.groomingDigest(ctx, projectID)
	if err != nil {
		return err
	}
	var revision int64
	err = s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(revision_no),0)+1 FROM blink_grooming_revision WHERE project_id=$1`, projectID).Scan(&revision)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(view)
	_, err = s.pool.Exec(ctx, `
		INSERT INTO blink_grooming_revision (project_id, revision_no, reason, content_digest, payload_json, created_by)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''))
	`, projectID, revision, reason, view.SessionDigest, payload, actor)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO blink_grooming_session (project_id, revision, readiness_json, session_digest, updated_at)
		VALUES ($1,$2,$3,$4,NOW())
		ON CONFLICT (project_id) DO UPDATE SET revision=$2, readiness_json=$3, session_digest=$4, updated_at=NOW()
	`, projectID, revision, payload, view.SessionDigest)
	return err
}

func (s *Service) groomingDigest(ctx context.Context, projectID int64) (*GroomingReadinessView, error) {
	var questions, answers, decisions, contexts, blockers []byte
	for _, query := range []struct {
		target *[]byte
		sql    string
	}{
		{&questions, `SELECT COALESCE(jsonb_agg(jsonb_build_object('key',question_key,'digest',source_digest) ORDER BY question_key),'[]'::jsonb) FROM blink_grooming_question WHERE project_id=$1`},
		{&answers, `SELECT COALESCE(jsonb_agg(jsonb_build_object('question',question_id,'digest',answer_digest) ORDER BY question_id),'[]'::jsonb) FROM blink_grooming_answer WHERE project_id=$1 AND superseded_at IS NULL`},
		{&decisions, `SELECT COALESCE(jsonb_agg(jsonb_build_object('key',decision_key,'digest',decision_digest) ORDER BY decision_key),'[]'::jsonb) FROM blink_grooming_decision WHERE project_id=$1 AND invalidated_at IS NULL`},
		{&contexts, `SELECT COALESCE(jsonb_agg(jsonb_build_object('key',context_key,'digest',context_digest) ORDER BY context_key),'[]'::jsonb) FROM blink_grooming_context WHERE project_id=$1 AND invalidated_at IS NULL`},
		{&blockers, `SELECT COALESCE(jsonb_agg(jsonb_build_object('key',blocker_key,'digest',source_digest) ORDER BY blocker_key),'[]'::jsonb) FROM blink_grooming_blocker WHERE project_id=$1 AND resolved_at IS NULL`},
	} {
		if err := s.pool.QueryRow(ctx, query.sql, projectID).Scan(query.target); err != nil {
			return nil, err
		}
	}
	raw, _ := json.Marshal(map[string]json.RawMessage{"questions": questions, "answers": answers, "decisions": decisions, "contexts": contexts, "blockers": blockers})
	view := &GroomingReadinessView{SessionDigest: DigestBytes(raw)}
	return view, nil
}

type GraphProposal struct {
	FrameworkRunID string      `json:"frameworkRunId,omitempty"`
	Requirement    []GraphEdge `json:"requirementEdges"`
	Technical      []GraphEdge `json:"technicalEdges"`
}

func (s *Service) AdoptFrameworkGraph(ctx context.Context, projectID int64, proposal GraphProposal, actor string) (*GraphView, error) {
	effective := combineEdges(proposal.Requirement, proposal.Technical)
	for _, edge := range effective {
		if strings.TrimSpace(edge.From) == "" || strings.TrimSpace(edge.To) == "" {
			return nil, fmt.Errorf("graph edges require from and to")
		}
	}
	cycles := detectCycles(effective)
	if len(cycles) > 0 {
		return nil, fmt.Errorf("cannot adopt framework graph with dependency cycles")
	}
	frontier := computeFrontier(effective)
	order := append([]string(nil), frontier...)
	payload, _ := json.Marshal(map[string]any{"requirement": proposal.Requirement, "technical": proposal.Technical, "effective": effective, "frontier": frontier, "order": order})
	digest := DigestBytes(payload)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
		INSERT INTO blink_dependency_graph
			(project_id,revision,requirement_edges_json,technical_edges_json,effective_edges_json,graph_digest,cycles_json,frontier_json,execution_order_json,source_kind,source_run_id,adopted_at,invalidated_at,invalidation_reason,updated_at)
		VALUES ($1,1,$2,$3,$4,$5,'[]'::jsonb,$6,$7,'framework',$8,NOW(),NULL,NULL,NOW())
		ON CONFLICT (project_id) DO UPDATE SET revision=blink_dependency_graph.revision+1,
			requirement_edges_json=EXCLUDED.requirement_edges_json, technical_edges_json=EXCLUDED.technical_edges_json,
			effective_edges_json=EXCLUDED.effective_edges_json, graph_digest=EXCLUDED.graph_digest,
			cycles_json=EXCLUDED.cycles_json, frontier_json=EXCLUDED.frontier_json, execution_order_json=EXCLUDED.execution_order_json,
			source_kind='framework', source_run_id=EXCLUDED.source_run_id, adopted_at=NOW(), invalidated_at=NULL, invalidation_reason=NULL, updated_at=NOW()
	`, projectID, mustJSON(proposal.Requirement), mustJSON(proposal.Technical), mustJSON(effective), digest, mustJSON(frontier), mustJSON(order), proposal.FrameworkRunID)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO blink_framework_graph_adoption (id,project_id,framework_run_id,proposal_digest,accepted_digest,proposal_json,adopted_by) VALUES ($1,$2,NULLIF($3,''),$4,$4,$5,NULLIF($6,''))`, uuid.NewString(), projectID, proposal.FrameworkRunID, digest, payload, actor)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetGraph(ctx, projectID)
}

func (s *Service) InvalidateFrameworkGraph(ctx context.Context, projectID int64, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE blink_dependency_graph SET invalidated_at=NOW(), invalidation_reason=NULLIF($2,''), updated_at=NOW()
		WHERE project_id=$1 AND source_kind='framework' AND invalidated_at IS NULL
	`, projectID, reason)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE blink_framework_graph_adoption SET invalidated_at=NOW(), invalidation_reason=NULLIF($2,'') WHERE project_id=$1 AND invalidated_at IS NULL`, projectID, reason)
	return err
}

type ArchitectureView struct {
	Revision        int64             `json:"revision"`
	Architecture    json.RawMessage   `json:"architecture"`
	Digest          string            `json:"digest"`
	ConfirmedDigest string            `json:"confirmedDigest,omitempty"`
	InvalidatedAt   *string           `json:"invalidatedAt,omitempty"`
	Pins            []ArchitecturePin `json:"pins"`
}

type ArchitecturePin struct {
	Key    string          `json:"key"`
	Value  json.RawMessage `json:"value"`
	Digest string          `json:"digest"`
}

func (s *Service) GetArchitecture(ctx context.Context, projectID int64) (*ArchitectureView, error) {
	view := &ArchitectureView{Pins: []ArchitecturePin{}}
	var confirmed *string
	var invalidated *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT revision, architecture_json, architecture_digest, confirmed_digest, invalidated_at
		FROM blink_architecture_snapshot WHERE project_id=$1
	`, projectID).Scan(&view.Revision, &view.Architecture, &view.Digest, &confirmed, &invalidated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("architecture not recorded")
	}
	if err != nil {
		return nil, err
	}
	if confirmed != nil {
		view.ConfirmedDigest = *confirmed
	}
	if invalidated != nil {
		at := invalidated.UTC().Format(time.RFC3339)
		view.InvalidatedAt = &at
	}
	rows, err := s.pool.Query(ctx, `
		SELECT pin_key, pin_value_json, pin_digest FROM blink_architecture_pin
		WHERE project_id=$1 AND invalidated_at IS NULL ORDER BY pin_key
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pin ArchitecturePin
		if err := rows.Scan(&pin.Key, &pin.Value, &pin.Digest); err != nil {
			return nil, err
		}
		view.Pins = append(view.Pins, pin)
	}
	return view, rows.Err()
}

func (s *Service) SaveArchitecture(ctx context.Context, projectID int64, architecture map[string]any, actor string) (string, error) {
	raw, _ := json.Marshal(architecture)
	digest := DigestBytes(raw)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO blink_architecture_snapshot (project_id,revision,architecture_json,architecture_digest,updated_at)
		VALUES ($1,1,$2,$3,NOW())
		ON CONFLICT (project_id) DO UPDATE SET revision=blink_architecture_snapshot.revision+1,
			architecture_json=EXCLUDED.architecture_json, architecture_digest=EXCLUDED.architecture_digest,
			confirmed_digest=CASE WHEN blink_architecture_snapshot.architecture_digest=EXCLUDED.architecture_digest THEN blink_architecture_snapshot.confirmed_digest ELSE NULL END,
			confirmed_by=CASE WHEN blink_architecture_snapshot.architecture_digest=EXCLUDED.architecture_digest THEN blink_architecture_snapshot.confirmed_by ELSE NULL END,
			confirmed_at=CASE WHEN blink_architecture_snapshot.architecture_digest=EXCLUDED.architecture_digest THEN blink_architecture_snapshot.confirmed_at ELSE NULL END,
			invalidated_at=NULL, invalidation_reason=NULL, updated_at=NOW()
	`, projectID, raw, digest)
	if err != nil {
		return "", err
	}
	return digest, nil
}

func (s *Service) ConfirmArchitecture(ctx context.Context, projectID int64, digest, actor string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE blink_architecture_snapshot SET confirmed_digest=architecture_digest, confirmed_by=NULLIF($3,''), confirmed_at=NOW(), updated_at=NOW()
		WHERE project_id=$1 AND architecture_digest=$2 AND invalidated_at IS NULL
	`, projectID, digest, actor)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("architecture digest mismatch or architecture is invalidated")
	}
	return s.gates.recordHumanDecision(ctx, projectID, "architecture", digest, actor, map[string]any{"architectureDigest": digest})
}

func (s *Service) PinArchitecture(ctx context.Context, projectID int64, key string, value map[string]any, actor string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("architecture pin key is required")
	}
	raw, _ := json.Marshal(value)
	digest := DigestBytes(raw)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE blink_architecture_pin SET invalidated_at=NOW(), invalidation_reason='superseded' WHERE project_id=$1 AND pin_key=$2 AND invalidated_at IS NULL`, projectID, key); err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO blink_architecture_pin (id,project_id,pin_key,pin_value_json,pin_digest,pinned_by) VALUES ($1,$2,$3,$4,$5,$6)`, uuid.NewString(), projectID, key, raw, digest, actor)
	if err != nil {
		return "", err
	}
	return digest, tx.Commit(ctx)
}

func (s *Service) InvalidateArchitecture(ctx context.Context, projectID int64, reason string) error {
	_, err := s.pool.Exec(ctx, `UPDATE blink_architecture_snapshot SET confirmed_digest=NULL, confirmed_by=NULL, confirmed_at=NULL, invalidated_at=NOW(), invalidation_reason=NULLIF($2,''), updated_at=NOW() WHERE project_id=$1`, projectID, reason)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE blink_architecture_pin SET invalidated_at=NOW(), invalidation_reason=NULLIF($2,'') WHERE project_id=$1 AND invalidated_at IS NULL`, projectID, reason)
	return err
}

type ExecutionScopeView struct {
	ID        string `json:"id"`
	ScopeKind string `json:"scopeKind"`
	ScopeRef  string `json:"scopeRef"`
	Status    string `json:"status"`
}

type ExecutionLeaseView struct {
	ID           string `json:"id"`
	ScopeID      string `json:"scopeId"`
	Holder       string `json:"holder"`
	FencingToken int64  `json:"fencingToken"`
	ExpiresAt    string `json:"expiresAt"`
}

// RequireExecutionScope ensures a caller cannot operate on an opaque scope
// belonging to a different project.
func (s *Service) RequireExecutionScope(ctx context.Context, projectID int64, scopeID string) error {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blink_execution_scope WHERE id=$1 AND project_id=$2)`, scopeID, projectID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("execution scope not found")
	}
	return nil
}

func (s *Service) RequireExecutionLease(ctx context.Context, projectID int64, leaseID string) error {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM blink_execution_lease l
			JOIN blink_execution_scope s ON s.id=l.scope_id
			WHERE l.id=$1 AND s.project_id=$2
		)
	`, leaseID, projectID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("execution lease not found")
	}
	return nil
}

func (s *Service) OpenExecutionScope(ctx context.Context, projectID int64, kind, ref, digest, actor string) (*ExecutionScopeView, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return nil, fmt.Errorf("execution scope kind is required")
	}
	if digest == "" {
		digest = DigestBytes([]byte(kind + ":" + ref))
	}
	id := uuid.NewString()
	var view ExecutionScopeView
	err := s.pool.QueryRow(ctx, `
		INSERT INTO blink_execution_scope (id,project_id,scope_kind,scope_ref,scope_digest,created_by)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''))
		ON CONFLICT (project_id,scope_kind,scope_ref) WHERE status IN ('active','recovering')
			DO UPDATE SET updated_at=NOW()
		RETURNING id::text,scope_kind,scope_ref,status
	`, id, projectID, kind, strings.TrimSpace(ref), digest, actor).Scan(&view.ID, &view.ScopeKind, &view.ScopeRef, &view.Status)
	return &view, err
}

func (s *Service) AcquireExecutionLease(ctx context.Context, scopeID, holder string, ttl time.Duration) (*ExecutionLeaseView, error) {
	if strings.TrimSpace(holder) == "" {
		return nil, fmt.Errorf("lease holder is required")
	}
	if ttl <= 0 || ttl > 24*time.Hour {
		ttl = 15 * time.Minute
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM blink_execution_scope WHERE id=$1 FOR UPDATE`, scopeID).Scan(&status); err != nil {
		return nil, err
	}
	if status != "active" && status != "recovering" {
		return nil, fmt.Errorf("execution scope is not leasable: %s", status)
	}
	if _, err = tx.Exec(ctx, `UPDATE blink_execution_lease SET status='expired', released_at=NOW() WHERE scope_id=$1 AND status='active' AND lease_expires_at <= NOW()`, scopeID); err != nil {
		return nil, err
	}
	var active string
	err = tx.QueryRow(ctx, `SELECT id::text FROM blink_execution_lease WHERE scope_id=$1 AND status='active' FOR UPDATE`, scopeID).Scan(&active)
	if err == nil {
		return nil, fmt.Errorf("execution scope already has an active lease")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	out := &ExecutionLeaseView{ID: uuid.NewString(), ScopeID: scopeID, Holder: holder}
	var expires time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO blink_execution_lease (id,scope_id,holder,lease_expires_at)
		VALUES ($1,$2,$3,NOW()+$4::interval)
		RETURNING fencing_token,lease_expires_at
	`, out.ID, scopeID, holder, ttl.String()).Scan(&out.FencingToken, &expires)
	if err != nil {
		return nil, err
	}
	out.ExpiresAt = expires.UTC().Format(time.RFC3339)
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) RenewExecutionLease(ctx context.Context, leaseID, holder string, fencingToken int64, ttl time.Duration) (*ExecutionLeaseView, error) {
	if ttl <= 0 || ttl > 24*time.Hour {
		ttl = 15 * time.Minute
	}
	var out ExecutionLeaseView
	var expires time.Time
	err := s.pool.QueryRow(ctx, `
		UPDATE blink_execution_lease SET lease_expires_at=NOW()+$4::interval
		WHERE id=$1 AND holder=$2 AND fencing_token=$3 AND status='active' AND lease_expires_at > NOW()
		RETURNING id::text,scope_id::text,holder,fencing_token,lease_expires_at
	`, leaseID, holder, fencingToken, ttl.String()).Scan(&out.ID, &out.ScopeID, &out.Holder, &out.FencingToken, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("lease is stale, expired, or fenced")
	}
	if err != nil {
		return nil, err
	}
	out.ExpiresAt = expires.UTC().Format(time.RFC3339)
	return &out, nil
}

func (s *Service) RecordExecutionEvidence(ctx context.Context, scopeID, leaseID string, fencingToken int64, kind string, evidence map[string]any, actor string) (string, error) {
	if strings.TrimSpace(kind) == "" {
		return "", fmt.Errorf("evidence kind is required")
	}
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM blink_execution_lease
		WHERE id=$1 AND scope_id=$2 AND fencing_token=$3 AND status='active' AND lease_expires_at > NOW()
	`, leaseID, scopeID, fencingToken).Scan(&count)
	if err != nil {
		return "", err
	}
	if count != 1 {
		return "", fmt.Errorf("execution lease is stale, expired, or fenced")
	}
	raw, _ := json.Marshal(evidence)
	id := uuid.NewString()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO blink_execution_evidence (id,scope_id,lease_id,fencing_token,evidence_kind,evidence_json,evidence_digest,recorded_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''))
	`, id, scopeID, leaseID, fencingToken, kind, raw, DigestBytes(raw), actor)
	return id, err
}

func (s *Service) RecoverExecutionScope(ctx context.Context, scopeID, actor, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE blink_execution_scope SET status='recovering', recovery_json=jsonb_build_object('reason',$3,'actor',$2,'at',NOW()), updated_at=NOW()
		WHERE id=$1 AND status IN ('active','recovering')
	`, scopeID, actor, reason)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE blink_execution_lease SET status='recovered', released_at=NOW() WHERE scope_id=$1 AND status='active'`, scopeID)
	return err
}

func (s *Service) ReconcileProviderResource(ctx context.Context, projectID int64, provider, key, desiredDigest, observedDigest, status string, outboxID *int64, details map[string]any) error {
	provider, key = strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(key)
	if provider == "" || key == "" {
		return fmt.Errorf("provider and resource key are required")
	}
	if status == "" {
		status = "pending"
	}
	raw, _ := json.Marshal(details)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO blink_provider_reconciliation
			(project_id,provider,resource_key,desired_digest,observed_digest,status,backend_write,last_outbox_id,details_json,last_reconciled_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,TRUE,$7,$8,NOW(),NOW())
		ON CONFLICT (project_id,provider,resource_key) DO UPDATE SET
			desired_digest=EXCLUDED.desired_digest, observed_digest=EXCLUDED.observed_digest,
			status=EXCLUDED.status, backend_write=TRUE, last_outbox_id=EXCLUDED.last_outbox_id,
			details_json=EXCLUDED.details_json,last_reconciled_at=NOW(),updated_at=NOW()
	`, projectID, provider, key, desiredDigest, observedDigest, status, outboxID, raw)
	return err
}
