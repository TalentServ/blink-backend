package canonical

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type RepositoryRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
	HTMLURL string `json:"htmlUrl,omitempty"`
	Owner   string `json:"owner,omitempty"`
}

type RepoTechnologyRow struct {
	RepoID    string `json:"repoId"`
	Language  string `json:"language"`
	Framework string `json:"framework"`
	Database  string `json:"database"`
	BuildTool string `json:"buildTool"`
	Status    string `json:"status"`
}

type GraphEdge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Kind     string `json:"kind"`
	Accepted bool   `json:"accepted"`
}

type GraphView struct {
	Revision       int64       `json:"revision"`
	GraphDigest    string      `json:"graphDigest"`
	Cycles         [][]string  `json:"cycles"`
	Frontier       []string    `json:"frontier"`
	ExecutionOrder []string    `json:"executionOrder"`
	Requirement    []GraphEdge `json:"requirementEdges"`
	Technical      []GraphEdge `json:"technicalEdges"`
	Effective      []GraphEdge `json:"effectiveEdges"`
}

type ShipSessionView struct {
	ID        string `json:"id"`
	ScopeKind string `json:"scopeKind"`
	Substage  string `json:"substage"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updatedAt"`
}

type RequirementRevisionView struct {
	ID            int64  `json:"id"`
	RevisionNo    int    `json:"revisionNo"`
	ViewKind      string `json:"viewKind"`
	ContentDigest string `json:"contentDigest"`
	CreatedAt     string `json:"createdAt"`
}

func parseRepositories(root wizardRoot) ([]RepositoryRow, string) {
	topology := wizardString(root, "topology")
	rows := make([]RepositoryRow, 0)
	for _, item := range wizardArray(root, "repositories") {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		name, _ := m["name"].(string)
		if id == "" && name == "" {
			continue
		}
		rows = append(rows, RepositoryRow{
			ID:      id,
			Name:    name,
			Purpose: strField(m, "purpose"),
			HTMLURL: strField(m, "htmlUrl"),
			Owner:   strField(m, "owner"),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, topology
}

func parseRepoTechnologies(root wizardRoot) []RepoTechnologyRow {
	out := make([]RepoTechnologyRow, 0)
	for _, item := range wizardArray(root, "repoTechnologies") {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, RepoTechnologyRow{
			RepoID:    strField(m, "repoId"),
			Language:  strField(m, "language"),
			Framework: strField(m, "framework"),
			Database:  strField(m, "database"),
			BuildTool: strField(m, "buildTool"),
			Status:    strField(m, "status"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RepoID < out[j].RepoID })
	return out
}

func strField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

func (s *Service) SyncRepositoryRoster(ctx context.Context, projectID int64, wizardState []byte) error {
	root := parseWizard(wizardState)
	repos, topology := parseRepositories(root)
	payload, _ := json.Marshal(map[string]any{"topology": topology, "repos": repos})
	digest := DigestBytes(payload)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO blink_repository_roster (project_id, revision, topology, roster_json, roster_digest, updated_at)
		VALUES ($1, 1, $2, $3, $4, NOW())
		ON CONFLICT (project_id) DO UPDATE SET
			revision = blink_repository_roster.revision + 1,
			topology = EXCLUDED.topology,
			roster_json = EXCLUDED.roster_json,
			roster_digest = EXCLUDED.roster_digest,
			confirmed_digest = CASE
				WHEN blink_repository_roster.roster_digest = EXCLUDED.roster_digest THEN blink_repository_roster.confirmed_digest
				ELSE NULL END,
			confirmed_at = CASE
				WHEN blink_repository_roster.roster_digest = EXCLUDED.roster_digest THEN blink_repository_roster.confirmed_at
				ELSE NULL END,
			updated_at = NOW()
	`, projectID, topology, payload, digest)
	return err
}

func (s *Service) SyncRepoTechnologies(ctx context.Context, projectID int64, wizardState []byte) error {
	root := parseWizard(wizardState)
	rows := parseRepoTechnologies(root)
	for _, row := range rows {
		if row.RepoID == "" {
			continue
		}
		raw, _ := json.Marshal(row)
		digest := DigestBytes(raw)
		_, err := s.pool.Exec(ctx, `
			INSERT INTO blink_repo_technology (project_id, repo_id, recommendation_json, recommendation_digest, updated_at)
			VALUES ($1,$2,$3,$4,NOW())
			ON CONFLICT (project_id, repo_id) DO UPDATE SET
				recommendation_json = EXCLUDED.recommendation_json,
				recommendation_digest = EXCLUDED.recommendation_digest,
				confirmed_digest = CASE
					WHEN blink_repo_technology.recommendation_digest = EXCLUDED.recommendation_digest
					THEN blink_repo_technology.confirmed_digest ELSE NULL END,
				updated_at = NOW()
		`, projectID, row.RepoID, raw, digest)
		if err != nil {
			return err
		}
	}
	return nil
}

func deriveGraphEdges(root wizardRoot) (req []GraphEdge, tech []GraphEdge) {
	plan, _ := root["technicalPlan"].(map[string]any)
	steps, _ := plan["steps"].([]any)
	prev := ""
	for i, item := range steps {
		title := fmt.Sprintf("step-%d", i+1)
		if m, ok := item.(map[string]any); ok {
			if t := strField(m, "title"); t != "" {
				title = t
			}
		}
		if prev != "" {
			tech = append(tech, GraphEdge{From: prev, To: title, Kind: "technical", Accepted: true})
		}
		prev = title
	}
	scope, _ := root["productScope"].(map[string]any)
	stories, _ := scope["stories"].([]any)
	for i, item := range stories {
		id := fmt.Sprintf("story-%d", i+1)
		if m, ok := item.(map[string]any); ok {
			if v := strField(m, "id"); v != "" {
				id = v
			}
		}
		if i > 0 {
			prevID := fmt.Sprintf("story-%d", i)
			if m, ok := stories[i-1].(map[string]any); ok {
				if v := strField(m, "id"); v != "" {
					prevID = v
				}
			}
			req = append(req, GraphEdge{
				From: prevID, To: id, Kind: "requirement",
				Accepted: wizardBool(root, "planAcknowledged") || wizardBool(root, "shipPlanAcknowledged"),
			})
		}
	}
	return req, tech
}

func combineEdges(parts ...[]GraphEdge) []GraphEdge {
	out := make([]GraphEdge, 0)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func nodeListFromEdges(edges []GraphEdge) []string {
	nodes := map[string]struct{}{}
	for _, e := range edges {
		if !e.Accepted {
			continue
		}
		nodes[e.From] = struct{}{}
		nodes[e.To] = struct{}{}
	}
	out := make([]string, 0, len(nodes))
	for n := range nodes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func detectCycles(edges []GraphEdge) [][]string {
	adj := map[string][]string{}
	for _, e := range edges {
		if !e.Accepted {
			continue
		}
		adj[e.From] = append(adj[e.From], e.To)
	}
	cycles := make([][]string, 0)
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var path []string
	var dfs func(string)
	dfs = func(n string) {
		if visiting[n] {
			cycles = append(cycles, append(append([]string{}, path...), n))
			return
		}
		if visited[n] {
			return
		}
		visiting[n] = true
		path = append(path, n)
		for _, next := range adj[n] {
			dfs(next)
		}
		path = path[:len(path)-1]
		delete(visiting, n)
		visited[n] = true
	}
	for _, n := range nodeListFromEdges(edges) {
		dfs(n)
	}
	return cycles
}

func computeFrontier(edges []GraphEdge) []string {
	incoming := map[string]int{}
	for _, e := range edges {
		if !e.Accepted {
			continue
		}
		incoming[e.To]++
	}
	nodes := nodeListFromEdges(edges)
	out := make([]string, 0)
	for _, n := range nodes {
		if incoming[n] == 0 {
			out = append(out, n)
		}
	}
	return out
}

func (s *Service) RecomputeGraph(ctx context.Context, projectID int64, wizardState []byte) (*GraphView, error) {
	root := parseWizard(wizardState)
	req, tech := deriveGraphEdges(root)
	effective := combineEdges(req, tech)
	cycles := detectCycles(effective)
	frontier := computeFrontier(effective)
	order := append([]string{}, frontier...)
	payload, _ := json.Marshal(map[string]any{
		"requirement": req, "technical": tech, "effective": effective,
		"cycles": cycles, "frontier": frontier, "order": order,
	})
	digest := DigestBytes(payload)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO blink_dependency_graph (
			project_id, revision, requirement_edges_json, technical_edges_json, effective_edges_json,
			graph_digest, cycles_json, frontier_json, execution_order_json, updated_at)
		VALUES ($1,1,$2,$3,$4,$5,$6,$7,$8,NOW())
		ON CONFLICT (project_id) DO UPDATE SET
			revision = blink_dependency_graph.revision + 1,
			requirement_edges_json = EXCLUDED.requirement_edges_json,
			technical_edges_json = EXCLUDED.technical_edges_json,
			effective_edges_json = EXCLUDED.effective_edges_json,
			graph_digest = EXCLUDED.graph_digest,
			cycles_json = EXCLUDED.cycles_json,
			frontier_json = EXCLUDED.frontier_json,
			execution_order_json = EXCLUDED.execution_order_json,
			updated_at = NOW()
	`, projectID, mustJSON(req), mustJSON(tech), mustJSON(effective), digest, mustJSON(cycles), mustJSON(frontier), mustJSON(order))
	if err != nil {
		return nil, err
	}
	var revision int64
	_ = s.pool.QueryRow(ctx, `SELECT revision FROM blink_dependency_graph WHERE project_id=$1`, projectID).Scan(&revision)
	return &GraphView{
		Revision: revision, GraphDigest: digest, Cycles: cycles, Frontier: frontier, ExecutionOrder: order,
		Requirement: req, Technical: tech, Effective: effective,
	}, nil
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

func (s *Service) RecordRepositoryRosterConfirmation(ctx context.Context, projectID int64, digest, actor string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE blink_repository_roster
		SET confirmed_digest=$2, confirmed_by=NULLIF($3,''), confirmed_at=NOW(), updated_at=NOW()
		WHERE project_id=$1 AND roster_digest=$2
	`, projectID, digest, actor)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("roster digest mismatch or roster not synced")
	}
	return s.gates.recordHumanDecision(ctx, projectID, "repository-roster", digest, actor, map[string]any{"rosterDigest": digest})
}

func (s *Service) ConfirmCurrentRepositoryRoster(ctx context.Context, projectID int64, actor string) error {
	var digest string
	err := s.pool.QueryRow(ctx, `
		SELECT roster_digest FROM blink_repository_roster WHERE project_id=$1
	`, projectID).Scan(&digest)
	if errors.Is(err, pgx.ErrNoRows) || digest == "" {
		return fmt.Errorf("roster not synced; save the project first")
	}
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE blink_repository_roster
		SET confirmed_digest=roster_digest, confirmed_by=NULLIF($2,''), confirmed_at=NOW(), updated_at=NOW()
		WHERE project_id=$1 AND roster_digest <> ''
	`, projectID, actor)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("roster not synced")
	}
	return s.gates.recordHumanDecision(ctx, projectID, "repository-roster", digest, actor, map[string]any{"rosterDigest": digest})
}

func (s *Service) ConfirmRepoTechnology(ctx context.Context, projectID int64, repoID, digest, actor string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE blink_repo_technology
		SET confirmed_digest=$3, confirmed_by=NULLIF($4,''), confirmed_at=NOW(), updated_at=NOW()
		WHERE project_id=$1 AND repo_id=$2 AND recommendation_digest=$3
	`, projectID, repoID, digest, actor)
	return err
}

func (s *Service) ConfirmAllRepoTechnologies(ctx context.Context, projectID int64, actor string) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE blink_repo_technology
		SET confirmed_digest=recommendation_digest, confirmed_by=NULLIF($2,''), confirmed_at=NOW(), updated_at=NOW()
		WHERE project_id=$1 AND recommendation_digest <> '' AND (confirmed_digest IS NULL OR confirmed_digest <> recommendation_digest)
	`, projectID, actor)
	if err != nil {
		return 0, err
	}
	n := int(tag.RowsAffected())
	if n > 0 {
		_ = s.gates.recordHumanDecision(ctx, projectID, "repo-technology-all", "", actor, map[string]any{"confirmedCount": n})
	}
	return n, nil
}

func (s *Service) GetGraph(ctx context.Context, projectID int64) (*GraphView, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT revision, graph_digest, requirement_edges_json, technical_edges_json, effective_edges_json,
			cycles_json, frontier_json, execution_order_json
		FROM blink_dependency_graph WHERE project_id=$1
	`, projectID)
	var view GraphView
	var req, tech, eff, cycles, frontier, order []byte
	err := row.Scan(&view.Revision, &view.GraphDigest, &req, &tech, &eff, &cycles, &frontier, &order)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("graph not computed")
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(req, &view.Requirement)
	_ = json.Unmarshal(tech, &view.Technical)
	_ = json.Unmarshal(eff, &view.Effective)
	_ = json.Unmarshal(cycles, &view.Cycles)
	_ = json.Unmarshal(frontier, &view.Frontier)
	_ = json.Unmarshal(order, &view.ExecutionOrder)
	return &view, nil
}

func (s *Service) ListRequirementRevisions(ctx context.Context, projectID int64, viewKind string, limit int) ([]RequirementRevisionView, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, revision_no, view_kind, content_digest, created_at
		FROM blink_requirement_revision
		WHERE project_id=$1 AND ($2='' OR view_kind=$2)
		ORDER BY id DESC LIMIT $3
	`, projectID, viewKind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RequirementRevisionView, 0)
	for rows.Next() {
		var v RequirementRevisionView
		var created time.Time
		if err := rows.Scan(&v.ID, &v.RevisionNo, &v.ViewKind, &v.ContentDigest, &created); err != nil {
			return nil, err
		}
		v.CreatedAt = created.UTC().Format(time.RFC3339)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Service) RecordRequirementViews(ctx context.Context, projectID int64, wizardState []byte) error {
	root := parseWizard(wizardState)
	text := wizardString(root, "requirementsText")
	groom := wizardString(root, "groomDraft")
	desc := wizardString(root, "description")
	views := map[string]string{
		"source": text,
		"brd":    groom,
		"prd":    text,
		"frd":    desc,
	}
	for kind, content := range views {
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		digest := DigestBytes([]byte(content))
		if err := s.AppendRequirementRevision(ctx, projectID, kind, digest, map[string]any{
			"length": len(content),
			"kind":   kind,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) EnsureShipSession(ctx context.Context, projectID int64, substage, actor string) (*ShipSessionView, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT id::text FROM blink_ship_session
		WHERE project_id=$1 AND status='active'
		ORDER BY updated_at DESC LIMIT 1
	`, projectID).Scan(&id)
	if err == nil {
		_, _ = s.pool.Exec(ctx, `UPDATE blink_ship_session SET substage=$2, updated_at=NOW() WHERE id=$1`, id, substage)
		return s.getShipSession(ctx, id)
	}
	id = uuid.NewString()
	scopeRef := s.pickScopeRef(ctx, projectID)
	_, err = s.pool.Exec(ctx, `
		INSERT INTO blink_ship_session (id, project_id, scope_kind, scope_ref, status, substage, created_by)
		VALUES ($1,$2,'PROJECT',NULLIF($5,''),'active',$3,NULLIF($4,''))
	`, id, projectID, substage, actor, scopeRef)
	if err != nil {
		return nil, err
	}
	return s.getShipSession(ctx, id)
}

func (s *Service) RecordShipStep(ctx context.Context, sessionID, stepKind, idempotencyKey string, payload map[string]any) error {
	if idempotencyKey != "" {
		var exists int64
		err := s.pool.QueryRow(ctx, `
			SELECT id FROM blink_ship_step WHERE session_id=$1 AND idempotency_key=$2
		`, sessionID, idempotencyKey).Scan(&exists)
		if err == nil {
			return nil
		}
	}
	raw, _ := json.Marshal(payload)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO blink_ship_step (session_id, step_kind, status, payload_json, idempotency_key, finished_at)
		VALUES ($1,$2,'completed',$3,NULLIF($4,''),NOW())
	`, sessionID, stepKind, raw, idempotencyKey)
	return err
}

func (s *Service) getShipSession(ctx context.Context, id string) (*ShipSessionView, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id::text, scope_kind, substage, status, updated_at FROM blink_ship_session WHERE id=$1
	`, id)
	var v ShipSessionView
	var updated time.Time
	if err := row.Scan(&v.ID, &v.ScopeKind, &v.Substage, &v.Status, &updated); err != nil {
		return nil, err
	}
	v.UpdatedAt = updated.UTC().Format(time.RFC3339)
	return &v, nil
}

func (s *Service) GetActiveShipSession(ctx context.Context, projectID int64) (*ShipSessionView, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT id::text FROM blink_ship_session WHERE project_id=$1 AND status='active' ORDER BY updated_at DESC LIMIT 1
	`, projectID).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.getShipSession(ctx, id)
}
