package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nisha-ts-40599/blink-backend/internal/project"
	"github.com/nisha-ts-40599/blink-backend/internal/s3ws"
	"github.com/nisha-ts-40599/blink-backend/internal/zipkit"
)

func overlaysFromAgentJSON(raw json.RawMessage) []s3ws.OverlayFile {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	arr, _ := root["overlayFiles"].([]any)
	out := make([]s3ws.OverlayFile, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		path, _ := m["path"].(string)
		content, _ := m["content"].(string)
		path = zipkit.SanitizeOverlayPath(path)
		if path == "" {
			continue
		}
		out = append(out, s3ws.OverlayFile{Path: path, Content: content})
	}
	return out
}

func (s *Server) persistAgentOverlays(r *http.Request, projectName string, id int64, raw json.RawMessage) {
	files := overlaysFromAgentJSON(raw)
	if len(files) == 0 {
		return
	}
	pid := id
	if _, err := s.s3.PutOverlayFiles(r.Context(), projectName, &pid, files); err != nil {
		// Non-fatal for the client response — local draft / JSON still returned.
		_ = err
	}
}

func (s *Server) requireReadyWorkspace(w http.ResponseWriter, r *http.Request, projectName string, id int64) bool {
	pid := id
	s.s3.EnsureProvisioned(r.Context(), projectName, &pid)
	st := s.s3.Status(projectName, &pid)
	status, _ := st["workspaceStatus"].(string)
	if status == "failed" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"status":  "error",
			"message": "Project workspace provisioning failed. Retry Save & Continue, then try again.",
			"errors":  []string{"workspace_failed"},
		})
		return false
	}
	// preparing or ready (or nil when S3 disabled) — allow advisory agents to proceed.
	return true
}

func (s *Server) advisoryPayload(r *http.Request, projectName string, id int64, body map[string]any) map[string]any {
	if body == nil {
		body = map[string]any{}
	}
	body["projectName"] = projectName
	body["projectId"] = strconv.FormatInt(id, 10)
	if _, ok := body["actor"]; !ok || strings.TrimSpace(fmt.Sprint(body["actor"])) == "" {
		body["actor"] = sessionEmail(r)
	}
	return body
}

func (s *Server) writeAgentResult(w http.ResponseWriter, r *http.Request, projectName string, id int64, command string, raw json.RawMessage, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	// Neon audit / S3 overlays must not hold the HTTP response under pool pressure.
	carrier := requestCarrier(r.Header.Get)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		files := overlaysFromAgentJSON(raw)
		if len(files) > 0 {
			pid := id
			_, _ = s.s3.PutOverlayFiles(ctx, projectName, &pid, files)
		}
		s.noteAgentRun(ctx, carrier, id, command, raw)
	}()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// softOwnedProjectName resolves ownership with a short Neon budget; falls back to
// the request body so agent calls stay unblocked when the pool is busy.
func (s *Server) softOwnedProjectName(r *http.Request, id int64, body map[string]any) (string, error) {
	if body == nil {
		body = map[string]any{}
	}
	projectName := strings.TrimSpace(fmt.Sprint(body["projectName"]))
	actor := sessionEmail(r)
	ownCtx, ownCancel := context.WithTimeout(r.Context(), 6*time.Second)
	p, err := s.proj.RequireOwned(ownCtx, id, actor)
	ownCancel()
	if err == nil {
		if projectName == "" {
			projectName = p.ProjectName
		}
		return projectName, nil
	}
	if projectName != "" {
		return projectName, nil
	}
	return "", busyOr(err)
}

func (s *Server) confirmProductScope(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body map[string]any
	_ = readJSON(r, &body)
	projectName := strings.TrimSpace(fmt.Sprint(body["projectName"]))
	actor := sessionEmail(r)

	// Short ownership check — under Neon pressure fall back to agent-only confirm.
	ownCtx, ownCancel := context.WithTimeout(r.Context(), 6*time.Second)
	p, err := s.proj.RequireOwned(ownCtx, id, actor)
	ownCancel()
	if err != nil {
		if projectName == "" {
			writeErr(w, busyOr(err))
			return
		}
		payload := s.advisoryPayload(r, projectName, id, body)
		raw, agentErr := s.agent.ConfirmProductScope(r.Context(), payload)
		if agentErr != nil {
			writeErr(w, agentErr)
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			s.persistAgentOverlaysCtx(ctx, projectName, id, raw)
			_ = s.afterProductScopeConfirm(ctx, id, actor, payload, raw)
		}()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
		return
	}
	if projectName == "" {
		projectName = p.ProjectName
	}
	if !s.requireReadyWorkspace(w, r, projectName, id) {
		return
	}
	payload := s.advisoryPayload(r, projectName, id, body)
	// Heal stakeholders gate in background — do not block confirm on Neon.
	if s.canonical != nil {
		stakes := make([]map[string]string, 0, len(p.Stakeholders))
		stakeReqs := make([]project.StakeholderRequest, 0, len(p.Stakeholders))
		for _, st := range p.Stakeholders {
			stakes = append(stakes, map[string]string{"roleCode": st.RoleCode, "name": st.Name, "email": st.Email})
			stakeReqs = append(stakeReqs, project.StakeholderRequest{RoleCode: st.RoleCode, Name: st.Name, Email: st.Email})
		}
		digest := canonicalDigestStakeholders(stakeReqs)
		go func() {
			gateCtx, gateCancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer gateCancel()
			_ = s.canonical.RecordStakeholderConfirmation(gateCtx, id, stakes, digest, actor)
		}()
	}
	raw, err := s.agent.ConfirmProductScope(r.Context(), payload)
	if err != nil {
		writeErr(w, err)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		s.persistAgentOverlaysCtx(ctx, projectName, id, raw)
		_ = s.afterProductScopeConfirm(ctx, id, actor, payload, raw)
	}()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// architectureProposal is agent-first: skip Neon canonical command bookkeeping so
// Project Shape auto-suggest keeps working when the pool is cold or busy.
func (s *Server) architectureProposal(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body map[string]any
	_ = readJSON(r, &body)
	projectName := strings.TrimSpace(fmt.Sprint(body["projectName"]))
	actor := sessionEmail(r)

	ownCtx, ownCancel := context.WithTimeout(r.Context(), 6*time.Second)
	p, err := s.proj.RequireOwned(ownCtx, id, actor)
	ownCancel()
	if err == nil && projectName == "" {
		projectName = p.ProjectName
	}
	if projectName == "" {
		projectName = "project"
	}
	payload := s.advisoryPayload(r, projectName, id, body)
	raw, err := s.agent.ArchitectureProposal(r.Context(), payload)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// recommendRepositoryRoster is agent-first: repository roster from requirement + shape.
func (s *Server) recommendRepositoryRoster(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body map[string]any
	_ = readJSON(r, &body)
	projectName := strings.TrimSpace(fmt.Sprint(body["projectName"]))
	actor := sessionEmail(r)

	ownCtx, ownCancel := context.WithTimeout(r.Context(), 6*time.Second)
	p, err := s.proj.RequireOwned(ownCtx, id, actor)
	ownCancel()
	if err == nil && projectName == "" {
		projectName = p.ProjectName
	}
	if projectName == "" {
		projectName = "project"
	}
	payload := s.advisoryPayload(r, projectName, id, body)
	log.Printf(
		"recommend-repository-roster start project=%d model=%q actor=%q",
		id, strings.TrimSpace(fmt.Sprint(body["repositoryModel"])), actor,
	)
	raw, err := s.agent.RecommendRepositoryRoster(r.Context(), payload)
	if err != nil {
		log.Printf("recommend-repository-roster agent error project=%d err=%v", id, err)
		writeErr(w, err)
		return
	}
	log.Printf("recommend-repository-roster ok project=%d bytes=%d", id, len(raw))
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// recommendTechnologyStack is agent-first: skip Neon so Technology can apply
// AI-SDLC stack suggestions even when the pool is cold.
func (s *Server) recommendTechnologyStack(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body map[string]any
	_ = readJSON(r, &body)
	projectName := strings.TrimSpace(fmt.Sprint(body["projectName"]))
	actor := sessionEmail(r)

	ownCtx, ownCancel := context.WithTimeout(r.Context(), 6*time.Second)
	p, err := s.proj.RequireOwned(ownCtx, id, actor)
	ownCancel()
	if err == nil && projectName == "" {
		projectName = p.ProjectName
	}
	if projectName == "" {
		projectName = "project"
	}
	payload := s.advisoryPayload(r, projectName, id, body)
	repoCount := 0
	if arr, ok := body["repositories"].([]any); ok {
		repoCount = len(arr)
	}
	log.Printf(
		"recommend-technology-stack start project=%d repos=%d topology=%q actor=%q",
		id, repoCount, strings.TrimSpace(fmt.Sprint(body["topology"])), actor,
	)
	raw, err := s.agent.RecommendTechnologyStack(r.Context(), payload)
	if err != nil {
		log.Printf("recommend-technology-stack agent error project=%d err=%v", id, err)
		writeErr(w, err)
		return
	}
	log.Printf("recommend-technology-stack ok project=%d summary=%s", id, summarizeRecommendTechnologyStack(raw))
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func summarizeRecommendTechnologyStack(raw json.RawMessage) string {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return "unparseable_agent_json"
	}
	status, _ := root["status"].(string)
	rows, _ := root["repoTechnologies"].([]any)
	parts := make([]string, 0, len(rows)+1)
	if status != "" {
		parts = append(parts, "status="+status)
	}
	for _, item := range rows {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		repoID := strings.TrimSpace(fmt.Sprint(m["repoId"]))
		rowStatus := strings.TrimSpace(fmt.Sprint(m["status"]))
		lang := strings.TrimSpace(fmt.Sprint(m["language"]))
		fw := strings.TrimSpace(fmt.Sprint(m["framework"]))
		optN := 0
		if opts, ok := m["technologyOptions"].([]any); ok {
			optN = len(opts)
		}
		parts = append(parts, fmt.Sprintf("%s:%s lang=%q fw=%q opts=%d", repoID, rowStatus, lang, fw, optN))
	}
	if len(parts) == 0 {
		return "empty_summary"
	}
	return strings.Join(parts, " | ")
}

// confirmTopology is agent-first; Neon shape-gate write runs in the background.
func (s *Server) confirmTopology(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body map[string]any
	_ = readJSON(r, &body)
	projectName := strings.TrimSpace(fmt.Sprint(body["projectName"]))
	actor := sessionEmail(r)

	ownCtx, ownCancel := context.WithTimeout(r.Context(), 6*time.Second)
	p, err := s.proj.RequireOwned(ownCtx, id, actor)
	ownCancel()
	if err == nil && projectName == "" {
		projectName = p.ProjectName
	}
	if projectName == "" {
		projectName = "project"
	}
	payload := s.advisoryPayload(r, projectName, id, body)
	raw, err := s.agent.ConfirmTopology(r.Context(), payload)
	if err != nil {
		writeErr(w, err)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		_ = s.afterTopologyConfirm(ctx, id, actor, payload, raw)
	}()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (s *Server) classifyWork(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body map[string]any
	_ = readJSON(r, &body)
	projectName, err := s.softOwnedProjectName(r, id, body)
	if err != nil {
		writeErr(w, err)
		return
	}
	if projectName == "" {
		projectName = "project"
	}
	payload := s.advisoryPayload(r, projectName, id, body)
	raw, err := s.agent.ClassifyWork(r.Context(), payload)
	s.writeAgentResult(w, r, projectName, id, "classify-work", raw, err)
}

func (s *Server) proposeDesigns(w http.ResponseWriter, r *http.Request) {
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
	raw, err := s.agent.ProposeDesigns(r.Context(), payload)
	s.writeAgentResult(w, r, p.ProjectName, id, "propose-designs", raw, err)
}

func (s *Server) proposeDesignsStandalone(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = readJSON(r, &body)
	if body == nil {
		body = map[string]any{}
	}
	if _, ok := body["actor"]; !ok || strings.TrimSpace(fmt.Sprint(body["actor"])) == "" {
		body["actor"] = sessionEmail(r)
	}
	raw, err := s.agent.ProposeDesigns(r.Context(), body)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (s *Server) createSpec(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body map[string]any
	_ = readJSON(r, &body)
	projectName, err := s.softOwnedProjectName(r, id, body)
	if err != nil {
		writeErr(w, err)
		return
	}
	if projectName == "" {
		projectName = "project"
	}
	payload := s.advisoryPayload(r, projectName, id, body)
	raw, err := s.agent.CreateSpec(r.Context(), payload)
	s.writeAgentResult(w, r, projectName, id, "create-spec", raw, err)
}

func (s *Server) technicalPlan(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body map[string]any
	_ = readJSON(r, &body)
	projectName, err := s.softOwnedProjectName(r, id, body)
	if err != nil {
		writeErr(w, err)
		return
	}
	if projectName == "" {
		projectName = "project"
	}
	payload := s.advisoryPayload(r, projectName, id, body)
	raw, err := s.agent.TechnicalPlan(r.Context(), payload)
	s.writeAgentResult(w, r, projectName, id, "technical-plan", raw, err)
}

func (s *Server) sdlcStart(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body map[string]any
	_ = readJSON(r, &body)
	projectName := strings.TrimSpace(fmt.Sprint(body["projectName"]))
	actor := sessionEmail(r)

	ownCtx, ownCancel := context.WithTimeout(r.Context(), 6*time.Second)
	p, err := s.proj.RequireOwned(ownCtx, id, actor)
	ownCancel()
	if err != nil {
		if projectName == "" {
			writeErr(w, busyOr(err))
			return
		}
		payload := s.advisoryPayload(r, projectName, id, body)
		raw, agentErr := s.agent.SdlcStart(r.Context(), payload)
		if agentErr != nil {
			writeErr(w, agentErr)
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			_ = s.afterSdlcStart(ctx, id, raw)
		}()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
		return
	}
	if projectName == "" {
		projectName = p.ProjectName
	}
	if !s.requireReadyWorkspace(w, r, projectName, id) {
		return
	}
	payload := s.advisoryPayload(r, projectName, id, body)
	raw, err := s.agent.SdlcStart(r.Context(), payload)
	if err != nil {
		writeErr(w, err)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		_ = s.afterSdlcStart(ctx, id, raw)
	}()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (s *Server) sdlcNext(w http.ResponseWriter, r *http.Request) {
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
	raw, err := s.agent.SdlcNext(r.Context(), payload)
	s.writeAgentResult(w, r, p.ProjectName, id, "sdlc-next", raw, err)
}
