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
	ID                  uuid.UUID
	ProjectID           int64
	IssueID             string
	WorkflowKind        WorkflowKind
	ActiveOpenAISession string
	Model               string
	ContainerSize       string
	Status              SessionStatus
	History             []map[string]any
	LatestFiles         map[string]string
	BaseFiles           map[string]string
	StagedFiles         map[string]string
	UncommittedFiles    map[string]string
	CreatedAt           time.Time
	UpdatedAt           time.Time
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
	Files           map[string]string
	Attachments     []map[string]any
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
		       history_json, latest_files_json,
		       COALESCE(base_files_json, '{}'::jsonb),
		       COALESCE(staged_files_json, '{}'::jsonb),
		       COALESCE(uncommitted_files_json, '{}'::jsonb),
		       created_at, updated_at
		FROM blink_codex_session WHERE id = $1`, id)
	var sess Session
	var historyRaw, filesRaw, baseRaw, stagedRaw, uncommittedRaw []byte
	var kind, status string
	if err := row.Scan(
		&sess.ID, &sess.ProjectID, &sess.IssueID, &kind, &sess.ActiveOpenAISession,
		&sess.Model, &sess.ContainerSize, &status, &historyRaw, &filesRaw,
		&baseRaw, &stagedRaw, &uncommittedRaw, &sess.CreatedAt, &sess.UpdatedAt,
	); err != nil {
		return nil, err
	}
	sess.WorkflowKind = WorkflowKind(kind)
	sess.Status = SessionStatus(status)
	_ = json.Unmarshal(historyRaw, &sess.History)
	if sess.History == nil {
		sess.History = []map[string]any{}
	}
	sess.LatestFiles = decodeStringMap(filesRaw)
	sess.BaseFiles = decodeStringMap(baseRaw)
	sess.StagedFiles = decodeStringMap(stagedRaw)
	sess.UncommittedFiles = decodeStringMap(uncommittedRaw)
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

type RunningSession struct {
	ID        uuid.UUID
	ProjectID int64
	IssueID   string
	UpdatedAt time.Time
}

type ProjectUsageSummary struct {
	ProjectID   int64
	Turns       int
	Completed   int
	Failed      int
	Cancelled   int
	TotalTokens float64
}

func (s *Store) ProjectUsage(ctx context.Context, projectID int64) (ProjectUsageSummary, error) {
	var out ProjectUsageSummary
	out.ProjectID = projectID
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)::int,
		       COUNT(*) FILTER (WHERE status = 'completed')::int,
		       COUNT(*) FILTER (WHERE status = 'failed')::int,
		       COUNT(*) FILTER (WHERE status = 'cancelled')::int,
		       COALESCE(SUM(
		           CASE
		               WHEN usage_json->>'totalTokens' ~ '^[0-9]+([.][0-9]+)?$'
		               THEN (usage_json->>'totalTokens')::numeric
		               ELSE 0
		           END
		       ), 0)::float8
		FROM blink_codex_turn t
		JOIN blink_codex_session s ON s.id = t.codex_session_id
		WHERE s.project_id = $1`, projectID).Scan(
		&out.Turns, &out.Completed, &out.Failed, &out.Cancelled, &out.TotalTokens,
	)
	return out, err
}

func (s *Store) RunningSessions(ctx context.Context, projectID int64) ([]RunningSession, error) {
	q := `
		SELECT id, project_id, COALESCE(issue_id,''), updated_at
		FROM blink_codex_session
		WHERE status = 'running'`
	args := []any{}
	if projectID > 0 {
		q += ` AND project_id = $1`
		args = append(args, projectID)
	}
	q += ` ORDER BY updated_at ASC`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunningSession{}
	for rows.Next() {
		var row RunningSession
		if err := rows.Scan(&row.ID, &row.ProjectID, &row.IssueID, &row.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// AbandonIfRunning marks a session failed only while it is still running.
// Returns true when a stuck row was cleared.
func (s *Store) AbandonIfRunning(ctx context.Context, id uuid.UUID, errCode, errMsg string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE blink_codex_session
		SET status = 'failed', active_openai_session_id = NULL, updated_at = NOW()
		WHERE id = $1 AND status = 'running'`, id)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	_, _ = s.pool.Exec(ctx, `
		UPDATE blink_codex_turn
		SET status = 'failed', error_code = NULLIF($2,''), error_message = NULLIF($3,''), finished_at = NOW()
		WHERE codex_session_id = $1 AND status = 'running'`, id, errCode, errMsg)
	return true, nil
}

// ReapAbandoned clears running sessions this process is not actually executing.
func (s *Store) ReapAbandoned(ctx context.Context, projectID int64, live map[uuid.UUID]struct{}, minAge time.Duration) ([]RunningSession, error) {
	rows, err := s.RunningSessions(ctx, projectID)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-minAge)
	cleared := []RunningSession{}
	for _, row := range rows {
		if _, ok := live[row.ID]; ok {
			continue
		}
		if row.UpdatedAt.After(cutoff) {
			continue
		}
		ok, err := s.AbandonIfRunning(ctx, row.ID, "abandoned", "no active Codex turn in this API process")
		if err != nil {
			return cleared, err
		}
		if ok {
			cleared = append(cleared, row)
		}
	}
	return cleared, nil
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

func decodeStringMap(raw []byte) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		return map[string]string{}
	}
	return out
}

func (s *Store) SessionForIssue(ctx context.Context, projectID int64, issueID string) (*Session, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM blink_codex_session
		WHERE project_id = $1 AND issue_id = $2
		ORDER BY updated_at DESC LIMIT 1`, projectID, strings.TrimSpace(issueID)).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.GetSession(ctx, id)
}

func (s *Store) ListTurns(ctx context.Context, sessionID uuid.UUID) ([]Turn, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, codex_session_id, turn_number, COALESCE(user_prompt,''), status, COALESCE(output_text,''),
		       COALESCE(commands_json, '[]'::jsonb), COALESCE(files_json, '{}'::jsonb),
		       COALESCE(attachments_json, '[]'::jsonb), COALESCE(usage_json, '{}'::jsonb),
		       created_at, finished_at
		FROM blink_codex_turn
		WHERE codex_session_id = $1
		ORDER BY turn_number ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Turn{}
	for rows.Next() {
		var t Turn
		var status string
		var commandsRaw, filesRaw, attachmentsRaw, usageRaw []byte
		if err := rows.Scan(&t.ID, &t.CodexSessionID, &t.TurnNumber, &t.UserPrompt, &status, &t.OutputText, &commandsRaw, &filesRaw, &attachmentsRaw, &usageRaw, &t.CreatedAt, &t.FinishedAt); err != nil {
			return nil, err
		}
		t.Status = SessionStatus(status)
		_ = json.Unmarshal(commandsRaw, &t.Commands)
		t.Files = decodeStringMap(filesRaw)
		_ = json.Unmarshal(attachmentsRaw, &t.Attachments)
		_ = json.Unmarshal(usageRaw, &t.Usage)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) SaveTurnWorkbench(ctx context.Context, turnID uuid.UUID, files map[string]string, attachments []map[string]any) error {
	if files == nil {
		files = map[string]string{}
	}
	if attachments == nil {
		attachments = []map[string]any{}
	}
	fb, _ := json.Marshal(files)
	ab, _ := json.Marshal(attachments)
	_, err := s.pool.Exec(ctx, `
		UPDATE blink_codex_turn
		SET files_json = $2::jsonb, attachments_json = $3::jsonb
		WHERE id = $1`, turnID, string(fb), string(ab))
	return err
}

func (s *Store) SaveTrees(ctx context.Context, sessionID uuid.UUID, base, staged, uncommitted map[string]string) error {
	if base == nil {
		base = map[string]string{}
	}
	if staged == nil {
		staged = map[string]string{}
	}
	if uncommitted == nil {
		uncommitted = map[string]string{}
	}
	b, _ := json.Marshal(base)
	st, _ := json.Marshal(staged)
	u, _ := json.Marshal(uncommitted)
	_, err := s.pool.Exec(ctx, `
		UPDATE blink_codex_session
		SET base_files_json = $2::jsonb,
		    staged_files_json = $3::jsonb,
		    uncommitted_files_json = $4::jsonb,
		    updated_at = NOW()
		WHERE id = $1`, sessionID, string(b), string(st), string(u))
	return err
}

func (s *Store) MergeUncommitted(ctx context.Context, sessionID uuid.UUID, files map[string]string) error {
	sess, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.UncommittedFiles == nil {
		sess.UncommittedFiles = map[string]string{}
	}
	for k, v := range files {
		sess.UncommittedFiles[k] = v
	}
	return s.SaveTrees(ctx, sessionID, sess.BaseFiles, sess.StagedFiles, sess.UncommittedFiles)
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
