package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/nisha-ts-40599/blink-backend/internal/githubgit"
	"github.com/nisha-ts-40599/blink-backend/internal/s3ws"
)

type overlayPayload struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type repoRef struct {
	Name    string `json:"name"`
	HTMLURL string `json:"htmlUrl"`
	Purpose string `json:"purpose"`
}

func filterAIOverlayFiles(files []overlayPayload) []githubgit.File {
	out := make([]githubgit.File, 0, len(files))
	for _, f := range files {
		path := strings.TrimSpace(strings.TrimPrefix(f.Path, "/"))
		if path == "" || strings.Contains(path, "..") {
			continue
		}
		if strings.HasPrefix(path, ".cursor/ai-sdlc/") || path == "requirement.md" || strings.HasSuffix(path, "/requirement.md") {
			out = append(out, githubgit.File{Path: path, Content: f.Content})
		}
	}
	return out
}

func pickWorkspaceRepo(org string, repos []repoRef, explicit string) (owner, repo string, err error) {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		if strings.Contains(explicit, "/") || strings.Contains(explicit, "://") {
			return githubgit.ParseOwnerRepo(explicit)
		}
		if org != "" {
			return org, explicit, nil
		}
		return "", "", fmt.Errorf("workspaceRepo %q needs organization or owner/repo", explicit)
	}
	for _, r := range repos {
		name := strings.TrimSpace(r.Name)
		url := strings.TrimSpace(r.HTMLURL)
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, "-workspace") || strings.HasSuffix(lower, "_workspace") || strings.Contains(lower, "workspace") {
			if url != "" {
				return githubgit.ParseOwnerRepo(url)
			}
			if org != "" && name != "" {
				return org, name, nil
			}
			if strings.Contains(name, "/") {
				return githubgit.ParseOwnerRepo(name)
			}
		}
	}
	return "", "", fmt.Errorf("no *-workspace repository found; pass workspaceRepo or create one first")
}

// pickWorkspaceRepoWithPersonalOwner handles repositories created through
// GitHub's /user/repos endpoint, where no organization is configured.
func pickWorkspaceRepoWithPersonalOwner(org, personalOwner string, repos []repoRef, explicit string) (owner, repo string, err error) {
	owner, repo, err = pickWorkspaceRepo(org, repos, explicit)
	if err == nil || strings.TrimSpace(org) != "" || strings.TrimSpace(personalOwner) == "" {
		return owner, repo, err
	}
	return pickWorkspaceRepo(personalOwner, repos, explicit)
}

func pickAppRepos(org string, repos []repoRef) []struct{ Owner, Repo, Kind string } {
	var out []struct{ Owner, Repo, Kind string }
	for _, r := range repos {
		name := strings.TrimSpace(r.Name)
		purpose := strings.ToLower(strings.TrimSpace(r.Purpose))
		lower := strings.ToLower(name)
		kind := ""
		switch {
		case strings.HasSuffix(lower, "-workspace"), strings.HasSuffix(lower, "_workspace"), strings.Contains(lower, "workspace"):
			continue
		case purpose == "backend", strings.HasSuffix(lower, "-backend"), strings.Contains(lower, "backend"):
			kind = "backend"
		case purpose == "frontend", strings.HasSuffix(lower, "-frontend"), strings.Contains(lower, "frontend"), strings.Contains(lower, "web"):
			kind = "frontend"
		default:
			continue
		}
		owner, repo, err := "", "", error(nil)
		if r.HTMLURL != "" {
			owner, repo, err = githubgit.ParseOwnerRepo(r.HTMLURL)
		} else if strings.Contains(name, "/") {
			owner, repo, err = githubgit.ParseOwnerRepo(name)
		} else if org != "" {
			owner, repo = org, name
		} else {
			continue
		}
		if err != nil || owner == "" || repo == "" {
			continue
		}
		out = append(out, struct{ Owner, Repo, Kind string }{Owner: owner, Repo: repo, Kind: kind})
	}
	return out
}

func (s *Server) gitApply(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.proj.RequireOwned(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Confirm       bool             `json:"confirm"`
		OverlayFiles  []overlayPayload `json:"overlayFiles"`
		Repositories  []repoRef        `json:"repositories"`
		WorkspaceRepo string           `json:"workspaceRepo"`
		CommitMessage string           `json:"commitMessage"`
		IssueKey      string           `json:"issueKey"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": err.Error()})
		return
	}
	if !body.Confirm {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "Explicit confirm=true is required before Git apply.",
			"errors":  []string{"confirm_required"},
		})
		return
	}
	files := filterAIOverlayFiles(body.OverlayFiles)
	if len(files) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "No .cursor/ai-sdlc overlay files to commit.",
			"errors":  []string{"overlay_empty"},
		})
		return
	}
	token, org, ok := s.integ.GitHubCreds(r.Context(), id)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "Connect GitHub (vaulted PAT/OAuth) before Git apply.",
			"errors":  []string{"github_token_missing"},
		})
		return
	}
	gh := githubgit.New(token)
	personalOwner := ""
	if strings.TrimSpace(org) == "" {
		personalOwner, _ = gh.CurrentUserLogin(r.Context())
	}
	owner, repo, err := pickWorkspaceRepoWithPersonalOwner(org, personalOwner, body.Repositories, body.WorkspaceRepo)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": err.Error(), "errors": []string{"workspace_repo_missing"}})
		return
	}
	msg := strings.TrimSpace(body.CommitMessage)
	if msg == "" {
		msg = "chore(blink): apply AI-SDLC workspace overlay"
	}
	result, err := gh.CommitFiles(r.Context(), owner, repo, "", msg, files)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"status":     "error",
			"message":    "Git apply failed (fail closed): " + err.Error(),
			"errors":     []string{"git_apply_failed"},
			"gitWritten": false,
		})
		return
	}
	// Persist overlays to S3 as well (best effort).
	s3files := make([]s3ws.OverlayFile, 0, len(files))
	for _, f := range files {
		s3files = append(s3files, s3ws.OverlayFile{Path: f.Path, Content: f.Content})
	}
	pid := id
	_, _ = s.s3.PutOverlayFiles(r.Context(), p.ProjectName, &pid, s3files)

	evidence := map[string]any{}
	if key := strings.TrimSpace(body.IssueKey); key != "" {
		comment := fmt.Sprintf("Blink overlay commit: %s/%s@%s (%d files).\n%s\n(Not a G-BOOTSTRAP gate approval.)",
			owner, repo, result.SHA, result.TreeCount, result.URL)
		if cid, e := s.integ.PostJiraGateEvidence(r.Context(), id, key, "G-PR-OPEN", comment); e == nil {
			evidence["jiraCommentId"] = cid
			evidence["issueKey"] = key
		} else {
			evidence["jiraError"] = e.Error()
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"message":    fmt.Sprintf("Committed %d overlay file(s) to %s/%s@%s", result.TreeCount, owner, repo, result.SHA[:min(7, len(result.SHA))]),
		"gitWritten": true,
		"commit":     result,
		"evidence":   evidence,
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *Server) implementStepApply(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.proj.RequireOwned(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !s.requireReadyWorkspace(w, r, p.ProjectName, id) {
		return
	}
	var body map[string]any
	_ = readJSON(r, &body)
	if body == nil {
		body = map[string]any{}
	}
	confirm, _ := body["confirm"].(bool)
	if !confirm {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status": "error", "message": "Explicit confirm=true is required before implement-step.",
			"errors": []string{"confirm_required"},
		})
		return
	}
	gitWritten, _ := body["gitWritten"].(bool)
	if !gitWritten {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status": "error", "message": "Git apply must succeed (gitWritten=true) before implement-step.",
			"errors": []string{"git_apply_required"},
		})
		return
	}

	payload := s.advisoryPayload(r, p.ProjectName, id, body)
	raw, err := s.agent.ImplementStep(r.Context(), payload)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.persistAgentOverlays(r, p.ProjectName, id, raw)

	var agent map[string]any
	_ = json.Unmarshal(raw, &agent)
	if st, _ := agent["status"].(string); st != "" && st != "ok" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
		return
	}

	impl, _ := agent["implementStep"].(map[string]any)
	if impl == nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"status": "error", "message": "Agent returned no implementStep payload.", "agent": agent})
		return
	}
	filesRaw, _ := impl["files"].([]any)
	commitMsg, _ := impl["commitMessage"].(string)
	issueID, _ := agent["issueId"].(string)
	if issueID == "" {
		issueID, _ = impl["issueId"].(string)
	}
	if commitMsg == "" {
		commitMsg = "feat: blink implement-step"
	}

	reposIn := []repoRef{}
	if arr, ok := body["repositories"].([]any); ok {
		b, _ := json.Marshal(arr)
		_ = json.Unmarshal(b, &reposIn)
	}
	token, org, ok := s.integ.GitHubCreds(r.Context(), id)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "Connect GitHub before implement-step.", "errors": []string{"github_token_missing"}})
		return
	}
	appRepos := pickAppRepos(org, reposIn)
	if len(appRepos) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "No *-backend / *-frontend app repos found.", "errors": []string{"app_repos_missing"}})
		return
	}

	byHint := map[string][]githubgit.File{}
	for _, item := range filesRaw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := m["path"].(string)
		content, _ := m["content"].(string)
		hint, _ := m["repoHint"].(string)
		path = strings.TrimSpace(strings.TrimPrefix(path, "/"))
		if path == "" || strings.HasPrefix(path, ".cursor/") || strings.HasPrefix(path, "automation_sdlc/") {
			continue
		}
		hint = strings.ToLower(strings.TrimSpace(hint))
		if hint == "" || hint == "app" {
			hint = "backend"
		}
		byHint[hint] = append(byHint[hint], githubgit.File{Path: path, Content: content})
	}
	if len(byHint) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "No safe app-repo patches in implementStep.", "errors": []string{"patches_empty"}})
		return
	}

	gh := githubgit.New(token)
	branch := "blink/implement-" + sanitizeBranch(issueID)
	prs := make([]map[string]any, 0)
	for _, target := range appRepos {
		files := byHint[target.Kind]
		if len(files) == 0 && target.Kind == "backend" {
			files = byHint["backend"]
		}
		if len(files) == 0 {
			continue
		}
		title := fmt.Sprintf("Blink implement-step: %s", issueID)
		bodyText := fmt.Sprintf("Draft PR from Blink implement-step for `%s`.\n\nDo not merge automatically.", issueID)
		pr, err := gh.CommitBranchAndDraftPR(r.Context(), target.Owner, target.Repo, branch, "", title, bodyText, commitMsg, files)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"status":  "error",
				"message": "Draft PR failed (fail closed): " + err.Error(),
				"partial": prs,
				"errors":  []string{"draft_pr_failed"},
			})
			return
		}
		prs = append(prs, map[string]any{
			"url": pr.URL, "number": pr.Number, "branch": pr.Branch,
			"owner": pr.Owner, "repo": pr.Repo, "sha": pr.SHA, "kind": target.Kind,
		})
	}
	if len(prs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "No patches matched app repos.", "errors": []string{"no_matching_repos"}})
		return
	}

	evidence := map[string]any{}
	if key, _ := body["issueKey"].(string); strings.TrimSpace(key) != "" {
		urls := make([]string, 0, len(prs))
		for _, pr := range prs {
			if u, _ := pr["url"].(string); u != "" {
				urls = append(urls, u)
			}
		}
		comment := fmt.Sprintf("Blink G-PR-OPEN: draft PR(s) opened for %s:\n%s", issueID, strings.Join(urls, "\n"))
		if cid, e := s.integ.PostJiraGateEvidence(r.Context(), id, key, "G-PR-OPEN", comment); e == nil {
			evidence["jiraCommentId"] = cid
		} else {
			evidence["jiraError"] = e.Error()
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "ok",
		"message":           fmt.Sprintf("Opened %d draft PR(s) for implement-step.", len(prs)),
		"implementStep":     impl,
		"draftPullRequests": prs,
		"issueId":           issueID,
		"nextCommand":       "/qa-validation",
		"overlayFiles":      agent["overlayFiles"],
		"evidence":          evidence,
	})
}

func sanitizeBranch(issue string) string {
	issue = strings.TrimSpace(issue)
	if issue == "" {
		return "unassigned"
	}
	var b strings.Builder
	for _, r := range issue {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := b.String()
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

func (s *Server) qaValidation(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.proj.RequireOwned(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !s.requireReadyWorkspace(w, r, p.ProjectName, id) {
		return
	}
	var body map[string]any
	_ = readJSON(r, &body)
	if body == nil {
		body = map[string]any{}
	}

	// Enrich with GitHub CI status when draft PR SHA / repo provided.
	token, _, ok := s.integ.GitHubCreds(r.Context(), id)
	if ok {
		owner, _ := body["owner"].(string)
		repo, _ := body["repo"].(string)
		sha, _ := body["sha"].(string)
		if owner != "" && repo != "" && sha != "" {
			gh := githubgit.New(token)
			state, _, _ := gh.CombinedStatus(r.Context(), owner, repo, sha)
			if state != "" {
				body["ciStatus"] = state
			}
		}
	}

	payload := s.advisoryPayload(r, p.ProjectName, id, body)
	raw, err := s.agent.QaValidation(r.Context(), payload)
	s.writeAgentResult(w, r, p.ProjectName, id, raw, err)
}

func (s *Server) jiraGateEvidence(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	_, err = s.proj.RequireOwned(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		IssueKey string `json:"issueKey"`
		Gate     string `json:"gate"`
		Message  string `json:"message"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": err.Error()})
		return
	}
	gate := strings.ToUpper(strings.TrimSpace(body.Gate))
	allowed := map[string]bool{"G-GROOM": true, "G-PLAN": true, "G-BOOTSTRAP": true, "G-PR-OPEN": true}
	if !allowed[gate] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "gate must be G-GROOM, G-PLAN, G-BOOTSTRAP, or G-PR-OPEN"})
		return
	}
	cid, err := s.integ.PostJiraGateEvidence(r.Context(), id, body.IssueKey, gate, body.Message)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "skipped",
			"message": "Jira evidence best-effort failed: " + err.Error(),
			"gate":    gate,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "gate": gate, "issueKey": body.IssueKey, "commentId": cid,
		"message": "Gate evidence posted to Jira.",
	})
}

// Grooming advisory proxies (same pattern as classify/spec/plan).
func (s *Server) groomingStakeholderPack(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.proj.RequireOwned(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !s.requireReadyWorkspace(w, r, p.ProjectName, id) {
		return
	}
	var body map[string]any
	_ = readJSON(r, &body)
	payload := s.advisoryPayload(r, p.ProjectName, id, body)
	raw, err := s.agent.GroomingStakeholderPack(r.Context(), payload)
	s.writeAgentResult(w, r, p.ProjectName, id, raw, err)
}

func (s *Server) groomingRevision(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.proj.RequireOwned(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !s.requireReadyWorkspace(w, r, p.ProjectName, id) {
		return
	}
	var body map[string]any
	_ = readJSON(r, &body)
	payload := s.advisoryPayload(r, p.ProjectName, id, body)
	raw, err := s.agent.GroomingRevision(r.Context(), payload)
	s.writeAgentResult(w, r, p.ProjectName, id, raw, err)
}

func (s *Server) groomingSignOffCapture(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.proj.RequireOwned(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if !s.requireReadyWorkspace(w, r, p.ProjectName, id) {
		return
	}
	var body map[string]any
	_ = readJSON(r, &body)
	payload := s.advisoryPayload(r, p.ProjectName, id, body)
	raw, err := s.agent.GroomingSignOffCapture(r.Context(), payload)
	s.writeAgentResult(w, r, p.ProjectName, id, raw, err)
}
