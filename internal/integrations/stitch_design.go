package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const stitchMCP = "https://stitch.googleapis.com/mcp"

var stitchLayouts = []struct {
	name   string
	layout string
}{
	{"Focused", "Use a single centered column. One primary action."},
	{"Sidebar", "Use a left sidebar for navigation and a wide main panel."},
	{"Cards", "Use a card grid. Put the main task in the first card."},
}

func (s *Service) ProposeStitchDesigns(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	_ = readJSON(r, &req)
	prompt := stitchPrompt(req)
	if prompt == "" {
		writeErr(w, http.StatusBadRequest, "A groomed requirement is required.")
		return
	}
	key := strings.TrimSpace(s.cfg.StitchAPIKey)
	if key == "" {
		writeErr(w, http.StatusBadRequest, "Stitch is not configured. Set STITCH_API_KEY on the server.")
		return
	}
	projectID, err := stitchCreateProject(r.Context(), key, firstNonEmpty(str(req["projectName"]), "Blink design"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	options := make([]map[string]any, len(stitchLayouts))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i, layout := range stitchLayouts {
		wg.Add(1)
		go func(i int, layout struct {
			name   string
			layout string
		}) {
			defer wg.Done()
			screenPrompt := prompt + "\n\nLayout: " + layout.layout + "\nMake one desktop screen a product team can compare with the other options."
			raw, callErr := stitchCall(r.Context(), key, "generate_screen_from_text", map[string]any{
				"projectId":  projectID,
				"prompt":     screenPrompt,
				"deviceType": "DESKTOP",
			})
			mu.Lock()
			defer mu.Unlock()
			if callErr != nil {
				if firstErr == nil {
					firstErr = callErr
				}
				return
			}
			options[i] = map[string]any{
				"id":             fmt.Sprintf("stitch-%d", i+1),
				"name":           layout.name,
				"summary":        layout.layout,
				"layout":         []string{"linear", "split", "hub"}[i],
				"imageUrl":       stitchImage(raw),
				"stitchScreenId": stitchScreenID(raw),
				"screens":        []any{},
			}
		}(i, layout)
	}
	wg.Wait()
	ready := make([]any, 0, len(options))
	for _, option := range options {
		if option != nil && str(option["imageUrl"]) != "" {
			ready = append(ready, option)
		}
	}
	if len(ready) == 0 {
		writeErr(w, http.StatusBadGateway, firstNonEmpty(errText(firstErr), "Stitch did not return a design."))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"projectId": projectID,
		"message":   "Pick one design. Export that screen from Stitch to Figma, then bind the file here.",
		"options":   ready,
	})
}

func stitchPrompt(req map[string]any) string {
	text := strings.TrimSpace(str(req["requirementText"]))
	if stories, ok := req["stories"].([]any); ok {
		var names []string
		for _, raw := range stories {
			item, _ := raw.(map[string]any)
			title := firstNonEmpty(str(item["title"]), str(item["name"]))
			if title != "" {
				names = append(names, title)
			}
		}
		if len(names) > 0 {
			text += "\n\nStories:\n- " + strings.Join(names, "\n- ")
		}
	}
	if len(text) > 4000 {
		text = text[:4000]
	}
	return strings.TrimSpace(text)
}

func stitchCreateProject(ctx context.Context, key, title string) (string, error) {
	raw, err := stitchCall(ctx, key, "create_project", map[string]any{"title": title})
	if err != nil {
		return "", err
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return "", fmt.Errorf("Stitch did not return a project.")
	}
	id := stitchProjectID(payload)
	if id == "" {
		return "", fmt.Errorf("Stitch did not return a project.")
	}
	return id, nil
}

func stitchProjectID(v any) string {
	switch node := v.(type) {
	case map[string]any:
		for _, key := range []string{"name", "projectId", "id"} {
			if id := bareStitchID(str(node[key])); id != "" {
				return id
			}
		}
		for _, child := range node {
			if id := stitchProjectID(child); id != "" {
				return id
			}
		}
	case []any:
		for _, child := range node {
			if id := stitchProjectID(child); id != "" {
				return id
			}
		}
	}
	return ""
}

func bareStitchID(value string) string {
	value = strings.TrimSpace(value)
	if i := strings.LastIndex(value, "/"); i >= 0 {
		value = value[i+1:]
	}
	if value == "" {
		return ""
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return value
}

func stitchSession(ctx context.Context, key string, client *http.Client) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "blink", "version": "0.1.0"},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stitchMCP, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-Goog-Api-Key", key)
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Could not reach Stitch.")
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("%s", firstNonEmpty(stitchErrorText(raw), fmt.Sprintf("Stitch returned HTTP %d.", res.StatusCode)))
	}
	session := res.Header.Get("Mcp-Session-Id")
	if session == "" {
		return "", nil
	}
	note, _ := http.NewRequestWithContext(ctx, http.MethodPost, stitchMCP, strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	note.Header.Set("Content-Type", "application/json")
	note.Header.Set("Accept", "application/json, text/event-stream")
	note.Header.Set("MCP-Protocol-Version", "2024-11-05")
	note.Header.Set("Mcp-Session-Id", session)
	note.Header.Set("X-Goog-Api-Key", key)
	ready, err := client.Do(note)
	if err == nil {
		ready.Body.Close()
	}
	return session, nil
}

func stitchErrorText(raw []byte) string {
	var payload map[string]any
	if json.Unmarshal(stitchRPCPayload(raw), &payload) != nil {
		return ""
	}
	errNode, _ := payload["error"].(map[string]any)
	return str(errNode["message"])
}

func stitchCall(ctx context.Context, key, tool string, args map[string]any) (json.RawMessage, error) {
	client := &http.Client{Timeout: 4 * time.Minute}
	session, err := stitchSession(ctx, key, client)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      time.Now().UnixNano(),
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stitchMCP, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2024-11-05")
	req.Header.Set("X-Goog-Api-Key", key)
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Could not reach Stitch.")
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("Stitch returned HTTP %d.", res.StatusCode)
	}
	payload := stitchRPCPayload(raw)
	var envelope struct {
		Error  *struct{ Message string `json:"message"` } `json:"error"`
		Result json.RawMessage                                         `json:"result"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return nil, fmt.Errorf("Stitch returned an unreadable response.")
	}
	if envelope.Error != nil && envelope.Error.Message != "" {
		return nil, fmt.Errorf("%s", envelope.Error.Message)
	}
	var toolResult struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		Content           []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(envelope.Result, &toolResult) != nil {
		return envelope.Result, nil
	}
	if toolResult.IsError {
		text := ""
		for _, part := range toolResult.Content {
			text += part.Text
		}
		return nil, fmt.Errorf("%s", firstNonEmpty(text, "Stitch could not generate this design."))
	}
	if len(toolResult.StructuredContent) > 0 && string(toolResult.StructuredContent) != "null" {
		return toolResult.StructuredContent, nil
	}
	for _, part := range toolResult.Content {
		if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
			return json.RawMessage(part.Text), nil
		}
	}
	return envelope.Result, nil
}

func stitchRPCPayload(raw []byte) []byte {
	text := strings.TrimSpace(string(raw))
	if strings.HasPrefix(text, "{") {
		return []byte(text)
	}
	var last string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			last = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	if last == "" {
		return raw
	}
	return []byte(last)
}

func stitchImage(raw json.RawMessage) string {
	var payload any
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	return findStitchImage(payload)
}

func findStitchImage(v any) string {
	switch node := v.(type) {
	case map[string]any:
		if shot, ok := node["screenshot"].(map[string]any); ok {
			if url := str(shot["downloadUrl"]); strings.HasPrefix(url, "http") {
				return url
			}
		}
		if url := str(node["downloadUrl"]); strings.HasPrefix(url, "http") && strings.Contains(strings.ToLower(str(node["mimeType"])+str(node["type"])), "image") {
			return url
		}
		for _, child := range node {
			if url := findStitchImage(child); url != "" {
				return url
			}
		}
	case []any:
		for _, child := range node {
			if url := findStitchImage(child); url != "" {
				return url
			}
		}
	}
	return ""
}

func stitchScreenID(raw json.RawMessage) string {
	var payload any
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	return findStitchID(payload)
}

func findStitchID(v any) string {
	switch node := v.(type) {
	case map[string]any:
		if id := firstNonEmpty(str(node["screenId"]), str(node["projectId"]), str(node["id"])); id != "" && !strings.Contains(id, " ") {
			if str(node["screenId"]) != "" || str(node["projectId"]) != "" {
				return id
			}
		}
		for _, child := range node {
			if id := findStitchID(child); id != "" {
				return id
			}
		}
	case []any:
		for _, child := range node {
			if id := findStitchID(child); id != "" {
				return id
			}
		}
	}
	return ""
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
