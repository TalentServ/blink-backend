package canonical

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool    *pgxpool.Pool
	gates   *Gates
	cmdExec CommandExecutor
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, gates: NewGates(pool)}
}

type Snapshot struct {
	ProjectID         int64           `json:"projectId"`
	Revision          int64           `json:"revision"`
	WizardDigest      string          `json:"wizardDigest"`
	StageSpine        string          `json:"stageSpine"`
	Eligibility       Eligibility     `json:"eligibility"`
	Blockers          json.RawMessage `json:"blockers"`
	Metadata          json.RawMessage `json:"metadata"`
	MigratedFromWizard bool           `json:"migratedFromWizard"`
	UpdatedAt         string          `json:"updatedAt"`
}

type DomainEvent struct {
	ID            int64           `json:"id"`
	Revision      int64           `json:"revision"`
	EventType     string          `json:"eventType"`
	Payload       json.RawMessage `json:"payload"`
	ActorEmail    *string         `json:"actorEmail,omitempty"`
	CorrelationID *string         `json:"correlationId,omitempty"`
	CreatedAt     string          `json:"createdAt"`
}

type ProjectionRow struct {
	ID         int64           `json:"id"`
	Provider   string          `json:"provider"`
	ActionType string          `json:"actionType"`
	Status     string          `json:"status"`
	Attempts   int             `json:"attempts"`
	LastError  *string         `json:"lastError,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	UpdatedAt  string          `json:"updatedAt"`
}

type CommandRequest struct {
	Command          string          `json:"command"`
	ExpectedRevision *int64          `json:"expectedRevision"`
	IdempotencyKey   string          `json:"idempotencyKey"`
	Payload          json.RawMessage `json:"payload"`
}

type CommandResult struct {
	RunID      string          `json:"runId"`
	Status     string          `json:"status"`
	Revision   int64           `json:"revision"`
	Preview    json.RawMessage `json:"preview,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	AgentRoute string          `json:"agentRoute,omitempty"`
}

func (s *Service) Snapshot(ctx context.Context, projectID int64, actorEmail string) (*Snapshot, error) {
	if err := s.ensureAggregate(ctx, projectID, actorEmail); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `
		SELECT revision, wizard_digest, stage_spine, eligibility_json, blockers_json,
			metadata_json, migrated_from_wizard, updated_at
		FROM blink_project_aggregate WHERE project_id=$1
	`, projectID)
	var snap Snapshot
	var eligRaw, blockers, meta []byte
	var updated time.Time
	snap.ProjectID = projectID
	err := row.Scan(&snap.Revision, &snap.WizardDigest, &snap.StageSpine, &eligRaw, &blockers, &meta,
		&snap.MigratedFromWizard, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("project not found")
	}
	if err != nil {
		return nil, err
	}
	if len(eligRaw) > 0 {
		_ = json.Unmarshal(eligRaw, &snap.Eligibility)
	}
	EnrichShipEligibility(ctx, s.pool, projectID, &snap.Eligibility)
	blockerList := s.gates.LoadBlockers(ctx, projectID, snap.Eligibility)
	blockerList = s.appendShipSubstageBlockers(ctx, projectID, snap.Eligibility, blockerList)
	if len(blockerList) > 0 {
		if merged, err := json.Marshal(blockerList); err == nil {
			blockers = merged
		}
	}
	snap.Blockers = json.RawMessage(blockers)
	snap.Metadata = json.RawMessage(meta)
	snap.UpdatedAt = updated.UTC().Format(time.RFC3339)
	return &snap, nil
}

func (s *Service) History(ctx context.Context, projectID int64, limit int) ([]DomainEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, revision, event_type, payload_json, actor_email, correlation_id, created_at
		FROM blink_domain_event
		WHERE project_id=$1
		ORDER BY revision DESC
		LIMIT $2
	`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DomainEvent
	for rows.Next() {
		var ev DomainEvent
		var payload []byte
		var created time.Time
		if err := rows.Scan(&ev.ID, &ev.Revision, &ev.EventType, &payload, &ev.ActorEmail, &ev.CorrelationID, &created); err != nil {
			return nil, err
		}
		ev.Payload = json.RawMessage(payload)
		ev.CreatedAt = created.UTC().Format(time.RFC3339)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *Service) Projections(ctx context.Context, projectID int64) ([]ProjectionRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, provider, action_type, status, attempts, last_error, payload_json, updated_at
		FROM blink_projection_outbox
		WHERE project_id=$1
		ORDER BY id DESC
		LIMIT 100
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectionRow
	for rows.Next() {
		var row ProjectionRow
		var payload []byte
		var updated time.Time
		if err := rows.Scan(&row.ID, &row.Provider, &row.ActionType, &row.Status, &row.Attempts, &row.LastError, &payload, &updated); err != nil {
			return nil, err
		}
		if len(payload) > 0 {
			row.Payload = json.RawMessage(payload)
		}
		row.UpdatedAt = updated.UTC().Format(time.RFC3339)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Service) PreviewCommand(ctx context.Context, projectID int64, req CommandRequest, actorEmail, correlationID string) (*CommandResult, error) {
	snap, err := s.Snapshot(ctx, projectID, actorEmail)
	if err != nil {
		return nil, err
	}
	if req.ExpectedRevision != nil && *req.ExpectedRevision != snap.Revision {
		return nil, fmt.Errorf("stale revision: expected %d have %d", *req.ExpectedRevision, snap.Revision)
	}
	preview, _ := json.Marshal(map[string]any{
		"command":     req.Command,
		"eligible":    snap.Eligibility,
		"revision":    snap.Revision,
		"agentTarget": "framework-runtime",
		"note":        "Preview only; execute records a command run and may invoke the Framework agent runtime.",
	})
	return &CommandResult{
		RunID:      uuid.NewString(),
		Status:     "preview",
		Revision:   snap.Revision,
		Preview:    preview,
		AgentRoute: "framework-runtime",
	}, nil
}

func (s *Service) ExecuteCommand(ctx context.Context, projectID int64, req CommandRequest, actorEmail, correlationID string) (*CommandResult, error) {
	if req.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	snap, err := s.Snapshot(ctx, projectID, actorEmail)
	if err != nil {
		return nil, err
	}
	if req.ExpectedRevision != nil && *req.ExpectedRevision != snap.Revision {
		return nil, fmt.Errorf("stale revision: expected %d have %d", *req.ExpectedRevision, snap.Revision)
	}

	runID := uuid.NewString()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if req.IdempotencyKey != "" {
		var existing uuid.UUID
		err = tx.QueryRow(ctx, `
			SELECT id FROM blink_command_run
			WHERE project_id=$1 AND idempotency_key=$2
		`, projectID, req.IdempotencyKey).Scan(&existing)
		if err == nil {
			var status string
			var result []byte
			_ = tx.QueryRow(ctx, `SELECT status, result_json FROM blink_command_run WHERE id=$1`, existing).Scan(&status, &result)
			_ = tx.Rollback(ctx)
			return &CommandResult{
				RunID:    existing.String(),
				Status:   status,
				Revision: snap.Revision,
				Result:   json.RawMessage(result),
			}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}

	cmd := normalizeAgentCommand(req.Command)
	var result json.RawMessage
	status := "accepted"
	needsRefreshTx := cmd == "refresh-eligibility" || cmd == "sync-wizard-draft"

	if needsRefreshTx {
		if err := s.refreshFromWizardTx(ctx, tx, projectID, actorEmail, correlationID); err != nil {
			return nil, err
		}
		result, _ = json.Marshal(map[string]string{"status": "refreshed"})
		status = "completed"
	} else if agentCommandSupported(cmd) {
		if s.cmdExec == nil {
			return nil, fmt.Errorf("agent command executor not configured")
		}
		_ = tx.Rollback(ctx)
		raw, execErr := s.cmdExec.ExecuteAgentCommand(ctx, projectID, cmd, req.Payload, actorEmail, correlationID)
		if execErr != nil {
			return nil, execErr
		}
		result = raw
		status = "completed"
		tx, err = s.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)
	} else {
		result, _ = json.Marshal(map[string]any{
			"status":     "queued",
			"command":    req.Command,
			"agentRoute": "framework-runtime",
			"error":      "unsupported command",
		})
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO blink_command_run (id, project_id, command_name, idempotency_key, status, expected_revision,
			result_json, actor_email, correlation_id, finished_at)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9,NOW())
	`, runID, projectID, req.Command, req.IdempotencyKey, status, snap.Revision, result, actorEmail, correlationID)
	if err != nil {
		return nil, err
	}

	newSnap, err := s.loadSnapshotTx(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return &CommandResult{
		RunID:      runID,
		Status:     status,
		Revision:   newSnap.Revision,
		Result:     result,
		AgentRoute: "framework-runtime",
	}, nil
}

func (s *Service) ensureAggregate(ctx context.Context, projectID int64, actorEmail string) error {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blink_project_aggregate WHERE project_id=$1)`, projectID).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return s.refreshFromWizard(ctx, projectID, actorEmail, "")
	}
	return s.importWizard(ctx, projectID, actorEmail, "")
}

func (s *Service) importWizard(ctx context.Context, projectID int64, actorEmail, correlationID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.importWizardTx(ctx, tx, projectID, actorEmail, correlationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) importWizardTx(ctx context.Context, tx pgx.Tx, projectID int64, actorEmail, correlationID string) error {
	var step *string
	var through *int
	var state []byte
	err := tx.QueryRow(ctx, `
		SELECT wizard_step, wizard_completed_through, wizard_state_json
		FROM project WHERE id=$1
	`, projectID).Scan(&step, &through, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("project not found")
	}
	if err != nil {
		return err
	}
	digest := DigestBytes(state)
	elig := ComputeEligibility(step, through, state)
	eligRaw, _ := json.Marshal(elig)
	meta, _ := json.Marshal(map[string]any{"source": "wizard_import"})
	_, err = tx.Exec(ctx, `
		INSERT INTO blink_project_aggregate (project_id, revision, wizard_digest, eligibility_json, metadata_json, migrated_from_wizard, updated_at)
		VALUES ($1, 1, $2, $3, $4, TRUE, NOW())
		ON CONFLICT (project_id) DO NOTHING
	`, projectID, digest, eligRaw, meta)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"wizardDigest": digest, "eligibility": elig})
	_, err = tx.Exec(ctx, `
		INSERT INTO blink_domain_event (project_id, revision, event_type, payload_json, actor_email, correlation_id)
		VALUES ($1, 1, 'wizard.imported', $2, NULLIF($3,''), NULLIF($4,''))
	`, projectID, payload, actorEmail, correlationID)
	return err
}

func (s *Service) RefreshEligibility(ctx context.Context, projectID int64, actorEmail string) error {
	return s.refreshFromWizard(ctx, projectID, actorEmail, "")
}

func (s *Service) refreshFromWizard(ctx context.Context, projectID int64, actorEmail, correlationID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.refreshFromWizardTx(ctx, tx, projectID, actorEmail, correlationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) refreshFromWizardTx(ctx context.Context, tx pgx.Tx, projectID int64, actorEmail, correlationID string) error {
	var step *string
	var through *int
	var state []byte
	var revision int64
	var prevDigest string
	err := tx.QueryRow(ctx, `
		SELECT p.wizard_step, p.wizard_completed_through, p.wizard_state_json,
			COALESCE(a.revision, 0), COALESCE(a.wizard_digest, '')
		FROM project p
		LEFT JOIN blink_project_aggregate a ON a.project_id = p.id
		WHERE p.id=$1
	`, projectID).Scan(&step, &through, &state, &revision, &prevDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("project not found")
	}
	if err != nil {
		return err
	}
	digest := DigestBytes(state)
	if digest == prevDigest && revision > 0 {
		return nil
	}
	_ = s.SyncGroomingSession(ctx, projectID, state)
	_ = s.SyncRepositoryRoster(ctx, projectID, state)
	_ = s.SyncRepoTechnologies(ctx, projectID, state)
	_, _ = s.RecomputeGraph(ctx, projectID, state)
	_ = s.RecordRequirementViews(ctx, projectID, state)
	_ = s.gates.SyncShapeGateDigest(ctx, projectID, ShapeFingerprint(state))
	_ = s.gates.SyncWorkPlanGateDigest(ctx, projectID, WorkPlanPackageDigest(state))
	_ = s.gates.SyncGroomGateDigest(ctx, projectID, GroomSessionDigest(state))
	elig := ComputeEligibility(step, through, state)
	EnrichShipEligibility(ctx, s.pool, projectID, &elig)
	eligRaw, _ := json.Marshal(elig)
	nextRev := revision + 1
	if revision == 0 {
		return s.importWizardTx(ctx, tx, projectID, actorEmail, correlationID)
	}
	_, err = tx.Exec(ctx, `
		UPDATE blink_project_aggregate
		SET revision=$2, wizard_digest=$3, eligibility_json=$4, updated_at=NOW()
		WHERE project_id=$1
	`, projectID, nextRev, digest, eligRaw)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"wizardDigest": digest, "eligibility": elig})
	_, err = tx.Exec(ctx, `
		INSERT INTO blink_domain_event (project_id, revision, event_type, payload_json, actor_email, correlation_id)
		VALUES ($1, $2, 'wizard.synced', $3, NULLIF($4,''), NULLIF($5,''))
	`, projectID, nextRev, payload, actorEmail, correlationID)
	return err
}

func (s *Service) loadSnapshotTx(ctx context.Context, tx pgx.Tx, projectID int64) (*Snapshot, error) {
	row := tx.QueryRow(ctx, `
		SELECT revision, wizard_digest, stage_spine, eligibility_json, blockers_json,
			metadata_json, migrated_from_wizard, updated_at
		FROM blink_project_aggregate WHERE project_id=$1
	`, projectID)
	var snap Snapshot
	var eligRaw, blockers, meta []byte
	var updated time.Time
	snap.ProjectID = projectID
	err := row.Scan(&snap.Revision, &snap.WizardDigest, &snap.StageSpine, &eligRaw, &blockers, &meta,
		&snap.MigratedFromWizard, &updated)
	if err != nil {
		return nil, err
	}
	if len(eligRaw) > 0 {
		_ = json.Unmarshal(eligRaw, &snap.Eligibility)
	}
	snap.Blockers = json.RawMessage(blockers)
	snap.Metadata = json.RawMessage(meta)
	snap.UpdatedAt = updated.UTC().Format(time.RFC3339)
	return &snap, nil
}

// RecordAIRun stores provenance when Blink invokes a Framework agent command.
func (s *Service) RecordStakeholderConfirmation(ctx context.Context, projectID int64, assignments any, registryDigest, actorEmail string) error {
	return s.gates.RecordStakeholderConfirmation(ctx, projectID, assignments, registryDigest, actorEmail)
}

func (s *Service) RecordProductScopeConfirmation(ctx context.Context, projectID int64, sourceDigest, scopeDigest, confirmedDigest, actorEmail, sdlcIssue string) error {
	return s.gates.RecordProductScopeConfirmation(ctx, projectID, sourceDigest, scopeDigest, confirmedDigest, actorEmail, sdlcIssue)
}

func (s *Service) RecordSDLStartIssue(ctx context.Context, projectID int64, issueID string) error {
	return s.gates.RecordSDLStartIssue(ctx, projectID, issueID)
}

func (s *Service) RecordShapeConfirmation(ctx context.Context, projectID int64, shapeDigest, actorEmail string) error {
	if err := s.gates.RecordShapeConfirmation(ctx, projectID, shapeDigest, actorEmail); err != nil {
		return err
	}
	return s.RefreshEligibility(ctx, projectID, actorEmail)
}

func (s *Service) RecordGroomConfirmation(ctx context.Context, projectID int64, sessionDigest, actorEmail string) error {
	if err := s.gates.RecordGroomConfirmation(ctx, projectID, sessionDigest, actorEmail); err != nil {
		return err
	}
	return s.RefreshEligibility(ctx, projectID, actorEmail)
}

func (s *Service) RecordWorkPlanConfirmation(ctx context.Context, projectID int64, packageDigest, actorEmail string) error {
	if err := s.gates.RecordWorkPlanConfirmation(ctx, projectID, packageDigest, actorEmail); err != nil {
		return err
	}
	return s.RefreshEligibility(ctx, projectID, actorEmail)
}

func (s *Service) RevokeStakeholderConfirmation(ctx context.Context, projectID int64) error {
	return s.gates.RevokeStakeholderConfirmation(ctx, projectID)
}

func (s *Service) RecordAIRun(ctx context.Context, projectID int64, command, model, inputDigest, outputDigest, correlationID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO blink_ai_run (id, project_id, command_name, model, input_digest, output_digest, framework_runtime, correlation_id)
		VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),TRUE,NULLIF($7,''))
	`, uuid.NewString(), projectID, command, model, inputDigest, outputDigest, correlationID)
	return err
}
