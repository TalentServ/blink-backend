package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

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
	s.persistAgentOverlays(r, projectName, id, raw)
	s.noteAgentRun(r.Context(), requestCarrier(r.Header.Get), id, command, raw)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (s *Server) confirmProductScope(w http.ResponseWriter, r *http.Request) {
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
	raw, err := s.agent.ConfirmProductScope(r.Context(), payload)
	if err == nil {
		s.afterProductScopeConfirm(r.Context(), id, sessionEmail(r), body, raw)
	}
	s.writeAgentResult(w, r, p.ProjectName, id, "confirm-product-scope", raw, err)
}

func (s *Server) classifyWork(w http.ResponseWriter, r *http.Request) {
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
	raw, err := s.agent.ClassifyWork(r.Context(), payload)
	s.writeAgentResult(w, r, p.ProjectName, id, "classify-work", raw, err)
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
	raw, err := s.agent.CreateSpec(r.Context(), payload)
	s.writeAgentResult(w, r, p.ProjectName, id, "create-spec", raw, err)
}

func (s *Server) technicalPlan(w http.ResponseWriter, r *http.Request) {
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
	raw, err := s.agent.TechnicalPlan(r.Context(), payload)
	s.writeAgentResult(w, r, p.ProjectName, id, "technical-plan", raw, err)
}

func (s *Server) sdlcStart(w http.ResponseWriter, r *http.Request) {
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
	raw, err := s.agent.SdlcStart(r.Context(), payload)
	if err == nil {
		s.afterSdlcStart(r.Context(), id, raw)
	}
	s.writeAgentResult(w, r, p.ProjectName, id, "sdlc-start", raw, err)
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
