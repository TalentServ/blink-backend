package httpapi

import (
	"context"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/nisha-ts-40599/blink-backend/internal/codex"
	"github.com/nisha-ts-40599/blink-backend/internal/githubgit"
)

func stageCodexAttachments(items []codexAttachment, overlays map[string]string) (images []string, note string, stored []map[string]any) {
	var lines []string
	for _, item := range items {
		kind := strings.ToLower(strings.TrimSpace(item.Kind))
		name := path.Base(strings.TrimSpace(item.Name))
		if name == "." || name == "/" || name == "" {
			name = "attachment"
		}
		switch kind {
		case "image":
			if strings.TrimSpace(item.DataURL) == "" {
				continue
			}
			images = append(images, item.DataURL)
			lines = append(lines, "Image attached: "+name)
			stored = append(stored, map[string]any{"name": name, "kind": "image", "dataUrl": item.DataURL})
		case "file_ref":
			ref := strings.TrimSpace(item.Path)
			if ref == "" {
				continue
			}
			lines = append(lines, "Referenced file: @"+strings.TrimPrefix(ref, "@"))
			stored = append(stored, map[string]any{"name": name, "kind": "file_ref", "path": ref})
		default:
			if strings.TrimSpace(item.Content) == "" {
				continue
			}
			rel := "attachments/" + name
			overlays[rel] = item.Content
			lines = append(lines, "Staged attachment: /workspace/"+rel)
			stored = append(stored, map[string]any{"name": name, "kind": "file", "path": rel})
		}
	}
	if len(lines) > 0 {
		note = "Attachments:\n- " + strings.Join(lines, "\n- ")
	}
	return images, note, stored
}

func (s *Server) persistWorkbench(ctx context.Context, result *codex.RunResult, baseline, product map[string]string, attachments []map[string]any) {
	if s.codexStore == nil || result == nil || result.CodexSessionID == uuid.Nil {
		return
	}
	if result.TurnID != uuid.Nil {
		_ = s.codexStore.SaveTurnWorkbench(ctx, result.TurnID, product, attachments)
	}
	sess, err := s.codexStore.GetSession(ctx, result.CodexSessionID)
	if err != nil || sess == nil {
		return
	}
	base := sess.BaseFiles
	if base == nil {
		base = map[string]string{}
	}
	uncommitted := sess.UncommittedFiles
	if uncommitted == nil {
		uncommitted = map[string]string{}
	}
	for p, content := range product {
		if _, ok := base[p]; !ok {
			if prev, ok := baseline[p]; ok {
				base[p] = prev
			}
		}
		uncommitted[p] = content
	}
	_ = s.codexStore.SaveTrees(ctx, sess.ID, base, sess.StagedFiles, uncommitted)
}

func (s *Server) codexWorkbench(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := s.proj.RequireOwned(r.Context(), id, sessionEmail(r)); err != nil {
		writeErr(w, err)
		return
	}
	issueID := strings.TrimSpace(r.URL.Query().Get("issueId"))
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = "uncommitted"
	}
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	commitSHA := strings.TrimSpace(r.URL.Query().Get("commitSha"))

	payload := map[string]any{
		"status":    "ok",
		"issueId":   issueID,
		"scope":     scope,
		"turns":     []any{},
		"diff":      []codex.FileDiff{},
		"files":     []map[string]any{},
		"commits":   []githubgit.CommitInfo{},
		"branch":    "",
		"additions": 0,
		"deletions": 0,
	}
	if s.codexStore == nil || issueID == "" {
		writeJSON(w, http.StatusOK, payload)
		return
	}
	sess, err := s.codexStore.SessionForIssue(r.Context(), id, issueID)
	if err != nil || sess == nil {
		writeJSON(w, http.StatusOK, payload)
		return
	}
	turns, _ := s.codexStore.ListTurns(r.Context(), sess.ID)
	branch := "blink/implement-" + sanitizeBranch(issueID)
	payload["branch"] = branch
	payload["sessionId"] = sess.ID.String()
	payload["sessionStatus"] = string(sess.Status)
	payload["turns"] = workbenchTurns(turns)

	var diffs []codex.FileDiff
	note := ""
	switch scope {
	case "last_turn":
		var prev map[string]string
		var current map[string]string
		if len(turns) > 0 {
			current = turns[len(turns)-1].Files
		}
		if len(turns) > 1 {
			prev = turns[len(turns)-2].Files
		}
		diffs = codex.LastTurnDiff(sess.BaseFiles, prev, current)
	case "staged":
		diffs = codex.DiffMaps(sess.BaseFiles, sess.StagedFiles)
	case "branch", "commit":
		diffs, note = s.githubScopeDiff(r.Context(), id, owner, repo, branch, scope, commitSHA)
		if note != "" && len(diffs) == 0 {
			diffs = codex.DiffMaps(sess.BaseFiles, sess.UncommittedFiles)
		}
	default:
		scope = "uncommitted"
		diffs = codex.DiffMaps(sess.BaseFiles, sess.UncommittedFiles)
	}
	add, del := 0, 0
	for _, d := range diffs {
		add += d.Additions
		del += d.Deletions
	}
	stagedPaths := make([]string, 0, len(sess.StagedFiles))
	for p := range sess.StagedFiles {
		stagedPaths = append(stagedPaths, p)
	}
	sort.Strings(stagedPaths)
	payload["scope"] = scope
	payload["diff"] = diffs
	payload["stagedPaths"] = stagedPaths
	payload["additions"] = add
	payload["deletions"] = del
	payload["scopeNote"] = note
	payload["files"] = workbenchTree(r.Context(), s, id, owner, repo, branch, sess)
	payload["commits"] = s.workbenchCommits(r.Context(), id, owner, repo, branch)
	writeJSON(w, http.StatusOK, payload)
}

func workbenchTurns(turns []codex.Turn) []map[string]any {
	out := make([]map[string]any, 0, len(turns))
	for _, t := range turns {
		activity := []map[string]any{}
		for _, cmd := range t.Commands {
			if delta, _ := cmd["delta"].(string); strings.TrimSpace(delta) != "" {
				activity = append(activity, map[string]any{"kind": "command", "text": delta, "status": string(t.Status)})
			}
		}
		out = append(out, map[string]any{
			"id":          t.ID.String(),
			"turnNumber":  t.TurnNumber,
			"prompt":      t.UserPrompt,
			"status":      string(t.Status),
			"markdown":    t.OutputText,
			"attachments": t.Attachments,
			"activity":    activity,
			"files":       t.Files,
			"createdAt":   t.CreatedAt,
			"finishedAt":  t.FinishedAt,
		})
	}
	return out
}

func (s *Server) githubScopeDiff(ctx context.Context, projectID int64, owner, repo, branch, scope, commitSHA string) ([]codex.FileDiff, string) {
	gh, owner, repo, base, ok := s.workbenchGitHub(ctx, projectID, owner, repo)
	if !ok {
		return nil, "GitHub compare is unavailable; showing the working tree."
	}
	head := branch
	if scope == "commit" && commitSHA != "" {
		head = commitSHA
		base = commitSHA + "^"
	}
	files, err := gh.Compare(ctx, owner, repo, base, head)
	if err != nil {
		return nil, "GitHub compare failed; showing the working tree."
	}
	out := make([]codex.FileDiff, 0, len(files))
	for _, f := range files {
		out = append(out, codex.ParseUnifiedPatch(f.Path, f.Status, f.Patch, f.Additions, f.Deletions))
	}
	return out, ""
}

func (s *Server) workbenchCommits(ctx context.Context, projectID int64, owner, repo, branch string) []githubgit.CommitInfo {
	gh, owner, repo, _, ok := s.workbenchGitHub(ctx, projectID, owner, repo)
	if !ok {
		return []githubgit.CommitInfo{}
	}
	rows, err := gh.ListCommits(ctx, owner, repo, branch, 20)
	if err != nil {
		return []githubgit.CommitInfo{}
	}
	return rows
}

func (s *Server) workbenchGitHub(ctx context.Context, projectID int64, owner, repo string) (*githubgit.Client, string, string, string, bool) {
	token, org, ok := s.integ.GitHubCreds(ctx, projectID)
	if !ok {
		return nil, "", "", "", false
	}
	gh := githubgit.New(token)
	if strings.TrimSpace(owner) == "" {
		owner = org
	}
	if strings.TrimSpace(owner) == "" {
		owner, _ = gh.CurrentUserLogin(ctx)
	}
	if strings.TrimSpace(repo) == "" {
		repos := pickAppRepos(owner, s.reposForImplement(ctx, projectID, nil))
		if len(repos) == 0 {
			return nil, "", "", "", false
		}
		owner = repos[0].Owner
		repo = repos[0].Repo
	}
	base := implementBaseBranch(ctx, gh, owner, repo)
	return gh, owner, repo, base, owner != "" && repo != ""
}

func workbenchTree(ctx context.Context, s *Server, projectID int64, owner, repo, branch string, sess *codex.Session) []map[string]any {
	statusOf := map[string]string{}
	for p := range sess.BaseFiles {
		statusOf[p] = "clean"
	}
	for p, content := range sess.UncommittedFiles {
		if base, ok := sess.BaseFiles[p]; !ok {
			statusOf[p] = "added"
		} else if base != content {
			statusOf[p] = "modified"
		} else {
			statusOf[p] = "clean"
		}
	}
	for p := range sess.StagedFiles {
		if _, ok := statusOf[p]; !ok {
			statusOf[p] = "modified"
		}
	}
	paths := map[string]struct{}{}
	for p := range statusOf {
		paths[p] = struct{}{}
	}
	if gh, owner, repo, _, ok := s.workbenchGitHub(ctx, projectID, owner, repo); ok {
		if tree, err := gh.TreePaths(ctx, owner, repo, branch); err == nil {
			if len(tree) > 400 {
				tree = tree[:400]
			}
			for _, p := range tree {
				paths[p] = struct{}{}
				if _, exists := statusOf[p]; !exists {
					statusOf[p] = "clean"
				}
			}
		}
	}
	out := make([]map[string]any, 0, len(paths))
	for p := range paths {
		out = append(out, map[string]any{
			"path":     p,
			"language": languageFor(p),
			"status":   statusOf[p],
		})
	}
	return out
}

func languageFor(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".py":
		return "python"
	case ".css":
		return "css"
	case ".md":
		return "markdown"
	case ".json":
		return "json"
	case ".sql":
		return "sql"
	default:
		return "text"
	}
}

func (s *Server) codexWorkbenchFile(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := s.proj.RequireOwned(r.Context(), id, sessionEmail(r)); err != nil {
		writeErr(w, err)
		return
	}
	issueID := strings.TrimSpace(r.URL.Query().Get("issueId"))
	filePath := strings.TrimSpace(r.URL.Query().Get("path"))
	if filePath == "" || strings.Contains(filePath, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "path is required"})
		return
	}
	content := ""
	source := "missing"
	if s.codexStore != nil && issueID != "" {
		if sess, err := s.codexStore.SessionForIssue(r.Context(), id, issueID); err == nil && sess != nil {
			if v, ok := sess.UncommittedFiles[filePath]; ok {
				content, source = v, "working"
			} else if v, ok := sess.BaseFiles[filePath]; ok {
				content, source = v, "base"
			}
		}
	}
	if source == "missing" {
		owner := r.URL.Query().Get("owner")
		repo := r.URL.Query().Get("repo")
		branch := "blink/implement-" + sanitizeBranch(issueID)
		if gh, owner, repo, _, ok := s.workbenchGitHub(r.Context(), id, owner, repo); ok {
			if raw, err := gh.FileBytes(r.Context(), owner, repo, branch, filePath); err == nil {
				content, source = string(raw), "github"
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "path": filePath, "content": content, "source": source})
}

func (s *Server) codexGitAction(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := s.proj.RequireOwned(r.Context(), id, sessionEmail(r)); err != nil {
		writeErr(w, err)
		return
	}
	body, _ := readCodexBody(r)
	action, _ := body.Raw["action"].(string)
	filePath, _ := body.Raw["path"].(string)
	hunkIndex := -1
	switch v := body.Raw["hunkIndex"].(type) {
	case float64:
		hunkIndex = int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			hunkIndex = n
		}
	}
	if s.codexStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "message": "Codex store is not configured"})
		return
	}
	sess, err := s.codexStore.SessionForIssue(r.Context(), id, body.IssueID)
	if err != nil || sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"status": "error", "message": "No Codex session for this story"})
		return
	}
	base, uncommitted, staged, commitFiles, err := codex.ApplyGitAction(sess.BaseFiles, sess.UncommittedFiles, sess.StagedFiles, action, filePath, hunkIndex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": err.Error()})
		return
	}
	evidence := map[string]any{}
	if strings.EqualFold(action, "commit_staged") && len(commitFiles) > 0 {
		branch := firstNonEmpty(body.Branch, "blink/implement-"+sanitizeBranch(body.IssueID))
		if gh, owner, repo, _, ok := s.workbenchGitHub(r.Context(), id, body.Owner, body.Repo); ok {
			files := make([]githubgit.File, 0, len(commitFiles))
			for p, content := range commitFiles {
				files = append(files, githubgit.File{Path: p, Content: content})
			}
			cr, perr := gh.CommitOntoExistingBranch(r.Context(), owner, repo, branch, "chore: stage workbench changes", files)
			if perr != nil {
				evidence["commitError"] = perr.Error()
			} else {
				evidence["sha"] = cr.SHA
				evidence["branch"] = branch
			}
		} else {
			evidence["draftPrSkipped"] = "github_token_missing"
		}
	}
	if err := s.codexStore.SaveTrees(r.Context(), sess.ID, base, staged, uncommitted); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "action": action, "evidence": evidence})
}

func (s *Server) codexSteerStream(w http.ResponseWriter, r *http.Request) {
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
	if !s.requireCodex(w) {
		return
	}
	body, raw := readCodexBody(r)
	text := strings.TrimSpace(body.Instruction)
	if text == "" {
		text = strings.TrimSpace(body.FollowUp)
	}
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "instruction is required"})
		return
	}
	_, _, stored := stageCodexAttachments(body.Attachments, map[string]string{})
	images := []string{}
	for _, item := range body.Attachments {
		if strings.EqualFold(item.Kind, "image") && item.DataURL != "" {
			images = append(images, item.DataURL)
		}
	}
	if err := s.codex.Steer(r.Context(), id, body.IssueID, text, images); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "injected", "message": "Steering message sent to the live session.", "attachments": stored})
		return
	}
	_ = s.codex.CancelActive(r.Context(), id, body.IssueID)
	_, writeSSE, ok := startSSE(w)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "message": "SSE not supported"})
		return
	}
	emit := func(event string, payload map[string]any) bool { return writeSSE(event, payload) }
	body.FollowUp = text
	body.Instruction = text
	body.Confirm = true
	body.GitWritten = true
	result, prs, evidence, runErr := s.executeCodexImplement(r.Context(), id, p.ProjectName, p.WizardState, sessionEmail(r), body.IssueID, body, raw, applyInPlace, emit)
	done := map[string]any{
		"status": "ok", "message": "Steering restarted the turn.", "draftPullRequests": prs, "evidence": evidence,
	}
	if result != nil {
		done["codexSessionId"] = result.CodexSessionID.String()
		done["turnNumber"] = result.TurnNumber
	}
	if runErr != nil {
		if evidence != nil && evidence["blocked"] == true {
			done["status"] = "blocked"
		} else {
			done["status"] = "error"
			done["message"] = runErr.Error()
			_ = writeSSE("error", map[string]any{"message": runErr.Error()})
		}
	}
	_ = writeSSE("done", done)
}
