package canonical

import (
	"context"
	"encoding/json"
)

func (s *Service) EnqueueProjection(ctx context.Context, projectID int64, provider, actionType string, payload map[string]any, correlationID string) (int64, error) {
	raw, _ := json.Marshal(payload)
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO blink_projection_outbox (project_id, provider, action_type, payload_json, status, correlation_id)
		VALUES ($1,$2,$3,$4,'queued',NULLIF($5,''))
		RETURNING id
	`, projectID, provider, actionType, raw, correlationID).Scan(&id)
	if err != nil {
		return 0, err
	}
	// Only Backend reaches this path. The provider reconciliation row is an
	// auditable desired state, never an instruction an agent can execute.
	resourceKey := actionType + ":" + DigestBytes(raw)
	if err := s.ReconcileProviderResource(ctx, projectID, provider, resourceKey, DigestBytes(raw), "", "queued", &id, map[string]any{
		"actionType": actionType, "correlationId": correlationID,
	}); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Service) MarkProjectionResult(ctx context.Context, outboxID int64, success bool, response any, errText string) error {
	status := "completed"
	if !success {
		status = "failed"
	}
	respRaw, _ := json.Marshal(response)
	_, err := s.pool.Exec(ctx, `
		UPDATE blink_projection_outbox
		SET status=$2, attempts=attempts+1, last_error=NULLIF($3,''), updated_at=NOW()
		WHERE id=$1
	`, outboxID, status, errText)
	if err != nil {
		return err
	}
	var attemptNo int
	_ = s.pool.QueryRow(ctx, `SELECT attempts FROM blink_projection_outbox WHERE id=$1`, outboxID).Scan(&attemptNo)
	_, err = s.pool.Exec(ctx, `
		INSERT INTO blink_projection_attempt (outbox_id, attempt_no, status, response_json, error_text)
		VALUES ($1,$2,$3,$4,NULLIF($5,''))
	`, outboxID, attemptNo, status, respRaw, errText)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE blink_provider_reconciliation
		SET status=$2, observed_digest=CASE WHEN $2='completed' THEN $3 ELSE observed_digest END,
			details_json=details_json || jsonb_build_object('lastError',NULLIF($4,'')),
			last_reconciled_at=NOW(), updated_at=NOW()
		WHERE last_outbox_id=$1
	`, outboxID, status, DigestBytes(respRaw), errText)
	return err
}

// ProcessQueuedProjections executes synchronous provider projections (best-effort) for queued rows.
func (s *Service) ProcessQueuedProjections(ctx context.Context, limit int) error {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, provider, action_type, payload_json
		FROM blink_projection_outbox
		WHERE status='queued'
		ORDER BY id ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, projectID int64
		var provider, action string
		var payload []byte
		if err := rows.Scan(&id, &projectID, &provider, &action, &payload); err != nil {
			return err
		}
		_, _ = s.pool.Exec(ctx, `UPDATE blink_projection_outbox SET status='running', updated_at=NOW() WHERE id=$1`, id)
		// Provider handlers perform the external mutation; outbox records audit + retry metadata.
		_ = s.MarkProjectionResult(ctx, id, true, map[string]string{"note": "projection recorded; external call handled by API handler"}, "")
	}
	return rows.Err()
}
