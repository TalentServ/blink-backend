package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/nisha-ts-40599/blink-backend/internal/canonical"
)

func (s *Server) requireCanonicalProject(r *http.Request) (int64, string, error) {
	id, err := projectIDParam(r)
	if err != nil {
		return 0, "", err
	}
	actor := sessionEmail(r)
	if _, err = s.proj.RequireOwned(r.Context(), id, actor); err != nil {
		return 0, "", err
	}
	return id, actor, nil
}

func (s *Server) canonicalGroomingQuestion(w http.ResponseWriter, r *http.Request) {
	id, actor, err := s.requireCanonicalProject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body canonical.GroomingQuestionInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	questionID, err := s.canonical.UpsertGroomingQuestion(r.Context(), id, body, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"questionId": questionID})
}

func (s *Server) canonicalGroomingAnswer(w http.ResponseWriter, r *http.Request) {
	id, actor, err := s.requireCanonicalProject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		QuestionKey string         `json:"questionKey"`
		Answer      string         `json:"answer"`
		Status      string         `json:"status"`
		Evidence    map[string]any `json:"evidence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	answerID, err := s.canonical.RecordGroomingAnswer(r.Context(), id, body.QuestionKey, body.Answer, body.Status, actor, body.Evidence)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"answerId": answerID})
}

func (s *Server) canonicalGroomingDecision(w http.ResponseWriter, r *http.Request) {
	id, actor, err := s.requireCanonicalProject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Key      string         `json:"key"`
		Decision map[string]any `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	decisionID, err := s.canonical.RecordGroomingDecision(r.Context(), id, body.Key, body.Decision, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"decisionId": decisionID})
}

func (s *Server) canonicalGroomingContext(w http.ResponseWriter, r *http.Request) {
	id, actor, err := s.requireCanonicalProject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Key                    string         `json:"key"`
		Context                map[string]any `json:"context"`
		InheritedFromProjectID *int64         `json:"inheritedFromProjectId"`
		InheritedFromRevision  *int64         `json:"inheritedFromRevision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	if err := s.canonical.SetGroomingContext(r.Context(), id, body.Key, body.Context, body.InheritedFromProjectID, body.InheritedFromRevision, actor); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) canonicalGroomingBlocker(w http.ResponseWriter, r *http.Request) {
	id, _, err := s.requireCanonicalProject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Key      string         `json:"key"`
		Severity string         `json:"severity"`
		Message  string         `json:"message"`
		Details  map[string]any `json:"details"`
		Resolve  bool           `json:"resolve"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	if body.Resolve {
		err = s.canonical.ResolveGroomingBlocker(r.Context(), id, body.Key)
	} else {
		err = s.canonical.SetGroomingBlocker(r.Context(), id, body.Key, body.Severity, body.Message, body.Details)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) canonicalGroomingReadinessOwned(w http.ResponseWriter, r *http.Request) {
	id, _, err := s.requireCanonicalProject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	view, err := s.canonical.GroomingReadiness(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"readiness": view})
}

func (s *Server) canonicalGraphAdopt(w http.ResponseWriter, r *http.Request) {
	id, actor, err := s.requireCanonicalProject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var proposal canonical.GraphProposal
	if err := json.NewDecoder(r.Body).Decode(&proposal); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	view, err := s.canonical.AdoptFrameworkGraph(r.Context(), id, proposal, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) canonicalArchitecture(w http.ResponseWriter, r *http.Request) {
	id, actor, err := s.requireCanonicalProject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if r.Method == http.MethodGet {
		view, err := s.canonical.GetArchitecture(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
		return
	}
	var body struct {
		Architecture map[string]any `json:"architecture"`
		Digest       string         `json:"digest"`
		Confirm      bool           `json:"confirm"`
		Invalidate   bool           `json:"invalidate"`
		Reason       string         `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	switch {
	case body.Invalidate:
		err = s.canonical.InvalidateArchitecture(r.Context(), id, body.Reason)
	case body.Confirm:
		err = s.canonical.ConfirmArchitecture(r.Context(), id, strings.TrimSpace(body.Digest), actor)
	default:
		var digest string
		digest, err = s.canonical.SaveArchitecture(r.Context(), id, body.Architecture, actor)
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"digest": digest})
			return
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) canonicalArchitecturePin(w http.ResponseWriter, r *http.Request) {
	id, actor, err := s.requireCanonicalProject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Key   string         `json:"key"`
		Value map[string]any `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	digest, err := s.canonical.PinArchitecture(r.Context(), id, body.Key, body.Value, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"digest": digest})
}

func (s *Server) canonicalExecutionScope(w http.ResponseWriter, r *http.Request) {
	id, actor, err := s.requireCanonicalProject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Kind   string `json:"kind"`
		Ref    string `json:"ref"`
		Digest string `json:"digest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	scope, err := s.canonical.OpenExecutionScope(r.Context(), id, body.Kind, body.Ref, body.Digest, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, scope)
}

func (s *Server) canonicalExecutionLease(w http.ResponseWriter, r *http.Request) {
	projectID, actor, err := s.requireCanonicalProject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		ScopeID      string `json:"scopeId"`
		LeaseID      string `json:"leaseId"`
		FencingToken int64  `json:"fencingToken"`
		TTLSeconds   int    `json:"ttlSeconds"`
		Renew        bool   `json:"renew"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	ttl := time.Duration(body.TTLSeconds) * time.Second
	var lease *canonical.ExecutionLeaseView
	if body.Renew {
		if err := s.canonical.RequireExecutionLease(r.Context(), projectID, body.LeaseID); err != nil {
			writeErr(w, err)
			return
		}
		lease, err = s.canonical.RenewExecutionLease(r.Context(), body.LeaseID, actor, body.FencingToken, ttl)
	} else {
		if err := s.canonical.RequireExecutionScope(r.Context(), projectID, body.ScopeID); err != nil {
			writeErr(w, err)
			return
		}
		lease, err = s.canonical.AcquireExecutionLease(r.Context(), body.ScopeID, actor, ttl)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func (s *Server) canonicalExecutionEvidence(w http.ResponseWriter, r *http.Request) {
	projectID, actor, err := s.requireCanonicalProject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		ScopeID      string         `json:"scopeId"`
		LeaseID      string         `json:"leaseId"`
		FencingToken int64          `json:"fencingToken"`
		Kind         string         `json:"kind"`
		Evidence     map[string]any `json:"evidence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	if err := s.canonical.RequireExecutionScope(r.Context(), projectID, body.ScopeID); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.canonical.RequireExecutionLease(r.Context(), projectID, body.LeaseID); err != nil {
		writeErr(w, err)
		return
	}
	evidenceID, err := s.canonical.RecordExecutionEvidence(r.Context(), body.ScopeID, body.LeaseID, body.FencingToken, body.Kind, body.Evidence, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"evidenceId": evidenceID})
}

func (s *Server) canonicalExecutionRecover(w http.ResponseWriter, r *http.Request) {
	projectID, actor, err := s.requireCanonicalProject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		ScopeID string `json:"scopeId"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	if err := s.canonical.RequireExecutionScope(r.Context(), projectID, body.ScopeID); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.canonical.RecoverExecutionScope(r.Context(), body.ScopeID, actor, body.Reason); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) canonicalProviderReconciliation(w http.ResponseWriter, r *http.Request) {
	id, _, err := s.requireCanonicalProject(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Provider       string         `json:"provider"`
		ResourceKey    string         `json:"resourceKey"`
		DesiredDigest  string         `json:"desiredDigest"`
		ObservedDigest string         `json:"observedDigest"`
		Status         string         `json:"status"`
		Details        map[string]any `json:"details"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, badRequest("invalid JSON body"))
		return
	}
	if err := s.canonical.ReconcileProviderResource(r.Context(), id, body.Provider, body.ResourceKey, body.DesiredDigest, body.ObservedDigest, body.Status, nil, body.Details); err != nil {
		writeErr(w, err)
		return
	}
	// This endpoint records observation only. It deliberately never invokes a provider.
	w.WriteHeader(http.StatusNoContent)
}
