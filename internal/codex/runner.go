package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// EventEmitter receives normalized SSE payloads for the Blink UI.
type EventEmitter func(event string, payload map[string]any) bool

// ExecutionRunner is the provider-neutral execution contract used by the
// Blink control plane. Concrete providers may use OpenAI hosted sessions,
// a local runner, or another managed execution service.
type ExecutionRunner interface {
	Configured() bool
	Run(context.Context, RunRequest, EventEmitter) (*RunResult, error)
	Steer(context.Context, int64, string, string, []string) error
	CancelActive(context.Context, int64, string) error
	CancelAll(context.Context, int64) error
	LiveBlinkSessions() map[uuid.UUID]struct{}
}

// RunRequest drives one Codex sandbox turn.
type RunRequest struct {
	ProjectID      int64
	IssueID        string
	WorkflowKind   WorkflowKind
	PromptStem     string
	PromptVars     PromptVars
	UserInput      string
	IdempotencyKey string
	KitRoot        string
	Workspace      *WorkspacePlan
	PriorHistory   []map[string]any
	PriorFiles     map[string]string
	Model          string
	ContainerSize  string
	NetworkAccess  string
	AllowedDomains []string
	TurnTimeout    time.Duration
	CodexSessionID uuid.UUID // optional existing Neon session
	ImageDataURLs  []string
}

// RunResult is the durable outcome of a turn after OpenAI session deletion.
type RunResult struct {
	CodexSessionID  uuid.UUID
	TurnID          uuid.UUID
	TurnNumber      int
	OpenAISessionID string
	OpenAITurnID    string
	Status          SessionStatus
	OutputText      string
	ResultJSON      map[string]any
	Files           map[string]string // path -> content (from blink-result + downloaded outputs)
	Artifacts       []map[string]any
	Commands        []map[string]any
	Usage           map[string]any
	ErrorCode       string
	ErrorMessage    string
	History         []map[string]any
}

// Runner hosts OpenAI Agents API sessions.
type Runner struct {
	client openai.Client
	store  *Store
	cfg    RunnerConfig

	mu             sync.Mutex
	activeSessions map[string]uuid.UUID // openai session id -> neon session id
}

type RunnerConfig struct {
	APIKey         string
	Model          string
	ContainerSize  string
	NetworkAccess  string
	AllowedDomains []string
	TurnTimeout    time.Duration
}

func NewRunner(store *Store, cfg RunnerConfig) *Runner {
	opts := []option.RequestOption{}
	if strings.TrimSpace(cfg.APIKey) != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-6-luna"
	}
	if cfg.ContainerSize == "" {
		cfg.ContainerSize = "small"
	}
	if cfg.NetworkAccess == "" {
		cfg.NetworkAccess = "disabled"
	}
	if cfg.TurnTimeout <= 0 {
		cfg.TurnTimeout = 10 * time.Minute
	}
	return &Runner{
		client:         openai.NewClient(opts...),
		store:          store,
		cfg:            cfg,
		activeSessions: map[string]uuid.UUID{},
	}
}

func (r *Runner) Configured() bool {
	return strings.TrimSpace(r.cfg.APIKey) != ""
}

// CancelActive cancels an in-flight OpenAI turn and clears Neon running state.
func (r *Runner) LiveBlinkSessions() map[uuid.UUID]struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[uuid.UUID]struct{}{}
	for _, id := range r.activeSessions {
		out[id] = struct{}{}
	}
	return out
}

func (r *Runner) CancelAll(ctx context.Context, projectID int64) error {
	rows, err := r.store.RunningSessions(ctx, projectID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("no running Codex session")
	}
	var first error
	for _, row := range rows {
		if err := r.CancelActive(ctx, projectID, row.IssueID); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (r *Runner) CancelActive(ctx context.Context, projectID int64, issueID string) error {
	sessUUID, openaiID, err := r.store.LatestRunningOpenAISession(ctx, projectID, issueID)
	if err != nil {
		return fmt.Errorf("no running Codex session")
	}
	if openaiID != "" {
		_ = r.cancelOpenAI(ctx, openaiID)
		_ = r.deleteSessionWithRetry(ctx, openaiID)
	}
	sess, _ := r.store.GetSession(ctx, sessUUID)
	history := []map[string]any{}
	files := map[string]string{}
	if sess != nil {
		history = sess.History
		files = sess.LatestFiles
	}
	return r.store.FinishSession(ctx, sessUUID, StatusCancelled, history, files)
}

func (r *Runner) cancelOpenAI(ctx context.Context, sessionID string) error {
	cancel := openai.NewAgentSessionInputParamAgentSessionInputCancel()
	return r.client.Beta.Agents.Sessions.Events.New(ctx, sessionID, openai.BetaAgentSessionEventNewParams{
		Events: []openai.AgentSessionInputParamUnion{{
			OfParamAgentSessionInputCancel: &cancel,
		}},
	})
}

// Run executes one sandbox turn, streams events, downloads artifacts, deletes the OpenAI session.
func (r *Runner) Run(ctx context.Context, req RunRequest, emit EventEmitter) (*RunResult, error) {
	if !r.Configured() {
		return nil, fmt.Errorf("OPENAI_API_KEY is not configured")
	}
	if emit == nil {
		emit = func(string, map[string]any) bool { return true }
	}
	model := first(req.Model, r.cfg.Model)
	container := first(req.ContainerSize, r.cfg.ContainerSize)
	network := first(req.NetworkAccess, r.cfg.NetworkAccess)
	timeout := req.TurnTimeout
	if timeout <= 0 {
		timeout = r.cfg.TurnTimeout
	}

	var sess *Session
	var err error
	if req.CodexSessionID != uuid.Nil {
		sess, err = r.store.GetSession(ctx, req.CodexSessionID)
	} else {
		sess, err = r.store.UpsertSession(ctx, req.ProjectID, req.IssueID, req.WorkflowKind, model, container)
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		bg, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cleared, abandonErr := r.store.AbandonIfRunning(bg, sess.ID, "abandoned", "turn ended without a terminal status")
		if abandonErr != nil {
			log.Printf("codex abandon session=%s project=%d issue=%s err=%v", sess.ID, sess.ProjectID, sess.IssueID, abandonErr)
			return
		}
		if cleared {
			log.Printf("codex abandon session=%s project=%d issue=%s reason=turn ended without a terminal status", sess.ID, sess.ProjectID, sess.IssueID)
		}
	}()

	instructions, err := BuildInstructions(req.KitRoot, req.PromptStem, req.PromptVars)
	if err != nil {
		return nil, err
	}

	turn, err := r.store.BeginTurn(ctx, sess.ID, req.UserInput, req.IdempotencyKey)
	if err != nil {
		return nil, err
	}

	_ = emit("status", map[string]any{
		"phase": "provisioning", "codexSessionId": sess.ID.String(), "turnNumber": turn.TurnNumber,
		"model": model, "containerSize": container, "networkAccess": network,
	})

	env, err := r.buildEnvironment(ctx, req, network)
	if err != nil {
		_ = r.store.CompleteTurn(ctx, turn.ID, "", "", string(StatusFailed), "", nil, nil, nil, "environment", err.Error())
		_ = r.store.FinishSession(ctx, sess.ID, StatusFailed, sess.History, sess.LatestFiles)
		return nil, err
	}

	input := buildUserInput(req)
	params := openai.BetaAgentSessionNewParams{
		Agent: openai.BetaAgentSessionNewParamsAgent{
			Model:        openai.String(model),
			Instructions: openai.String(instructions),
			Reasoning: openai.AgentReasoningParam{
				Summary: "auto",
			},
		},
		Environment: env,
		Input:       sessionInput(input, req.ImageDataURLs),
	}

	var opts []option.RequestOption
	if key := strings.TrimSpace(req.IdempotencyKey); key != "" {
		opts = append(opts, option.WithHeader("Idempotency-Key", key))
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := r.runStreamingWithRetry(runCtx, params, opts, sess, turn, emit)
	return result, err
}

func (r *Runner) buildEnvironment(ctx context.Context, req RunRequest, network string) (openai.EnvironmentParamUnion, error) {
	hosted := &openai.EnvironmentParamOpenAIHosted{
		ContainerSize:         first(req.ContainerSize, r.cfg.ContainerSize),
		CapabilityDirectories: CapabilityDirectoryList(),
		Network: openai.EnvironmentParamOpenAIHostedNetwork{
			Access: network,
		},
	}
	if network == "restricted" {
		domains := req.AllowedDomains
		if len(domains) == 0 {
			domains = r.cfg.AllowedDomains
		}
		hosted.Network.AllowedDomains = domains
	}
	ws := req.Workspace
	if ws == nil {
		ws = &WorkspacePlan{SetupCommands: []string{"mkdir -p /workspace/outputs /workspace/repos"}}
	}
	hosted.Packages = openai.EnvironmentParamOpenAIHostedPackages{
		Python: ws.PackagesPython,
		Npm:    ws.PackagesNpm,
		System: ws.PackagesSystem,
	}
	for _, cmd := range ws.SetupCommands {
		hosted.SetupCommands = append(hosted.SetupCommands, openai.SetupCommandParam{Command: cmd})
	}

	if ws.UseArchive && len(ws.ArchiveBytes) > 0 {
		fileID, err := r.uploadWorkspaceArchive(ctx, ws.ArchiveName, ws.ArchiveBytes)
		if err != nil {
			return openai.EnvironmentParamUnion{}, err
		}
		hosted.Files = []openai.HostedEnvironmentFileParamUnion{{
			OfParamFileID: &openai.HostedEnvironmentFileParamFileID{
				FileID: fileID,
				Path:   "/workspace/" + first(ws.ArchiveName, "blink-workspace.tar.gz"),
			},
		}}
	} else {
		for _, f := range ws.Files {
			hosted.Files = append(hosted.Files, openai.HostedEnvironmentFileParamOfParamInline(
				EncodeInlineBase64(f.Content),
				f.Path,
			))
		}
	}
	return openai.EnvironmentParamUnion{OfParamOpenAIHosted: hosted}, nil
}

func (r *Runner) uploadWorkspaceArchive(ctx context.Context, name string, data []byte) (string, error) {
	if name == "" {
		name = "blink-workspace.tar.gz"
	}
	file, err := r.client.Files.New(ctx, openai.FileNewParams{
		File:    openai.File(bytes.NewReader(data), name, "application/gzip"),
		Purpose: openai.FilePurposeUserData,
	})
	if err != nil {
		// Fallback purpose if user_data is rejected by older accounts.
		file, err = r.client.Files.New(ctx, openai.FileNewParams{
			File:    openai.File(bytes.NewReader(data), name, "application/gzip"),
			Purpose: openai.FilePurposeAssistants,
		})
		if err != nil {
			return "", fmt.Errorf("upload workspace archive: %w", err)
		}
	}
	return file.ID, nil
}

func (r *Runner) runStreamingWithRetry(
	ctx context.Context,
	params openai.BetaAgentSessionNewParams,
	opts []option.RequestOption,
	sess *Session,
	turn *Turn,
	emit EventEmitter,
) (*RunResult, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			_ = emit("status", map[string]any{"phase": "retry", "attempt": attempt + 1, "backoffMs": backoff.Milliseconds()})
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
		res, retryable, err := r.runOnce(ctx, params, opts, sess, turn, emit)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !retryable {
			return res, err
		}
	}
	return nil, lastErr
}

func (r *Runner) runOnce(
	ctx context.Context,
	params openai.BetaAgentSessionNewParams,
	opts []option.RequestOption,
	sess *Session,
	turn *Turn,
	emit EventEmitter,
) (*RunResult, bool, error) {
	stream := r.client.Beta.Agents.Sessions.NewStreaming(ctx, params, opts...)
	defer stream.Close()
	openai.BetaAgentSessionWithResultCollection(stream)

	var openaiSessionID, openaiTurnID string
	var output strings.Builder
	commands := []map[string]any{}
	artifactsMeta := []map[string]any{}
	usage := map[string]any{}
	startedAt := time.Now()
	status := StatusCompleted
	errCode, errMsg := "", ""

	cleanup := func() {
		if openaiSessionID == "" {
			return
		}
		r.mu.Lock()
		delete(r.activeSessions, openaiSessionID)
		r.mu.Unlock()
		delCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = r.deleteSessionWithRetry(delCtx, openaiSessionID)
		_ = emit("status", map[string]any{"phase": "deleted", "openaiSessionId": openaiSessionID})
	}
	defer cleanup()

	var commandBuf strings.Builder
	for stream.Next() {
		ev := stream.Current()
		switch ev.Type {
		case "agent.session.created":
			openaiSessionID = ev.Session.ID
			if openaiSessionID == "" {
				openaiSessionID = ev.SessionID
			}
			_ = r.store.SetRunning(ctx, sess.ID, openaiSessionID)
			r.mu.Lock()
			r.activeSessions[openaiSessionID] = sess.ID
			r.mu.Unlock()
			_ = emit("session", map[string]any{
				"openaiSessionId": openaiSessionID,
				"codexSessionId":  sess.ID.String(),
				"phase":           "connected",
			})
		case "agent.session.environment.ready":
			_ = emit("status", map[string]any{"phase": "connected"})
			_ = emit("activity", map[string]any{"kind": "status", "text": "Sandbox ready", "status": "completed"})
		case "agent.session.environment.failed":
			status = StatusFailed
			errCode = "environment_failed"
			errMsg = ev.RawJSON()
			_ = emit("error", map[string]any{"message": "Sandbox environment failed", "detail": errMsg})
		case "agent.session.turn.output_text.delta":
			output.WriteString(ev.Delta)
			_ = emit("output", map[string]any{"delta": ev.Delta})
		case "agent.output.command_execution_output.delta":
			commands = append(commands, map[string]any{"delta": ev.Delta})
			_ = emit("command", map[string]any{"delta": ev.Delta})
			commandBuf.WriteString(ev.Delta)
			for {
				text := commandBuf.String()
				idx := strings.IndexByte(text, '\n')
				if idx < 0 {
					break
				}
				line := strings.TrimSpace(text[:idx])
				commandBuf.Reset()
				commandBuf.WriteString(text[idx+1:])
				if line != "" {
					_ = emit("activity", map[string]any{"kind": "command", "text": line, "status": "running"})
					if stage, message, ok := parseProgressMarker(line); ok {
						_ = emit("activity", map[string]any{
							"kind":   "stage",
							"stage":  stage,
							"text":   message,
							"status": "running",
						})
					}
				}
			}
		case "agent.session.turn.completed":
			if ev.Turn.ID != "" {
				openaiTurnID = ev.Turn.ID
			}
			usage["inputTokens"] = ev.Usage.InputTokens
			usage["outputTokens"] = ev.Usage.OutputTokens
			usage["cachedInputTokens"] = ev.Usage.InputTokensDetails.CachedTokens
			usage["reasoningTokens"] = ev.Usage.OutputTokensDetails.ReasoningTokens
			usage["totalTokens"] = ev.Usage.TotalTokens
			if ev.Turn.SubagentID != "" {
				// ignore nested subagent completion for top-level status
				continue
			}
			_ = emit("status", map[string]any{"phase": "turn_completed", "turnId": openaiTurnID})
			_ = emit("activity", map[string]any{"kind": "status", "text": "Turn finished in the sandbox", "status": "completed"})
		case "agent.session.turn.failed", "agent.session.failed", "error":
			status = StatusFailed
			errCode = ev.Type
			errMsg = ev.RawJSON()
			_ = emit("error", map[string]any{"message": "Codex turn failed", "detail": truncate(errMsg, 500)})
		case "agent.session.turn.cancelled":
			status = StatusCancelled
			errCode = "cancelled"
			errMsg = "turn cancelled"
			_ = emit("status", map[string]any{"phase": "cancelled"})
			_ = emit("activity", map[string]any{"kind": "status", "text": "Turn cancelled", "status": "cancelled"})
		case "agent.session.requires_action":
			_ = emit("status", map[string]any{"phase": "requires_action"})
		default:
			if strings.Contains(ev.Type, "thinking") || strings.Contains(ev.Type, "reasoning") {
				_ = emit("thinking", map[string]any{"text": ev.Delta, "type": ev.Type})
			}
		}
	}
	if err := stream.Err(); err != nil {
		retryable := isRetryableErr(err)
		if openaiSessionID == "" {
			_ = r.store.CompleteTurn(ctx, turn.ID, "", "", string(StatusFailed), output.String(), commands, nil, nil, "stream", err.Error())
			_ = r.store.FinishSession(ctx, sess.ID, StatusFailed, sess.History, sess.LatestFiles)
			return nil, retryable, err
		}
		// Attempt recovery via final result / items when stream disconnects mid-turn.
		if final, ferr := openai.BetaAgentSessionFinalResult(stream); ferr == nil && final != nil {
			if output.Len() == 0 {
				output.WriteString(final.OutputText())
			}
			if openaiSessionID == "" {
				openaiSessionID = final.SessionID()
			}
		} else if retryable {
			_ = r.store.CompleteTurn(ctx, turn.ID, openaiSessionID, openaiTurnID, string(StatusFailed), output.String(), commands, nil, nil, "stream", err.Error())
			return nil, true, err
		} else {
			status = StatusFailed
			errCode = "stream"
			errMsg = err.Error()
		}
	}

	final, ferr := openai.BetaAgentSessionFinalResult(stream)
	if ferr == nil && final != nil {
		if output.Len() == 0 {
			output.WriteString(final.OutputText())
		}
		if openaiSessionID == "" {
			openaiSessionID = final.SessionID()
		}
	}

	files := map[string]string{}
	resultJSON := map[string]any{}
	if openaiSessionID != "" && (status == StatusCompleted || status == StatusFailed) {
		downloaded, meta, derr := r.downloadOutputs(ctx, openaiSessionID, final)
		if derr != nil {
			_ = emit("status", map[string]any{"phase": "artifact_warning", "message": derr.Error()})
		}
		artifactsMeta = meta
		for k, v := range downloaded {
			files[k] = v
		}
		raw := downloaded["/workspace/outputs/blink-result.json"]
		resultJSON = parseResultEnvelope(raw, output.String())
		files = mergeEnvelopeFiles(files, resultJSON)
		for p := range files {
			if strings.Contains(p, "blink-result.json") || IsGovernancePath(p) {
				continue
			}
			_ = emit("activity", map[string]any{"kind": "file", "text": p, "status": string(status)})
		}
	}
	usage["durationMs"] = time.Since(startedAt).Milliseconds()
	usage["commandCount"] = len(commands)
	usage["filesChanged"] = len(files)

	history := append([]map[string]any{}, sess.History...)
	history = append(history, map[string]any{
		"turn":           turn.TurnNumber,
		"prompt":         turn.UserPrompt,
		"output":         truncate(output.String(), 8000),
		"status":         string(status),
		"summary":        stringField(resultJSON, "summary"),
		"recommendation": stringField(resultJSON, "recommendation"),
		"filesChanged":   len(files),
	})
	mergedFiles := map[string]string{}
	for k, v := range sess.LatestFiles {
		mergedFiles[k] = v
	}
	for k, v := range files {
		mergedFiles[k] = v
	}

	_ = r.store.CompleteTurn(ctx, turn.ID, openaiSessionID, openaiTurnID, string(status), output.String(), commands, artifactsMeta, usage, errCode, errMsg)
	_ = r.store.FinishSession(ctx, sess.ID, status, history, mergedFiles)

	out := &RunResult{
		CodexSessionID:  sess.ID,
		TurnID:          turn.ID,
		TurnNumber:      turn.TurnNumber,
		OpenAISessionID: openaiSessionID,
		OpenAITurnID:    openaiTurnID,
		Status:          status,
		OutputText:      output.String(),
		ResultJSON:      resultJSON,
		Files:           mergedFiles,
		Artifacts:       artifactsMeta,
		Commands:        commands,
		Usage:           usage,
		ErrorCode:       errCode,
		ErrorMessage:    errMsg,
		History:         history,
	}
	if status == StatusFailed {
		return out, isRetryableMessage(errMsg), fmt.Errorf("codex turn failed: %s", first(errMsg, errCode))
	}
	if status == StatusCancelled {
		return out, false, fmt.Errorf("codex turn cancelled")
	}
	_ = emit("status", map[string]any{"phase": "completed", "turnId": openaiTurnID})
	_ = emit("activity", map[string]any{"kind": "status", "text": "Artifacts harvested", "status": "completed"})
	return out, false, nil
}

func parseProgressMarker(line string) (stage, message string, ok bool) {
	const prefix = "[ai-sdlc-progress]"
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, prefix) {
		return "", "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	stage = strings.TrimSpace(parts[0])
	message = strings.TrimSpace(parts[1])
	if stage == "" || message == "" {
		return "", "", false
	}
	return stage, message, true
}

func (r *Runner) downloadOutputs(ctx context.Context, sessionID string, final *openai.BetaAgentTurnResult) (map[string]string, []map[string]any, error) {
	out := map[string]string{}
	meta := []map[string]any{}
	if final != nil {
		var buf bytes.Buffer
		art, err := r.client.Beta.Agents.Sessions.Artifacts.ForResult(final).Download(ctx, "/workspace/outputs/blink-result.json", &buf)
		if err == nil {
			out["/workspace/outputs/blink-result.json"] = buf.String()
			meta = append(meta, map[string]any{"path": "/workspace/outputs/blink-result.json", "id": art.ID})
		}
	}
	page, err := r.client.Beta.Agents.Sessions.Artifacts.List(ctx, sessionID, openai.BetaAgentSessionArtifactListParams{})
	if err != nil {
		if len(out) > 0 {
			return out, meta, nil
		}
		return out, meta, err
	}
	for _, art := range page.Data {
		p := art.Path
		if !strings.HasPrefix(p, "/workspace/outputs/") {
			continue
		}
		if _, ok := out[p]; ok {
			continue
		}
		res, err := r.client.Beta.Agents.Sessions.Artifacts.Content(ctx, sessionID, art.ID)
		if err != nil {
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		_ = res.Body.Close()
		out[p] = string(raw)
		meta = append(meta, map[string]any{"path": p, "id": art.ID, "bytes": len(raw)})
	}
	return out, meta, nil
}

func (r *Runner) deleteSessionWithRetry(ctx context.Context, sessionID string) error {
	var last error
	for i := 0; i < 6; i++ {
		_, err := r.client.Beta.Agents.Sessions.Delete(ctx, sessionID)
		if err == nil {
			return nil
		}
		last = err
		var apiErr *openai.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(500*(i+1)) * time.Millisecond):
			}
			continue
		}
		// already gone
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	}
	return last
}

func sessionInput(text string, images []string) openai.BetaAgentSessionNewParamsInputUnion {
	if len(images) == 0 {
		return openai.BetaAgentSessionNewParamsInputUnion{OfString: openai.String(text)}
	}
	parts := []openai.InputContentParamUnion{openai.InputContentParamOfParamInputText(text)}
	for _, image := range images {
		if strings.TrimSpace(image) == "" {
			continue
		}
		parts = append(parts, openai.InputContentParamOfParamInputImage(image))
	}
	return openai.BetaAgentSessionNewParamsInputUnion{
		OfArrayOfInputMessages: []openai.AgentSessionInputMessageParam{{
			Content: parts,
			Type:    openai.AgentSessionInputMessageParamTypeMessage,
		}},
	}
}

// Steer injects a user message into the live OpenAI session.
func (r *Runner) Steer(ctx context.Context, projectID int64, issueID, text string, images []string) error {
	_, openaiID, err := r.store.LatestRunningOpenAISession(ctx, projectID, issueID)
	if err != nil || openaiID == "" {
		return fmt.Errorf("no running Codex session")
	}
	parts := []openai.InputContentParamUnion{openai.InputContentParamOfParamInputText(text)}
	for _, image := range images {
		if strings.TrimSpace(image) != "" {
			parts = append(parts, openai.InputContentParamOfParamInputImage(image))
		}
	}
	msg := openai.AgentSessionInputParamOfParamAgentSessionInputMessage([]openai.AgentSessionInputMessageParam{{
		Content: parts,
		Type:    openai.AgentSessionInputMessageParamTypeMessage,
	}})
	return r.client.Beta.Agents.Sessions.Events.New(ctx, openaiID, openai.BetaAgentSessionEventNewParams{
		Events: []openai.AgentSessionInputParamUnion{msg},
	})
}

func buildUserInput(req RunRequest) string {
	var b strings.Builder
	b.WriteString("Workflow: ")
	b.WriteString(string(req.WorkflowKind))
	b.WriteString("\nIssue: ")
	b.WriteString(req.IssueID)
	b.WriteString("\n\n")
	if hist := HistorySummary(req.PriorHistory, 6); hist != "" {
		b.WriteString("## Prior turn history (rehydrated)\n```json\n")
		b.WriteString(hist)
		b.WriteString("\n```\n\n")
	}
	if len(req.PriorFiles) > 0 {
		b.WriteString(fmt.Sprintf("## Prior modified files rehydrated into workspace (%d files)\n", len(req.PriorFiles)))
		i := 0
		for p := range req.PriorFiles {
			b.WriteString("- ")
			b.WriteString(p)
			b.WriteString("\n")
			i++
			if i >= 40 {
				b.WriteString("- …\n")
				break
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("## Current task\n")
	b.WriteString(strings.TrimSpace(req.UserInput))
	b.WriteString("\n\nWrite /workspace/outputs/blink-result.json when finished.\n")
	out := b.String()
	if len(out) > 3<<20 {
		out = out[:3<<20] + "\n…[truncated]"
	}
	return out
}

func isRetryableErr(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode == http.StatusServiceUnavailable {
			return true
		}
		if strings.Contains(strings.ToLower(apiErr.Error()), "overloaded") ||
			strings.Contains(strings.ToLower(apiErr.Error()), "rate_limit") ||
			strings.Contains(strings.ToLower(apiErr.Error()), "files_api_rate_limit") {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "overloaded") || strings.Contains(msg, "timeout") || strings.Contains(msg, "temporarily")
}

func isRetryableMessage(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "overloaded") || strings.Contains(m, "rate_limit") || strings.Contains(m, "429") || strings.Contains(m, "503")
}

func parseFallbackJSON(text string) map[string]any {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(text[start:end+1]), &m); err != nil {
		return nil
	}
	return m
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func first(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
