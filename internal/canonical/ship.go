package canonical

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

var ShipSubstageOrder = []string{"workspace", "implementation", "review-pr", "release"}

const (
	ShipStepGitApply       = "git-apply"
	ShipStepImplement      = "implement-step"
	ShipStepQAValidation   = "qa-validation"
	ShipStepSubstageNav    = "substage-nav"
)

type ShipStepView struct {
	ID             int64           `json:"id"`
	StepKind       string          `json:"stepKind"`
	Status         string          `json:"status"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	IdempotencyKey *string         `json:"idempotencyKey,omitempty"`
	CreatedAt      string          `json:"createdAt"`
	FinishedAt     *string         `json:"finishedAt,omitempty"`
}

type ShipSessionDetail struct {
	ShipSessionView
	ScopeRef           string         `json:"scopeRef,omitempty"`
	AllowedSubstages   []string       `json:"allowedSubstages"`
	MaxSubstage        string         `json:"maxSubstage"`
	RecentSteps        []ShipStepView `json:"recentSteps"`
}

func shipSubstageIndex(substage string) int {
	s := strings.ToLower(strings.TrimSpace(substage))
	for i, item := range ShipSubstageOrder {
		if item == s {
			return i
		}
	}
	return 0
}

func (s *Service) projectHasShipStep(ctx context.Context, projectID int64, stepKind string) bool {
	var n int
	_ = s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM blink_ship_step st
		JOIN blink_ship_session ss ON ss.id = st.session_id
		WHERE ss.project_id=$1 AND st.step_kind=$2 AND st.status='completed'
	`, projectID, stepKind).Scan(&n)
	return n > 0
}

func (s *Service) ProjectHasShipStep(ctx context.Context, projectID int64, stepKind string) bool {
	return s.projectHasShipStep(ctx, projectID, stepKind)
}

// MaxAllowedShipSubstage derives the furthest substage the project may enter from recorded ship steps.
func (s *Service) MaxAllowedShipSubstage(ctx context.Context, projectID int64) string {
	maxIdx := 0
	if s.projectHasShipStep(ctx, projectID, ShipStepGitApply) {
		maxIdx = 1
	}
	if s.projectHasShipStep(ctx, projectID, ShipStepImplement) {
		maxIdx = 2
	}
	if s.projectHasShipStep(ctx, projectID, ShipStepQAValidation) {
		maxIdx = 3
	}
	if maxIdx >= len(ShipSubstageOrder) {
		maxIdx = len(ShipSubstageOrder) - 1
	}
	return ShipSubstageOrder[maxIdx]
}

func allowedSubstagesUpTo(maxSubstage string) []string {
	idx := shipSubstageIndex(maxSubstage)
	out := make([]string, 0, idx+1)
	for i := 0; i <= idx && i < len(ShipSubstageOrder); i++ {
		out = append(out, ShipSubstageOrder[i])
	}
	return out
}

func EnrichShipEligibility(ctx context.Context, pool *pgxpool.Pool, projectID int64, elig *Eligibility) {
	if pool == nil || elig == nil || projectID <= 0 {
		return
	}
	if elig.CompletedIndex < indexForStep("ship") && elig.CurrentStep != "ship" {
		return
	}
	svc := &Service{pool: pool, gates: NewGates(pool)}
	max := svc.MaxAllowedShipSubstage(ctx, projectID)
	elig.MaxShipSubstage = max
	elig.AllowedShipSubstages = allowedSubstagesUpTo(max)
	if elig.ShipSubstage == "" {
		elig.ShipSubstage = "workspace"
	}
}

func (s *Service) ValidateShipSubstage(ctx context.Context, projectID int64, substage string) error {
	target := strings.ToLower(strings.TrimSpace(substage))
	if target == "" {
		target = "workspace"
	}
	max := s.MaxAllowedShipSubstage(ctx, projectID)
	if shipSubstageIndex(target) > shipSubstageIndex(max) {
		return fmt.Errorf("ship substage %q is not allowed yet (max %q); complete prior delivery steps", target, max)
	}
	return nil
}

func (s *Service) pickScopeRef(ctx context.Context, projectID int64) string {
	var frontier []byte
	err := s.pool.QueryRow(ctx, `
		SELECT frontier_json FROM blink_dependency_graph WHERE project_id=$1
	`, projectID).Scan(&frontier)
	if err != nil || len(frontier) == 0 {
		return ""
	}
	var items []string
	if json.Unmarshal(frontier, &items) != nil || len(items) == 0 {
		return ""
	}
	return strings.TrimSpace(items[0])
}

func (s *Service) RecordShipStepWithResult(ctx context.Context, sessionID, stepKind, idempotencyKey, status string, payload, result map[string]any) error {
	if status == "" {
		status = "completed"
	}
	if idempotencyKey != "" {
		var exists int64
		err := s.pool.QueryRow(ctx, `
			SELECT id FROM blink_ship_step WHERE session_id=$1 AND idempotency_key=$2
		`, sessionID, idempotencyKey).Scan(&exists)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	payloadRaw, _ := json.Marshal(payload)
	resultRaw, _ := json.Marshal(result)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO blink_ship_step (session_id, step_kind, status, payload_json, result_json, idempotency_key, finished_at)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),CASE WHEN $3='completed' THEN NOW() ELSE NULL END)
	`, sessionID, stepKind, status, payloadRaw, resultRaw, idempotencyKey)
	return err
}

func (s *Service) RecordShipDelivery(ctx context.Context, projectID int64, substage, stepKind, idempotencyKey, actor string, payload, result map[string]any) error {
	sess, err := s.EnsureShipSession(ctx, projectID, substage, actor)
	if err != nil {
		return err
	}
	return s.RecordShipStepWithResult(ctx, sess.ID, stepKind, idempotencyKey, "completed", payload, result)
}

func (s *Service) GetShipSessionDetail(ctx context.Context, projectID int64) (*ShipSessionDetail, error) {
	sess, err := s.GetActiveShipSession(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var scopeRef *string
	var checkpoint []byte
	_ = s.pool.QueryRow(ctx, `
		SELECT scope_ref, checkpoint_json FROM blink_ship_session WHERE id=$1
	`, sess.ID).Scan(&scopeRef, &checkpoint)
	detail := &ShipSessionDetail{
		ShipSessionView:  *sess,
		MaxSubstage:      "",
		AllowedSubstages: nil,
	}
	max := s.MaxAllowedShipSubstage(ctx, projectID)
	detail.MaxSubstage = max
	detail.AllowedSubstages = allowedSubstagesUpTo(max)
	detail.RecentSteps = []ShipStepView{}
	if scopeRef != nil {
		detail.ScopeRef = *scopeRef
	}
	rows, err := s.pool.Query(ctx, `
		SELECT st.id, st.step_kind, st.status, st.payload_json, st.result_json, st.idempotency_key, st.created_at, st.finished_at
		FROM blink_ship_step st
		WHERE st.session_id=$1
		ORDER BY st.id DESC
		LIMIT 25
	`, sess.ID)
	if err != nil {
		return detail, nil
	}
	defer rows.Close()
	for rows.Next() {
		var v ShipStepView
		var payload, result []byte
		var idempotency *string
		var created time.Time
		var finished *time.Time
		if err := rows.Scan(&v.ID, &v.StepKind, &v.Status, &payload, &result, &idempotency, &created, &finished); err != nil {
			continue
		}
		if len(payload) > 0 {
			v.Payload = json.RawMessage(payload)
		}
		if len(result) > 0 {
			v.Result = json.RawMessage(result)
		}
		v.IdempotencyKey = idempotency
		v.CreatedAt = created.UTC().Format(time.RFC3339)
		if finished != nil {
			s := finished.UTC().Format(time.RFC3339)
			v.FinishedAt = &s
		}
		detail.RecentSteps = append(detail.RecentSteps, v)
	}
	return detail, nil
}

func (s *Service) appendShipSubstageBlockers(ctx context.Context, projectID int64, elig Eligibility, blockers []map[string]any) []map[string]any {
	if elig.CompletedIndex < indexForStep("sdlc-plan") {
		return blockers
	}
	if elig.CurrentStep != "ship" && elig.CompletedIndex < indexForStep("ship") {
		return blockers
	}
	max := s.MaxAllowedShipSubstage(ctx, projectID)
	cur := strings.ToLower(strings.TrimSpace(elig.ShipSubstage))
	if cur == "" {
		cur = "workspace"
	}
	if shipSubstageIndex(cur) > shipSubstageIndex(max) {
		blockers = append(blockers, map[string]any{
			"code":     "ship-substage-ahead",
			"message":  fmt.Sprintf("Current substage %q is ahead of allowed %q.", cur, max),
			"step":     "ship",
			"substage": cur,
			"maxSubstage": max,
		})
	}
	curIdx := shipSubstageIndex(cur)
	if curIdx >= 1 && !s.projectHasShipStep(ctx, projectID, ShipStepGitApply) {
		blockers = append(blockers, map[string]any{
			"code":     "ship-git-apply-pending",
			"message":  "Run Git apply in Workspace before Implementation.",
			"step":     "ship",
			"substage": "workspace",
		})
	}
	if curIdx >= 2 && !s.projectHasShipStep(ctx, projectID, ShipStepImplement) {
		blockers = append(blockers, map[string]any{
			"code":     "ship-implement-pending",
			"message":  "Complete implement-step before Review & PR.",
			"step":     "ship",
			"substage": "implementation",
		})
	}
	if curIdx >= 3 && !s.projectHasShipStep(ctx, projectID, ShipStepQAValidation) {
		blockers = append(blockers, map[string]any{
			"code":     "ship-qa-pending",
			"message":  "Run QA validation before Release.",
			"step":     "ship",
			"substage": "review-pr",
		})
	}
	return blockers
}
