package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/nisha-ts-40599/blink-backend/internal/chat"
	"github.com/nisha-ts-40599/blink-backend/internal/project"
)

func badRequest(msg string) error {
	return fmt.Errorf("%s", msg)
}

func (s *Server) getProjectChat(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := s.proj.RequireOwned(r.Context(), id, sessionEmail(r)); err != nil {
		writeErr(w, err)
		return
	}
	thread, err := s.chat.EnsureThread(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	msgs, err := s.chat.ListMessages(r.Context(), thread.ID, 100)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"thread":   thread,
		"messages": msgs,
		"models":   []string{"gpt-5.6-luna", "terra", "sol", "gemini-3.8-flash", "gemini-3.5-flash-lite"},
	})
}

func (s *Server) clearProjectChat(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := s.proj.RequireOwned(r.Context(), id, sessionEmail(r)); err != nil {
		writeErr(w, err)
		return
	}
	thread, err := s.chat.EnsureThread(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.chat.ClearMessages(r.Context(), thread.ID); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "cleared": true})
}

func (s *Server) postProjectChatMessage(w http.ResponseWriter, r *http.Request) {
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

	var req struct {
		Text        string `json:"text"`
		Mode        string `json:"mode"`
		Model       string `json:"model"`
		CurrentStep string `json:"currentStep"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeErr(w, badRequest("message text is required"))
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode != "agent" {
		mode = "ask"
	}
	// Phase 1 forces Ask semantics even if client sends agent.
	mode = "ask"

	model, err := chat.NormalizeModel(req.Model)
	if err != nil {
		writeErr(w, badRequest(err.Error()))
		return
	}

	thread, err := s.chat.EnsureThread(r.Context(), id, sessionEmail(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	turnID := uuid.NewString()
	modelPtr, modePtr, turnPtr := model, mode, turnID
	userMsg, err := s.chat.AddMessage(r.Context(), thread.ID, "user", text, &modelPtr, &modePtr, &turnPtr, nil)
	if err != nil {
		writeErr(w, err)
		return
	}

	history, _ := s.chat.ListMessages(r.Context(), thread.ID, 40)
	agentMessages := make([]map[string]any, 0, len(history))
	for _, m := range history {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		agentMessages = append(agentMessages, map[string]any{
			"role":    m.Role,
			"content": m.Content,
		})
	}

	contextPayload := s.buildChatContext(r, p, req.CurrentStep)
	payload := map[string]any{
		"command":     "chat-turn",
		"mode":        mode,
		"model":       model,
		"messages":    agentMessages,
		"context":     contextPayload,
		"projectId":   strconv.FormatInt(id, 10),
		"projectName": p.ProjectName,
		"actor":       sessionEmail(r),
	}

	wantsStream := strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	if wantsStream {
		s.streamChatTurn(w, r, thread.ID, turnID, modelPtr, modePtr, userMsg, payload)
		return
	}

	// Non-streaming JSON fallback (tests / older clients).
	raw, agentErr := s.agent.ChatTurn(r.Context(), payload)
	reply := ""
	var toolMeta json.RawMessage
	if agentErr != nil {
		reply = "I could not reach the Blink agent runtime right now. Check that the agent is running, then try again.\n\n(" + agentErr.Error() + ")"
	} else {
		var root map[string]any
		_ = json.Unmarshal(raw, &root)
		if v, ok := root["reply"].(string); ok {
			reply = strings.TrimSpace(v)
		}
		if reply == "" {
			if v, ok := root["message"].(string); ok {
				reply = strings.TrimSpace(v)
			}
		}
		if reply == "" {
			reply = "No reply was returned from the agent."
		}
		meta := map[string]any{}
		if needs, ok := root["needsConnect"].(string); ok && strings.TrimSpace(needs) != "" {
			meta["needsConnect"] = needs
		}
		if tools, ok := root["toolEvents"]; ok {
			meta["toolEvents"] = tools
		}
		if len(meta) > 0 {
			toolMeta, _ = json.Marshal(meta)
		}
	}

	assistantMsg, err := s.chat.AddMessage(r.Context(), thread.ID, "assistant", reply, &modelPtr, &modePtr, &turnPtr, toolMeta)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"turnId":    turnID,
		"user":      userMsg,
		"assistant": assistantMsg,
	})
}

func (s *Server) streamChatTurn(
	w http.ResponseWriter,
	r *http.Request,
	threadID int64,
	turnID string,
	modelPtr string,
	modePtr string,
	userMsg *chat.Message,
	payload map[string]any,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, badRequest("streaming is not supported on this connection"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	writeSSE := func(event string, payload any) bool {
		b, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !writeSSE("user", map[string]any{"turnId": turnID, "message": userMsg}) {
		return
	}
	if !writeSSE("assistant_start", map[string]any{"turnId": turnID}) {
		return
	}

	var streamed strings.Builder
	result, agentErr := s.agent.ChatTurnStream(r.Context(), payload, func(delta string) error {
		streamed.WriteString(delta)
		if !writeSSE("token", map[string]any{"text": delta}) {
			return fmt.Errorf("client disconnected")
		}
		return nil
	})

	reply := strings.TrimSpace(streamed.String())
	var toolMeta json.RawMessage
	if agentErr != nil {
		if reply == "" {
			reply = "I could not reach the Blink agent runtime right now. Check that the agent is running, then try again.\n\n(" + agentErr.Error() + ")"
			_ = writeSSE("token", map[string]any{"text": reply})
		}
		_ = writeSSE("error", map[string]any{"message": agentErr.Error()})
	} else if result != nil {
		if strings.TrimSpace(result.Reply) != "" {
			reply = strings.TrimSpace(result.Reply)
		}
		meta := map[string]any{}
		if result.NeedsConnect != "" {
			meta["needsConnect"] = result.NeedsConnect
		}
		if result.ToolEvents != nil {
			meta["toolEvents"] = result.ToolEvents
		}
		if len(meta) > 0 {
			toolMeta, _ = json.Marshal(meta)
		}
		if result.Status == "error" && reply == "" && result.Message != "" {
			reply = result.Message
		}
	}
	if reply == "" {
		reply = "No reply was returned from the agent."
	}

	turnPtr := turnID
	assistantMsg, err := s.chat.AddMessage(r.Context(), threadID, "assistant", reply, &modelPtr, &modePtr, &turnPtr, toolMeta)
	if err != nil {
		_ = writeSSE("error", map[string]any{"message": err.Error()})
		return
	}
	_ = writeSSE("message_done", map[string]any{
		"turnId":  turnID,
		"message": assistantMsg,
		"user":    userMsg,
	})
}

func (s *Server) buildChatContext(r *http.Request, p *project.ProjectResponse, currentStep string) map[string]any {
	connected := []string{}
	bindings := map[string]any{}
	for _, provider := range []string{"jira", "github", "figma"} {
		if s.integ.IsConnected(r.Context(), p.ID, sessionEmail(r), provider) {
			connected = append(connected, provider)
			if snap := s.integ.ConnectionSnapshot(r.Context(), p.ID, sessionEmail(r), provider); snap != nil {
				bindings[provider] = snap
			}
		}
	}

	wizard := map[string]any{}
	if len(p.WizardState) > 0 {
		_ = json.Unmarshal(p.WizardState, &wizard)
	}
	compact := map[string]any{
		"projectName":        p.ProjectName,
		"description":        p.Description,
		"currentStep":        currentStep,
		"wizardStep":         p.WizardStep,
		"connectedProviders": connected,
		"bindings":           bindings,
	}
	if v, ok := wizard["groomConfirmed"]; ok {
		compact["groomConfirmed"] = v
	}
	if v, ok := wizard["groomStatus"]; ok {
		compact["groomStatus"] = v
	}
	if v, ok := wizard["projectName"]; ok && compact["projectName"] == "" {
		compact["projectName"] = v
	}
	if stakes, ok := wizard["stakeholderAssignments"]; ok {
		compact["stakeholders"] = stakes
	} else if len(p.Stakeholders) > 0 {
		compact["stakeholders"] = p.Stakeholders
	}
	if v, ok := wizard["jiraCreatedIssues"]; ok {
		compact["jiraCreatedIssues"] = v
	}
	if v, ok := wizard["questions"]; ok {
		compact["leftoverQuestions"] = v
	}
	if v, ok := wizard["integrations"]; ok {
		compact["wizardIntegrations"] = v
	}
	if draft, ok := wizard["groomDraft"].(string); ok && strings.TrimSpace(draft) != "" {
		if len(draft) > 1200 {
			draft = draft[:1200] + "…"
		}
		compact["groomDraft"] = draft
	} else if req, ok := wizard["requirementsText"].(string); ok && strings.TrimSpace(req) != "" {
		if len(req) > 1200 {
			req = req[:1200] + "…"
		}
		compact["requirementsText"] = req
	}
	return compact
}
