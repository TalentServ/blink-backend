package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func TestBuildUserInputIncludesHistoryAndTruncationGuard(t *testing.T) {
	in := buildUserInput(RunRequest{
		WorkflowKind: WorkflowImplementStep,
		IssueID:      "X-1",
		UserInput:    "do the work",
		PriorHistory: []map[string]any{{"turn": 1, "summary": "first"}},
		PriorFiles:   map[string]string{"src/main.go": "package main"},
	})
	if !strings.Contains(in, "Prior turn history") || !strings.Contains(in, "do the work") {
		t.Fatalf("unexpected input: %s", in)
	}
	if !strings.Contains(in, "src/main.go") {
		t.Fatalf("missing prior file listing")
	}
}

func TestIsRetryableErr(t *testing.T) {
	err := &openai.Error{StatusCode: http.StatusTooManyRequests}
	if !isRetryableErr(err) {
		t.Fatal("429 should retry")
	}
	err = &openai.Error{StatusCode: http.StatusBadRequest}
	if isRetryableErr(err) {
		t.Fatal("400 should not retry")
	}
}

func TestParseProgressMarker(t *testing.T) {
	stage, message, ok := parseProgressMarker("[ai-sdlc-progress] self-review: checked acceptance criteria")
	if !ok || stage != "self-review" || message != "checked acceptance criteria" {
		t.Fatalf("unexpected progress marker: %q %q %v", stage, message, ok)
	}
	if _, _, ok := parseProgressMarker("ordinary command output"); ok {
		t.Fatal("ordinary output should not be treated as a progress marker")
	}
}

func TestMockAgentsSessionLifecycle(t *testing.T) {
	var deleted atomic.Int32
	var deleteCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/agents/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", 405)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(payload any) {
			b, _ := json.Marshal(payload)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", payload.(map[string]any)["type"], b)
			flusher.Flush()
		}
		write(map[string]any{
			"type": "agent.session.created", "event_id": "e1", "session_id": "sess_test",
			"session": map[string]any{"id": "sess_test"},
		})
		write(map[string]any{
			"type": "agent.session.environment.ready", "event_id": "e2", "session_id": "sess_test",
		})
		write(map[string]any{
			"type": "agent.session.turn.output_text.delta", "event_id": "e3", "session_id": "sess_test",
			"delta": "hello from codex",
		})
		write(map[string]any{
			"type": "agent.output.command_execution_output.delta", "event_id": "e4", "session_id": "sess_test",
			"delta": "$ echo ok\n",
		})
		write(map[string]any{
			"type": "agent.session.turn.completed", "event_id": "e5", "session_id": "sess_test",
			"turn": map[string]any{"id": "turn_1", "subagent_id": ""},
		})
	})
	mux.HandleFunc("/v1/agents/sessions/sess_test", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method", 405)
			return
		}
		n := deleteCalls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"message":"teardown"}}`))
			return
		}
		deleted.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"sess_test","deleted":true}`))
	})
	mux.HandleFunc("/v1/agents/sessions/sess_test/artifacts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"art1","path":"/workspace/outputs/blink-result.json"}],"has_more":false}`))
	})
	mux.HandleFunc("/v1/agents/sessions/sess_test/artifacts/art1/content", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(`{"summary":"ok","files":[],"recommendation":"COMPLETED"}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := openai.NewClient(
		option.WithAPIKey("test"),
		option.WithBaseURL(srv.URL+"/v1/"),
	)
	r := &Runner{
		client:         client,
		cfg:            RunnerConfig{APIKey: "test", Model: "gpt-6-luna", ContainerSize: "small", NetworkAccess: "disabled", TurnTimeout: time.Minute},
		activeSessions: map[string]uuid.UUID{},
	}
	stream := client.Beta.Agents.Sessions.NewStreaming(context.Background(), openai.BetaAgentSessionNewParams{
		Agent: openai.BetaAgentSessionNewParamsAgent{Model: openai.String("gpt-6-luna")},
		Environment: openai.EnvironmentParamUnion{OfParamOpenAIHosted: &openai.EnvironmentParamOpenAIHosted{
			ContainerSize: "small",
			Network:       openai.EnvironmentParamOpenAIHostedNetwork{Access: "disabled"},
		}},
		Input: openai.BetaAgentSessionNewParamsInputUnion{OfString: openai.String("test")},
	})
	defer stream.Close()
	openai.BetaAgentSessionWithResultCollection(stream)
	var gotOutput strings.Builder
	sessionID := ""
	for stream.Next() {
		ev := stream.Current()
		if ev.Type == "agent.session.created" {
			sessionID = ev.Session.ID
		}
		if ev.Type == "agent.session.turn.output_text.delta" {
			gotOutput.WriteString(ev.Delta)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if sessionID != "sess_test" {
		t.Fatalf("session id=%q", sessionID)
	}
	if gotOutput.String() != "hello from codex" {
		t.Fatalf("output=%q", gotOutput.String())
	}
	if err := r.deleteSessionWithRetry(context.Background(), "sess_test"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted.Load() != 1 {
		t.Fatalf("expected successful delete after 409 retry, got deleted=%d calls=%d", deleted.Load(), deleteCalls.Load())
	}
	_ = io.Discard
}
