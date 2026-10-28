package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nisha-ts-40599/blink-backend/internal/storyrun"
)

type storedStoryRun struct {
	RunID        string   `json:"runId"`
	ProjectID    int64    `json:"projectId"`
	IssueID      string   `json:"issueId,omitempty"`
	Title        string   `json:"title,omitempty"`
	Status       string   `json:"status"`
	Tier         int      `json:"tier"`
	Reasons      []string `json:"reasons"`
	Plan         []string `json:"plan"`
	Message      string   `json:"message"`
	PreviewHTML  string   `json:"previewHtml"`
	PullRequests []string `json:"pullRequests"`
	CreatedAt    string   `json:"createdAt"`
}

type storyRunMemory struct {
	mu    sync.Mutex
	byKey map[string]storedStoryRun
}

func (s *Server) storyRuns() *storyRunMemory {
	if s.storyRunStore == nil {
		s.storyRunStore = &storyRunMemory{byKey: map[string]storedStoryRun{}}
	}
	return s.storyRunStore
}

func (s *Server) createStoryRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := s.proj.RequireOwned(r.Context(), id, sessionEmail(r)); err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Requirement  string   `json:"requirement"`
		Title        string   `json:"title"`
		IssueID      string   `json:"issueId"`
		PullRequests []string `json:"pullRequests"`
	}
	_ = readJSON(r, &body)
	decision := storyrun.Classify(body.Requirement)
	status := decision.Status
	message := "Add a requirement before running the story."
	prs := cleanURLs(body.PullRequests)
	switch decision.Status {
	case "needs_person":
		message = "A person needs to approve this story before implementation."
	case "preview_ready":
		message = "Tier 1 preview is ready. Nothing is merged."
		if len(prs) > 0 {
			status = "pull_request_ready"
			message = "Draft pull requests are ready to preview before merge."
		}
	}
	run := storedStoryRun{
		RunID:        "run-" + uuid.NewString(),
		ProjectID:    id,
		IssueID:      strings.TrimSpace(body.IssueID),
		Title:        strings.TrimSpace(body.Title),
		Status:       status,
		Tier:         decision.Tier,
		Reasons:      decision.Reasons,
		Plan:         storyrun.Plan(),
		Message:      message,
		PullRequests: prs,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if decision.Status != "blocked" {
		run.PreviewHTML = storyrun.PreviewHTML(body.Title, body.Requirement, prs)
	}
	store := s.storyRuns()
	store.mu.Lock()
	store.byKey[storyRunKey(id)] = run
	store.mu.Unlock()
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) latestStoryRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := s.proj.RequireOwned(r.Context(), id, sessionEmail(r)); err != nil {
		writeErr(w, err)
		return
	}
	store := s.storyRuns()
	store.mu.Lock()
	run, ok := store.byKey[storyRunKey(id)]
	store.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"status": "empty"})
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func storyRunKey(projectID int64) string {
	return "project:" + strconv.FormatInt(projectID, 10)
}

func cleanURLs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if strings.HasPrefix(item, "https://") || strings.HasPrefix(item, "http://") {
			out = append(out, item)
		}
	}
	return out
}
