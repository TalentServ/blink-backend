package integrations

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// publicIntegration is safe to return to the browser (no tokens).
type publicIntegration struct {
	Provider     string `json:"provider"`
	Connected    bool   `json:"connected"`
	Account      string `json:"account,omitempty"`
	Detail       string `json:"detail,omitempty"`
	BaseURL      string `json:"baseUrl,omitempty"`
	Email        string `json:"email,omitempty"`
	Username     string `json:"username,omitempty"`
	Organization string `json:"organization,omitempty"`
	Workspace    string `json:"workspace,omitempty"`
	ProjectKey   string `json:"projectKey,omitempty"`
	ProjectName  string `json:"projectName,omitempty"`
	SpaceKey     string `json:"spaceKey,omitempty"`
	CloudID      string `json:"cloudId,omitempty"`
	AuthType     string `json:"authType,omitempty"`
	Scope        string `json:"scope,omitempty"` // "user" | "project"
}

func toPublic(in storedIntegration, scope string) publicIntegration {
	detail := ""
	if in.Account != "" {
		detail = "Connected as " + in.Account
	}
	return publicIntegration{
		Provider: in.Provider, Connected: strings.TrimSpace(in.AccessToken) != "",
		Account: in.Account, Detail: detail, BaseURL: in.BaseURL, Email: in.Email,
		Username: in.Username, Organization: in.Organization, Workspace: in.Workspace,
		ProjectKey: in.ProjectKey, ProjectName: in.ProjectName, SpaceKey: in.SpaceKey,
		CloudID: in.CloudID, AuthType: in.AuthType, Scope: scope,
	}
}

// ListMine returns the signed-in user's saved provider connections.
func (s *Service) ListMine(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.Header.Get("X-Blink-Owner-Email"))
	if email == "" {
		email = ownerFromAuth(r)
	}
	if email == "" {
		writeErr(w, http.StatusUnauthorized, "Sign in to continue.")
		return
	}
	rows, err := s.listUser(r.Context(), email)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]publicIntegration, 0, len(rows))
	for _, row := range rows {
		p := toPublic(row, "user")
		out = append(out, p)
		if row.Provider == "jira" {
			c := p
			c.Provider = "confluence"
			out = append(out, c)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"integrations": out})
}

// ListForProject returns project bindings, falling back to user-level credentials.
func (s *Service) ListForProject(w http.ResponseWriter, r *http.Request) {
	projectID := parseID(chi.URLParam(r, "id"))
	if projectID <= 0 {
		projectID = parseID(r.URL.Query().Get("projectId"))
	}
	if projectID <= 0 {
		writeErr(w, http.StatusBadRequest, "Project id is required.")
		return
	}
	owner := s.projectOwner(r.Context(), projectID)
	email := ownerFromAuth(r)
	if email != "" && owner != "" && !strings.EqualFold(email, owner) {
		writeErr(w, http.StatusForbidden, "You do not own this project.")
		return
	}
	if email == "" {
		email = owner
	}

	providers := []string{"jira", "github", "figma", "confluence", "bitbucket"}
	out := make([]publicIntegration, 0, len(providers))
	seen := map[string]bool{}
	for _, provider := range providers {
		stored, ok := s.load(r.Context(), projectID, provider)
		scope := "project"
		if !ok || strings.TrimSpace(stored.AccessToken) == "" {
			if email != "" {
				if u, uok := s.loadUser(r.Context(), email, provider); uok {
					stored = mergeStored(stored, u)
					stored.ProjectID = projectID
					scope = "user"
					ok = true
				}
			}
		}
		if !ok {
			continue
		}
		p := toPublic(stored, scope)
		p.Connected = strings.TrimSpace(stored.AccessToken) != ""
		out = append(out, p)
		seen[provider] = true
	}
	if seen["jira"] && !seen["confluence"] {
		if j, ok := s.load(r.Context(), projectID, "jira"); ok {
			c := toPublic(j, "project")
			c.Provider = "confluence"
			out = append(out, c)
		} else if email != "" {
			if u, ok := s.loadUser(r.Context(), email, "jira"); ok {
				c := toPublic(u, "user")
				c.Provider = "confluence"
				out = append(out, c)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projectId": projectID, "integrations": out})
}

// ApplyUserToProject copies the signed-in user's connections onto a Blink project.
func (s *Service) ApplyUserToProject(w http.ResponseWriter, r *http.Request) {
	projectID := parseID(chi.URLParam(r, "id"))
	if projectID <= 0 {
		var body struct {
			ProjectID any `json:"projectId"`
		}
		_ = readJSON(r, &body)
		projectID = parseID(body.ProjectID)
	}
	email := ownerFromAuth(r)
	if email == "" {
		writeErr(w, http.StatusUnauthorized, "Sign in to continue.")
		return
	}
	if projectID <= 0 {
		writeErr(w, http.StatusBadRequest, "Project id is required.")
		return
	}
	owner := s.projectOwner(r.Context(), projectID)
	if owner != "" && !strings.EqualFold(owner, email) {
		writeErr(w, http.StatusForbidden, "You do not own this project.")
		return
	}
	applied, err := s.ApplyUserConnections(r.Context(), email, projectID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"projectId": projectID, "applied": applied,
		"integrations": applied,
	})
}

func (s *Service) ApplyUserConnections(ctx context.Context, ownerEmail string, projectID int64) ([]publicIntegration, error) {
	rows, err := s.listUser(ctx, ownerEmail)
	if err != nil {
		return nil, err
	}
	out := make([]publicIntegration, 0, len(rows))
	for _, row := range rows {
		row.ProjectID = projectID
		if err := s.persistProjectOnly(ctx, row); err != nil {
			return nil, err
		}
		p := toPublic(row, "project")
		out = append(out, p)
		if row.Provider == "jira" {
			c := row
			c.Provider = "confluence"
			_ = s.persistProjectOnly(ctx, c)
			cp := toPublic(c, "project")
			out = append(out, cp)
		}
	}
	return out, nil
}

func (s *Service) persistUser(ctx context.Context, ownerEmail string, in storedIntegration) error {
	ownerEmail = strings.TrimSpace(strings.ToLower(ownerEmail))
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	if ownerEmail == "" || provider == "" {
		return nil
	}
	existing, _ := s.loadUser(ctx, ownerEmail, provider)
	merged := mergeStored(existing, in)
	merged.Provider = provider
	accessEnc, err := s.seal(merged.AccessToken)
	if err != nil {
		return err
	}
	refreshEnc, err := s.seal(merged.RefreshToken)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO user_integration (
			owner_email, provider, account, base_url, email, username, organization, workspace,
			project_key, project_name, space_key, cloud_id, auth_type,
			access_token_enc, refresh_token_enc, expires_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,NOW(),NOW())
		ON CONFLICT (owner_email, provider) DO UPDATE SET
			account=EXCLUDED.account, base_url=EXCLUDED.base_url, email=EXCLUDED.email,
			username=EXCLUDED.username, organization=EXCLUDED.organization, workspace=EXCLUDED.workspace,
			project_key=EXCLUDED.project_key, project_name=EXCLUDED.project_name, space_key=EXCLUDED.space_key,
			cloud_id=EXCLUDED.cloud_id, auth_type=EXCLUDED.auth_type,
			access_token_enc=EXCLUDED.access_token_enc, refresh_token_enc=EXCLUDED.refresh_token_enc,
			expires_at=EXCLUDED.expires_at, updated_at=NOW()
	`, ownerEmail, provider, nullStr(merged.Account), nullStr(merged.BaseURL), nullStr(merged.Email),
		nullStr(merged.Username), nullStr(merged.Organization), nullStr(merged.Workspace),
		nullStr(merged.ProjectKey), nullStr(merged.ProjectName), nullStr(merged.SpaceKey), nullStr(merged.CloudID),
		nullStr(merged.AuthType), nullStr(accessEnc), nullStr(refreshEnc), merged.ExpiresAt)
	return err
}

func (s *Service) loadUser(ctx context.Context, ownerEmail, provider string) (storedIntegration, bool) {
	var out storedIntegration
	ownerEmail = strings.TrimSpace(strings.ToLower(ownerEmail))
	provider = strings.ToLower(strings.TrimSpace(provider))
	if ownerEmail == "" || provider == "" {
		return out, false
	}
	var accessEnc, refreshEnc *string
	err := s.pool.QueryRow(ctx, `
		SELECT 0, provider, COALESCE(account,''), COALESCE(base_url,''), COALESCE(email,''),
			COALESCE(username,''), COALESCE(organization,''), COALESCE(workspace,''),
			COALESCE(project_key,''), COALESCE(project_name,''), COALESCE(space_key,''), COALESCE(cloud_id,''),
			COALESCE(auth_type,''), access_token_enc, refresh_token_enc, expires_at
		FROM user_integration WHERE lower(owner_email)=lower($1) AND provider=$2
	`, ownerEmail, provider).Scan(
		&out.ProjectID, &out.Provider, &out.Account, &out.BaseURL, &out.Email,
		&out.Username, &out.Organization, &out.Workspace, &out.ProjectKey, &out.ProjectName,
		&out.SpaceKey, &out.CloudID, &out.AuthType, &accessEnc, &refreshEnc, &out.ExpiresAt,
	)
	if err != nil {
		return out, false
	}
	if accessEnc != nil {
		out.AccessToken, _ = s.open(*accessEnc)
	}
	if refreshEnc != nil {
		out.RefreshToken, _ = s.open(*refreshEnc)
	}
	return out, strings.TrimSpace(out.AccessToken) != "" || out.Account != ""
}

func (s *Service) listUser(ctx context.Context, ownerEmail string) ([]storedIntegration, error) {
	ownerEmail = strings.TrimSpace(strings.ToLower(ownerEmail))
	if ownerEmail == "" {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT provider, COALESCE(account,''), COALESCE(base_url,''), COALESCE(email,''),
			COALESCE(username,''), COALESCE(organization,''), COALESCE(workspace,''),
			COALESCE(project_key,''), COALESCE(project_name,''), COALESCE(space_key,''), COALESCE(cloud_id,''),
			COALESCE(auth_type,''), access_token_enc, refresh_token_enc, expires_at
		FROM user_integration WHERE lower(owner_email)=lower($1) ORDER BY provider
	`, ownerEmail)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]storedIntegration, 0)
	for rows.Next() {
		var row storedIntegration
		var accessEnc, refreshEnc *string
		if err := rows.Scan(
			&row.Provider, &row.Account, &row.BaseURL, &row.Email, &row.Username, &row.Organization,
			&row.Workspace, &row.ProjectKey, &row.ProjectName, &row.SpaceKey, &row.CloudID, &row.AuthType,
			&accessEnc, &refreshEnc, &row.ExpiresAt,
		); err != nil {
			return nil, err
		}
		if accessEnc != nil {
			row.AccessToken, _ = s.open(*accessEnc)
		}
		if refreshEnc != nil {
			row.RefreshToken, _ = s.open(*refreshEnc)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Service) projectOwner(ctx context.Context, projectID int64) string {
	if projectID <= 0 {
		return ""
	}
	var email *string
	err := s.pool.QueryRow(ctx, `SELECT owner_email FROM project WHERE id=$1`, projectID).Scan(&email)
	if err != nil || email == nil {
		return ""
	}
	return strings.TrimSpace(*email)
}

func (s *Service) persistProjectOnly(ctx context.Context, in storedIntegration) error {
	if in.ProjectID <= 0 || strings.TrimSpace(in.Provider) == "" {
		return nil
	}
	existing, _ := s.loadProjectRow(ctx, in.ProjectID, in.Provider)
	merged := mergeStored(existing, in)
	accessEnc, err := s.seal(merged.AccessToken)
	if err != nil {
		return err
	}
	refreshEnc, err := s.seal(merged.RefreshToken)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO project_integration (
			project_id, provider, account, base_url, email, username, organization, workspace,
			project_key, project_name, space_key, cloud_id, auth_type,
			access_token_enc, refresh_token_enc, expires_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,NOW(),NOW())
		ON CONFLICT (project_id, provider) DO UPDATE SET
			account=EXCLUDED.account, base_url=EXCLUDED.base_url, email=EXCLUDED.email,
			username=EXCLUDED.username, organization=EXCLUDED.organization, workspace=EXCLUDED.workspace,
			project_key=EXCLUDED.project_key, project_name=EXCLUDED.project_name, space_key=EXCLUDED.space_key,
			cloud_id=EXCLUDED.cloud_id, auth_type=EXCLUDED.auth_type,
			access_token_enc=EXCLUDED.access_token_enc, refresh_token_enc=EXCLUDED.refresh_token_enc,
			expires_at=EXCLUDED.expires_at, updated_at=NOW()
	`, merged.ProjectID, merged.Provider, nullStr(merged.Account), nullStr(merged.BaseURL), nullStr(merged.Email),
		nullStr(merged.Username), nullStr(merged.Organization), nullStr(merged.Workspace),
		nullStr(merged.ProjectKey), nullStr(merged.ProjectName), nullStr(merged.SpaceKey), nullStr(merged.CloudID),
		nullStr(merged.AuthType), nullStr(accessEnc), nullStr(refreshEnc), merged.ExpiresAt)
	return err
}

func (s *Service) loadProjectRow(ctx context.Context, projectID int64, provider string) (storedIntegration, bool) {
	var out storedIntegration
	if projectID <= 0 || provider == "" {
		return out, false
	}
	var accessEnc, refreshEnc *string
	err := s.pool.QueryRow(ctx, `
		SELECT project_id, provider, COALESCE(account,''), COALESCE(base_url,''), COALESCE(email,''),
			COALESCE(username,''), COALESCE(organization,''), COALESCE(workspace,''),
			COALESCE(project_key,''), COALESCE(project_name,''), COALESCE(space_key,''), COALESCE(cloud_id,''),
			COALESCE(auth_type,''), access_token_enc, refresh_token_enc, expires_at
		FROM project_integration WHERE project_id=$1 AND provider=$2
	`, projectID, strings.ToLower(provider)).Scan(
		&out.ProjectID, &out.Provider, &out.Account, &out.BaseURL, &out.Email,
		&out.Username, &out.Organization, &out.Workspace, &out.ProjectKey, &out.ProjectName,
		&out.SpaceKey, &out.CloudID, &out.AuthType, &accessEnc, &refreshEnc, &out.ExpiresAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return out, false
		}
		return out, false
	}
	if accessEnc != nil {
		out.AccessToken, _ = s.open(*accessEnc)
	}
	if refreshEnc != nil {
		out.RefreshToken, _ = s.open(*refreshEnc)
	}
	return out, true
}

// ownerFromAuth extracts email from Bearer session via a lightweight DB lookup of token hash —
// prefer X-Blink-Owner-Email set by httpapi when available.
func ownerFromAuth(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Blink-Owner-Email")); v != "" {
		return v
	}
	return ""
}

// IsConnected reports whether the project/user has a usable token for provider.
func (s *Service) IsConnected(ctx context.Context, projectID int64, ownerEmail, provider string) bool {
	stored, ok := s.load(ctx, projectID, provider)
	if ok && strings.TrimSpace(stored.AccessToken) != "" {
		return true
	}
	if ownerEmail != "" {
		if u, uok := s.loadUser(ctx, ownerEmail, provider); uok && strings.TrimSpace(u.AccessToken) != "" {
			return true
		}
	}
	return false
}

// ConnectionSnapshot returns non-secret binding fields for chat context.
func (s *Service) ConnectionSnapshot(ctx context.Context, projectID int64, ownerEmail, provider string) map[string]any {
	stored, ok := s.load(ctx, projectID, provider)
	if (!ok || strings.TrimSpace(stored.AccessToken) == "") && ownerEmail != "" {
		if u, uok := s.loadUser(ctx, ownerEmail, provider); uok {
			stored = mergeStored(stored, u)
			ok = true
		}
	}
	if !ok {
		return nil
	}
	return map[string]any{
		"provider":     provider,
		"account":      stored.Account,
		"organization": stored.Organization,
		"projectKey":   stored.ProjectKey,
		"projectName":  stored.ProjectName,
		"baseUrl":      stored.BaseURL,
		"cloudId":      stored.CloudID,
	}
}

