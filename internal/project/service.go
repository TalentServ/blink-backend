package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/google/uuid"
	"github.com/nisha-ts-40599/blink-backend/internal/roles"
)

type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

type StakeholderRequest struct {
	RoleCode string `json:"roleCode"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

type ProjectRequest struct {
	ProjectType             string               `json:"projectType"`
	ProjectName             string               `json:"projectName"`
	Description             string               `json:"description"`
	Stakeholders            []StakeholderRequest `json:"stakeholders"`
	WizardStep              *string              `json:"wizardStep"`
	WizardCompletedThrough  *int                 `json:"wizardCompletedThrough"`
	WizardState             json.RawMessage      `json:"wizardState"`
}

type StakeholderResponse struct {
	ID       int64  `json:"id"`
	RoleCode string `json:"roleCode"`
	RoleName string `json:"roleName"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

type ProjectResponse struct {
	ID                     int64                 `json:"id"`
	ProjectName            string                `json:"projectName"`
	ProjectCode            string                `json:"projectCode"`
	Description            string                `json:"description,omitempty"`
	Status                 *string               `json:"status,omitempty"`
	ProjectType            string                `json:"projectType"`
	Stakeholders           []StakeholderResponse `json:"stakeholders"`
	WorkspaceKey           *string               `json:"workspaceKey,omitempty"`
	WorkspaceURL           *string               `json:"workspaceUrl,omitempty"`
	WorkspaceStatus        *string               `json:"workspaceStatus,omitempty"`
	SodWarnings            []string              `json:"sodWarnings,omitempty"`
	NextCommand            string                `json:"nextCommand,omitempty"`
	GovernanceStatus       string                `json:"governanceStatus,omitempty"`
	OwnerEmail             *string               `json:"ownerEmail,omitempty"`
	WizardStep             *string               `json:"wizardStep,omitempty"`
	WizardCompletedThrough *int                  `json:"wizardCompletedThrough,omitempty"`
	WizardState            json.RawMessage       `json:"wizardState,omitempty"`
	WizardUpdatedAt        *string               `json:"wizardUpdatedAt,omitempty"`
}

func (s *Service) Create(ctx context.Context, req ProjectRequest, ownerEmail string) (*ProjectResponse, error) {
	if err := validateProject(req); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	ptype := normalizeType(req.ProjectType)
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO project (project_name, project_type, description, owner_email, wizard_step, wizard_completed_through, wizard_state_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NOW(),NOW()) RETURNING id
	`, strings.TrimSpace(req.ProjectName), ptype, strings.TrimSpace(req.Description), nullIfEmpty(ownerEmail),
		req.WizardStep, req.WizardCompletedThrough, nullJSON(req.WizardState)).Scan(&id)
	if err != nil {
		return nil, err
	}
	if err := s.replaceStakeholders(ctx, tx, id, req.Stakeholders); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *Service) Update(ctx context.Context, id int64, req ProjectRequest, ownerEmail string) (*ProjectResponse, error) {
	if err := validateProject(req); err != nil {
		return nil, err
	}
	existing, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing.OwnerEmail != nil && ownerEmail != "" && !strings.EqualFold(*existing.OwnerEmail, ownerEmail) {
		return nil, fmt.Errorf("forbidden: not project owner")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
		UPDATE project SET project_name=$2, project_type=$3, description=$4,
			owner_email=COALESCE(NULLIF($5,''), owner_email),
			wizard_step=$6, wizard_completed_through=$7, wizard_state_json=$8, updated_at=NOW()
		WHERE id=$1
	`, id, strings.TrimSpace(req.ProjectName), normalizeType(req.ProjectType), strings.TrimSpace(req.Description),
		ownerEmail, req.WizardStep, req.WizardCompletedThrough, nullJSON(req.WizardState))
	if err != nil {
		return nil, err
	}
	if err := s.replaceStakeholders(ctx, tx, id, req.Stakeholders); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *Service) Get(ctx context.Context, id int64) (*ProjectResponse, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, project_name, COALESCE(project_type,''), COALESCE(description,''), owner_email,
			wizard_step, wizard_completed_through, wizard_state_json, updated_at
		FROM project WHERE id=$1
	`, id)
	var resp ProjectResponse
	var owner *string
	var step *string
	var through *int
	var state []byte
	var updated time.Time
	var ptype string
	err := row.Scan(&resp.ID, &resp.ProjectName, &ptype, &resp.Description, &owner, &step, &through, &state, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("project not found")
	}
	if err != nil {
		return nil, err
	}
	resp.ProjectType = denormType(ptype)
	resp.ProjectCode = Slug(resp.ProjectName)
	resp.OwnerEmail = owner
	resp.WizardStep = step
	resp.WizardCompletedThrough = through
	if len(state) > 0 {
		resp.WizardState = json.RawMessage(state)
	}
	ts := updated.Format("2006-01-02T15:04:05")
	resp.WizardUpdatedAt = &ts
	resp.NextCommand = "/plan-product-scope"
	resp.GovernanceStatus = "idle"
	resp.Stakeholders, err = s.listStakeholders(ctx, id)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (s *Service) List(ctx context.Context) ([]ProjectResponse, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM project ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectResponse
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		p, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Service) ListMine(ctx context.Context, ownerEmail string) ([]ProjectResponse, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM project WHERE lower(owner_email)=lower($1) ORDER BY id DESC`, ownerEmail)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectResponse
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		p, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Service) LatestForOwner(ctx context.Context, ownerEmail string) (*ProjectResponse, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM project WHERE lower(owner_email)=lower($1) ORDER BY updated_at DESC NULLS LAST, id DESC LIMIT 1
	`, ownerEmail).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("project not found")
	}
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *Service) RequireOwned(ctx context.Context, id int64, ownerEmail string) (*ProjectResponse, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.OwnerEmail != nil && ownerEmail != "" && !strings.EqualFold(*p.OwnerEmail, ownerEmail) {
		return nil, fmt.Errorf("forbidden: not project owner")
	}
	return p, nil
}

func (s *Service) replaceStakeholders(ctx context.Context, tx pgx.Tx, projectID int64, stakeholders []StakeholderRequest) error {
	if _, err := tx.Exec(ctx, `DELETE FROM stakeholder WHERE project_id=$1`, projectID); err != nil {
		return err
	}
	for _, st := range stakeholders {
		code := "blink_" + uuid.NewString()
		name := strings.TrimSpace(st.Name)
		_, err := tx.Exec(ctx, `
			INSERT INTO stakeholder (project_id, role_code, person_name, person_email, code, name, category, active, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,'BUSINESS',true,NOW(),NOW())
		`, projectID, roles.NormalizeRoleCode(st.RoleCode), name, strings.TrimSpace(st.Email), code, name)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) listStakeholders(ctx context.Context, projectID int64) ([]StakeholderResponse, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, COALESCE(role_code,''), COALESCE(person_name,''), COALESCE(person_email,'')
		FROM stakeholder WHERE project_id=$1 ORDER BY id
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StakeholderResponse
	for rows.Next() {
		var st StakeholderResponse
		if err := rows.Scan(&st.ID, &st.RoleCode, &st.Name, &st.Email); err != nil {
			return nil, err
		}
		st.RoleCode = roles.NormalizeRoleCode(st.RoleCode)
		st.RoleName = roles.RoleDisplayName(st.RoleCode)
		out = append(out, st)
	}
	return out, rows.Err()
}

func validateProject(req ProjectRequest) error {
	if strings.TrimSpace(req.ProjectName) == "" {
		return fmt.Errorf("projectName is required")
	}
	if strings.TrimSpace(req.Description) == "" {
		return fmt.Errorf("description is required")
	}
	if len(req.Stakeholders) == 0 {
		return fmt.Errorf("stakeholders are required")
	}
	t := strings.ToLower(strings.TrimSpace(req.ProjectType))
	if t != "new" && t != "existing" {
		return fmt.Errorf("projectType must be new or existing")
	}
	return nil
}

func normalizeType(t string) string {
	if strings.EqualFold(strings.TrimSpace(t), "existing") {
		return "EXISTING"
	}
	return "NEW"
}

func denormType(t string) string {
	if strings.EqualFold(t, "EXISTING") {
		return "existing"
	}
	return "new"
}

func nullIfEmpty(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

func nullJSON(raw json.RawMessage) *string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	s := string(raw)
	return &s
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func Slug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := nonSlug.ReplaceAllString(b.String(), "_")
	out = strings.Trim(out, "_")
	if out == "" {
		return "project"
	}
	if len(out) > 80 {
		out = strings.TrimRight(out[:80], "_")
	}
	return out
}

func Folder(name string, id *int64) string {
	slug := Slug(name)
	if strings.HasSuffix(slug, "_workspace") {
		slug = strings.TrimSuffix(slug, "_workspace")
		slug = strings.TrimRight(slug, "_")
		if slug == "" {
			slug = "project"
		}
	}
	if id != nil && *id > 0 {
		return fmt.Sprintf("%s_%d_workspace", slug, *id)
	}
	return slug + "_workspace"
}

func IsBlinkWorkspaceFolder(folder string) bool {
	folder = strings.TrimSpace(folder)
	if folder == "" || strings.ContainsAny(folder, `/\`) || strings.Contains(folder, "..") {
		return false
	}
	ok, _ := regexp.MatchString(`^[a-z0-9][a-z0-9_]{0,120}_workspace$`, folder)
	return ok
}
