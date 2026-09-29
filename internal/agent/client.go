package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/nisha-ts-40599/blink-backend/internal/config"
)

type Client struct {
	cfg          config.Config
	client       *http.Client
	streamClient *http.Client
	lambdaOnce   sync.Once
	lambda       *lambda.Client
}

type requestedModelContextKey struct{}

// WithRequestedModel attaches a validated, non-secret model preference to an
// incoming Blink request. Invoke and CommandStream forward it to the runtime.
func WithRequestedModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, requestedModelContextKey{}, model)
}

func requestedModel(ctx context.Context) string {
	model, _ := ctx.Value(requestedModelContextKey{}).(string)
	return strings.TrimSpace(model)
}

func New(cfg config.Config) *Client {
	return &Client{
		cfg: cfg,
		client: &http.Client{
			Timeout: 90 * time.Second,
		},
		// Streaming turns must not use a hard client timeout; rely on context cancel.
		streamClient: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				ResponseHeaderTimeout: 45 * time.Second,
				IdleConnTimeout:       90 * time.Second,
			},
		},
	}
}

func (c *Client) Invoke(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if strings.TrimSpace(c.cfg.AgentRuntimeToken) == "" {
		return nil, fmt.Errorf("BLINK_AGENT_RUNTIME_TOKEN is not configured")
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if model := requestedModel(ctx); model != "" {
		if _, supplied := payload["model"]; !supplied {
			payload["model"] = model
		}
	}
	if shouldInvokeLambda(c.cfg, c.cfg.AgentRuntimeURL) {
		return c.invokeLambda(ctx, payload)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.AgentRuntimeURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Blink-Backend-Go/1.0")
	req.Header.Set("Authorization", "Bearer "+c.cfg.AgentRuntimeToken)
	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("agent runtime HTTP %d: %s", res.StatusCode, truncate(string(raw), 400))
	}
	return json.RawMessage(raw), nil
}

func (c *Client) invokeLambda(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	event, err := wrapAPIGWv2(body, strings.TrimSpace(c.cfg.AgentRuntimeToken))
	if err != nil {
		return nil, err
	}
	c.lambdaOnce.Do(func() {
		c.lambda = lambda.New(lambda.Options{
			Region: c.cfg.AWSRegion,
			Credentials: credentials.NewStaticCredentialsProvider(
				strings.TrimSpace(c.cfg.AWSAccessKeyID),
				strings.TrimSpace(c.cfg.AWSSecretAccessKey),
				"",
			),
			HTTPClient: &http.Client{Timeout: 95 * time.Second},
		})
	})
	out, err := c.lambda.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(strings.TrimSpace(c.cfg.AgentLambdaName)),
		Payload:      event,
	})
	if err != nil {
		return nil, fmt.Errorf("agent runtime Lambda invoke: %w", err)
	}
	if out.FunctionError != nil && strings.TrimSpace(*out.FunctionError) != "" {
		return nil, fmt.Errorf("agent runtime Lambda failed: %s", *out.FunctionError)
	}
	status, inner, err := unwrapLambdaPayload(out.Payload)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("agent runtime HTTP %d: %s", status, truncate(string(inner), 400))
	}
	return json.RawMessage(inner), nil
}

func (c *Client) Clarify(ctx context.Context, body map[string]any) (json.RawMessage, error) {
	body["command"] = "clarify-requirement"
	if _, ok := body["mode"]; !ok {
		body["mode"] = "discovery"
	}
	return c.Invoke(ctx, body)
}

func (c *Client) PlanProductScopeWithPayload(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "plan-product-scope"
	return c.Invoke(ctx, payload)
}

func (c *Client) PlanProductScope(ctx context.Context, projectName, projectID, requirementText, actor string) (json.RawMessage, error) {
	payload := map[string]any{"projectName": projectName}
	if projectID != "" {
		payload["projectId"] = projectID
	}
	if requirementText != "" {
		payload["requirementText"] = requirementText
	}
	if actor != "" {
		payload["actor"] = actor
	}
	return c.PlanProductScopeWithPayload(ctx, payload)
}

func (c *Client) ClarifyProductScope(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "clarify-product-scope"
	return c.Invoke(ctx, payload)
}

func (c *Client) ConfirmProductScope(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "confirm-product-scope"
	return c.Invoke(ctx, payload)
}

func (c *Client) ClassifyWork(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "classify-work"
	return c.Invoke(ctx, payload)
}

func (c *Client) ProposeDesigns(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "propose-designs"
	return c.Invoke(ctx, payload)
}

func (c *Client) CreateSpec(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "create-spec"
	return c.Invoke(ctx, payload)
}

func (c *Client) TechnicalPlan(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "technical-plan"
	return c.Invoke(ctx, payload)
}

func (c *Client) GroomingStakeholderPack(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "grooming-stakeholder-pack"
	return c.Invoke(ctx, payload)
}

func (c *Client) GroomingRevision(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "grooming-revision"
	return c.Invoke(ctx, payload)
}

func (c *Client) GroomingSignOffCapture(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "grooming-sign-off-capture"
	return c.Invoke(ctx, payload)
}

// GroomingQuestions, GroomingAnalysis, DependencyGraph, and
// ArchitectureProposal return Framework proposals only. The Backend canonical
// API validates and adopts any proposal before it affects durable state.
func (c *Client) GroomingQuestions(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "grooming-questions"
	return c.Invoke(ctx, payload)
}

func (c *Client) GroomingAnalysis(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "grooming-analysis"
	return c.Invoke(ctx, payload)
}

func (c *Client) DependencyGraph(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "dependency-graph"
	return c.Invoke(ctx, payload)
}

func (c *Client) ArchitectureProposal(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "architecture-proposal"
	return c.Invoke(ctx, payload)
}

func (c *Client) ImplementStep(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "implement-step"
	return c.Invoke(ctx, payload)
}

func (c *Client) QaValidation(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "qa-validation"
	return c.Invoke(ctx, payload)
}

func (c *Client) SdlcStart(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "sdlc-start"
	return c.Invoke(ctx, payload)
}

func (c *Client) SdlcNext(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "sdlc-next"
	return c.Invoke(ctx, payload)
}

func (c *Client) ConfirmStakeholders(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "confirm-stakeholders"
	return c.Invoke(ctx, payload)
}

func (c *Client) SummarizeDiscussion(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "summarize-discussion"
	return c.Invoke(ctx, payload)
}

func (c *Client) ConfigureStakeholders(ctx context.Context, projectName, projectID string, stakeholders []map[string]string) (json.RawMessage, error) {
	payload := map[string]any{
		"command":      "configure-stakeholders",
		"mode":         "apply",
		"projectName":  projectName,
		"stakeholders": stakeholders,
	}
	if projectID != "" {
		payload["projectId"] = projectID
	}
	return c.Invoke(ctx, payload)
}

// SetupNewWorkspace invokes setup-new-workspace apply on the agent runtime.
func (c *Client) SetupNewWorkspace(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "setup-new-workspace"
	if _, ok := payload["mode"]; !ok {
		payload["mode"] = "apply"
	}
	return c.Invoke(ctx, payload)
}

func (c *Client) ChatTurn(ctx context.Context, payload map[string]any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "chat-turn"
	return c.Invoke(ctx, payload)
}

// ChatTurnResult is the terminal payload from a streamed chat-turn.
type ChatTurnResult struct {
	Reply        string
	NeedsConnect string
	ToolEvents   any
	Model        string
	Status       string
	Message      string
	Raw          json.RawMessage
}

// ChatTurnStream POSTs to the agent /chat/stream SSE endpoint and invokes onToken for each delta.
func (c *Client) ChatTurnStream(ctx context.Context, payload map[string]any, onToken func(string) error) (*ChatTurnResult, error) {
	if strings.TrimSpace(c.cfg.AgentRuntimeToken) == "" {
		return nil, fmt.Errorf("BLINK_AGENT_RUNTIME_TOKEN is not configured")
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "chat-turn"
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(c.cfg.AgentRuntimeURL, "/") + "/chat/stream"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Blink-Backend-Go/1.0")
	req.Header.Set("Authorization", "Bearer "+c.cfg.AgentRuntimeToken)
	req.Header.Set("Cache-Control", "no-cache")

	res, err := c.streamClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, fmt.Errorf("agent runtime HTTP %d: %s", res.StatusCode, truncate(string(raw), 400))
	}

	result := &ChatTurnResult{Status: "ok"}
	var gotDone bool
	err = readSSE(res.Body, func(event string, data []byte) error {
		switch event {
		case "token":
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(data, &payload); err != nil {
				return nil
			}
			if payload.Text == "" || onToken == nil {
				return nil
			}
			return onToken(payload.Text)
		case "error":
			var payload struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(data, &payload)
			if payload.Message != "" {
				result.Message = payload.Message
				result.Status = "error"
			}
			return nil
		case "done":
			gotDone = true
			result.Raw = append(json.RawMessage(nil), data...)
			var root map[string]any
			if err := json.Unmarshal(data, &root); err != nil {
				return nil
			}
			if v, ok := root["reply"].(string); ok {
				result.Reply = strings.TrimSpace(v)
			}
			if v, ok := root["needsConnect"].(string); ok {
				result.NeedsConnect = strings.TrimSpace(v)
			}
			if v, ok := root["model"].(string); ok {
				result.Model = v
			}
			if v, ok := root["status"].(string); ok {
				result.Status = v
			}
			if v, ok := root["message"].(string); ok {
				result.Message = v
			}
			if tools, ok := root["toolEvents"]; ok {
				result.ToolEvents = tools
			}
			return nil
		default:
			return nil
		}
	})
	if err != nil {
		return nil, err
	}
	if !gotDone && result.Reply == "" && result.Message == "" {
		return nil, fmt.Errorf("agent stream ended without a done event")
	}
	return result, nil
}

// CommandStream POSTs to agent /sdlc/stream and forwards thinking deltas.
func (c *Client) CommandStream(ctx context.Context, payload map[string]any, onThinking func(string) error) (json.RawMessage, error) {
	if strings.TrimSpace(c.cfg.AgentRuntimeToken) == "" {
		return nil, fmt.Errorf("BLINK_AGENT_RUNTIME_TOKEN is not configured")
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if model := requestedModel(ctx); model != "" {
		if _, supplied := payload["model"]; !supplied {
			payload["model"] = model
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(c.cfg.AgentRuntimeURL, "/") + "/sdlc/stream"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Blink-Backend-Go/1.0")
	req.Header.Set("Authorization", "Bearer "+c.cfg.AgentRuntimeToken)
	req.Header.Set("Cache-Control", "no-cache")

	res, err := c.streamClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, fmt.Errorf("agent runtime HTTP %d: %s", res.StatusCode, truncate(string(raw), 400))
	}

	var done json.RawMessage
	var gotDone bool
	err = readSSE(res.Body, func(event string, data []byte) error {
		switch event {
		case "thinking":
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(data, &payload); err != nil || payload.Text == "" || onThinking == nil {
				return nil
			}
			return onThinking(payload.Text)
		case "error":
			return nil
		case "done":
			gotDone = true
			done = append(json.RawMessage(nil), data...)
			return nil
		default:
			return nil
		}
	})
	if err != nil {
		return nil, err
	}
	if !gotDone || len(done) == 0 {
		return nil, fmt.Errorf("agent stream ended without a done event")
	}
	return done, nil
}

func (c *Client) ClarifyStream(ctx context.Context, body map[string]any, onThinking func(string) error) (json.RawMessage, error) {
	if body == nil {
		body = map[string]any{}
	}
	body["command"] = "clarify-requirement"
	if _, ok := body["mode"]; !ok {
		body["mode"] = "discovery"
	}
	raw, err := c.CommandStream(ctx, body, onThinking)
	if err == nil {
		return raw, nil
	}
	return c.Clarify(ctx, body)
}

func (c *Client) PlanProductScopeStreamWithPayload(ctx context.Context, payload map[string]any, onThinking func(string) error) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["command"] = "plan-product-scope"
	raw, err := c.CommandStream(ctx, payload, onThinking)
	if err == nil {
		return raw, nil
	}
	return c.PlanProductScopeWithPayload(ctx, payload)
}

func (c *Client) PlanProductScopeStream(ctx context.Context, projectName, projectID, requirementText, actor string, onThinking func(string) error) (json.RawMessage, error) {
	payload := map[string]any{"projectName": projectName}
	if projectID != "" {
		payload["projectId"] = projectID
	}
	if requirementText != "" {
		payload["requirementText"] = requirementText
	}
	if actor != "" {
		payload["actor"] = actor
	}
	return c.PlanProductScopeStreamWithPayload(ctx, payload, onThinking)
}

func readSSE(r io.Reader, handle func(event string, data []byte) error) error {
	scanner := bufio.NewScanner(r)
	// LLM tokens / JSON frames can be large.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	event := "message"
	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			event = "message"
			return nil
		}
		data := []byte(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		ev := event
		event = "message"
		return handle(ev, data)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / keepalive
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(line[len("event:"):])
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(line[len("data:"):]))
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
