package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WorkflowKind string

const (
	WorkflowImplementStep     WorkflowKind = "implement-step"
	WorkflowImplementBatch    WorkflowKind = "implement-batch"
	WorkflowPRReview          WorkflowKind = "pr-review"
	WorkflowFixReviewFindings WorkflowKind = "fix-review-findings"
)

type SessionStatus string

const (
	StatusIdle      SessionStatus = "idle"
	StatusRunning   SessionStatus = "running"
	StatusCompleted SessionStatus = "completed"
	StatusFailed    SessionStatus = "failed"
	StatusCancelled SessionStatus = "cancelled"
)

type Session struct {
	ID                   uuid.UUID
	ProjectID            int64
	IssueID              string
	WorkflowKind         WorkflowKind
	ActiveOpenAISession  string
	Model                string
	ContainerSize        string
	Status               SessionStatus
	History              []map[string]any
	LatestFiles          map[string]string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type Turn struct {
	ID              uuid.UUID
	CodexSessionID  uuid.UUID
	TurnNumber      int
	OpenAISessionID string
	OpenAITurnID    string
	IdempotencyKey  string
	UserPrompt      string
	Status          SessionStatus
	OutputText      string
	Commands        []map[string]any
	Artifacts       []map[string]any
	ErrorCode       string
	ErrorMessage    string
	Usage           map[string]any
	CreatedAt       time.Time
	FinishedAt      *time.Time
}

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) UpsertSession(ctx context.Context, projectID int64, issueID string, kind WorkflowKind, model, container string) (*Session, error) {
	issueID = strings.TrimSpace(issueID)
	row := s.pool.QueryRow(ctx, `
		SELECT id FROM blink_codex_session
		WHERE project_id = $1 AND issue_id = $2 AND workflow_kind = $3
		ORDER BY updated_at DESC
		LIMIT 1`, projectID, issueID, string(kind))
	var existing uuid.UUID
	err := row.Scan(&existing)
	if err == nil {
		_, err = s.pool.Exec(ctx, `
			UPDATE blink_codex_session
			SET model = $2, container_size = $3, updated_at = NOW()
			WHERE id = $1`, existing, model, container)
		if err != nil {
			return nil, err
		}
		return s.GetSession(ctx, existing)
	}
	if err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	id := uuid.New()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO blink_codex_session (
			id, project_id, issue_id, workflow_kind, model, container_size, status, history_json, latest_files_json
		) VALUES ($1,$2,$3,$4,$5,$6,'idle','[]'::jsonb,'{}'::jsonb)`,
		id, projectID, issueID, string(kind), model, container)
	if err != nil {
		return nil, err
	}
	return s.GetSession(ctx, id)
}

func (s *Store) GetSession(ctx context.Context, id uuid.UUID) (*Session, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, project_id, issue_id, workflow_kind, COALESCE(active_openai_session_id,''), model, container_size, status,
		       history_json, latest_files_json, created_at, updated_at
		FROM blink_codex_session WHERE id = $1`, id)
	var sess Session
	var historyRaw, filesRaw []byte
	var kind, status string
	if err := row.Scan(
		&sess.ID, &sess.ProjectID, &sess.IssueID, &kind, &sess.ActiveOpenAISession,
		&sess.Model, &sess.ContainerSize, &status, &historyRaw, &filesRaw, &sess.CreatedAt, &sess.UpdatedAt,
	); err != nil {
		return nil, err
	}
	sess.WorkflowKind = WorkflowKind(kind)
	sess.Status = SessionStatus(status)
	_ = json.Unmarshal(historyRaw, &sess.History)
	if sess.History == nil {
		sess.History = []map[string]any{}
	}
	_ = json.Unmarshal(filesRaw, &sess.LatestFiles)
	if sess.LatestFiles == nil {
		sess.LatestFiles = map[string]string{}
	}
	return &sess, nil
}

func (s *Store) SetRunning(ctx context.Context, id uuid.UUID, openaiSessionID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE blink_codex_session
		SET status = 'running', active_openai_session_id = $2, updated_at = NOW()
		WHERE id = $1`, id, nullIfEmpty(openaiSessionID))
	return err
}

func (s *Store) FinishSession(ctx context.Context, id uuid.UUID, status SessionStatus, history []map[string]any, files map[string]string) error {
	if history == nil {
		history = []map[string]any{}
	}
	if files == nil {
		files = map[string]string{}
	}
	h, _ := json.Marshal(history)
	f, _ := json.Marshal(files)
	_, err := s.pool.Exec(ctx, `
		UPDATE blink_codex_session
		SET status = $2, active_openai_session_id = NULL, history_json = $3::jsonb,
		    latest_files_json = $4::jsonb, updated_at = NOW()
		WHERE id = $1`, id, string(status), string(h), string(f))
	return err
}

func (s *Store) BeginTurn(ctx context.Context, sessionID uuid.UUID, prompt, idempotencyKey string) (*Turn, error) {
	var next int
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(turn_number), 0) + 1 FROM blink_codex_turn WHERE codex_session_id = $1`, sessionID).Scan(&next)
	if err != nil {
		return nil, err
	}
	id := uuid.New()
	_, err = s.pool.Exec(ctx, `
		INSERT INTO blink_codex_turn (
			id, codex_session_id, turn_number, idempotency_key, user_prompt, status
		) VALUES ($1,$2,$3,NULLIF($4,''),$5,'running')`,
		id, sessionID, next, idempotencyKey, prompt)
	if err != nil {
		return nil, err
	}
	return &Turn{
		ID:             id,
		CodexSessionID: sessionID,
		TurnNumber:     next,
		IdempotencyKey: idempotencyKey,
		UserPrompt:     prompt,
		Status:         StatusRunning,
		Commands:       []map[string]any{},
		Artifacts:      []map[string]any{},
		Usage:          map[string]any{},
		CreatedAt:      time.Now().UTC(),
	}, nil
}

func (s *Store) CompleteTurn(ctx context.Context, turnID uuid.UUID, openaiSessionID, openaiTurnID, status, output string, commands, artifacts []map[string]any, usage map[string]any, errCode, errMsg string) error {
	if commands == nil {
		commands = []map[string]any{}
	}
	if artifacts == nil {
		artifacts = []map[string]any{}
	}
	if usage == nil {
		usage = map[string]any{}
	}
	c, _ := json.Marshal(commands)
	a, _ := json.Marshal(artifacts)
	u, _ := json.Marshal(usage)
	_, err := s.pool.Exec(ctx, `
		UPDATE blink_codex_turn SET
			openai_session_id = $2,
			openai_turn_id = $3,
			status = $4,
			output_text = $5,
			commands_json = $6::jsonb,
			artifacts_json = $7::jsonb,
			usage_json = $8::jsonb,
			error_code = NULLIF($9,''),
			error_message = NULLIF($10,''),
			finished_at = NOW()
		WHERE id = $1`,
		turnID, nullIfEmpty(openaiSessionID), nullIfEmpty(openaiTurnID), status, output,
		string(c), string(a), string(u), errCode, errMsg)
	return err
}

func (s *Store) ProjectHasRunning(ctx context.Context, projectID int64) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM blink_codex_session
		WHERE project_id = $1 AND status = 'running'`, projectID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) LatestRunningOpenAISession(ctx context.Context, projectID int64, issueID string) (sessionUUID uuid.UUID, openaiID string, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT id, COALESCE(active_openai_session_id,'')
		FROM blink_codex_session
		WHERE project_id = $1 AND issue_id = $2 AND status = 'running'
		ORDER BY updated_at DESC LIMIT 1`, projectID, strings.TrimSpace(issueID)).Scan(&sessionUUID, &openaiID)
	if err != nil {
		return uuid.Nil, "", err
	}
	return sessionUUID, openaiID, nil
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// HistorySummary builds a compact prior-turn summary for rehydration input.
func HistorySummary(history []map[string]any, limit int) string {
	if limit <= 0 {
		limit = 6
	}
	if len(history) == 0 {
		return ""
	}
	start := 0
	if len(history) > limit {
		start = len(history) - limit
	}
	b, err := json.MarshalIndent(history[start:], "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", history[start:])
	}
	return string(b)
}
