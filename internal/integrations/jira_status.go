package integrations

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var (
	jiraIssueKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9]+-\d+$`)
	errConnectJira = errors.New("Connect Jira and select a project for this Blink project first.")
)

func (s *Service) JiraIssueStatuses(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "Add at least one Jira issue key.")
		return
	}
	projectID := parseID(req["projectId"])
	keys := sanitizeIssueKeys(req["issueKeys"])
	if projectID <= 0 || len(keys) == 0 {
		writeErr(w, http.StatusBadRequest, "Add at least one Jira issue key.")
		return
	}
	ctxJ, projectKey, err := s.readyJira(r, projectID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	issues := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		item, err := s.fetchIssueStatus(r, ctxJ, projectKey, key)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		issues = append(issues, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": issues})
}

func (s *Service) TransitionJiraIssue(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "Jira transition request is required.")
		return
	}
	target := strings.ToLower(strings.TrimSpace(str(req["target"])))
	if target != "done" && target != "closed" {
		writeErr(w, http.StatusBadRequest, "Choose done or closed.")
		return
	}
	issueKey := strings.TrimSpace(str(req["issueKey"]))
	if !jiraIssueKeyRe.MatchString(issueKey) {
		writeErr(w, http.StatusBadRequest, "Jira issue key is not valid.")
		return
	}
	projectID := parseID(req["projectId"])
	ctxJ, projectKey, err := s.readyJira(r, projectID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	current, err := s.fetchIssueStatus(r, ctxJ, projectKey, issueKey)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if strings.EqualFold(str(current["name"]), target) {
		writeJSON(w, http.StatusOK, current)
		return
	}
	listURL := ctxJ.APIBase + "/rest/api/3/issue/" + url.PathEscape(issueKey) + "/transitions"
	status, body, err := s.do(r.Context(), http.MethodGet, listURL, ctxJ.Headers, nil)
	if err != nil || status >= 400 {
		writeErr(w, http.StatusBadGateway, jiraAPIError(body, status))
		return
	}
	transitionID := pickTransitionID(body, target)
	if transitionID == "" {
		writeErr(w, http.StatusConflict, issueKey+" has no "+target+" step in its Jira workflow.")
		return
	}
	payload, _ := json.Marshal(map[string]any{"transition": map[string]string{"id": transitionID}})
	status, body, err = s.do(r.Context(), http.MethodPost, listURL, withJSON(ctxJ.Headers), payload)
	if err != nil || status >= 400 {
		writeErr(w, http.StatusBadGateway, issueKey+": "+jiraAPIError(body, status))
		return
	}
	updated, err := s.fetchIssueStatus(r, ctxJ, projectKey, issueKey)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if projectID > 0 {
		s.recordProjection(r.Context(), projectID, "jira", "issue.transition", map[string]any{
			"issueKey": issueKey,
			"target":   target,
			"status":   updated,
		})
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Service) readyJira(r *http.Request, projectID int64) (jiraCtx, string, error) {
	stored, ok := s.load(r.Context(), projectID, "jira")
	if !ok || strings.TrimSpace(stored.ProjectKey) == "" {
		return jiraCtx{}, "", errConnectJira
	}
	owner := strings.TrimSpace(r.Header.Get("X-Blink-Owner-Email"))
	if _, err := s.ensureJiraOAuthFresh(r.Context(), owner, &stored, false); err != nil {
		return jiraCtx{}, "", err
	}
	ctxJ, err := s.jiraFromStored(stored)
	if err != nil {
		return jiraCtx{}, "", err
	}
	return ctxJ, stored.ProjectKey, nil
}

func (s *Service) fetchIssueStatus(r *http.Request, ctxJ jiraCtx, projectKey, issueKey string) (map[string]any, error) {
	item, status, body := s.readIssueStatus(r, ctxJ.APIBase, ctxJ.Headers, projectKey, issueKey)
	if status == http.StatusNotFound && !gatewayNotFound(body) {
		return map[string]any{"key": issueKey, "name": nil, "category": "missing"}, nil
	}
	if item == nil && gatewayNotFound(body) {
		site := strings.TrimRight(ctxJ.BrowseBase, "/")
		api := strings.TrimRight(ctxJ.APIBase, "/")
		if site != "" && !strings.EqualFold(site, api) && strings.Contains(site, ".atlassian.net") {
			item, status, body = s.readIssueStatus(r, site, ctxJ.Headers, projectKey, issueKey)
		}
	}
	if item != nil {
		return item, nil
	}
	if status == http.StatusNotFound {
		return map[string]any{"key": issueKey, "name": nil, "category": "missing"}, nil
	}
	return nil, fmt.Errorf("%s", jiraAPIError(body, status))
}

func (s *Service) readIssueStatus(r *http.Request, apiBase string, headers map[string]string, projectKey, issueKey string) (map[string]any, int, string) {
	rawURL := strings.TrimRight(apiBase, "/") + "/rest/api/3/issue/" + url.PathEscape(issueKey) + "?fields=status,project"
	status, body, err := s.do(r.Context(), http.MethodGet, rawURL, headers, nil)
	if err != nil || status >= 400 {
		return nil, status, body
	}
	var issue map[string]any
	if json.Unmarshal([]byte(body), &issue) != nil {
		return nil, status, body
	}
	fields, _ := issue["fields"].(map[string]any)
	project, _ := fields["project"].(map[string]any)
	if pk := str(project["key"]); pk != "" && !strings.EqualFold(pk, projectKey) {
		return map[string]any{"key": issueKey, "name": nil, "category": "missing"}, status, body
	}
	st, _ := fields["status"].(map[string]any)
	name := strings.TrimSpace(str(st["name"]))
	catNode, _ := st["statusCategory"].(map[string]any)
	category := statusCategory(str(catNode["key"]), name)
	var nameOut any
	if name != "" {
		nameOut = name
	}
	return map[string]any{"key": firstNonEmpty(str(issue["key"]), issueKey), "name": nameOut, "category": category}, status, body
}

func statusCategory(raw, name string) string {
	switch raw {
	case "new":
		return "todo"
	case "indeterminate":
		return "in-progress"
	case "done":
		return "done"
	default:
		if name == "" {
			return "missing"
		}
		return "unknown"
	}
}

func pickTransitionID(body, target string) string {
	var root map[string]any
	if json.Unmarshal([]byte(body), &root) != nil {
		return ""
	}
	list, _ := root["transitions"].([]any)
	for _, raw := range list {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		name := strings.ToLower(str(item["name"]))
		to, _ := item["to"].(map[string]any)
		toName := strings.ToLower(str(to["name"]))
		if name == target || toName == target || strings.Contains(toName, target) || strings.Contains(name, target) {
			return str(item["id"])
		}
	}
	return ""
}

func sanitizeIssueKeys(v any) []string {
	list, _ := v.([]any)
	if list == nil {
		if one := strings.TrimSpace(str(v)); jiraIssueKeyRe.MatchString(one) {
			return []string{one}
		}
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, item := range list {
		key := strings.TrimSpace(str(item))
		if !jiraIssueKeyRe.MatchString(key) || seen[strings.ToUpper(key)] {
			continue
		}
		seen[strings.ToUpper(key)] = true
		out = append(out, key)
	}
	return out
}

func gatewayNotFound(body string) bool {
	return strings.Contains(strings.ToLower(body), "page not found")
}
