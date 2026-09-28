package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var AllowedModels = map[string]string{
	"gpt-5.6-luna":          "gpt-5.6-luna",
	"luna":                  "gpt-5.6-luna",
	"terra":                 "gpt-5.6-terra",
	"gpt-5.6-terra":         "gpt-5.6-terra",
	"sol":                   "gpt-5.6-sol",
	"gpt-5.6-sol":           "gpt-5.6-sol",
	"gemini-3.8-flash":      "gemini-3.8-flash",
	"gemini-3.5-flash-lite": "gemini-3.5-flash-lite",
}

func NormalizeModel(raw string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return "gpt-5.6-luna", nil
	}
	if v, ok := AllowedModels[key]; ok {
		return v, nil
	}
	return "", fmt.Errorf("model must be one of gpt-5.6-luna, terra, sol, gemini-3.8-flash, gemini-3.5-flash-lite")
}

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type Thread struct {
	ID         int64     `json:"id"`
	ProjectID  int64     `json:"projectId"`
	OwnerEmail string    `json:"ownerEmail"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type Message struct {
	ID        int64           `json:"id"`
	ThreadID  int64           `json:"threadId"`
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	Model     *string         `json:"model,omitempty"`
	Mode      *string         `json:"mode,omitempty"`
	ToolJSON  json.RawMessage `json:"toolJson,omitempty"`
	TurnID    *string         `json:"turnId,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

func (s *Store) EnsureThread(ctx context.Context, projectID int64, ownerEmail string) (*Thread, error) {
	var t Thread
	err := s.pool.QueryRow(ctx, `
		INSERT INTO chat_thread (project_id, owner_email)
		VALUES ($1, $2)
		ON CONFLICT (project_id) DO UPDATE SET updated_at = chat_thread.updated_at
		RETURNING id, project_id, owner_email, created_at, updated_at
	`, projectID, ownerEmail).Scan(&t.ID, &t.ProjectID, &t.OwnerEmail, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) GetThreadByProject(ctx context.Context, projectID int64) (*Thread, error) {
	var t Thread
	err := s.pool.QueryRow(ctx, `
		SELECT id, project_id, owner_email, created_at, updated_at
		FROM chat_thread WHERE project_id = $1
	`, projectID).Scan(&t.ID, &t.ProjectID, &t.OwnerEmail, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) ListMessages(ctx context.Context, threadID int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, thread_id, role, content, model, mode, tool_json, turn_id, created_at
		FROM chat_message
		WHERE thread_id = $1
		ORDER BY id ASC
		LIMIT $2
	`, threadID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Message, 0)
	for rows.Next() {
		var m Message
		var tool []byte
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.Model, &m.Mode, &tool, &m.TurnID, &m.CreatedAt); err != nil {
			return nil, err
		}
		if len(tool) > 0 {
			m.ToolJSON = json.RawMessage(tool)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) ClearMessages(ctx context.Context, threadID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM chat_message WHERE thread_id = $1`, threadID)
	if err != nil {
		return err
	}
	_, _ = s.pool.Exec(ctx, `UPDATE chat_thread SET updated_at = NOW() WHERE id = $1`, threadID)
	return nil
}

func (s *Store) AddMessage(ctx context.Context, threadID int64, role, content string, model, mode, turnID *string, tool json.RawMessage) (*Message, error) {
	var toolArg any
	if len(tool) > 0 {
		toolArg = []byte(tool)
	}
	var m Message
	var toolOut []byte
	err := s.pool.QueryRow(ctx, `
		INSERT INTO chat_message (thread_id, role, content, model, mode, tool_json, turn_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, thread_id, role, content, model, mode, tool_json, turn_id, created_at
	`, threadID, role, content, model, mode, toolArg, turnID).Scan(
		&m.ID, &m.ThreadID, &m.Role, &m.Content, &m.Model, &m.Mode, &toolOut, &m.TurnID, &m.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if len(toolOut) > 0 {
		m.ToolJSON = json.RawMessage(toolOut)
	}
	_, _ = s.pool.Exec(ctx, `UPDATE chat_thread SET updated_at = NOW() WHERE id = $1`, threadID)
	return &m, nil
}
