package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nisha-ts-40599/blink-backend/internal/canonical"
)

func (s *Server) canonicalGraph(w http.ResponseWriter, r *http.Request) {
	id, err := projectIDParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.proj.RequireOwned(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(p.WizardState) > 0 {
		_, _ = s.canonical.RecomputeGraph(r.Context(), id, p.WizardState)
	}
	view, err := s.canonical.GetGraph(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) canonicalRequirementHistory(w http.ResponseWriter, r *http.Request) {
	id, err := projectIDParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	_, err = s.proj.RequireOwned(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("view"))
	revs, err := s.canonical.ListRequirementRevisions(r.Context(), id, kind, 30)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": revs})
}

type domainGateBody struct {
	Kind             string `json:"kind"`
	Digest           string `json:"digest"`
	RepoID           string `json:"repoId"`
	Substage         string `json:"substage"`
	ExpectedRevision *int64 `json:"expectedRevision"`
	ConfirmAllTech   bool   `json:"confirmAllTech"`
}

func (s *Server) canonicalDomainGate(w http.ResponseWriter, r *http.Request) {
	id, err := projectIDParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body domainGateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	actor := sessionEmail(r)
	kind := strings.ToLower(strings.TrimSpace(body.Kind))
	switch kind {
	case "repository-roster", "repositories":
		err = s.canonical.RecordRepositoryRosterConfirmation(r.Context(), id, strings.TrimSpace(body.Digest), actor)
	case "repo-technology", "technology":
		if body.ConfirmAllTech {
			_, err = s.canonical.ConfirmAllRepoTechnologies(r.Context(), id, actor)
		} else {
			err = s.canonical.ConfirmRepoTechnology(r.Context(), id, body.RepoID, strings.TrimSpace(body.Digest), actor)
		}
	case "ship-substage", "ship":
		_, err = s.canonical.EnsureShipSession(r.Context(), id, strings.TrimSpace(body.Substage), actor)
	default:
		writeErr(w, badRequest("unknown domain gate kind"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	_ = s.canonical.RefreshEligibility(r.Context(), id, actor)
	snap, _ := s.canonical.Snapshot(r.Context(), id, actor)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "snapshot": snap})
}

func (s *Server) canonicalShipSession(w http.ResponseWriter, r *http.Request) {
	id, err := projectIDParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	_, err = s.proj.RequireOwned(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	detail, err := s.canonical.GetShipSessionDetail(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"session": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": detail})
}

func (s *Server) canonicalShipCheckpoint(w http.ResponseWriter, r *http.Request) {
	id, err := projectIDParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Substage       string         `json:"substage"`
		StepKind       string         `json:"stepKind"`
		IdempotencyKey string         `json:"idempotencyKey"`
		Payload        map[string]any `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	if err := s.canonical.ValidateShipSubstage(r.Context(), id, body.Substage); err != nil {
		writeErr(w, badRequest(err.Error()))
		return
	}
	actor := sessionEmail(r)
	sess, err := s.canonical.EnsureShipSession(r.Context(), id, body.Substage, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	stepKind := strings.TrimSpace(body.StepKind)
	if stepKind == "" {
		stepKind = canonical.ShipStepSubstageNav
	}
	_ = s.canonical.RecordShipStepWithResult(r.Context(), sess.ID, stepKind, body.IdempotencyKey, "completed", body.Payload, map[string]any{
		"substage": body.Substage,
	})
	_ = s.canonical.RefreshEligibility(r.Context(), id, actor)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "session": sess})
}
