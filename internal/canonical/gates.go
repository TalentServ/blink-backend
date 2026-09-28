package canonical

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Gates struct {
	pool *pgxpool.Pool
}

func NewGates(pool *pgxpool.Pool) *Gates { return &Gates{pool: pool} }

func (g *Gates) Require(ctx context.Context, projectID int64, requirements []GateRequirement) error {
	for _, requirement := range requirements {
		var confirmed *string
		switch requirement {
		case GateStakeholdersConfirmed:
			if err := g.pool.QueryRow(ctx, `
				SELECT confirmed_digest FROM blink_stakeholder_registry WHERE project_id=$1
			`, projectID).Scan(&confirmed); err != nil && err != pgx.ErrNoRows {
				return err
			}
		case GateProductScopeConfirmed:
			if err := g.pool.QueryRow(ctx, `
				SELECT confirmed_digest FROM blink_product_scope_gate WHERE project_id=$1
			`, projectID).Scan(&confirmed); err != nil && err != pgx.ErrNoRows {
				return err
			}
		case GateArchitectureConfirmed:
			if err := g.pool.QueryRow(ctx, `
				SELECT confirmed_digest FROM blink_architecture_snapshot
				WHERE project_id=$1 AND invalidated_at IS NULL
			`, projectID).Scan(&confirmed); err != nil && err != pgx.ErrNoRows {
				return err
			}
		default:
			return fmt.Errorf("unknown gate requirement: %s", requirement)
		}
		if confirmed == nil || *confirmed == "" {
			return fmt.Errorf("required gate is not confirmed: %s", requirement)
		}
	}
	return nil
}

func (g *Gates) RecordStakeholderConfirmation(ctx context.Context, projectID int64, assignments any, registryDigest, actorEmail string) error {
	raw, _ := json.Marshal(assignments)
	_, err := g.pool.Exec(ctx, `
		INSERT INTO blink_stakeholder_registry (project_id, revision, assignments_json, registry_digest, confirmed_revision, confirmed_digest, confirmed_by, confirmed_at, updated_at)
		VALUES ($1, 1, $2, $3, 1, $3, NULLIF($4,''), NOW(), NOW())
		ON CONFLICT (project_id) DO UPDATE SET
			revision = blink_stakeholder_registry.revision + 1,
			assignments_json = EXCLUDED.assignments_json,
			registry_digest = EXCLUDED.registry_digest,
			confirmed_revision = blink_stakeholder_registry.revision + 1,
			confirmed_digest = EXCLUDED.registry_digest,
			confirmed_by = EXCLUDED.confirmed_by,
			confirmed_at = NOW(),
			updated_at = NOW()
	`, projectID, raw, registryDigest, actorEmail)
	if err != nil {
		return err
	}
	return g.recordHumanDecision(ctx, projectID, "stakeholder-registry", registryDigest, actorEmail, map[string]any{"registryDigest": registryDigest})
}

func (g *Gates) RecordProductScopeConfirmation(ctx context.Context, projectID int64, sourceDigest, scopeDigest, confirmedDigest, actorEmail, sdlcIssue string) error {
	_, err := g.pool.Exec(ctx, `
		INSERT INTO blink_product_scope_gate (project_id, source_digest, scope_digest, confirmed_digest, confirmed_by, confirmed_at, sdlc_start_issue_id, updated_at)
		VALUES ($1,$2,$3,$4,NULLIF($5,''),NOW(),NULLIF($6,''),NOW())
		ON CONFLICT (project_id) DO UPDATE SET
			source_digest = EXCLUDED.source_digest,
			scope_digest = EXCLUDED.scope_digest,
			confirmed_digest = EXCLUDED.confirmed_digest,
			confirmed_by = EXCLUDED.confirmed_by,
			confirmed_at = NOW(),
			sdlc_start_issue_id = COALESCE(NULLIF(EXCLUDED.sdlc_start_issue_id,''), blink_product_scope_gate.sdlc_start_issue_id),
			updated_at = NOW()
	`, projectID, sourceDigest, scopeDigest, confirmedDigest, actorEmail, sdlcIssue)
	if err != nil {
		return err
	}
	return g.recordHumanDecision(ctx, projectID, "product-scope", confirmedDigest, actorEmail, map[string]any{
		"sourceDigest": sourceDigest,
		"scopeDigest":  scopeDigest,
	})
}

func (g *Gates) RecordShapeConfirmation(ctx context.Context, projectID int64, shapeDigest, actorEmail string) error {
	_, err := g.pool.Exec(ctx, `
		INSERT INTO blink_shape_gate (project_id, shape_digest, confirmed_digest, confirmed_by, confirmed_at, updated_at)
		VALUES ($1,$2,$2,NULLIF($3,''),NOW(),NOW())
		ON CONFLICT (project_id) DO UPDATE SET
			shape_digest = EXCLUDED.shape_digest,
			confirmed_digest = EXCLUDED.confirmed_digest,
			confirmed_by = EXCLUDED.confirmed_by,
			confirmed_at = NOW(),
			updated_at = NOW()
	`, projectID, shapeDigest, actorEmail)
	if err != nil {
		return err
	}
	return g.recordHumanDecision(ctx, projectID, "project-shape", shapeDigest, actorEmail, map[string]any{"shapeDigest": shapeDigest})
}

func (g *Gates) RecordGroomConfirmation(ctx context.Context, projectID int64, sessionDigest, actorEmail string) error {
	_, err := g.pool.Exec(ctx, `
		INSERT INTO blink_groom_gate (project_id, session_digest, confirmed_digest, confirmed_by, confirmed_at, updated_at)
		VALUES ($1,$2,$2,NULLIF($3,''),NOW(),NOW())
		ON CONFLICT (project_id) DO UPDATE SET
			session_digest = EXCLUDED.session_digest,
			confirmed_digest = EXCLUDED.confirmed_digest,
			confirmed_by = EXCLUDED.confirmed_by,
			confirmed_at = NOW(),
			updated_at = NOW()
	`, projectID, sessionDigest, actorEmail)
	if err != nil {
		return err
	}
	return g.recordHumanDecision(ctx, projectID, "g-groom", sessionDigest, actorEmail, map[string]any{"sessionDigest": sessionDigest})
}

func (g *Gates) RecordWorkPlanConfirmation(ctx context.Context, projectID int64, packageDigest, actorEmail string) error {
	_, err := g.pool.Exec(ctx, `
		INSERT INTO blink_work_plan_gate (project_id, package_digest, confirmed_digest, confirmed_by, confirmed_at, updated_at)
		VALUES ($1,$2,$2,NULLIF($3,''),NOW(),NOW())
		ON CONFLICT (project_id) DO UPDATE SET
			package_digest = EXCLUDED.package_digest,
			confirmed_digest = EXCLUDED.confirmed_digest,
			confirmed_by = EXCLUDED.confirmed_by,
			confirmed_at = NOW(),
			updated_at = NOW()
	`, projectID, packageDigest, actorEmail)
	if err != nil {
		return err
	}
	return g.recordHumanDecision(ctx, projectID, "g-plan", packageDigest, actorEmail, map[string]any{"packageDigest": packageDigest})
}

func (g *Gates) SyncShapeGateDigest(ctx context.Context, projectID int64, digest string) error {
	if digest == "" {
		return nil
	}
	_, err := g.pool.Exec(ctx, `
		INSERT INTO blink_shape_gate (project_id, shape_digest, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (project_id) DO UPDATE SET
			shape_digest = EXCLUDED.shape_digest,
			confirmed_digest = CASE
				WHEN blink_shape_gate.shape_digest = EXCLUDED.shape_digest THEN blink_shape_gate.confirmed_digest
				ELSE NULL END,
			confirmed_by = CASE
				WHEN blink_shape_gate.shape_digest = EXCLUDED.shape_digest THEN blink_shape_gate.confirmed_by
				ELSE NULL END,
			confirmed_at = CASE
				WHEN blink_shape_gate.shape_digest = EXCLUDED.shape_digest THEN blink_shape_gate.confirmed_at
				ELSE NULL END,
			updated_at = NOW()
	`, projectID, digest)
	return err
}

func (g *Gates) SyncWorkPlanGateDigest(ctx context.Context, projectID int64, digest string) error {
	if digest == "" {
		return nil
	}
	_, err := g.pool.Exec(ctx, `
		INSERT INTO blink_work_plan_gate (project_id, package_digest, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (project_id) DO UPDATE SET
			package_digest = EXCLUDED.package_digest,
			confirmed_digest = CASE
				WHEN blink_work_plan_gate.package_digest = EXCLUDED.package_digest THEN blink_work_plan_gate.confirmed_digest
				ELSE NULL END,
			confirmed_by = CASE
				WHEN blink_work_plan_gate.package_digest = EXCLUDED.package_digest THEN blink_work_plan_gate.confirmed_by
				ELSE NULL END,
			confirmed_at = CASE
				WHEN blink_work_plan_gate.package_digest = EXCLUDED.package_digest THEN blink_work_plan_gate.confirmed_at
				ELSE NULL END,
			updated_at = NOW()
	`, projectID, digest)
	return err
}

func (g *Gates) SyncGroomGateDigest(ctx context.Context, projectID int64, digest string) error {
	if digest == "" {
		return nil
	}
	_, err := g.pool.Exec(ctx, `
		INSERT INTO blink_groom_gate (project_id, session_digest, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (project_id) DO UPDATE SET
			session_digest = EXCLUDED.session_digest,
			confirmed_digest = CASE
				WHEN blink_groom_gate.session_digest = EXCLUDED.session_digest THEN blink_groom_gate.confirmed_digest
				ELSE NULL END,
			confirmed_by = CASE
				WHEN blink_groom_gate.session_digest = EXCLUDED.session_digest THEN blink_groom_gate.confirmed_by
				ELSE NULL END,
			confirmed_at = CASE
				WHEN blink_groom_gate.session_digest = EXCLUDED.session_digest THEN blink_groom_gate.confirmed_at
				ELSE NULL END,
			updated_at = NOW()
	`, projectID, digest)
	return err
}

func (g *Gates) RevokeStakeholderConfirmation(ctx context.Context, projectID int64) error {
	_, err := g.pool.Exec(ctx, `
		UPDATE blink_stakeholder_registry
		SET confirmed_digest=NULL, confirmed_at=NULL, confirmed_by=NULL, updated_at=NOW()
		WHERE project_id=$1
	`, projectID)
	return err
}

func (g *Gates) RecordSDLStartIssue(ctx context.Context, projectID int64, issueID string) error {
	_, err := g.pool.Exec(ctx, `
		UPDATE blink_product_scope_gate SET sdlc_start_issue_id=$2, updated_at=NOW() WHERE project_id=$1
	`, projectID, issueID)
	return err
}

func (g *Gates) recordHumanDecision(ctx context.Context, projectID int64, kind, digest, actor string, payload map[string]any) error {
	var revision int64
	_ = g.pool.QueryRow(ctx, `SELECT COALESCE(revision, 0) FROM blink_project_aggregate WHERE project_id=$1`, projectID).Scan(&revision)
	if revision == 0 {
		revision = 1
	}
	payloadRaw, _ := json.Marshal(payload)
	_, err := g.pool.Exec(ctx, `
		INSERT INTO blink_human_decision (id, project_id, decision_kind, bound_digest, bound_revision, actor_email, payload_json)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7)
	`, uuid.NewString(), projectID, kind, digest, revision, actor, payloadRaw)
	return err
}

func (g *Gates) LoadBlockers(ctx context.Context, projectID int64, elig Eligibility) []map[string]any {
	blockers := make([]map[string]any, 0, 4)
	var confirmedDigest *string
	err := g.pool.QueryRow(ctx, `SELECT confirmed_digest FROM blink_stakeholder_registry WHERE project_id=$1`, projectID).Scan(&confirmedDigest)
	if err != nil || confirmedDigest == nil || *confirmedDigest == "" {
		blockers = append(blockers, map[string]any{
			"code":    "stakeholders-unconfirmed",
			"message": "Confirm the stakeholder registry before downstream gates.",
			"step":    "project-stakeholders",
		})
	}
	var scopeConfirmed *string
	_ = g.pool.QueryRow(ctx, `SELECT confirmed_digest FROM blink_product_scope_gate WHERE project_id=$1`, projectID).Scan(&scopeConfirmed)
	if elig.CompletedIndex >= indexForStep("requirements") && (scopeConfirmed == nil || *scopeConfirmed == "") {
		blockers = append(blockers, map[string]any{
			"code":    "product-scope-unconfirmed",
			"message": "Explicitly confirm product scope before grooming and planning.",
			"step":    "requirements",
		})
	}
	var groomConfirmed *string
	_ = g.pool.QueryRow(ctx, `SELECT confirmed_digest FROM blink_groom_gate WHERE project_id=$1`, projectID).Scan(&groomConfirmed)
	if elig.CompletedIndex >= indexForStep("stakeholder-qa") && (groomConfirmed == nil || *groomConfirmed == "") {
		blockers = append(blockers, map[string]any{
			"code":    "g-groom-pending",
			"message": "Acknowledge G-GROOM on Stakeholder Q&A when grooming input is ready.",
			"step":    "stakeholder-qa",
		})
	}
	var shapeConfirmed *string
	_ = g.pool.QueryRow(ctx, `SELECT confirmed_digest FROM blink_shape_gate WHERE project_id=$1`, projectID).Scan(&shapeConfirmed)
	if elig.CompletedIndex >= indexForStep("project-shape") && (shapeConfirmed == nil || *shapeConfirmed == "") {
		blockers = append(blockers, map[string]any{
			"code":    "shape-unconfirmed",
			"message": "Confirm project shape before repositories and work plan.",
			"step":    "project-shape",
		})
	}
	var architectureConfirmed *string
	_ = g.pool.QueryRow(ctx, `
		SELECT confirmed_digest FROM blink_architecture_snapshot
		WHERE project_id=$1 AND invalidated_at IS NULL
	`, projectID).Scan(&architectureConfirmed)
	if elig.CompletedIndex >= indexForStep("project-shape") && (architectureConfirmed == nil || *architectureConfirmed == "") {
		blockers = append(blockers, map[string]any{
			"code": "architecture-unconfirmed", "message": "Confirm the architecture snapshot before governed execution.", "step": "project-shape",
		})
	}
	var planConfirmed *string
	_ = g.pool.QueryRow(ctx, `SELECT confirmed_digest FROM blink_work_plan_gate WHERE project_id=$1`, projectID).Scan(&planConfirmed)
	if elig.CompletedIndex >= indexForStep("sdlc-plan") && (planConfirmed == nil || *planConfirmed == "") {
		blockers = append(blockers, map[string]any{
			"code":    "g-plan-pending",
			"message": "Acknowledge G-PLAN on the work plan before Ship.",
			"step":    "sdlc-plan",
		})
	}
	var rosterConfirmed *string
	_ = g.pool.QueryRow(ctx, `SELECT confirmed_digest FROM blink_repository_roster WHERE project_id=$1`, projectID).Scan(&rosterConfirmed)
	if elig.CompletedIndex >= indexForStep("repositories") && (rosterConfirmed == nil || *rosterConfirmed == "") {
		blockers = append(blockers, map[string]any{
			"code": "repository-roster-unconfirmed", "message": "Confirm repository roster and topology.", "step": "repositories",
		})
	}
	var unconfirmedTech int
	_ = g.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM blink_repo_technology
		WHERE project_id=$1 AND recommendation_digest <> '' AND (confirmed_digest IS NULL OR confirmed_digest <> recommendation_digest)
	`, projectID).Scan(&unconfirmedTech)
	if elig.CompletedIndex >= indexForStep("technology-per-repo") && unconfirmedTech > 0 {
		blockers = append(blockers, map[string]any{
			"code": "technology-unconfirmed", "message": "Confirm technology for each repository.", "step": "technology-per-repo",
		})
	}
	var cycleCount int
	_ = g.pool.QueryRow(ctx, `
		SELECT COALESCE(jsonb_array_length(cycles_json), 0) FROM blink_dependency_graph WHERE project_id=$1
	`, projectID).Scan(&cycleCount)
	if elig.CompletedIndex >= indexForStep("sdlc-plan") && cycleCount > 0 {
		blockers = append(blockers, map[string]any{
			"code": "graph-cycle", "message": "Resolve dependency cycles before G-PLAN.", "step": "sdlc-plan",
		})
	}
	return blockers
}

func indexForStep(step string) int {
	for i, s := range StageOrder {
		if s == step {
			return i
		}
	}
	return 0
}
