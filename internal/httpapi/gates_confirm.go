package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nisha-ts-40599/blink-backend/internal/canonical"
)

type gateConfirmBody struct {
	Kind             string `json:"kind"`
	Digest           string `json:"digest"`
	ExpectedRevision *int64 `json:"expectedRevision"`
}

func (s *Server) canonicalGateConfirm(w http.ResponseWriter, r *http.Request) {
	id, err := projectIDParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body gateConfirmBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	kind := strings.ToLower(strings.TrimSpace(body.Kind))
	digest := strings.TrimSpace(body.Digest)
	if digest == "" && kind != "repo-technology-all" && kind != "repository-roster" && kind != "repositories" {
		writeErr(w, badRequest("digest is required"))
		return
	}
	actor := sessionEmail(r)
	if body.ExpectedRevision != nil {
		snap, err := s.canonical.Snapshot(r.Context(), id, actor)
		if err != nil {
			writeErr(w, err)
			return
		}
		if *body.ExpectedRevision != snap.Revision {
			writeErr(w, badRequest("stale revision"))
			return
		}
	}
	switch kind {
	case "shape", "project-shape":
		err = s.canonical.RecordShapeConfirmation(r.Context(), id, digest, actor)
	case "g-groom", "groom", "stakeholder-qa":
		err = s.canonical.RecordGroomConfirmation(r.Context(), id, digest, actor)
	case "g-plan", "work-plan", "sdlc-plan":
		err = s.canonical.RecordWorkPlanConfirmation(r.Context(), id, digest, actor)
	case "repository-roster", "repositories":
		if digest == "" {
			err = s.canonical.ConfirmCurrentRepositoryRoster(r.Context(), id, actor)
		} else {
			err = s.canonical.RecordRepositoryRosterConfirmation(r.Context(), id, digest, actor)
		}
	case "repo-technology-all":
		var n int
		n, err = s.canonical.ConfirmAllRepoTechnologies(r.Context(), id, actor)
		if err == nil && n == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"status": "noop", "message": "no technology rows to confirm"})
			return
		}
	default:
		writeErr(w, badRequest("unknown gate kind"))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	snap, err := s.canonical.Snapshot(r.Context(), id, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"kind":     kind,
		"digest":   digest,
		"snapshot": snap,
	})
}

func (s *Server) canonicalGroomingReadiness(w http.ResponseWriter, r *http.Request) {
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
	var state []byte
	if p.WizardState != nil {
		state = p.WizardState
	}
	readiness := canonical.DeriveGroomingReadiness(state)
	writeJSON(w, http.StatusOK, map[string]any{"readiness": readiness})
}
