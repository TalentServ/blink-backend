package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nisha-ts-40599/blink-backend/internal/config"
	"github.com/nisha-ts-40599/blink-backend/internal/crypto"
)

var (
	sourceEpicRe  = regexp.MustCompile(`(?m)^Source epic:\s*(\S+)\s*$`)
	sourceStoryRe = regexp.MustCompile(`(?m)^Source story:\s*(\S+)\s*$`)
	figmaTeamRe   = regexp.MustCompile(`(?:/files)?/team/(\d+)`)
)

type Service struct {
	pool              *pgxpool.Pool
	cfg               config.Config
	box               *crypto.Box
	http              *http.Client
	projectionEnqueue projectionEnqueueFunc
}

func New(pool *pgxpool.Pool, cfg config.Config, box *crypto.Box) *Service {
	return &Service{
		pool: pool,
		cfg:  cfg,
		box:  box,
		http: &http.Client{Timeout: 45 * time.Second},
	}
}

type storedIntegration struct {
	ProjectID    int64
	Provider     string
	Account      string
	BaseURL      string
	Email        string
	Username     string
	Organization string
	Workspace    string
	ProjectKey   string
	ProjectName  string
	SpaceKey     string
	CloudID      string
	AuthType     string
	AccessToken  string
	RefreshToken string
	ExpiresAt    *time.Time
}

type jiraCtx struct {
	APIBase    string
	BrowseBase string
	Headers    map[string]string
}

// --- HTTP handlers ---

func (s *Service) Connect(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	provider := strings.ToLower(strings.TrimSpace(str(req["provider"])))
	projectID := parseID(req["projectId"])
	token := str(req["token"])
	if provider == "" || token == "" {
		writeErr(w, http.StatusBadRequest, "provider and token are required.")
		return
	}
	account := firstNonEmpty(str(req["username"]), str(req["email"]), "Connected account")
	authType := "token"
	baseURL := str(req["baseUrl"])
	cloudID := ""
	var projects []any

	switch provider {
	case "jira", "confluence":
		if baseURL == "" {
			writeErr(w, http.StatusBadRequest, "baseUrl is required for Atlassian token connect.")
			return
		}
		email := str(req["email"])
		if email == "" {
			writeErr(w, http.StatusBadRequest, "email is required for Atlassian API token.")
			return
		}
		account = email
	case "github", "figma", "bitbucket":
		// token-only providers
	default:
		writeErr(w, http.StatusBadRequest, "Unsupported provider.")
		return
	}

	if err := s.persistOwned(r.Context(), ownerFromAuth(r), storedIntegration{
		ProjectID: projectID, Provider: provider, Account: account, BaseURL: baseURL,
		Email: str(req["email"]), Username: str(req["username"]), Organization: str(req["organization"]),
		Workspace: str(req["workspace"]), ProjectKey: str(req["projectKey"]), ProjectName: str(req["projectName"]),
		SpaceKey: str(req["spaceKey"]), CloudID: cloudID, AuthType: authType, AccessToken: token,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connected": true, "provider": provider, "account": account,
		"detail": "Connected " + provider, "projectKey": str(req["projectKey"]),
		"projectName": str(req["projectName"]), "baseUrl": baseURL, "cloudId": cloudID,
		"authType": authType, "accessToken": nil, "projects": projects,
	})
}

func (s *Service) CreateRepositories(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider     string `json:"provider"`
		ProjectID    any    `json:"projectId"`
		Organization string `json:"organization"`
		Token        string `json:"token"`
		Repositories []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"repositories"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.ToLower(strings.TrimSpace(req.Provider)) != "github" {
		writeErr(w, http.StatusBadRequest, "Only GitHub repository creation is supported.")
		return
	}
	stored, _ := s.load(r.Context(), parseID(req.ProjectID), "github")
	org := firstNonEmpty(req.Organization, stored.Organization)
	token := firstNonEmpty(req.Token, stored.AccessToken)
	if token == "" || org == "" {
		if owner := ownerFromAuth(r); owner != "" {
			if u, ok := s.loadUser(r.Context(), owner, "github"); ok {
				token = firstNonEmpty(token, u.AccessToken)
				org = firstNonEmpty(org, u.Organization)
			}
		}
	}
	if token == "" {
		writeErr(w, http.StatusBadRequest, "Connect GitHub on Integrations first (sign in with GitHub).")
		return
	}
	endpoint := "https://api.github.com/user/repos"
	if org != "" {
		endpoint = "https://api.github.com/orgs/" + url.PathEscape(org) + "/repos"
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/vnd.github+json",
		"Content-Type":  "application/json",
	}
	results := make([]map[string]any, 0, len(req.Repositories))
	for _, spec := range req.Repositories {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		body, _ := json.Marshal(map[string]any{
			"name":        name,
			"description": spec.Description,
			"private":     true,
			"auto_init":   true,
		})
		status, resp, err := s.do(r.Context(), http.MethodPost, endpoint, headers, body)
		if err != nil {
			results = append(results, map[string]any{
				"name": name, "status": "failed", "htmlUrl": "", "message": err.Error(),
			})
			continue
		}
		switch {
		case status == 422 && strings.Contains(strings.ToLower(resp), "already_exists"):
			htmlURL := jsonText(resp, "html_url")
			if htmlURL == "" && org != "" {
				htmlURL = "https://github.com/" + org + "/" + name
			}
			results = append(results, map[string]any{
				"name": name, "status": "exists", "htmlUrl": htmlURL, "message": "Repository already exists.",
			})
		case status >= 200 && status < 300:
			htmlURL := jsonText(resp, "html_url")
			results = append(results, map[string]any{
				"name": name, "status": "created", "htmlUrl": htmlURL, "message": "Created " + firstNonEmpty(htmlURL, name),
			})
		default:
			results = append(results, map[string]any{
				"name": name, "status": "failed", "htmlUrl": "", "message": fmt.Sprintf("GitHub HTTP %d", status),
			})
		}
	}
	projectID := parseID(req.ProjectID)
	if projectID > 0 {
		created := 0
		for _, row := range results {
			if str(row["status"]) == "created" || str(row["status"]) == "exists" {
				created++
			}
		}
		if created > 0 {
			s.recordProjection(r.Context(), projectID, "github", "repositories.create", map[string]any{
				"organization": org,
				"repositories": results,
			})
		}
	}
	// Match Java CreateRepositoriesResponse + blink_ui contract.
	writeJSON(w, http.StatusOK, map[string]any{"provider": "github", "repositories": results})
}

func (s *Service) JiraOAuthURL(w http.ResponseWriter, r *http.Request) {
	_ = optionalAuth(r) // session optional
	clientID := strings.TrimSpace(s.cfg.JiraClientID)
	if clientID == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false, "url": nil, "clientId": nil, "redirectUri": nil,
			"message": "Jira OAuth is not configured. Set BLINK_JIRA_CLIENT_ID and BLINK_JIRA_CLIENT_SECRET, or connect using an API token.",
		})
		return
	}
	redirect := s.resolveRedirect("jira", r.URL.Query().Get("redirectUri"), publicAPIBase(r))
	scopes := firstNonEmpty(s.cfg.JiraScopes, "read:jira-work write:jira-work read:jira-user read:me offline_access")
	u := "https://auth.atlassian.com/authorize" +
		"?audience=api.atlassian.com" +
		"&client_id=" + url.QueryEscape(clientID) +
		"&scope=" + url.QueryEscape(scopes) +
		"&redirect_uri=" + url.QueryEscape(redirect) +
		"&state=" + url.QueryEscape(uuid.NewString()) +
		"&response_type=code&prompt=consent"
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": true, "url": u, "clientId": clientID, "redirectUri": redirect,
		"message": "Ready for Atlassian authorization.",
	})
}

func (s *Service) JiraOAuthExchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code        string `json:"code"`
		RedirectURI string `json:"redirectUri"`
		ProjectID   any    `json:"projectId"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	clientID := strings.TrimSpace(s.cfg.JiraClientID)
	clientSecret := strings.TrimSpace(s.cfg.JiraClientSecret)
	if clientID == "" || clientSecret == "" {
		writeErr(w, http.StatusBadRequest, "Jira OAuth credentials (client ID/secret) are not configured on the server.")
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		writeErr(w, http.StatusBadRequest, "Authorization code is required.")
		return
	}
	redirect := s.resolveRedirect("jira", req.RedirectURI, "")
	tokenBody, _ := json.Marshal(map[string]string{
		"grant_type": "authorization_code", "client_id": clientID, "client_secret": clientSecret,
		"code": code, "redirect_uri": redirect,
	})
	status, body, err := s.do(r.Context(), http.MethodPost, "https://auth.atlassian.com/oauth/token",
		map[string]string{"Accept": "application/json", "Content-Type": "application/json"}, tokenBody)
	if err != nil || status < 200 || status >= 300 {
		writeErr(w, http.StatusUnauthorized, "Failed to exchange Atlassian code: "+firstNonEmpty(jsonText(body, "error_description"), jsonText(body, "error"), fmt.Sprintf("HTTP %d", status)))
		return
	}
	access := jsonText(body, "access_token")
	refresh := jsonText(body, "refresh_token")
	if access == "" {
		writeErr(w, http.StatusUnauthorized, "Atlassian response did not contain an access token.")
		return
	}
	var expiresAt *time.Time
	if n := jsonInt(body, "expires_in"); n > 0 {
		t := time.Now().UTC().Add(time.Duration(n) * time.Second)
		expiresAt = &t
	}
	_, resBody, _ := s.do(r.Context(), http.MethodGet, "https://api.atlassian.com/oauth/token/accessible-resources",
		map[string]string{"Authorization": "Bearer " + access, "Accept": "application/json"}, nil)
	cloudID, siteURL, siteName := "", "", ""
	var arr []map[string]any
	_ = json.Unmarshal([]byte(resBody), &arr)
	if len(arr) > 0 {
		cloudID = str(arr[0]["id"])
		siteURL = str(arr[0]["url"])
		siteName = str(arr[0]["name"])
	}
	if cloudID == "" {
		writeErr(w, http.StatusBadRequest, "No accessible Jira Cloud sites found for this Atlassian account.")
		return
	}
	account := "Atlassian User"
	_, meBody, _ := s.do(r.Context(), http.MethodGet,
		"https://api.atlassian.com/ex/jira/"+url.PathEscape(cloudID)+"/rest/api/3/myself",
		map[string]string{"Authorization": "Bearer " + access}, nil)
	if n := firstNonEmpty(jsonText(meBody, "displayName"), jsonText(meBody, "emailAddress")); n != "" {
		account = n
	}
	projects := s.fetchJiraProjects(r.Context(), jiraCtx{
		APIBase: "https://api.atlassian.com/ex/jira/" + cloudID,
		Headers: map[string]string{"Authorization": "Bearer " + access, "Accept": "application/json"},
	})
	projectKey, projectName := "", ""
	if len(projects) > 0 {
		projectKey = str(projects[0]["key"])
		projectName = str(projects[0]["name"])
	}
	_ = s.persistOwned(r.Context(), ownerFromAuth(r), storedIntegration{
		ProjectID: parseID(req.ProjectID), Provider: "jira", Account: account, BaseURL: siteURL,
		ProjectKey: projectKey, ProjectName: projectName, CloudID: cloudID, AuthType: "oauth",
		AccessToken: access, RefreshToken: refresh, ExpiresAt: expiresAt,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"connected": true, "provider": "jira", "account": account,
		"detail":     "Connected with Atlassian OAuth as " + account + " to " + firstNonEmpty(siteName, siteURL),
		"projectKey": projectKey, "projectName": projectName, "baseUrl": siteURL, "cloudId": cloudID,
		"authType": "oauth", "accessToken": nil, "projects": projects,
	})
}

func (s *Service) GitHubOAuthURL(w http.ResponseWriter, r *http.Request) {
	_ = optionalAuth(r)
	clientID := strings.TrimSpace(s.cfg.GitHubClientID)
	if clientID == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false, "url": nil, "clientId": nil, "redirectUri": nil,
			"message": "GitHub OAuth is not configured. Set BLINK_GITHUB_CLIENT_ID and BLINK_GITHUB_CLIENT_SECRET on the server.",
		})
		return
	}
	redirect := s.resolveRedirect("github", r.URL.Query().Get("redirectUri"), publicAPIBase(r))
	scopes := firstNonEmpty(s.cfg.GitHubScopes, "repo read:org user:email")
	u := "https://github.com/login/oauth/authorize" +
		"?client_id=" + url.QueryEscape(clientID) +
		"&redirect_uri=" + url.QueryEscape(redirect) +
		"&scope=" + url.QueryEscape(scopes) +
		"&state=" + url.QueryEscape(uuid.NewString()) +
		"&allow_signup=false"
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": true, "url": u, "clientId": clientID, "redirectUri": redirect,
		"message": "Ready for GitHub authorization.",
	})
}

func (s *Service) GitHubOAuthExchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code        string `json:"code"`
		RedirectURI string `json:"redirectUri"`
		ProjectID   any    `json:"projectId"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	clientID := strings.TrimSpace(s.cfg.GitHubClientID)
	clientSecret := strings.TrimSpace(s.cfg.GitHubClientSecret)
	if clientID == "" || clientSecret == "" {
		writeErr(w, http.StatusBadRequest, "GitHub OAuth credentials (client ID/secret) are not configured on the server.")
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		writeErr(w, http.StatusBadRequest, "Authorization code is required.")
		return
	}
	redirect := s.resolveRedirect("github", req.RedirectURI, "")
	form := url.Values{
		"client_id": {clientID}, "client_secret": {clientSecret},
		"code": {code}, "redirect_uri": {redirect},
	}
	status, body, err := s.do(r.Context(), http.MethodPost, "https://github.com/login/oauth/access_token",
		map[string]string{"Accept": "application/json", "Content-Type": "application/x-www-form-urlencoded"},
		[]byte(form.Encode()))
	if err != nil || status < 200 || status >= 300 {
		writeErr(w, http.StatusUnauthorized, "Failed to exchange GitHub code.")
		return
	}
	access := jsonText(body, "access_token")
	if access == "" {
		writeErr(w, http.StatusUnauthorized, firstNonEmpty(jsonText(body, "error_description"), "GitHub response did not contain an access token."))
		return
	}
	_, meBody, _ := s.do(r.Context(), http.MethodGet, "https://api.github.com/user",
		map[string]string{"Authorization": "Bearer " + access, "Accept": "application/vnd.github+json"}, nil)
	account := firstNonEmpty(jsonText(meBody, "login"), "GitHub User")
	_ = s.persistOwned(r.Context(), ownerFromAuth(r), storedIntegration{
		ProjectID: parseID(req.ProjectID), Provider: "github", Account: account,
		BaseURL: "https://github.com", Username: account, AuthType: "oauth", AccessToken: access,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"connected": true, "provider": "github", "account": account,
		"detail": "Connected with GitHub OAuth as " + account, "authType": "oauth", "accessToken": nil,
	})
}

func (s *Service) FigmaOAuthURL(w http.ResponseWriter, r *http.Request) {
	_ = optionalAuth(r)
	clientID := strings.TrimSpace(s.cfg.FigmaClientID)
	if clientID == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false, "url": nil, "clientId": nil, "redirectUri": nil,
			"message": "Figma OAuth is not configured. Set BLINK_FIGMA_CLIENT_ID and BLINK_FIGMA_CLIENT_SECRET on the server.",
		})
		return
	}
	redirect := s.resolveRedirect("figma", r.URL.Query().Get("redirectUri"), publicAPIBase(r))
	scopes := normalizeFigmaScopes(firstNonEmpty(s.cfg.FigmaScopes, figmaDefaultScopes))
	u := "https://www.figma.com/oauth" +
		"?client_id=" + url.QueryEscape(clientID) +
		"&redirect_uri=" + url.QueryEscape(redirect) +
		"&scope=" + url.QueryEscape(scopes) +
		"&state=" + url.QueryEscape(uuid.NewString()) +
		"&response_type=code"
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": true, "url": u, "clientId": clientID, "redirectUri": redirect,
		"message": "Ready for Figma authorization.",
	})
}

func (s *Service) FigmaOAuthExchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code         string `json:"code"`
		RedirectURI  string `json:"redirectUri"`
		ProjectID    any    `json:"projectId"`
		Organization string `json:"organization"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	clientID := strings.TrimSpace(s.cfg.FigmaClientID)
	clientSecret := strings.TrimSpace(s.cfg.FigmaClientSecret)
	if clientID == "" || clientSecret == "" {
		writeErr(w, http.StatusBadRequest, "Figma OAuth credentials are not configured on the server.")
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		writeErr(w, http.StatusBadRequest, "Authorization code is required.")
		return
	}
	redirect := s.resolveRedirect("figma", req.RedirectURI, "")
	form := url.Values{
		"redirect_uri": {redirect}, "code": {code}, "grant_type": {"authorization_code"},
	}
	status, body, err := s.do(r.Context(), http.MethodPost, "https://api.figma.com/v1/oauth/token",
		map[string]string{
			"Authorization": figmaBasicAuth(clientID, clientSecret),
			"Content-Type":  "application/x-www-form-urlencoded",
			"Accept":        "application/json",
		}, []byte(form.Encode()))
	if err != nil || status < 200 || status >= 300 {
		writeErr(w, http.StatusUnauthorized, "Failed to exchange Figma code.")
		return
	}
	access := jsonText(body, "access_token")
	refresh := jsonText(body, "refresh_token")
	if access == "" {
		writeErr(w, http.StatusUnauthorized, "Figma response did not contain an access token.")
		return
	}
	var expiresAt *time.Time
	if n := jsonInt(body, "expires_in"); n > 0 {
		t := time.Now().UTC().Add(time.Duration(n) * time.Second)
		expiresAt = &t
	}
	_, meBody, _ := s.do(r.Context(), http.MethodGet, "https://api.figma.com/v1/me",
		map[string]string{"Authorization": "Bearer " + access}, nil)
	account := firstNonEmpty(jsonText(meBody, "email"), jsonText(meBody, "handle"), "Figma User")
	org := firstNonEmpty(parseFigmaTeamID(req.Organization), req.Organization)
	_ = s.persistOwned(r.Context(), ownerFromAuth(r), storedIntegration{
		ProjectID: parseID(req.ProjectID), Provider: "figma", Account: account,
		BaseURL: "https://www.figma.com", Username: account, Organization: org,
		AuthType: "oauth", AccessToken: access, RefreshToken: refresh, ExpiresAt: expiresAt,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"connected": true, "provider": "figma", "account": account,
		"detail": "Connected with Figma OAuth as " + account, "organization": org,
		"authType": "oauth", "accessToken": nil,
	})
}

func (s *Service) JiraProjects(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	_ = readJSON(r, &req)
	ctxJ, err := s.resolveJiraOwned(r.Context(), parseID(req["projectId"]), req, ownerFromAuth(r))
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, s.fetchJiraProjects(r.Context(), ctxJ))
}

func (s *Service) GitHubOrgs(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	_ = readJSON(r, &req)
	stored, _ := s.load(r.Context(), parseID(req["projectId"]), "github")
	token := firstNonEmpty(str(req["token"]), stored.AccessToken)
	if token == "" {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	_, body, err := s.do(r.Context(), http.MethodGet, "https://api.github.com/user/orgs",
		map[string]string{"Authorization": "Bearer " + token, "Accept": "application/vnd.github+json"}, nil)
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	var raw []map[string]any
	_ = json.Unmarshal([]byte(body), &raw)
	out := make([]map[string]any, 0, len(raw))
	for _, o := range raw {
		out = append(out, map[string]any{
			"id": str(o["id"]), "login": str(o["login"]), "name": firstNonEmpty(str(o["name"]), str(o["login"])),
			"avatarUrl": str(o["avatar_url"]),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) FigmaTeams(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	_ = readJSON(r, &req)
	stored, _ := s.load(r.Context(), parseID(req["projectId"]), "figma")
	token := firstNonEmpty(str(req["token"]), stored.AccessToken)
	if token == "" {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	_, body, err := s.do(r.Context(), http.MethodGet, "https://api.figma.com/v1/teams",
		map[string]string{"Authorization": "Bearer " + token}, nil)
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	var root map[string]any
	_ = json.Unmarshal([]byte(body), &root)
	teams, _ := root["teams"].([]any)
	out := make([]map[string]any, 0)
	for _, t := range teams {
		m, _ := t.(map[string]any)
		if m == nil {
			continue
		}
		out = append(out, map[string]any{
			"id": str(m["id"]), "login": str(m["id"]), "name": str(m["name"]), "avatarUrl": "",
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) FigmaProjects(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	_ = readJSON(r, &req)
	stored, _ := s.load(r.Context(), parseID(req["projectId"]), "figma")
	token := firstNonEmpty(str(req["token"]), stored.AccessToken)
	teamID := parseFigmaTeamID(firstNonEmpty(str(req["organization"]), stored.Organization))
	if token == "" || teamID == "" {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	_, body, err := s.do(r.Context(), http.MethodGet, "https://api.figma.com/v1/teams/"+url.PathEscape(teamID)+"/projects",
		map[string]string{"Authorization": "Bearer " + token}, nil)
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	var root map[string]any
	_ = json.Unmarshal([]byte(body), &root)
	projects, _ := root["projects"].([]any)
	out := make([]map[string]any, 0)
	for _, p := range projects {
		m, _ := p.(map[string]any)
		if m == nil {
			continue
		}
		out = append(out, map[string]any{
			"id": str(m["id"]), "key": str(m["id"]), "name": str(m["name"]), "type": "figma", "avatar": "",
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) CreateJiraIssues(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID   any              `json:"projectId"`
		ProjectKey  string           `json:"projectKey"`
		BaseURL     string           `json:"baseUrl"`
		Email       string           `json:"email"`
		Token       string           `json:"token"`
		CloudID     string           `json:"cloudId"`
		AccessToken string           `json:"accessToken"`
		Epics       []map[string]any `json:"epics"`
		Stories     []map[string]any `json:"stories"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	projectKey := strings.TrimSpace(req.ProjectKey)
	if projectKey == "" {
		writeErr(w, http.StatusBadRequest, "Jira project key is required.")
		return
	}
	if len(req.Epics) == 0 && len(req.Stories) == 0 {
		writeErr(w, http.StatusBadRequest, "Add at least one epic or story to create in Jira.")
		return
	}
	projectID := parseID(req.ProjectID)
	stored, _ := s.load(r.Context(), projectID, "jira")
	owner := ownerFromAuth(r)
	ctxJ, err := s.resolveJiraOwned(r.Context(), projectID, map[string]any{
		"baseUrl": req.BaseURL, "email": req.Email, "token": req.Token,
		"cloudId": req.CloudID, "accessToken": req.AccessToken,
	}, owner)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if projectID > 0 {
		stored.ProjectID = projectID
		stored.Provider = "jira"
		stored.ProjectKey = projectKey
		if stored.AccessToken != "" {
			_ = s.persistOwned(r.Context(), owner, stored)
		}
	}

	created := make([]map[string]any, 0)
	errors := make([]string, 0)
	epicKeys := map[string]string{}

	total := 0
	for _, epic := range req.Epics {
		if strings.TrimSpace(str(epic["title"])) != "" {
			total++
		}
	}
	for _, story := range req.Stories {
		if strings.TrimSpace(str(story["title"])) != "" {
			total++
		}
	}

	stream := strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
	var writeSSE func(event string, payload any) bool
	if stream {
		flusher, ok := w.(http.Flusher)
		if !ok {
			stream = false
		} else {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache, no-transform")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			flusher.Flush()
			writeSSE = func(event string, payload any) bool {
				b, err := json.Marshal(payload)
				if err != nil {
					return false
				}
				if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
					return false
				}
				flusher.Flush()
				return true
			}
			_ = writeSSE("start", map[string]any{"total": total, "projectKey": projectKey})
		}
	}
	emitItem := func(item map[string]any) {
		if writeSSE != nil {
			_ = writeSSE("item", item)
		}
	}

	refreshedOnce := false
	tryCreate := func(issueType, title, desc, parent, id string) (map[string]any, error) {
		item, err := s.createJiraIssue(r.Context(), ctxJ, projectKey, issueType, title, desc, parent)
		if err != nil && !refreshedOnce && strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
			stored2, ok := s.load(r.Context(), projectID, "jira")
			if ok {
				if _, rerr := s.ensureJiraOAuthFresh(r.Context(), owner, &stored2, true); rerr == nil {
					refreshedOnce = true
					if rebuilt, berr := s.buildJiraCtx(stored2.CloudID, stored2.AccessToken, stored2.BaseURL, "", ""); berr == nil {
						ctxJ = rebuilt
						return s.createJiraIssue(r.Context(), ctxJ, projectKey, issueType, title, desc, parent)
					}
				} else {
					return nil, rerr
				}
			}
		}
		return item, err
	}

	for _, epic := range req.Epics {
		title := strings.TrimSpace(str(epic["title"]))
		if title == "" {
			continue
		}
		id := str(epic["id"])
		desc := strings.TrimSpace(str(epic["objective"]))
		if id != "" {
			if desc != "" {
				desc += "\n"
			}
			desc += "Source epic: " + id
		}
		item, err := tryCreate("Epic", title, desc, "", id)
		if err != nil {
			errors = append(errors, title+": "+err.Error())
			created = append(created, map[string]any{
				"id": id, "sourceId": id, "jiraKey": nil, "url": nil, "jiraUrl": nil,
				"type": "Epic", "status": "failed", "message": err.Error(),
			})
			emitItem(created[len(created)-1])
			continue
		}
		item["id"] = id
		item["sourceId"] = id
		item["type"] = "Epic"
		if u := str(item["url"]); u != "" {
			item["jiraUrl"] = u
		}
		created = append(created, item)
		if key := str(item["jiraKey"]); key != "" && id != "" {
			epicKeys[id] = key
		}
		emitItem(item)
	}

	for _, story := range req.Stories {
		title := strings.TrimSpace(str(story["title"]))
		if title == "" {
			continue
		}
		id := str(story["id"])
		desc := buildStoryDescription(story)
		parent := ""
		if eid := str(story["epicId"]); eid != "" {
			parent = epicKeys[eid]
		}
		if parent == "" {
			parent = str(story["epicKey"])
		}
		item, err := tryCreate("Story", title, desc, parent, id)
		if err != nil {
			errors = append(errors, title+": "+err.Error())
			failed := map[string]any{
				"id": id, "sourceId": id, "jiraKey": nil, "url": nil, "jiraUrl": nil,
				"type": "Story", "status": "failed", "message": err.Error(),
			}
			created = append(created, failed)
			emitItem(failed)
			continue
		}
		item["id"] = id
		item["sourceId"] = id
		item["type"] = "Story"
		if u := str(item["url"]); u != "" {
			item["jiraUrl"] = u
		}
		created = append(created, item)
		emitItem(item)
	}

	ok := 0
	for _, c := range created {
		if str(c["status"]) == "created" {
			ok++
		}
	}
	status := "ok"
	if len(errors) > 0 {
		if ok > 0 {
			status = "partial"
		} else {
			status = "error"
		}
	}
	msg := fmt.Sprintf("%d issue(s) created in Jira project %s", ok, projectKey)
	if len(errors) > 0 {
		msg += fmt.Sprintf(" (%d failed).", len(errors))
	} else {
		msg += "."
	}
	result := map[string]any{"status": status, "message": msg, "created": created, "errors": errors, "issues": created, "total": total, "linked": ok}
	if stream && writeSSE != nil {
		_ = writeSSE("done", result)
		return
	}
	if projectID > 0 && ok > 0 {
		s.recordProjection(r.Context(), projectID, "jira", "issues.create", map[string]any{
			"projectKey": projectKey,
			"created":    ok,
			"total":      total,
			"status":     status,
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) DeleteJiraIssues(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID any      `json:"projectId"`
		IssueKeys []string `json:"issueKeys"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	keys := make([]string, 0, len(req.IssueKeys))
	seenIn := map[string]struct{}{}
	for _, raw := range req.IssueKeys {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		if _, dup := seenIn[key]; dup {
			continue
		}
		seenIn[key] = struct{}{}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		writeErr(w, http.StatusBadRequest, "Add at least one Jira issue key to delete.")
		return
	}
	projectID := parseID(req.ProjectID)
	ctxJ, err := s.resolveJiraOwned(r.Context(), projectID, map[string]any{}, ownerFromAuth(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	deleted, errs := []string{}, []string{}
	done := map[string]bool{}
	pending := append([]string{}, keys...)
	for pass := 0; pass < 4 && len(pending) > 0; pass++ {
		next := []string{}
		progress := false
		for _, key := range pending {
			if done[key] {
				continue
			}
			err := s.deleteJiraIssueOpts(r.Context(), ctxJ, key, false)
			if err == nil {
				done[key] = true
				deleted = append(deleted, key)
				progress = true
				continue
			}
			low := strings.ToLower(err.Error())
			if strings.Contains(low, "404") || strings.Contains(low, "does not exist") || strings.Contains(low, "not found") {
				done[key] = true
				deleted = append(deleted, key)
				progress = true
				continue
			}
			if jiraDeleteBlockedByOtherIssues(err) && pass < 3 {
				next = append(next, key)
				continue
			}
			done[key] = true
			if jiraDeleteBlockedByOtherIssues(err) {
				errs = append(errs, key+": left in Jira because it still has other issues not created on this screen.")
				continue
			}
			errs = append(errs, key+": "+err.Error())
		}
		if !progress && len(next) > 0 {
			for _, key := range next {
				if done[key] {
					continue
				}
				done[key] = true
				errs = append(errs, key+": left in Jira because it still has other issues not created on this screen.")
			}
			break
		}
		pending = next
	}
	status := http.StatusOK
	msg := ""
	if len(deleted) == 0 && len(errs) > 0 {
		status = http.StatusBadGateway
		msg = strings.Join(errs, " ")
	}
	if projectID > 0 && len(deleted) > 0 {
		s.recordProjection(r.Context(), projectID, "jira", "issues.delete", map[string]any{
			"deletedKeys": deleted,
			"count":       len(deleted),
		})
	}
	writeJSON(w, status, map[string]any{
		"deleted":      len(deleted),
		"skipped":      0,
		"deletedKeys":  deleted,
		"skippedKeys":  []string{},
		"errors":       errs,
		"deletedCount": len(deleted),
		"skippedCount": 0,
		"message":      msg,
	})
}

func (s *Service) CreateJiraComment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID       any    `json:"projectId"`
		IssueKey        string `json:"issueKey"`
		Body            string `json:"body"`
		BlinkQuestionID string `json:"blinkQuestionId"`
		ParentCommentID string `json:"parentCommentId"`
		BaseURL         string `json:"baseUrl"`
		Email           string `json:"email"`
		Token           string `json:"token"`
		CloudID         string `json:"cloudId"`
		AccessToken     string `json:"accessToken"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	issueKey := strings.TrimSpace(req.IssueKey)
	bodyText := strings.TrimSpace(req.Body)
	if issueKey == "" || bodyText == "" {
		writeErr(w, http.StatusBadRequest, "Jira issue key and comment body are required.")
		return
	}
	qid := strings.TrimSpace(req.BlinkQuestionID)
	parentID := strings.TrimSpace(req.ParentCommentID)
	// Threaded replies must stay plain — do not re-tag with blink-question markers.
	if qid != "" && parentID == "" && !strings.Contains(bodyText, "blink-question:"+qid) {
		bodyText = "[blink-question:" + qid + "]\n" + bodyText
	}
	ctxJ, err := s.resolveJiraOwned(r.Context(), parseID(req.ProjectID), map[string]any{
		"baseUrl": req.BaseURL, "email": req.Email, "token": req.Token,
		"cloudId": req.CloudID, "accessToken": req.AccessToken,
	}, ownerFromAuth(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	payloadBody := map[string]any{"body": adfDocument(bodyText)}
	if parentID != "" {
		// Jira Cloud threaded reply: parentId on the comment body (numeric when possible).
		if n, err := strconv.ParseInt(parentID, 10, 64); err == nil {
			payloadBody["parentId"] = n
		} else {
			payloadBody["parentId"] = parentID
		}
	}
	payload, _ := json.Marshal(payloadBody)
	status, resp, err := s.do(r.Context(), http.MethodPost,
		ctxJ.APIBase+"/rest/api/3/issue/"+url.PathEscape(issueKey)+"/comment",
		withJSON(ctxJ.Headers), payload)
	if err != nil || status < 200 || status >= 300 {
		writeErr(w, http.StatusBadGateway, firstNonEmpty(jsonText(resp, "message"), fmt.Sprintf("Jira returned HTTP %d.", status)))
		return
	}
	commentID := firstNonEmpty(jsonText(resp, "id"), fmt.Sprintf("%v", jsonRaw(resp, "id")))
	projectID := parseID(req.ProjectID)
	if projectID > 0 {
		s.recordProjection(r.Context(), projectID, "jira", "comment.create", map[string]any{
			"issueKey":        issueKey,
			"commentId":       commentID,
			"blinkQuestionId": qid,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "message": "Comment posted on " + issueKey,
		"issueKey": issueKey, "commentId": commentID, "blinkQuestionId": qid,
		"parentCommentId": parentID,
	})
}

func (s *Service) PollJiraComments(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID   any    `json:"projectId"`
		BaseURL     string `json:"baseUrl"`
		Email       string `json:"email"`
		Token       string `json:"token"`
		CloudID     string `json:"cloudId"`
		AccessToken string `json:"accessToken"`
		Items       []struct {
			IssueKey        string `json:"issueKey"`
			BlinkQuestionID string `json:"blinkQuestionId"`
		} `json:"items"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Items) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "message": "Nothing to poll.", "replies": []any{}})
		return
	}
	ctxJ, err := s.resolveJiraOwned(r.Context(), parseID(req.ProjectID), map[string]any{
		"baseUrl": req.BaseURL, "email": req.Email, "token": req.Token,
		"cloudId": req.CloudID, "accessToken": req.AccessToken,
	}, ownerFromAuth(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	threads := make([]map[string]any, 0)
	legacyReplies := make([]map[string]any, 0)
	for _, item := range req.Items {
		issueKey := strings.TrimSpace(item.IssueKey)
		qid := strings.TrimSpace(item.BlinkQuestionID)
		if issueKey == "" || qid == "" {
			continue
		}
		_, body, err := s.do(r.Context(), http.MethodGet,
			ctxJ.APIBase+"/rest/api/3/issue/"+url.PathEscape(issueKey)+"/comment?maxResults=100",
			ctxJ.Headers, nil)
		if err != nil {
			continue
		}
		var root map[string]any
		_ = json.Unmarshal([]byte(body), &root)
		comments, _ := root["comments"].([]any)
		marker := "blink-question:" + qid
		parentCommentID := ""
		parentBody := ""
		threadReplies := make([]map[string]any, 0)
		siblingFallback := make([]map[string]any, 0)
		foundMarker := false
		for _, c := range comments {
			cm, _ := c.(map[string]any)
			if cm == nil {
				continue
			}
			plain := strings.TrimSpace(adfToPlain(cm["body"]))
			if plain == "" {
				continue
			}
			if strings.Contains(plain, marker) {
				foundMarker = true
				parentCommentID = str(cm["id"])
				parentBody = plain
				continue
			}
			// Skip other Blink outbound clarification comments.
			if strings.Contains(plain, "blink-question:") || strings.Contains(plain, "[blink-question:") {
				continue
			}
			author := ""
			if a, ok := cm["author"].(map[string]any); ok {
				author = firstNonEmpty(str(a["displayName"]), str(a["emailAddress"]), str(a["accountId"]))
			}
			row := map[string]any{
				"commentId": str(cm["id"]),
				"body":      plain,
				"author":    author,
				"created":   str(cm["created"]),
				"parentId":  commentParentID(cm),
			}
			if parentCommentID != "" && commentParentID(cm) == parentCommentID {
				threadReplies = append(threadReplies, row)
				continue
			}
			if foundMarker {
				siblingFallback = append(siblingFallback, row)
			}
		}
		// Prefer true threaded children; fall back to flat comments after the marker.
		replies := threadReplies
		if len(replies) == 0 {
			replies = siblingFallback
		}
		if len(replies) == 0 {
			continue
		}
		threads = append(threads, map[string]any{
			"blinkQuestionId": qid,
			"issueKey":        issueKey,
			"parentCommentId": parentCommentID,
			"parentBody":      parentBody,
			"replies":         replies,
		})
		last := replies[len(replies)-1]
		legacyReplies = append(legacyReplies, map[string]any{
			"issueKey":        issueKey,
			"blinkQuestionId": qid,
			"commentId":       last["commentId"],
			"body":            last["body"],
			"author":          last["author"],
			"created":         last["created"],
		})
	}
	msg := "No replies yet."
	replyCount := 0
	for _, t := range threads {
		switch arr := t["replies"].(type) {
		case []map[string]any:
			replyCount += len(arr)
		case []any:
			replyCount += len(arr)
		}
	}
	if replyCount > 0 {
		msg = fmt.Sprintf("%d reply(ies) across %d thread(s).", replyCount, len(threads))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "message": msg,
		"threads": threads,
		"replies": legacyReplies, // backward compatible: latest reply per question
	})
}

const blinkSimReplyMarker = "[blink-sim-reply]"

// ResetSimulatedJiraReplies deletes Blink-simulated stakeholder reply comments from Jira.
func (s *Service) ResetSimulatedJiraReplies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID   any    `json:"projectId"`
		BaseURL     string `json:"baseUrl"`
		Email       string `json:"email"`
		Token       string `json:"token"`
		CloudID     string `json:"cloudId"`
		AccessToken string `json:"accessToken"`
		Items       []struct {
			IssueKey        string `json:"issueKey"`
			BlinkQuestionID string `json:"blinkQuestionId"`
			ReplyCommentID  string `json:"replyCommentId"`
		} `json:"items"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Items) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "message": "Nothing to reset.", "deleted": 0})
		return
	}
	ctxJ, err := s.resolveJiraOwned(r.Context(), parseID(req.ProjectID), map[string]any{
		"baseUrl": req.BaseURL, "email": req.Email, "token": req.Token,
		"cloudId": req.CloudID, "accessToken": req.AccessToken,
	}, ownerFromAuth(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	deleted := 0
	errors := make([]string, 0)
	for _, item := range req.Items {
		issueKey := strings.TrimSpace(item.IssueKey)
		if issueKey == "" {
			continue
		}
		toDelete := map[string]bool{}
		if id := strings.TrimSpace(item.ReplyCommentID); id != "" {
			toDelete[id] = true
		}

		_, body, err := s.do(r.Context(), http.MethodGet,
			ctxJ.APIBase+"/rest/api/3/issue/"+url.PathEscape(issueKey)+"/comment?maxResults=100",
			ctxJ.Headers, nil)
		if err == nil {
			var root map[string]any
			_ = json.Unmarshal([]byte(body), &root)
			comments, _ := root["comments"].([]any)
			qid := strings.TrimSpace(item.BlinkQuestionID)
			marker := ""
			if qid != "" {
				marker = "blink-question:" + qid
			}
			foundMarker := marker == ""
			for _, c := range comments {
				cm, _ := c.(map[string]any)
				if cm == nil {
					continue
				}
				plain := strings.TrimSpace(adfToPlain(cm["body"]))
				id := str(cm["id"])
				if id == "" {
					continue
				}
				if marker != "" && strings.Contains(plain, marker) {
					foundMarker = true
					continue
				}
				if strings.Contains(plain, blinkSimReplyMarker) || strings.Contains(plain, "Blink simulation") {
					if foundMarker || marker == "" {
						toDelete[id] = true
					}
				}
			}
		}

		for id := range toDelete {
			status, resp, delErr := s.do(r.Context(), http.MethodDelete,
				ctxJ.APIBase+"/rest/api/3/issue/"+url.PathEscape(issueKey)+"/comment/"+url.PathEscape(id),
				ctxJ.Headers, nil)
			if delErr != nil || (status >= 300 && status != 404) {
				errors = append(errors, issueKey+":"+id+": "+firstNonEmpty(jsonText(resp, "message"), fmt.Sprintf("HTTP %d", status)))
				continue
			}
			deleted++
		}
	}

	status := "ok"
	if len(errors) > 0 && deleted == 0 {
		status = "error"
	} else if len(errors) > 0 {
		status = "partial"
	}
	msg := fmt.Sprintf("Deleted %d simulated reply comment(s).", deleted)
	if len(errors) > 0 {
		msg += " " + strings.Join(errors, "; ")
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "message": msg, "deleted": deleted, "errors": errors})
}

func (s *Service) Binding(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID    any    `json:"projectId"`
		Provider     string `json:"provider"`
		ProjectKey   string `json:"projectKey"`
		ProjectName  string `json:"projectName"`
		Organization string `json:"organization"`
		SpaceKey     string `json:"spaceKey"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	projectID := parseID(req.ProjectID)
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if projectID <= 0 || provider == "" {
		writeErr(w, http.StatusBadRequest, "Project id and provider are required.")
		return
	}
	stored, ok := s.load(r.Context(), projectID, provider)
	if !ok {
		writeErr(w, http.StatusBadRequest, "Connect "+provider+" before saving the project selection.")
		return
	}
	if k := strings.TrimSpace(req.ProjectKey); k != "" {
		stored.ProjectKey = k
	}
	if n := strings.TrimSpace(req.ProjectName); n != "" {
		stored.ProjectName = n
	}
	if provider == "github" || provider == "figma" {
		org := strings.TrimSpace(req.Organization)
		if provider == "figma" {
			org = parseFigmaTeamID(org)
		}
		if org != "" {
			stored.Organization = org
		}
	}
	if provider == "confluence" {
		if sk := strings.TrimSpace(req.SpaceKey); sk != "" {
			stored.SpaceKey = sk
		}
	}
	if err := s.persistOwned(r.Context(), ownerFromAuth(r), stored); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connected": true, "provider": stored.Provider, "account": stored.Account,
		"detail": "Saved " + provider + " binding", "projectKey": stored.ProjectKey,
		"projectName": stored.ProjectName, "baseUrl": stored.BaseURL, "cloudId": stored.CloudID,
		"authType": stored.AuthType, "accessToken": nil, "organization": stored.Organization,
	})
}

func (s *Service) JiraOAuthCallback(w http.ResponseWriter, r *http.Request) {
	writeOAuthHTML(w, "Atlassian", "JIRA_OAUTH_RESPONSE", r)
}

func (s *Service) GitHubOAuthCallback(w http.ResponseWriter, r *http.Request) {
	writeOAuthHTML(w, "GitHub", "GITHUB_OAUTH_RESPONSE", r)
}

func (s *Service) FigmaOAuthCallback(w http.ResponseWriter, r *http.Request) {
	writeOAuthHTML(w, "Figma", "FIGMA_OAUTH_RESPONSE", r)
}

func (s *Service) ListBlinkIssues(w http.ResponseWriter, r *http.Request) {
	projectID := parseID(r.URL.Query().Get("projectId"))
	if projectID <= 0 {
		writeErr(w, http.StatusBadRequest, "Blink project id is required.")
		return
	}
	stored, ok := s.load(r.Context(), projectID, "jira")
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "projectId": projectID, "projectKey": nil, "browseBase": nil,
			"issues": []any{}, "message": "Connect Jira for this Blink project first.",
		})
		return
	}
	projectKey := strings.TrimSpace(stored.ProjectKey)
	if projectKey == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "projectId": projectID, "projectKey": nil, "browseBase": nil,
			"issues": []any{}, "message": "Pick a Jira project on the integrations step before resetting issues.",
		})
		return
	}
	ctxJ, err := s.jiraFromStored(stored)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	issues := s.searchBlinkMarked(r.Context(), ctxJ, projectKey)
	msg := fmt.Sprintf("No Blink-marked issues in Jira project %s.", projectKey)
	if len(issues) > 0 {
		msg = fmt.Sprintf("%d Blink-marked issue(s) in %s.", len(issues), projectKey)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "projectId": projectID, "projectKey": projectKey,
		"browseBase": ctxJ.BrowseBase, "issues": issues, "message": msg,
	})
}

func (s *Service) DeleteBlinkIssue(w http.ResponseWriter, r *http.Request) {
	projectID := parseID(r.URL.Query().Get("projectId"))
	issueKey := strings.TrimSpace(chi.URLParam(r, "issueKey"))
	if projectID <= 0 || issueKey == "" {
		writeErr(w, http.StatusBadRequest, "Blink project id and Jira issue key are required.")
		return
	}
	stored, ok := s.load(r.Context(), projectID, "jira")
	if !ok || strings.TrimSpace(stored.ProjectKey) == "" {
		writeErr(w, http.StatusBadRequest, "Connect Jira and select a project for this Blink project first.")
		return
	}
	ctxJ, err := s.jiraFromStored(stored)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	projectKey := strings.TrimSpace(stored.ProjectKey)
	marked := s.fetchBlinkMarked(r.Context(), ctxJ, projectKey, issueKey)
	if marked == nil {
		writeErr(w, http.StatusBadRequest, "Issue is not a Blink-marked Jira item (missing Source epic:/Source story:).")
		return
	}
	if str(marked["sourceKind"]) == "epic" {
		foreign := s.unmarkedChildren(r.Context(), ctxJ, str(marked["key"]))
		if len(foreign) > 0 {
			writeErr(w, http.StatusConflict, "Epic "+str(marked["key"])+" still has non-Blink children: "+strings.Join(foreign, ", "))
			return
		}
		// delete marked children first
		for _, child := range s.listChildren(r.Context(), ctxJ, str(marked["key"])) {
			key := str(child["key"])
			plain := adfToPlain(child["fields"])
			if nested, ok := child["fields"].(map[string]any); ok {
				plain = adfToPlain(nested["description"])
			}
			if sourceEpicID(plain) != "" || sourceStoryID(plain) != "" {
				_ = s.deleteJiraIssue(r.Context(), ctxJ, key)
			}
		}
	}
	if err := s.deleteJiraIssue(r.Context(), ctxJ, str(marked["key"])); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deletedCount": 1, "skippedCount": 0, "deleted": []string{str(marked["key"])},
		"skipped": []string{}, "errors": []string{},
	})
}

func (s *Service) DeleteAllBlinkIssues(w http.ResponseWriter, r *http.Request) {
	projectID := parseID(r.URL.Query().Get("projectId"))
	if projectID <= 0 {
		writeErr(w, http.StatusBadRequest, "Blink project id is required.")
		return
	}
	stored, ok := s.load(r.Context(), projectID, "jira")
	if !ok || strings.TrimSpace(stored.ProjectKey) == "" {
		writeErr(w, http.StatusBadRequest, "Connect Jira and select a project for this Blink project first.")
		return
	}
	ctxJ, err := s.jiraFromStored(stored)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	projectKey := strings.TrimSpace(stored.ProjectKey)
	issues := s.searchBlinkMarked(r.Context(), ctxJ, projectKey)
	deleted, skipped, errs := []string{}, []string{}, []string{}

	for _, issue := range issues {
		if str(issue["sourceKind"]) != "story" {
			continue
		}
		key := str(issue["key"])
		again := s.fetchBlinkMarked(r.Context(), ctxJ, projectKey, key)
		if again == nil {
			skipped = append(skipped, key+" (not Blink-marked)")
			continue
		}
		if err := s.deleteJiraIssue(r.Context(), ctxJ, key); err != nil {
			errs = append(errs, key+": "+err.Error())
			continue
		}
		deleted = append(deleted, key)
	}
	for _, issue := range issues {
		if str(issue["sourceKind"]) != "epic" {
			continue
		}
		key := str(issue["key"])
		again := s.fetchBlinkMarked(r.Context(), ctxJ, projectKey, key)
		if again == nil {
			skipped = append(skipped, key+" (not Blink-marked)")
			continue
		}
		if foreign := s.unmarkedChildren(r.Context(), ctxJ, key); len(foreign) > 0 {
			skipped = append(skipped, key+" (has non-Blink children)")
			continue
		}
		if err := s.deleteJiraIssue(r.Context(), ctxJ, key); err != nil {
			errs = append(errs, key+": "+err.Error())
			continue
		}
		deleted = append(deleted, key)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deletedCount": len(deleted), "skippedCount": len(skipped),
		"deleted": deleted, "skipped": skipped, "errors": errs,
	})
}

// --- persistence ---

func (s *Service) persist(ctx context.Context, in storedIntegration) error {
	return s.persistOwned(ctx, "", in)
}

func (s *Service) persistOwned(ctx context.Context, ownerEmail string, in storedIntegration) error {
	if err := s.persistProjectOnly(ctx, in); err != nil {
		return err
	}
	owner := strings.TrimSpace(ownerEmail)
	if owner == "" {
		owner = s.projectOwner(ctx, in.ProjectID)
	}
	if owner != "" {
		_ = s.persistUser(ctx, owner, in)
	}
	return nil
}

func (s *Service) load(ctx context.Context, projectID int64, provider string) (storedIntegration, bool) {
	out, ok := s.loadProjectRow(ctx, projectID, provider)
	if ok && strings.TrimSpace(out.AccessToken) != "" {
		return out, true
	}
	if owner := s.projectOwner(ctx, projectID); owner != "" {
		if u, uok := s.loadUser(ctx, owner, provider); uok {
			merged := mergeStored(out, u)
			merged.ProjectID = projectID
			// Keep project-specific binding fields when present.
			if out.ProjectKey != "" {
				merged.ProjectKey = out.ProjectKey
			}
			if out.ProjectName != "" {
				merged.ProjectName = out.ProjectName
			}
			if out.Organization != "" {
				merged.Organization = out.Organization
			}
			if out.SpaceKey != "" {
				merged.SpaceKey = out.SpaceKey
			}
			return merged, true
		}
	}
	return out, ok
}

// GitHubCreds returns the vaulted GitHub access token and organization for a project.
func (s *Service) GitHubCreds(ctx context.Context, projectID int64) (token, org string, ok bool) {
	stored, found := s.load(ctx, projectID, "github")
	token = strings.TrimSpace(stored.AccessToken)
	org = strings.TrimSpace(stored.Organization)
	return token, org, found && token != ""
}

// PostJiraGateEvidence posts a best-effort Blink→Jira gate evidence comment. Never fails the caller hard.
func (s *Service) PostJiraGateEvidence(ctx context.Context, projectID int64, issueKey, gate, body string) (commentID string, err error) {
	issueKey = strings.TrimSpace(issueKey)
	body = strings.TrimSpace(body)
	gate = strings.TrimSpace(gate)
	if issueKey == "" || body == "" {
		return "", fmt.Errorf("issueKey and body required")
	}
	if gate != "" && !strings.Contains(body, gate) {
		body = "[" + gate + "]\n" + body
	}
	ctxJ, err := s.resolveJiraOwned(ctx, projectID, map[string]any{}, "")
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]any{"body": adfDocument(body)})
	status, resp, err := s.do(ctx, http.MethodPost,
		ctxJ.APIBase+"/rest/api/3/issue/"+url.PathEscape(issueKey)+"/comment",
		withJSON(ctxJ.Headers), payload)
	if err != nil || status < 200 || status >= 300 {
		return "", fmt.Errorf("jira comment HTTP %d: %s", status, firstNonEmpty(jsonText(resp, "message"), errString(err)))
	}
	return firstNonEmpty(jsonText(resp, "id"), fmt.Sprintf("%v", jsonRaw(resp, "id"))), nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Service) seal(plain string) (string, error) {
	if strings.TrimSpace(plain) == "" {
		return "", nil
	}
	if s.box == nil {
		return plain, nil
	}
	return s.box.Seal(plain)
}

func (s *Service) open(enc string) (string, error) {
	if strings.TrimSpace(enc) == "" {
		return "", nil
	}
	if s.box == nil {
		return enc, nil
	}
	return s.box.Open(enc)
}

func mergeStored(existing, in storedIntegration) storedIntegration {
	if existing.ProjectID == 0 {
		in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
		return in
	}
	out := existing
	out.Provider = strings.ToLower(strings.TrimSpace(firstNonEmpty(in.Provider, existing.Provider)))
	out.Account = firstNonEmpty(in.Account, existing.Account)
	out.BaseURL = firstNonEmpty(in.BaseURL, existing.BaseURL)
	out.Email = firstNonEmpty(in.Email, existing.Email)
	out.Username = firstNonEmpty(in.Username, existing.Username)
	out.Organization = firstNonEmpty(in.Organization, existing.Organization)
	out.Workspace = firstNonEmpty(in.Workspace, existing.Workspace)
	out.ProjectKey = firstNonEmpty(in.ProjectKey, existing.ProjectKey)
	out.ProjectName = firstNonEmpty(in.ProjectName, existing.ProjectName)
	out.SpaceKey = firstNonEmpty(in.SpaceKey, existing.SpaceKey)
	out.CloudID = firstNonEmpty(in.CloudID, existing.CloudID)
	out.AuthType = firstNonEmpty(in.AuthType, existing.AuthType)
	out.AccessToken = firstNonEmpty(in.AccessToken, existing.AccessToken)
	tokenReplaced := strings.TrimSpace(in.AccessToken) != "" && strings.TrimSpace(in.AccessToken) != strings.TrimSpace(existing.AccessToken)
	out.RefreshToken = firstNonEmpty(in.RefreshToken, existing.RefreshToken)
	if tokenReplaced && strings.TrimSpace(in.RefreshToken) == "" {
		out.RefreshToken = ""
	}
	if in.ExpiresAt != nil {
		out.ExpiresAt = in.ExpiresAt
	} else if tokenReplaced {
		out.ExpiresAt = nil
	}
	out.ProjectID = existing.ProjectID
	if in.ProjectID > 0 {
		out.ProjectID = in.ProjectID
	}
	return out
}

// --- Jira helpers ---

func (s *Service) resolveJira(ctx context.Context, projectID int64, req map[string]any) (jiraCtx, error) {
	return s.resolveJiraOwned(ctx, projectID, req, "")
}

func (s *Service) resolveJiraOwned(ctx context.Context, projectID int64, req map[string]any, ownerEmail string) (jiraCtx, error) {
	stored, _ := s.load(ctx, projectID, "jira")
	cloudID := firstNonEmpty(str(req["cloudId"]), stored.CloudID)
	access := firstNonEmpty(str(req["accessToken"]))
	if access == "" && strings.EqualFold(stored.AuthType, "oauth") {
		access = stored.AccessToken
	}
	token := firstNonEmpty(str(req["token"]))
	if token == "" && !strings.EqualFold(stored.AuthType, "oauth") {
		token = stored.AccessToken
	}
	baseURL := firstNonEmpty(str(req["baseUrl"]), stored.BaseURL)
	email := firstNonEmpty(str(req["email"]), stored.Email)

	if strings.EqualFold(stored.AuthType, "oauth") || (cloudID != "" && access != "") {
		owner := strings.TrimSpace(ownerEmail)
		if owner == "" {
			owner = s.projectOwner(ctx, projectID)
		}
		refreshed, err := s.ensureJiraOAuthFresh(ctx, owner, &stored, false)
		if err != nil && strings.TrimSpace(stored.AccessToken) == "" {
			return jiraCtx{}, err
		}
		if refreshed || strings.TrimSpace(stored.AccessToken) != "" {
			access = firstNonEmpty(stored.AccessToken, access)
			cloudID = firstNonEmpty(cloudID, stored.CloudID)
			baseURL = firstNonEmpty(baseURL, stored.BaseURL)
		}
	}
	return s.buildJiraCtx(cloudID, access, baseURL, email, token)
}

// ensureJiraOAuthFresh refreshes the Atlassian access token when expired/near expiry, or when force=true.
func (s *Service) ensureJiraOAuthFresh(ctx context.Context, ownerEmail string, stored *storedIntegration, force bool) (bool, error) {
	if stored == nil {
		return false, nil
	}
	oauthish := strings.EqualFold(stored.AuthType, "oauth") || (stored.CloudID != "" && stored.AccessToken != "")
	if !oauthish {
		return false, nil
	}
	if strings.TrimSpace(stored.RefreshToken) == "" {
		if force || (stored.ExpiresAt != nil && time.Now().UTC().After(*stored.ExpiresAt)) {
			return false, fmt.Errorf("Jira OAuth session expired. Reconnect Atlassian on Integrations.")
		}
		return false, nil
	}
	needs := force
	if !needs {
		if stored.ExpiresAt == nil {
			// Unknown expiry — refresh proactively; Atlassian access tokens are short-lived.
			needs = true
		} else if time.Until(stored.ExpiresAt.UTC()) < 2*time.Minute {
			needs = true
		}
	}
	if !needs {
		return false, nil
	}
	clientID := strings.TrimSpace(s.cfg.JiraClientID)
	clientSecret := strings.TrimSpace(s.cfg.JiraClientSecret)
	if clientID == "" || clientSecret == "" {
		return false, fmt.Errorf("Jira OAuth credentials are not configured; cannot refresh the access token.")
	}
	tokenBody, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     clientID,
		"client_secret": clientSecret,
		"refresh_token": stored.RefreshToken,
	})
	status, body, err := s.do(ctx, http.MethodPost, "https://auth.atlassian.com/oauth/token",
		map[string]string{"Accept": "application/json", "Content-Type": "application/json"}, tokenBody)
	if err != nil {
		return false, fmt.Errorf("Jira token refresh failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return false, fmt.Errorf("Jira OAuth session expired. Reconnect Atlassian on Integrations. (%s)",
			firstNonEmpty(jsonText(body, "error_description"), jsonText(body, "error"), fmt.Sprintf("HTTP %d", status)))
	}
	access := jsonText(body, "access_token")
	if access == "" {
		return false, fmt.Errorf("Jira token refresh did not return an access token. Reconnect Atlassian.")
	}
	stored.AccessToken = access
	if rt := jsonText(body, "refresh_token"); rt != "" {
		stored.RefreshToken = rt
	}
	if n := jsonInt(body, "expires_in"); n > 0 {
		t := time.Now().UTC().Add(time.Duration(n) * time.Second)
		stored.ExpiresAt = &t
	}
	if !strings.EqualFold(stored.AuthType, "oauth") {
		stored.AuthType = "oauth"
	}
	_ = s.persistOwned(ctx, ownerEmail, *stored)
	return true, nil
}

func (s *Service) jiraFromStored(stored storedIntegration) (jiraCtx, error) {
	if strings.EqualFold(stored.AuthType, "oauth") {
		return s.buildJiraCtx(stored.CloudID, stored.AccessToken, stored.BaseURL, "", "")
	}
	return s.buildJiraCtx("", "", stored.BaseURL, stored.Email, stored.AccessToken)
}

func (s *Service) buildJiraCtx(cloudID, access, baseURL, email, token string) (jiraCtx, error) {
	if cloudID != "" && access != "" {
		api := "https://api.atlassian.com/ex/jira/" + cloudID
		browse := strings.TrimRight(baseURL, "/")
		if browse == "" {
			browse = api
		}
		return jiraCtx{
			APIBase: api, BrowseBase: browse,
			Headers: map[string]string{"Authorization": "Bearer " + access, "Accept": "application/json"},
		}, nil
	}
	if baseURL != "" && email != "" && token != "" {
		host := atlassianHost(baseURL)
		if host == "" {
			return jiraCtx{}, fmt.Errorf("Invalid Atlassian Cloud URL.")
		}
		api := "https://" + host
		auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+token))
		return jiraCtx{
			APIBase: api, BrowseBase: api,
			Headers: map[string]string{"Authorization": auth, "Accept": "application/json"},
		}, nil
	}
	return jiraCtx{}, fmt.Errorf("Connect Jira with OAuth or an API token before creating issues.")
}

func (s *Service) fetchJiraProjects(ctx context.Context, jctx jiraCtx) []map[string]any {
	_, body, err := s.do(ctx, http.MethodGet, jctx.APIBase+"/rest/api/3/project", jctx.Headers, nil)
	if err != nil {
		return []map[string]any{}
	}
	var arr []map[string]any
	_ = json.Unmarshal([]byte(body), &arr)
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		key := str(item["key"])
		if key == "" {
			continue
		}
		avatar := ""
		if au, ok := item["avatarUrls"].(map[string]any); ok {
			avatar = str(au["48x48"])
		}
		out = append(out, map[string]any{
			"id": str(item["id"]), "key": key, "name": str(item["name"]),
			"type": str(item["projectTypeKey"]), "avatar": avatar,
		})
	}
	return out
}

func (s *Service) createJiraIssue(ctx context.Context, jctx jiraCtx, projectKey, issueType, summary, description, parentKey string) (map[string]any, error) {
	if len(summary) > 240 {
		summary = summary[:240]
	}
	fields := map[string]any{
		"project":   map[string]string{"key": projectKey},
		"summary":   summary,
		"issuetype": map[string]string{"name": issueType},
	}
	if strings.TrimSpace(description) != "" {
		fields["description"] = adfDocument(description)
	}
	if strings.TrimSpace(parentKey) != "" {
		fields["parent"] = map[string]string{"key": parentKey}
	}
	payload, _ := json.Marshal(map[string]any{"fields": fields})
	status, body, err := s.do(ctx, http.MethodPost, jctx.APIBase+"/rest/api/3/issue", withJSON(jctx.Headers), payload)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s", jiraAPIError(body, status))
	}
	key := jsonText(body, "key")
	browse := strings.TrimRight(jctx.BrowseBase, "/") + "/browse/" + key
	return map[string]any{
		"jiraKey": key,
		"url":     browse,
		"jiraUrl": browse,
		"status":  "created",
		"message": "Created " + key,
	}, nil
}

func (s *Service) searchBlinkMarked(ctx context.Context, jctx jiraCtx, projectKey string) []map[string]any {
	jql := `project = ` + projectKey + ` AND (description ~ "Source epic:" OR description ~ "Source story:") ORDER BY key ASC`
	payload, _ := json.Marshal(map[string]any{
		"jql": jql, "maxResults": 100,
		"fields": []string{"summary", "description", "issuetype", "project"},
	})
	status, body, err := s.do(ctx, http.MethodPost, jctx.APIBase+"/rest/api/3/search/jql", withJSON(jctx.Headers), payload)
	if err != nil || status < 200 || status >= 300 {
		return []map[string]any{}
	}
	var root map[string]any
	_ = json.Unmarshal([]byte(body), &root)
	issues, _ := root["issues"].([]any)
	out := make([]map[string]any, 0)
	for _, raw := range issues {
		issue, _ := raw.(map[string]any)
		if m := toBlinkMarked(jctx, projectKey, issue); m != nil {
			out = append(out, m)
		}
	}
	return out
}

func (s *Service) fetchBlinkMarked(ctx context.Context, jctx jiraCtx, projectKey, issueKey string) map[string]any {
	_, body, err := s.do(ctx, http.MethodGet,
		jctx.APIBase+"/rest/api/3/issue/"+url.PathEscape(issueKey)+"?fields=summary,description,issuetype,project",
		jctx.Headers, nil)
	if err != nil {
		return nil
	}
	var issue map[string]any
	_ = json.Unmarshal([]byte(body), &issue)
	return toBlinkMarked(jctx, projectKey, issue)
}

func toBlinkMarked(jctx jiraCtx, projectKey string, issue map[string]any) map[string]any {
	if issue == nil {
		return nil
	}
	key := str(issue["key"])
	fields, _ := issue["fields"].(map[string]any)
	if fields == nil {
		return nil
	}
	proj, _ := fields["project"].(map[string]any)
	if key == "" || !strings.EqualFold(str(proj["key"]), projectKey) {
		return nil
	}
	plain := adfToPlain(fields["description"])
	epicID := sourceEpicID(plain)
	storyID := sourceStoryID(plain)
	itype, _ := fields["issuetype"].(map[string]any)
	typeName := str(itype["name"])
	summary := firstNonEmpty(str(fields["summary"]), key)
	urlStr := strings.TrimRight(jctx.BrowseBase, "/") + "/browse/" + key
	if epicID != "" && (strings.EqualFold(typeName, "Epic") || storyID == "") {
		return map[string]any{"key": key, "type": firstNonEmpty(typeName, "Epic"), "summary": summary, "sourceKind": "epic", "sourceId": epicID, "url": urlStr}
	}
	if storyID != "" {
		return map[string]any{"key": key, "type": firstNonEmpty(typeName, "Story"), "summary": summary, "sourceKind": "story", "sourceId": storyID, "url": urlStr}
	}
	return nil
}

func issuesFromSearchBody(body string) []map[string]any {
	var root map[string]any
	_ = json.Unmarshal([]byte(body), &root)
	raw, _ := root["issues"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func (s *Service) listChildren(ctx context.Context, jctx jiraCtx, epicKey string) []map[string]any {
	seen := map[string]struct{}{}
	out := []map[string]any{}
	add := func(issues []map[string]any) {
		for _, m := range issues {
			if m == nil {
				continue
			}
			key := str(m["key"])
			if key == "" || strings.EqualFold(key, epicKey) {
				continue
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, m)
		}
	}
	jql := `parent = ` + epicKey + ` OR "Epic Link" = ` + epicKey
	payload, _ := json.Marshal(map[string]any{
		"jql": jql, "maxResults": 100, "fields": []string{"summary", "description", "issuetype", "project"},
	})
	if status, body, err := s.do(ctx, http.MethodPost, jctx.APIBase+"/rest/api/3/search/jql", withJSON(jctx.Headers), payload); err == nil && status >= 200 && status < 300 {
		add(issuesFromSearchBody(body))
	}
	if status, body, err := s.do(ctx, http.MethodGet, jctx.APIBase+"/rest/agile/1.0/epic/"+url.PathEscape(epicKey)+"/issue?maxResults=100", withJSON(jctx.Headers), nil); err == nil && status >= 200 && status < 300 {
		add(issuesFromSearchBody(body))
	}
	if status, body, err := s.do(ctx, http.MethodGet, jctx.APIBase+"/rest/api/3/issue/"+url.PathEscape(epicKey)+"?fields=subtasks", withJSON(jctx.Headers), nil); err == nil && status >= 200 && status < 300 {
		var issue map[string]any
		_ = json.Unmarshal([]byte(body), &issue)
		fields, _ := issue["fields"].(map[string]any)
		if fields != nil {
			raw, _ := fields["subtasks"].([]any)
			subs := make([]map[string]any, 0, len(raw))
			for _, r := range raw {
				if m, ok := r.(map[string]any); ok {
					subs = append(subs, m)
				}
			}
			add(subs)
		}
	}
	return out
}

func (s *Service) unmarkedChildren(ctx context.Context, jctx jiraCtx, epicKey string) []string {
	foreign := []string{}
	for _, child := range s.listChildren(ctx, jctx, epicKey) {
		key := str(child["key"])
		fields, _ := child["fields"].(map[string]any)
		plain := adfToPlain(nil)
		if fields != nil {
			plain = adfToPlain(fields["description"])
		}
		if sourceEpicID(plain) == "" && sourceStoryID(plain) == "" {
			foreign = append(foreign, key)
		}
	}
	return foreign
}

func (s *Service) deleteJiraIssue(ctx context.Context, jctx jiraCtx, issueKey string) error {
	return s.deleteJiraIssueOpts(ctx, jctx, issueKey, true)
}

func (s *Service) deleteJiraIssueOpts(ctx context.Context, jctx jiraCtx, issueKey string, deleteSubtasks bool) error {
	headers := map[string]string{}
	for k, v := range jctx.Headers {
		if strings.EqualFold(k, "Content-Type") {
			continue
		}
		headers[k] = v
	}
	headers["Accept"] = "application/json"
	target := jctx.APIBase + "/rest/api/3/issue/" + url.PathEscape(issueKey)
	if deleteSubtasks {
		target += "?deleteSubtasks=true"
	}
	status, body, err := s.do(ctx, http.MethodDelete, target, headers, nil)
	if err != nil {
		return err
	}
	if status == 204 || status == 404 || (status >= 200 && status < 300) {
		return nil
	}
	detail := jiraAPIError(body, status)
	if status == 401 {
		return fmt.Errorf("Jira OAuth session cannot delete %s. Reconnect Atlassian on Integrations, then try again", issueKey)
	}
	if status == 403 || jiraDeletePermissionDenied(detail) {
		if have, ok := s.jiraHasPermission(ctx, jctx, "DELETE_ISSUES", issueKey); ok && !have {
			return fmt.Errorf("%s: your Atlassian account cannot delete issues in this Jira project. Ask a Jira admin to grant the Delete Issues permission, then try again", issueKey)
		}
		if jiraDeletePermissionDenied(detail) {
			return fmt.Errorf("%s: your Atlassian account cannot delete issues in this Jira project. Ask a Jira admin to grant the Delete Issues permission, then try again", issueKey)
		}
		return fmt.Errorf("%s: %s", issueKey, detail)
	}
	return fmt.Errorf("%s", detail)
}

func jiraDeletePermissionDenied(detail string) bool {
	low := strings.ToLower(detail)
	return strings.Contains(low, "do not have permission to delete") ||
		(strings.Contains(low, "you do not have permission") && strings.Contains(low, "delete")) ||
		strings.Contains(low, "delete issues")
}

func (s *Service) jiraHasPermission(ctx context.Context, jctx jiraCtx, permission, issueKey string) (have bool, ok bool) {
	q := "/rest/api/3/mypermissions?permissions=" + url.QueryEscape(permission)
	if strings.TrimSpace(issueKey) != "" {
		q += "&issueKey=" + url.QueryEscape(issueKey)
	}
	status, body, err := s.do(ctx, http.MethodGet, jctx.APIBase+q, jctx.Headers, nil)
	if err != nil || status < 200 || status >= 300 {
		return false, false
	}
	var root map[string]any
	if json.Unmarshal([]byte(body), &root) != nil {
		return false, false
	}
	perms, _ := root["permissions"].(map[string]any)
	if perms == nil {
		return false, false
	}
	entry, _ := perms[permission].(map[string]any)
	if entry == nil {
		return false, false
	}
	switch v := entry["havePermission"].(type) {
	case bool:
		return v, true
	default:
		return false, false
	}
}

func jiraDeleteBlockedByOtherIssues(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "has issues associated") ||
		strings.Contains(low, "has sub-task") ||
		strings.Contains(low, "has subtask") ||
		strings.Contains(low, "you cannot delete") ||
		strings.Contains(low, "cannot be deleted") ||
		(strings.Contains(low, "must delete") && strings.Contains(low, "child"))
}

func buildStoryDescription(story map[string]any) string {
	var b strings.Builder
	asA, iWant, soThat := str(story["asA"]), str(story["iWant"]), str(story["soThat"])
	if asA != "" && iWant != "" {
		b.WriteString("As a " + asA + ", I want " + iWant)
		if soThat != "" {
			b.WriteString(" so that " + soThat)
		}
		b.WriteByte('\n')
	} else if obj := strings.TrimSpace(str(story["objective"])); obj != "" {
		b.WriteString(obj)
		b.WriteByte('\n')
	}
	if ac, ok := story["acceptanceCriteria"].([]any); ok && len(ac) > 0 {
		b.WriteString("Acceptance criteria:")
		for _, item := range ac {
			line := strings.TrimSpace(fmt.Sprint(item))
			if line != "" {
				b.WriteString("\n- " + line)
			}
		}
	}
	if id := str(story["id"]); id != "" {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("Source story: " + id)
	}
	return b.String()
}

func adfDocument(text string) map[string]any {
	content := make([]any, 0)
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		content = append(content, map[string]any{
			"type": "paragraph",
			"content": []any{
				map[string]any{"type": "text", "text": line},
			},
		})
	}
	if len(content) == 0 {
		t := strings.TrimSpace(text)
		if t == "" {
			t = " "
		}
		content = append(content, map[string]any{
			"type": "paragraph",
			"content": []any{
				map[string]any{"type": "text", "text": t},
			},
		})
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}
}

func adfToPlain(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		var b strings.Builder
		walkADF(t, &b)
		return b.String()
	default:
		return ""
	}
}

func commentParentID(cm map[string]any) string {
	if id := str(cm["parentId"]); id != "" {
		return id
	}
	switch p := cm["parent"].(type) {
	case string:
		return strings.TrimSpace(p)
	case float64:
		return strconv.FormatInt(int64(p), 10)
	case map[string]any:
		return firstNonEmpty(str(p["id"]), str(p["commentId"]))
	default:
		return ""
	}
}

func walkADF(node map[string]any, b *strings.Builder) {
	typ := str(node["type"])
	if typ == "hardBreak" {
		b.WriteByte('\n')
		return
	}
	if text := str(node["text"]); text != "" {
		if b.Len() > 0 {
			prev := b.String()
			if !strings.HasSuffix(prev, "\n") && !strings.HasSuffix(prev, " ") {
				b.WriteByte(' ')
			}
		}
		b.WriteString(text)
	}
	if arr, ok := node["content"].([]any); ok {
		for _, c := range arr {
			if m, ok := c.(map[string]any); ok {
				walkADF(m, b)
			}
		}
	}
	switch typ {
	case "paragraph", "heading", "bulletList", "orderedList", "listItem", "blockquote", "codeBlock", "rule":
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteByte('\n')
		}
	}
}

func sourceEpicID(plain string) string {
	m := sourceEpicRe.FindStringSubmatch(plain)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

func sourceStoryID(plain string) string {
	m := sourceStoryRe.FindStringSubmatch(plain)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

// --- OAuth / HTTP helpers ---

func oauthConfigured(cfg config.Config, provider string) string {
	switch provider {
	case "jira":
		return cfg.JiraRedirectURI
	case "github":
		return cfg.GitHubRedirectURI
	case "figma":
		return cfg.FigmaRedirectURI
	default:
		return ""
	}
}

func publicAPIBase(r *http.Request) string {
	proto := firstNonEmpty(r.Header.Get("X-Forwarded-Proto"), "http")
	if i := strings.Index(proto, ","); i >= 0 {
		proto = strings.TrimSpace(proto[:i])
	}
	host := firstNonEmpty(r.Header.Get("X-Forwarded-Host"), r.Host)
	if i := strings.Index(host, ","); i >= 0 {
		host = strings.TrimSpace(host[:i])
	}
	if host == "" {
		return ""
	}
	return proto + "://" + host
}

func optionalAuth(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("Authorization"))
}

func (s *Service) do(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (int, string, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return 0, "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := s.http.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return res.StatusCode, string(b), nil
}

func withJSON(h map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		out[k] = v
	}
	out["Content-Type"] = "application/json"
	out["Accept"] = "application/json"
	return out
}

func writeOAuthHTML(w http.ResponseWriter, title, msgType string, r *http.Request) {
	code := jsEscape(r.URL.Query().Get("code"))
	state := jsEscape(r.URL.Query().Get("state"))
	errMsg := r.URL.Query().Get("error")
	if d := r.URL.Query().Get("error_description"); d != "" {
		if errMsg != "" {
			errMsg += ": " + d
		} else {
			errMsg = d
		}
	}
	errSafe := jsEscape(errMsg)
	closeDelay := "1500"
	if errSafe != "" {
		closeDelay = "8000"
	}
	html := fmt.Sprintf(`<!DOCTYPE html>
<html><head><title>%s Authorization</title>
<style>body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#0f172a;color:#f8fafc;text-align:center}
.card{padding:2rem;border-radius:12px;background:#1e293b;border:1px solid #334155;max-width:400px}h2{margin-top:0;color:#38bdf8}</style>
</head><body><div class="card"><h2>%s</h2><p id="msg">Completing authorization with Blink...</p></div>
<script>
const code="%s",state="%s",error="%s";
const title=error?"Authorization failed":"%s Connected";
document.querySelector('h2').innerText=title;
if(window.opener){window.opener.postMessage({type:'%s',code:code||null,state:state||null,error:error||null},'*');
document.getElementById('msg').innerText=error?error:'Closing popup window...';setTimeout(()=>window.close(),%s);}
else{document.getElementById('msg').innerText=error||'Authorization complete. You can close this window.';}
</script></body></html>`, title, title, code, state, errSafe, title, msgType, closeDelay)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}

func jsEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "'", `\'`)
	return s
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

func readJSON(r *http.Request, dest any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

func parseID(v any) int64 {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	case json.Number:
		n, _ := t.Int64()
		return n
	default:
		n, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(t)), 10, 64)
		return n
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func nullStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func jiraAPIError(body string, status int) string {
	detail := firstNonEmpty(jsonText(body, "message"), fmt.Sprintf("Jira returned HTTP %d.", status))
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return detail
	}
	if msgs, ok := m["errorMessages"].([]any); ok && len(msgs) > 0 {
		return fmt.Sprint(msgs[0])
	}
	if errs, ok := m["errors"].(map[string]any); ok && len(errs) > 0 {
		for k, v := range errs {
			return fmt.Sprintf("%s: %v", k, v)
		}
	}
	return detail
}

func jsonText(body string, keys ...string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return ""
	}
	for _, k := range keys {
		if v := str(m[k]); v != "" && v != "<nil>" {
			return v
		}
	}
	return ""
}

func jsonInt(body, key string) int {
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return 0
	}
	switch t := m[key].(type) {
	case float64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	default:
		return 0
	}
}

func jsonRaw(body, key string) any {
	var m map[string]any
	_ = json.Unmarshal([]byte(body), &m)
	return m[key]
}

func parseFigmaTeamID(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if regexp.MustCompile(`^\d+$`).MatchString(v) {
		return v
	}
	if m := figmaTeamRe.FindStringSubmatch(v); len(m) > 1 {
		return m[1]
	}
	return v
}

func atlassianHost(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		if !strings.Contains(baseURL, "://") {
			u, err = url.Parse("https://" + strings.TrimSpace(baseURL))
		}
		if err != nil || u.Host == "" {
			return ""
		}
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, ".atlassian.net") && host != "atlassian.net" {
		return ""
	}
	return u.Host
}
