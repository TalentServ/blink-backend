package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/nisha-ts-40599/blink-backend/internal/canonical"
)

func (s *Server) canonicalSnapshot(w http.ResponseWriter, r *http.Request) {
	id, err := projectIDParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	snap, err := s.canonical.Snapshot(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) canonicalHistory(w http.ResponseWriter, r *http.Request) {
	id, err := projectIDParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil {
			limit = n
		}
	}
	events, err := s.canonical.History(r.Context(), id, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) canonicalProjections(w http.ResponseWriter, r *http.Request) {
	id, err := projectIDParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	rows, err := s.canonical.Projections(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projections": rows})
}

func (s *Server) canonicalBlockers(w http.ResponseWriter, r *http.Request) {
	id, err := projectIDParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	snap, err := s.canonical.Snapshot(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"revision":  snap.Revision,
		"blockers":  snap.Blockers,
		"eligibility": snap.Eligibility,
	})
}

func (s *Server) canonicalCommandPreview(w http.ResponseWriter, r *http.Request) {
	s.canonicalCommand(w, r, true)
}

func (s *Server) canonicalCommandExecute(w http.ResponseWriter, r *http.Request) {
	s.canonicalCommand(w, r, false)
}

func (s *Server) canonicalCommand(w http.ResponseWriter, r *http.Request, preview bool) {
	id, err := projectIDParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req canonical.CommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	correlationID := chimw.GetReqID(r.Context())
	var result *canonical.CommandResult
	if preview {
		result, err = s.canonical.PreviewCommand(r.Context(), id, req, sessionEmail(r), correlationID)
	} else {
		result, err = s.canonical.ExecuteCommand(r.Context(), id, req, sessionEmail(r), correlationID)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func projectIDParam(r *http.Request) (int64, error) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("invalid project id")
	}
	return id, nil
}
