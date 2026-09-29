package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/nisha-ts-40599/blink-backend/internal/agent"
	"github.com/nisha-ts-40599/blink-backend/internal/auth"
	"github.com/nisha-ts-40599/blink-backend/internal/canonical"
	"github.com/nisha-ts-40599/blink-backend/internal/chat"
	"github.com/nisha-ts-40599/blink-backend/internal/config"
	"github.com/nisha-ts-40599/blink-backend/internal/integrations"
	"github.com/nisha-ts-40599/blink-backend/internal/mailer"
	"github.com/nisha-ts-40599/blink-backend/internal/project"
	"github.com/nisha-ts-40599/blink-backend/internal/roles"
	"github.com/nisha-ts-40599/blink-backend/internal/s3ws"
)

type Server struct {
	cfg       config.Config
	auth      *auth.Service
	proj      *project.Service
	canonical *canonical.Service
	agent     *agent.Client
	mail      *mailer.Service
	integ     *integrations.Service
	s3        *s3ws.Service
	chat      *chat.Store
}

func New(cfg config.Config, authSvc *auth.Service, proj *project.Service, canonicalSvc *canonical.Service, agentClient *agent.Client, mail *mailer.Service, integ *integrations.Service, s3 *s3ws.Service, chatStore *chat.Store) http.Handler {
	integ.BindProjectionEnqueue(func(ctx context.Context, projectID int64, provider, actionType string, payload map[string]any, correlationID string) (int64, error) {
		return canonicalSvc.EnqueueProjection(ctx, projectID, provider, actionType, payload, correlationID)
	})
	s := &Server{cfg: cfg, auth: authSvc, proj: proj, canonical: canonicalSvc, agent: agentClient, mail: mail, integ: integ, s3: s3, chat: chatStore}
	s.bindCanonicalCommandExecutor()
	r := chi.NewRouter()
	r.Use(chimw.RequestID, chimw.RealIP, chimw.Logger, chimw.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: cfg.CORSOrigins,
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type", "X-Blink-AI-Model"},
		ExposedHeaders: []string{
			"Content-Disposition",
			"X-Blink-Stakeholder-Source",
			"X-Blink-Workspace-Structure",
			"X-Blink-File-Count",
			"X-Blink-Next-Command",
			"X-Blink-Setup-Status",
			"X-Blink-Setup-Validated",
			"X-Blink-Identity-Source",
			"X-Blink-Overlay-Count",
			"X-Blink-Context-Ready",
			"X-Blink-Delivery-Ready",
			"X-Blink-Folder-Status",
		},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		uiHint := "http://localhost:5173"
		if strings.EqualFold(os.Getenv("RENDER"), "true") {
			uiHint = "https://blink-ui.onrender.com"
		}
		mail := "smtp"
		if s.cfg.LocalMail() {
			mail = "local"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"service": "blink-backend",
			"status":  "UP",
			"hint":    "This is the Blink API only. Open the UI at " + uiHint,
			"health":  "/actuator/health",
			"mail":    mail,
		})
	})
	r.Get("/actuator/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "UP"})
	})

	r.Route("/api", func(api chi.Router) {
		api.Route("/auth", func(ar chi.Router) {
			ar.Get("/config", s.authConfig)
			ar.Post("/otp/request", s.otpRequest)
			ar.Post("/otp/verify", s.otpVerify)
			ar.With(s.requireAuth).Get("/me", s.authMe)
			ar.With(s.requireAuth).Post("/logout", s.authLogout)
		})

		// OAuth browser callbacks stay public (provider redirect).
		api.Get("/integrations/jira/oauth/callback", s.integ.JiraOAuthCallback)
		api.Get("/integrations/github/oauth/callback", s.integ.GitHubOAuthCallback)
		api.Get("/integrations/figma/oauth/callback", s.integ.FigmaOAuthCallback)
		api.Post("/integrations/figma/webhooks", s.integ.FigmaWebhook)

		api.Group(func(pr chi.Router) {
			pr.Use(s.requireAuth)
			pr.Use(s.aiModelPreference)

			pr.Get("/db-status", s.dbStatus)
			pr.Get("/stakeholder-roles", s.stakeholderRoles)
			pr.Post("/stakeholder-questions/send", s.sendStakeholderQuestions)
			pr.Post("/grooming/clarify", s.groomClarify)

			pr.Route("/projects", func(prr chi.Router) {
				prr.Get("/", s.listProjects)
				prr.Get("/mine", s.mineProjects)
				prr.Get("/workspace-tree", s.workspaceTree)
				prr.Get("/workspace-status", s.workspaceStatus)
				prr.Post("/", s.createProject)
				prr.Put("/{id}", s.updateProject)
				prr.Get("/{id}/integrations", s.integ.ListForProject)
				prr.Post("/{id}/integrations/apply", s.integ.ApplyUserToProject)
				prr.Get("/{id}/governance-status", s.governanceStatus)
				prr.Get("/{id}/workspace", s.projectWorkspace)
				prr.Post("/{id}/configure-stakeholders", s.configureStakeholders)
				prr.Post("/{id}/confirm-stakeholders", s.confirmStakeholders)
				prr.Post("/{id}/plan-product-scope", s.planProductScopeID)
				prr.Post("/plan-product-scope", s.planProductScope)
				prr.Post("/{id}/clarify-product-scope", s.clarifyProductScopeID)
				prr.Post("/clarify-product-scope", s.clarifyProductScope)
				prr.Post("/{id}/confirm-product-scope", s.confirmProductScope)
				prr.Post("/{id}/classify-work", s.classifyWork)
				prr.Post("/{id}/propose-designs", s.proposeDesigns)
				prr.Post("/propose-designs", s.proposeDesignsStandalone)
				prr.Post("/{id}/create-spec", s.createSpec)
				prr.Post("/{id}/technical-plan", s.technicalPlan)
				prr.Post("/{id}/grooming-stakeholder-pack", s.groomingStakeholderPack)
				prr.Post("/{id}/grooming-revision", s.groomingRevision)
				prr.Post("/{id}/grooming-sign-off-capture", s.groomingSignOffCapture)
				prr.Post("/{id}/git-apply", s.gitApply)
				prr.Post("/{id}/implement-step", s.implementStepApply)
				prr.Post("/{id}/qa-validation", s.qaValidation)
				prr.Post("/{id}/sdlc-start", s.sdlcStart)
				prr.Post("/{id}/sdlc-next", s.sdlcNext)
				prr.Post("/{id}/jira-gate-evidence", s.jiraGateEvidence)
				prr.Post("/{id}/setup", s.setupProject)
				prr.Post("/{id}/download", s.downloadProject)
				prr.Get("/{id}/chat", s.getProjectChat)
				prr.Post("/{id}/chat/messages", s.postProjectChatMessage)
				prr.Delete("/{id}/chat/messages", s.clearProjectChat)
				prr.Get("/{id}/canonical/snapshot", s.canonicalSnapshot)
				prr.Get("/{id}/canonical/history", s.canonicalHistory)
				prr.Get("/{id}/canonical/blockers", s.canonicalBlockers)
				prr.Get("/{id}/canonical/projections", s.canonicalProjections)
				prr.Post("/{id}/canonical/commands/preview", s.canonicalCommandPreview)
				prr.Post("/{id}/canonical/commands/execute", s.canonicalCommandExecute)
				prr.Post("/{id}/canonical/gates/confirm", s.canonicalGateConfirm)
				prr.Get("/{id}/canonical/grooming-readiness", s.canonicalGroomingReadiness)
				prr.Post("/{id}/canonical/grooming/questions", s.canonicalGroomingQuestion)
				prr.Post("/{id}/canonical/grooming/answers", s.canonicalGroomingAnswer)
				prr.Post("/{id}/canonical/grooming/decisions", s.canonicalGroomingDecision)
				prr.Post("/{id}/canonical/grooming/contexts", s.canonicalGroomingContext)
				prr.Post("/{id}/canonical/grooming/blockers", s.canonicalGroomingBlocker)
				prr.Get("/{id}/canonical/graph", s.canonicalGraph)
				prr.Post("/{id}/canonical/graph/adopt", s.canonicalGraphAdopt)
				prr.Get("/{id}/canonical/requirements/history", s.canonicalRequirementHistory)
				prr.Get("/{id}/canonical/architecture", s.canonicalArchitecture)
				prr.Post("/{id}/canonical/architecture", s.canonicalArchitecture)
				prr.Post("/{id}/canonical/architecture/pins", s.canonicalArchitecturePin)
				prr.Post("/{id}/canonical/execution/scopes", s.canonicalExecutionScope)
				prr.Post("/{id}/canonical/execution/leases", s.canonicalExecutionLease)
				prr.Post("/{id}/canonical/execution/evidence", s.canonicalExecutionEvidence)
				prr.Post("/{id}/canonical/execution/recover", s.canonicalExecutionRecover)
				prr.Post("/{id}/canonical/provider-reconciliation", s.canonicalProviderReconciliation)
				prr.Post("/{id}/canonical/domain/confirm", s.canonicalDomainGate)
				prr.Get("/{id}/canonical/ship/session", s.canonicalShipSession)
				prr.Post("/{id}/canonical/ship/checkpoint", s.canonicalShipCheckpoint)
				prr.Get("/{id}", s.getProject)
			})

			pr.Route("/integrations", func(ir chi.Router) {
				ir.Get("/", s.integ.ListMine)
				ir.Post("/connect", s.integ.Connect)
				ir.Post("/repositories", s.integ.CreateRepositories)
				ir.Get("/jira/oauth/url", s.integ.JiraOAuthURL)
				ir.Post("/jira/oauth/exchange", s.integ.JiraOAuthExchange)
				ir.Get("/github/oauth/url", s.integ.GitHubOAuthURL)
				ir.Post("/github/oauth/exchange", s.integ.GitHubOAuthExchange)
				ir.Get("/figma/oauth/url", s.integ.FigmaOAuthURL)
				ir.Post("/figma/oauth/exchange", s.integ.FigmaOAuthExchange)
				ir.Post("/jira/projects", s.integ.JiraProjects)
				ir.Post("/github/orgs", s.integ.GitHubOrgs)
				ir.Post("/figma/teams", s.integ.FigmaTeams)
				ir.Post("/figma/projects", s.integ.FigmaProjects)
				ir.Post("/figma/files", s.integ.FigmaFiles)
				ir.Post("/figma/frames", s.integ.FigmaFrames)
				ir.Get("/figma/design", s.integ.GetFigmaDesign)
				ir.Post("/figma/design", s.integ.SaveFigmaDesign)
				ir.Delete("/figma/design", s.integ.ClearFigmaDesign)
				ir.Post("/figma/ingest", s.integ.IngestFigmaDesign)
				ir.Post("/figma/sync", s.integ.IngestFigmaDesign)
				ir.Post("/stitch/designs", s.integ.ProposeStitchDesigns)
				ir.Post("/stitch/designs/choose", s.integ.ChooseStitchDesign)
				ir.Post("/jira/issues", s.integ.CreateJiraIssues)
				ir.Post("/jira/issues/delete", s.integ.DeleteJiraIssues)
				ir.Post("/jira/issues/statuses", s.integ.JiraIssueStatuses)
				ir.Post("/jira/issues/transition", s.integ.TransitionJiraIssue)
				ir.Post("/jira/comments", s.integ.CreateJiraComment)
				ir.Post("/jira/comments/poll", s.integ.PollJiraComments)
				ir.Post("/jira/comments/reset-simulated", s.integ.ResetSimulatedJiraReplies)
				ir.Post("/jira/discussions/summarize", s.summarizeDiscussion)
				ir.Post("/binding", s.integ.Binding)
			})

			pr.Route("/dev/workspaces", func(dr chi.Router) {
				dr.Get("/", s.s3.List)
				dr.Delete("/", s.s3.DeleteAll)
				dr.Delete("/{folder}", s.s3.DeleteOne)
			})
			pr.Route("/dev/jira/issues", func(dr chi.Router) {
				dr.Get("/", s.integ.ListBlinkIssues)
				dr.Delete("/", s.integ.DeleteAllBlinkIssues)
				dr.Delete("/{issueKey}", s.integ.DeleteBlinkIssue)
			})
		})
	})

	return r
}

func (s *Server) aiModelPreference(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawModel := r.Header.Get("X-Blink-AI-Model")
		if strings.TrimSpace(rawModel) == "" {
			next.ServeHTTP(w, r)
			return
		}
		model, err := chat.NormalizeModel(rawModel)
		if err != nil {
			writeErr(w, badRequest(err.Error()))
			return
		}
		next.ServeHTTP(w, r.WithContext(agent.WithRequestedModel(r.Context(), model)))
	})
}

type ctxKey string

const sessionKey ctxKey = "session"

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.auth.RequireSession(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			writeErr(w, err)
			return
		}
		ctx := context.WithValue(r.Context(), sessionKey, sess)
		r = r.WithContext(ctx)
		r.Header.Set("X-Blink-Owner-Email", sess.Email)
		next.ServeHTTP(w, r)
	})
}

func sessionEmail(r *http.Request) string {
	if v, ok := r.Context().Value(sessionKey).(*auth.Session); ok && v != nil {
		return v.Email
	}
	return ""
}

func (s *Server) authConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.auth.Config())
}

func (s *Server) otpRequest(w http.ResponseWriter, r *http.Request) {
	var req auth.OTPRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	resp, err := s.auth.RequestOTP(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) otpVerify(w http.ResponseWriter, r *http.Request) {
	var req auth.OTPVerify
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	resp, err := s.auth.VerifyOTP(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	resp, err := s.auth.Me(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.Logout(r.Context(), r.Header.Get("Authorization"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) dbStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "engine": "postgres"})
}

func (s *Server) stakeholderRoles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, roles.Catalog())
}

func (s *Server) sendStakeholderQuestions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Questions []mailer.QuestionItem `json:"questions"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if len(body.Questions) == 0 {
		writeErr(w, fmt.Errorf("questions are required"))
		return
	}
	writeJSON(w, http.StatusOK, s.mail.SendQuestions(r.Context(), body.Questions))
}

func (s *Server) groomClarify(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if wantsSSE(r) {
		_, writeSSE, ok := startSSE(w)
		if !ok {
			writeErr(w, fmt.Errorf("streaming is not supported on this connection"))
			return
		}
		raw, err := s.agent.ClarifyStream(r.Context(), body, func(delta string) error {
			if !writeSSE("thinking", map[string]any{"text": delta}) {
				return fmt.Errorf("client disconnected")
			}
			return nil
		})
		if err != nil {
			_ = writeSSE("error", map[string]any{"message": err.Error()})
			return
		}
		_ = writeSSE("done", json.RawMessage(raw))
		return
	}
	raw, err := s.agent.Clarify(r.Context(), body)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	// Tenant isolation: list only the caller's projects (was global in Java).
	list, err := s.proj.ListMine(r.Context(), sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) mineProjects(w http.ResponseWriter, r *http.Request) {
	p, err := s.proj.LatestForOwner(r.Context(), sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req project.ProjectRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.proj.Create(r.Context(), req, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	id := p.ID
	if email := sessionEmail(r); email != "" {
		_, _ = s.integ.ApplyUserConnections(r.Context(), email, id)
	}
	go s.s3.ProvisionAsync(context.Background(), p.ProjectName, &id)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req project.ProjectRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.proj.Update(r.Context(), id, req, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.canonical != nil {
		stakeDigest := canonicalDigestStakeholders(req.Stakeholders)
		if stale, _ := s.canonical.StakeholderRegistryStale(r.Context(), id, stakeDigest); stale {
			_ = s.canonical.RevokeStakeholderConfirmation(r.Context(), id)
		}
		_ = s.canonical.RefreshEligibility(r.Context(), id, sessionEmail(r))
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) governanceStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "idle", "sodWarnings": []string{}, "message": "ok"})
}

func (s *Server) projectWorkspace(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, s.s3.Progress(p.ProjectName, &id))
}

func (s *Server) workspaceStatus(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("projectName")
	var idPtr *int64
	if raw := strings.TrimSpace(r.URL.Query().Get("projectId")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			idPtr = &n
		}
	}
	if strings.TrimSpace(name) != "" {
		s.s3.EnsureProvisioned(r.Context(), name, idPtr)
	}
	writeJSON(w, http.StatusOK, s.s3.Progress(name, idPtr))
}

func (s *Server) workspaceTree(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"nodes": []any{}})
}

func (s *Server) configureStakeholders(w http.ResponseWriter, r *http.Request) {
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
	stakes := make([]map[string]string, 0, len(p.Stakeholders))
	for _, st := range p.Stakeholders {
		stakes = append(stakes, map[string]string{"role_id": st.RoleCode, "name": st.Name, "email": st.Email})
	}
	raw, err := s.agent.ConfigureStakeholders(r.Context(), p.ProjectName, strconv.FormatInt(id, 10), stakes)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (s *Server) confirmStakeholders(w http.ResponseWriter, r *http.Request) {
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
	stakes := make([]map[string]string, 0, len(p.Stakeholders))
	for _, st := range p.Stakeholders {
		stakes = append(stakes, map[string]string{"role_id": st.RoleCode, "name": st.Name, "email": st.Email})
	}
	var body map[string]any
	_ = readJSON(r, &body)
	payload := s.advisoryPayload(r, p.ProjectName, id, body)
	payload["stakeholders"] = stakes
	raw, err := s.executeCanonicalCompatibility(r, id, "confirm-stakeholders", payload)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (s *Server) planProductScopeID(w http.ResponseWriter, r *http.Request) {
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
	var body map[string]any
	_ = readJSON(r, &body)
	payload := s.advisoryPayload(r, p.ProjectName, id, body)
	if wantsSSE(r) {
		_, writeSSE, ok := startSSE(w)
		if !ok {
			writeErr(w, fmt.Errorf("streaming is not supported on this connection"))
			return
		}
		raw, err := s.agent.PlanProductScopeStreamWithPayload(r.Context(), payload, func(delta string) error {
			if !writeSSE("thinking", map[string]any{"text": delta}) {
				return fmt.Errorf("client disconnected")
			}
			return nil
		})
		if err != nil {
			_ = writeSSE("error", map[string]any{"message": err.Error()})
			return
		}
		s.persistAgentOverlays(r, p.ProjectName, id, raw)
		_ = writeSSE("done", json.RawMessage(raw))
		return
	}
	raw, err := s.agent.PlanProductScopeWithPayload(r.Context(), payload)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.persistAgentOverlays(r, p.ProjectName, id, raw)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (s *Server) planProductScope(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if _, ok := body["actor"]; !ok || strings.TrimSpace(fmt.Sprint(body["actor"])) == "" {
		body["actor"] = sessionEmail(r)
	}
	name, _ := body["projectName"].(string)
	if name != "" {
		body["projectName"] = name
	}
	if wantsSSE(r) {
		_, writeSSE, ok := startSSE(w)
		if !ok {
			writeErr(w, fmt.Errorf("streaming is not supported on this connection"))
			return
		}
		raw, err := s.agent.PlanProductScopeStreamWithPayload(r.Context(), body, func(delta string) error {
			if !writeSSE("thinking", map[string]any{"text": delta}) {
				return fmt.Errorf("client disconnected")
			}
			return nil
		})
		if err != nil {
			_ = writeSSE("error", map[string]any{"message": err.Error()})
			return
		}
		_ = writeSSE("done", json.RawMessage(raw))
		return
	}
	raw, err := s.agent.PlanProductScopeWithPayload(r.Context(), body)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (s *Server) clarifyProductScopeID(w http.ResponseWriter, r *http.Request) {
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
	var body map[string]any
	_ = readJSON(r, &body)
	payload := s.advisoryPayload(r, p.ProjectName, id, body)
	raw, err := s.agent.ClarifyProductScope(r.Context(), payload)
	s.writeAgentResult(w, r, p.ProjectName, id, "clarify-product-scope", raw, err)
}

func (s *Server) clarifyProductScope(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if _, ok := body["actor"]; !ok || strings.TrimSpace(fmt.Sprint(body["actor"])) == "" {
		body["actor"] = sessionEmail(r)
	}
	raw, err := s.agent.ClarifyProductScope(r.Context(), body)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// setupProject and downloadProject live in setup_download.go.

func pathID(r *http.Request) (int64, error) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid project id")
	}
	return id, nil
}

func readJSON(r *http.Request, dest any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	msg := err.Error()
	switch {
	case errors.Is(err, auth.ErrUnauthorized):
		status = http.StatusUnauthorized
		msg = "Sign in to continue."
	case errors.Is(err, auth.ErrForbidden):
		status = http.StatusForbidden
	case strings.Contains(strings.ToLower(msg), "agent runtime"):
		status = http.StatusBadGateway
	case strings.Contains(strings.ToLower(msg), "not found"):
		status = http.StatusNotFound
	case strings.Contains(strings.ToLower(msg), "forbidden"):
		status = http.StatusForbidden
	}
	writeJSON(w, status, map[string]string{"message": msg})
}
