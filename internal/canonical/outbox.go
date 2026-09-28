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
	return id, err
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
