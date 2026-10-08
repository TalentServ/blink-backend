package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

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
	dbCtx, dbCancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer dbCancel()
	// Soft check: allow confirm when revision only advanced (wizard.synced / autosave).
	// Reject only if the client is ahead of the server (impossible / confused client).
	if body.ExpectedRevision != nil {
		snap, err := s.canonical.Snapshot(dbCtx, id, actor)
		if err != nil {
			writeErr(w, busyOr(err))
			return
		}
		if *body.ExpectedRevision > snap.Revision {
			writeErr(w, badRequest(fmt.Sprintf("stale revision: expected %d have %d", *body.ExpectedRevision, snap.Revision)))
			return
		}
	}
	switch kind {
	case "shape", "project-shape":
		err = s.canonical.RecordShapeConfirmation(dbCtx, id, digest, actor)
	case "architecture":
		err = s.canonical.ConfirmArchitecture(dbCtx, id, digest, actor)
	case "g-groom", "groom", "stakeholder-qa":
		err = s.canonical.RecordGroomConfirmation(dbCtx, id, digest, actor)
	case "g-plan", "work-plan", "sdlc-plan":
		err = s.canonical.RecordWorkPlanConfirmation(dbCtx, id, digest, actor)
	case "repository-roster", "repositories":
		if digest == "" {
			err = s.canonical.ConfirmCurrentRepositoryRoster(dbCtx, id, actor)
		} else {
			err = s.canonical.RecordRepositoryRosterConfirmation(dbCtx, id, digest, actor)
		}
	case "repo-technology-all":
		var n int
		n, err = s.canonical.ConfirmAllRepoTechnologies(dbCtx, id, actor)
		if err == nil && n == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"status": "noop", "message": "no technology rows to confirm"})
			return
		}
	default:
		writeErr(w, badRequest("unknown gate kind"))
		return
	}
	if err != nil {
		writeErr(w, busyOr(err))
		return
	}
	snap, err := s.canonical.Snapshot(dbCtx, id, actor)
	if err != nil {
		// Gate write succeeded; snapshot is best-effort under Neon pressure.
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"kind":   kind,
			"digest": digest,
		})
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
	// Prefer first-class canonical grooming facts once a governed session has
	// been initialized. Fall back to the legacy wizard draft for projects that
	// have not migrated yet.
	if readiness, err := s.canonical.GroomingReadiness(r.Context(), id); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"readiness": readiness})
		return
	}
	var state []byte
	if p.WizardState != nil {
		state = p.WizardState
	}
	readiness := canonical.DeriveGroomingReadiness(state)
	writeJSON(w, http.StatusOK, map[string]any{"readiness": readiness})
}
