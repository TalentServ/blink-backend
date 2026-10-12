package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nisha-ts-40599/blink-backend/internal/project"
	"github.com/nisha-ts-40599/blink-backend/internal/setupproj"
	"github.com/nisha-ts-40599/blink-backend/internal/zipkit"
)

type setupAgentResponse struct {
	RunID             *string           `json:"runId"`
	Command           *string           `json:"command"`
	Status            *string           `json:"status"`
	ContextReady      *bool             `json:"contextReady"`
	DeliveryReady     *bool             `json:"deliveryReady"`
	GitWritten        *bool             `json:"gitWritten"`
	IdentitySource    *string           `json:"identitySource"`
	OverlayFiles      []overlayFileJSON `json:"overlayFiles"`
	AcceptedFileCount int               `json:"acceptedFileCount"`
	NextCommand       *string           `json:"nextCommand"`
	Message           *string           `json:"message"`
	Errors            []string          `json:"errors"`
}

type overlayFileJSON struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type setupAgentRequest struct {
	RequirementText string `json:"requirementText"`
	Mode            string `json:"mode"`
}

func (s *Server) setupProject(w http.ResponseWriter, r *http.Request) {
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
	var req setupAgentRequest
	_ = readJSON(r, &req)
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "apply"
	}
	if mode != "apply" {
		writeErr(w, fmt.Errorf("Only setup-new-workspace apply is hosted."))
		return
	}

	kitPath, err := s.ensureWorkspaceKit(r.Context())
	if err != nil {
		writeErr(w, fmt.Errorf("workspace kit is not ready: %w", err))
		return
	}
	resp, overlay, err := s.runSetup(r.Context(), p, strings.TrimSpace(req.RequirementText), nil, kitPath)
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.s3.Enabled() {
		s.s3.ProvisionAsync(context.Background(), p.ProjectName, &id)
		s.s3.PutCursorOverlayAsync(p.ProjectName, &id, overlay)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) downloadProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	started := time.Now()
	stage := func(name string) {
		log.Printf("download project=%d stage=%s elapsed=%s", id, name, time.Since(started).Round(time.Millisecond))
	}
	fail := func(name string, err error) {
		log.Printf("download project=%d stage=%s FAILED elapsed=%s err=%v", id, name, time.Since(started).Round(time.Millisecond), err)
		writeErr(w, err)
	}
	log.Printf("download start project=%d", id)
	ownCtx, ownCancel := context.WithTimeout(r.Context(), 20*time.Second)
	p, err := s.proj.RequireOwned(ownCtx, id, sessionEmail(r))
	ownCancel()
	if err != nil {
		fail("owned", busyOr(err))
		return
	}
	stage("owned")

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		// Allow non-multipart with form values if Content-Type is form-urlencoded.
		if err2 := r.ParseForm(); err2 != nil {
			fail("form", fmt.Errorf("invalid multipart form: %w", err))
			return
		}
	}
	stage("form")

	requirementsText := formValue(r, "requirementsText")
	setupContext := formValue(r, "setupContext")
	var fileName string
	var fileBytes []byte
	if f, hdr, err := r.FormFile("file"); err == nil {
		defer f.Close()
		fileName = hdr.Filename
		fileBytes, _ = io.ReadAll(io.LimitReader(f, 25<<20))
	}

	markdown, err := zipkit.ToMarkdown(p.ProjectName, fileName, fileBytes, requirementsText)
	if err != nil {
		fail("markdown", err)
		return
	}

	var blinkCtx map[string]any
	if strings.TrimSpace(setupContext) != "" {
		if err := json.Unmarshal([]byte(setupContext), &blinkCtx); err != nil || blinkCtx == nil {
			fail("setup-context", fmt.Errorf("Setup context is invalid."))
			return
		}
	}

	kitPath, err := s.ensureWorkspaceKit(r.Context())
	if err != nil {
		fail("kit", fmt.Errorf("workspace kit is not ready: %w", err))
		return
	}
	stage("kit")

	setup, setupOverlay, err := s.runSetup(r.Context(), p, markdown, blinkCtx, kitPath)
	if err != nil {
		fail("setup", fmt.Errorf("Canonical workspace setup was not validated: %w", err))
		return
	}
	stage(fmt.Sprintf("setup overlay=%d", len(setupOverlay)))

	s3Overlay := map[string]string{}
	if s.s3.Enabled() {
		if listed, lerr := s.s3.ListCursorOverlays(r.Context(), p.ProjectName, &id); lerr == nil {
			s3Overlay = listed
		}
		s.s3.ProvisionAsync(context.Background(), p.ProjectName, &id)
		s.s3.PutRequirementAsync(p.ProjectName, &id, markdown)
		s.s3.PutFrameworkCommandsAsync(p.ProjectName, &id, kitPath)
		stage(fmt.Sprintf("s3-kick overlay=%d", len(s3Overlay)))
	}
	overlay := zipkit.MergeOverlays(s3Overlay, setupOverlay)
	if s.s3.Enabled() {
		s.s3.PutCursorOverlayAsync(p.ProjectName, &id, overlay)
	}

	repos := toRepoFolders(r.Form["repoName"], r.Form["repoPurpose"], r.Form["repoDescription"])
	mcpProviders := r.Form["mcpProvider"]
	hints := zipkit.SiteHints{
		JiraURL:            formValue(r, "mcpJiraUrl"),
		JiraUsername:       formValue(r, "mcpJiraEmail"),
		JiraCloudID:        formValue(r, "mcpJiraCloudId"),
		ConfluenceURL:      formValue(r, "mcpConfluenceUrl"),
		ConfluenceUsername: formValue(r, "mcpConfluenceEmail"),
	}
	mcpProviders, hints = s.mergeDownloadMCP(r.Context(), id, sessionEmail(r), mcpProviders, hints)
	log.Printf("download project=%d mcp=%v atlassianSite=%s", id, mcpProviders, hints.JiraURL)

	root := project.Folder(p.ProjectName, &id)
	bundle, err := zipkit.PackageWorkspace(root, markdown, repos, overlay, mcpProviders, hints, kitPath)
	if err != nil {
		fail("package", fmt.Errorf("failed to package workspace: %w", err))
		return
	}
	stage(fmt.Sprintf("package files=%d bytes=%d", bundle.FileCount, len(bundle.ZipBytes)))

	nextCommand := zipkit.NextSDLCCommand
	if setup.NextCommand != nil && strings.TrimSpace(*setup.NextCommand) != "" {
		nextCommand = strings.TrimSpace(*setup.NextCommand)
	}
	if nextCommand != "" && !strings.HasPrefix(nextCommand, "/") {
		nextCommand = "/" + nextCommand
	}

	status := ""
	if setup.Status != nil {
		status = *setup.Status
	}
	identity := ""
	if setup.IdentitySource != nil {
		identity = *setup.IdentitySource
	}
	ctxReady := false
	if setup.ContextReady != nil {
		ctxReady = *setup.ContextReady
	}
	delivReady := false
	if setup.DeliveryReady != nil {
		delivReady = *setup.DeliveryReady
	}
	folderStatus := ""
	if s.s3.Enabled() {
		folderStatus = s.s3.FolderStatus(p.ProjectName, &id)
	}

	validated := status == "ok" || status == "overlay_ready"

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, bundle.Filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(bundle.ZipBytes)))
	w.Header().Set("X-Blink-Workspace-Structure", zipkit.EncodeStructure(bundle.Structure))
	w.Header().Set("X-Blink-File-Count", strconv.Itoa(bundle.FileCount))
	w.Header().Set("X-Blink-Next-Command", nextCommand)
	w.Header().Set("X-Blink-Setup-Status", status)
	w.Header().Set("X-Blink-Identity-Source", identity)
	w.Header().Set("X-Blink-Overlay-Count", strconv.Itoa(zipkit.OverlayCount(overlay)))
	w.Header().Set("X-Blink-Setup-Validated", strconv.FormatBool(validated))
	w.Header().Set("X-Blink-Context-Ready", strconv.FormatBool(ctxReady))
	w.Header().Set("X-Blink-Delivery-Ready", strconv.FormatBool(delivReady))
	w.Header().Set("X-Blink-Folder-Status", folderStatus)
	w.WriteHeader(http.StatusOK)
	n, werr := w.Write(bundle.ZipBytes)
	if werr != nil {
		log.Printf("download project=%d stage=write FAILED wrote=%d elapsed=%s err=%v", id, n, time.Since(started).Round(time.Millisecond), werr)
		return
	}
	stage(fmt.Sprintf("written bytes=%d folder=%s validated=%v", n, folderStatus, validated))
}

func (s *Server) mergeDownloadMCP(ctx context.Context, projectID int64, email string, providers []string, hints zipkit.SiteHints) ([]string, zipkit.SiteHints) {
	if s.integ == nil {
		return providers, hints
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(providers)+5)
	for _, raw := range providers {
		id := strings.ToLower(strings.TrimSpace(raw))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, provider := range []string{"jira", "confluence", "github", "figma", "bitbucket"} {
		if !s.integ.IsConnected(ctx, projectID, email, provider) {
			continue
		}
		if !seen[provider] {
			seen[provider] = true
			out = append(out, provider)
		}
		snap := s.integ.ConnectionSnapshot(ctx, projectID, email, provider)
		if snap == nil {
			continue
		}
		baseURL := snapString(snap, "baseUrl")
		cloudID := snapString(snap, "cloudId")
		switch provider {
		case "jira":
			if hints.JiraURL == "" {
				hints.JiraURL = baseURL
			}
			if hints.JiraCloudID == "" {
				hints.JiraCloudID = cloudID
			}
		case "confluence":
			if hints.ConfluenceURL == "" {
				hints.ConfluenceURL = baseURL
			}
		}
	}
	return out, hints
}

func (s *Server) ensureWorkspaceKit(ctx context.Context) (string, error) {
	kit, err := zipkit.EnsureFromLambda(
		ctx,
		s.cfg.AutomationSDLCPath,
		s.cfg.AgentLambdaName,
		s.cfg.AWSRegion,
		s.cfg.AWSAccessKeyID,
		s.cfg.AWSSecretAccessKey,
	)
	if err != nil {
		return "", err
	}
	return kit, nil
}

func (s *Server) runSetup(ctx context.Context, p *project.ProjectResponse, requirementText string, blinkContext map[string]any, kit string) (setupAgentResponse, map[string]string, error) {
	payload := setupproj.HostedPayload(
		p.ProjectName,
		"",
		strconv.FormatInt(p.ID, 10),
		p.Description,
		requirementText,
		blinkContext,
	)

	raw, agentErr := s.agent.SetupNewWorkspace(ctx, payload)
	if agentErr == nil {
		var loose map[string]any
		if err := json.Unmarshal(raw, &loose); err == nil {
			resp := parseSetupResponse(loose)
			overlay := overlayMap(resp.OverlayFiles)
			status := ""
			if resp.Status != nil {
				status = *resp.Status
			}
			if (status == "ok" || status == "overlay_ready") && setupproj.HasRequired(overlay) {
				return resp, overlay, nil
			}
			log.Printf("canonical setup agent status=%q overlay=%d, using local projector", status, len(overlay))
		} else {
			log.Printf("canonical setup agent JSON invalid (%v), using local projector", err)
		}
	} else {
		log.Printf("canonical setup agent unavailable (%v), using local projector", agentErr)
	}

	proj, err := setupproj.Apply(ctx, kit, s.cfg.CanonicalPython, s.cfg.CanonicalTimeout, payload)
	if err != nil {
		return setupUnavailable(err), map[string]string{}, err
	}
	return projectorResponse(proj), proj.Overlay, nil
}

func projectorResponse(proj setupproj.Result) setupAgentResponse {
	status := proj.Status
	cmd := "setup-new-workspace"
	next := proj.NextCommand
	if next == "" {
		next = zipkit.NextSDLCCommand
	}
	src := proj.IdentitySource
	ctxReady := proj.ContextReady
	delivReady := proj.DeliveryReady
	msg := proj.Message
	files := make([]overlayFileJSON, 0, len(proj.Overlay))
	for path, content := range proj.Overlay {
		files = append(files, overlayFileJSON{Path: path, Content: content})
	}
	return setupAgentResponse{
		Command:           &cmd,
		Status:            &status,
		ContextReady:      &ctxReady,
		DeliveryReady:     &delivReady,
		IdentitySource:    &src,
		OverlayFiles:      files,
		AcceptedFileCount: len(files),
		NextCommand:       &next,
		Message:           &msg,
		Errors:            proj.Errors,
	}
}

func parseSetupResponse(root map[string]any) setupAgentResponse {
	resp := setupAgentResponse{Errors: []string{}}
	resp.RunID = strPtr(root["runId"])
	resp.Command = strPtr(root["command"])
	resp.Status = strPtr(root["status"])
	resp.IdentitySource = strPtr(root["identitySource"])
	resp.NextCommand = strPtr(root["nextCommand"])
	resp.Message = strPtr(root["message"])
	resp.ContextReady = boolPtr(root["contextReady"])
	resp.DeliveryReady = boolPtr(root["deliveryReady"])
	resp.GitWritten = boolPtr(root["gitWritten"])
	if arr, ok := root["overlayFiles"].([]any); ok {
		for _, item := range arr {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			path, _ := m["path"].(string)
			path = zipkit.SanitizeOverlayPath(path)
			if path == "" {
				continue
			}
			content, _ := m["content"].(string)
			resp.OverlayFiles = append(resp.OverlayFiles, overlayFileJSON{Path: path, Content: content})
		}
	}
	if n, ok := root["acceptedFileCount"].(float64); ok {
		resp.AcceptedFileCount = int(n)
	} else {
		resp.AcceptedFileCount = len(resp.OverlayFiles)
	}
	if arr, ok := root["errors"].([]any); ok {
		for _, e := range arr {
			if s, ok := e.(string); ok {
				resp.Errors = append(resp.Errors, s)
			}
		}
	}
	return resp
}

func setupUnavailable(err error) setupAgentResponse {
	status := "unavailable"
	cmd := "setup-new-workspace"
	next := zipkit.NextSDLCCommand
	src := "hosted-agent"
	ctxReady := false
	delivReady := false
	msg := "Canonical setup agent unavailable."
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		msg = strings.TrimSpace(err.Error())
	}
	return setupAgentResponse{
		Command:           &cmd,
		Status:            &status,
		ContextReady:      &ctxReady,
		DeliveryReady:     &delivReady,
		IdentitySource:    &src,
		OverlayFiles:      nil,
		AcceptedFileCount: 0,
		NextCommand:       &next,
		Message:           &msg,
		Errors:            []string{msg},
	}
}

func overlayMap(files []overlayFileJSON) map[string]string {
	out := make(map[string]string, len(files))
	for _, f := range files {
		path := zipkit.SanitizeOverlayPath(f.Path)
		if path == "" {
			continue
		}
		out[path] = f.Content
	}
	return out
}

func toRepoFolders(names, purposes, descriptions []string) []zipkit.RepoFolder {
	out := make([]zipkit.RepoFolder, 0, len(names))
	for i, name := range names {
		if strings.TrimSpace(name) == "" {
			continue
		}
		purpose, desc := "", ""
		if i < len(purposes) {
			purpose = purposes[i]
		}
		if i < len(descriptions) {
			desc = descriptions[i]
		}
		out = append(out, zipkit.RepoFolder{Name: name, Purpose: purpose, Description: desc})
	}
	return out
}

func formValue(r *http.Request, key string) string {
	if r.MultipartForm != nil && r.MultipartForm.Value != nil {
		if vals := r.MultipartForm.Value[key]; len(vals) > 0 {
			return vals[0]
		}
	}
	return r.FormValue(key)
}

func snapString(snap map[string]any, key string) string {
	v, ok := snap[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func strPtr(v any) *string {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return nil
	}
	s = strings.TrimSpace(s)
	return &s
}

func boolPtr(v any) *bool {
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}
