package integrations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	figmaMCP              = "https://mcp.figma.com/mcp"
	figmaDemoCreatesPerDay = 6
	figmaDemoReadsPerDay   = 24
)

var (
	figmaUsageMu sync.Mutex
	figmaPlanKey sync.Map
)

type figmaDayUsage struct {
	Day     string `json:"day"`
	Reads   int    `json:"reads"`
	Creates int    `json:"creates"`
}

func (s *Service) ChooseStitchDesign(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "Choose a design first.")
		return
	}
	projectID := parseID(req["projectId"])
	name := firstNonEmpty(str(req["name"]), "Blink design")
	if projectID == 0 {
		writeErr(w, http.StatusBadRequest, "Save the project before choosing a design.")
		return
	}
	stored, ok := s.load(r.Context(), projectID, "figma")
	if !ok || strings.TrimSpace(stored.AccessToken) == "" {
		writeErr(w, http.StatusBadRequest, "Connect Figma on Integrations, then choose the design again.")
		return
	}
	token, err := s.figmaAccessToken(r, &stored, false)
	if err != nil || token == "" {
		writeErr(w, http.StatusBadRequest, "Reconnect Figma on Integrations, then choose the design again.")
		return
	}
	if strings.HasPrefix(token, "figd_") {
		writeErr(w, http.StatusBadRequest, "A personal Figma token cannot create a file. Reconnect Figma with OAuth.")
		return
	}
	if existing, found := s.loadFigmaDesign(r, projectID, ""); found && existing.FileKey != "" && strings.EqualFold(existing.FileName, name) {
		req["fileKey"] = existing.FileKey
		req["fileUrl"] = existing.FileURL
		req["syncJira"] = true
		s.ingestLoaded(w, r, projectID, token, stored.Organization, req, false)
		return
	}
	if err := reserveFigmaCreate(); err != nil {
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	}
	fileKey, fileURL, err := s.createFigmaFile(r, token, name)
	if err != nil {
		refundFigmaCreate()
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	req["fileKey"] = fileKey
	req["fileUrl"] = fileURL
	req["syncJira"] = true
	s.ingestLoaded(w, r, projectID, token, stored.Organization, req, true)
}

func (s *Service) createFigmaFile(r *http.Request, token, name string) (string, string, error) {
	planKey, err := s.figmaPlanKey(r, token)
	if err != nil {
		return "", "", err
	}
	created, err := figmaMCPCall(r, token, "create_new_file", map[string]any{
		"planKey": planKey, "fileName": name, "editorType": "design",
	})
	if err != nil {
		return "", "", err
	}
	fileKey := firstNonEmpty(str(created["file_key"]), str(created["fileKey"]))
	fileURL := firstNonEmpty(str(created["file_url"]), str(created["fileUrl"]))
	if fileKey == "" {
		return "", "", fmt.Errorf("Figma did not return a file.")
	}
	if fileURL == "" {
		fileURL = "https://www.figma.com/design/" + fileKey
	}
	_, _ = figmaMCPCall(r, token, "use_figma", map[string]any{
		"fileKey": fileKey,
		"code":    figmaFrameCode(name),
		"description": "Place the chosen Blink screen",
	})
	return fileKey, fileURL, nil
}

func (s *Service) figmaPlanKey(r *http.Request, token string) (string, error) {
	if cached, ok := figmaPlanKey.Load(token); ok {
		return cached.(string), nil
	}
	who, err := figmaMCPCall(r, token, "whoami", map[string]any{})
	if err != nil {
		return "", err
	}
	plans, _ := who["plans"].([]any)
	var fallback string
	for _, raw := range plans {
		plan, _ := raw.(map[string]any)
		key := str(plan["key"])
		if key == "" {
			continue
		}
		if fallback == "" {
			fallback = key
		}
		seat := strings.ToLower(str(plan["seat"]))
		tier := strings.ToLower(str(plan["tier"]))
		if strings.Contains(seat, "full") || strings.Contains(tier, "pro") {
			figmaPlanKey.Store(token, key)
			return key, nil
		}
	}
	if fallback == "" {
		return "", fmt.Errorf("Reconnect Figma on Integrations so Blink can create a file on the Professional plan.")
	}
	figmaPlanKey.Store(token, fallback)
	return fallback, nil
}

func figmaFrameCode(name string) string {
	safe := strings.ReplaceAll(name, `\`, ``)
	safe = strings.ReplaceAll(safe, `"`, `'`)
	return `const frame = figma.createFrame(); frame.name = "` + safe + `"; frame.resize(1440, 900); figma.currentPage.appendChild(frame);`
}

func figmaMCPCall(r *http.Request, token, tool string, args map[string]any) (map[string]any, error) {
	session, err := figmaMCPSession(r, token)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": time.Now().UnixNano(), "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, figmaMCP, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2024-11-05")
	req.Header.Set("Authorization", "Bearer "+token)
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	res, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Could not reach Figma.")
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("Reconnect Figma on Integrations. The Professional connection needs permission to create a file.")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("Figma returned HTTP %d.", res.StatusCode)
	}
	payload := stitchRPCPayload(raw)
	var envelope struct {
		Error  *struct{ Message string `json:"message"` } `json:"error"`
		Result json.RawMessage                                         `json:"result"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return nil, fmt.Errorf("Figma returned an unreadable response.")
	}
	if envelope.Error != nil && envelope.Error.Message != "" {
		return nil, fmt.Errorf("%s", envelope.Error.Message)
	}
	var toolResult struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		Content           []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(envelope.Result, &toolResult) != nil {
		var plain map[string]any
		if json.Unmarshal(envelope.Result, &plain) == nil {
			return plain, nil
		}
		return map[string]any{}, nil
	}
	if toolResult.IsError {
		text := ""
		for _, part := range toolResult.Content {
			text += part.Text
		}
		return nil, fmt.Errorf("%s", firstNonEmpty(text, "Figma could not create the file."))
	}
	if len(toolResult.StructuredContent) > 0 {
		var structured map[string]any
		if json.Unmarshal(toolResult.StructuredContent, &structured) == nil {
			return structured, nil
		}
	}
	for _, part := range toolResult.Content {
		var parsed map[string]any
		if json.Unmarshal([]byte(part.Text), &parsed) == nil {
			return parsed, nil
		}
	}
	return map[string]any{}, nil
}

func figmaMCPSession(r *http.Request, token string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "blink", "version": "0.1.0"},
		},
	})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, figmaMCP, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Could not reach Figma.")
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("Reconnect Figma on Integrations. The Professional connection needs permission to create a file.")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("Figma returned HTTP %d.", res.StatusCode)
	}
	session := res.Header.Get("Mcp-Session-Id")
	if session == "" {
		return "", nil
	}
	note, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, figmaMCP, strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	note.Header.Set("Content-Type", "application/json")
	note.Header.Set("Accept", "application/json, text/event-stream")
	note.Header.Set("MCP-Protocol-Version", "2024-11-05")
	note.Header.Set("Mcp-Session-Id", session)
	note.Header.Set("Authorization", "Bearer "+token)
	ready, err := client.Do(note)
	if err == nil {
		ready.Body.Close()
	}
	return session, nil
}

func noteFigmaRead() error {
	figmaUsageMu.Lock()
	defer figmaUsageMu.Unlock()
	usage := loadFigmaUsage()
	if usage.Reads >= figmaDemoReadsPerDay {
		return fmt.Errorf("This demo has used today's %d Figma file reads. Wait until tomorrow so the Professional plan stays available.", figmaDemoReadsPerDay)
	}
	usage.Reads++
	saveFigmaUsage(usage)
	return nil
}

func reserveFigmaCreate() error {
	figmaUsageMu.Lock()
	defer figmaUsageMu.Unlock()
	usage := loadFigmaUsage()
	if usage.Creates >= figmaDemoCreatesPerDay {
		return fmt.Errorf("This demo has created today's %d Figma files. Wait until tomorrow so the Professional plan stays available.", figmaDemoCreatesPerDay)
	}
	usage.Creates++
	saveFigmaUsage(usage)
	return nil
}

func refundFigmaCreate() {
	figmaUsageMu.Lock()
	defer figmaUsageMu.Unlock()
	usage := loadFigmaUsage()
	if usage.Creates > 0 {
		usage.Creates--
		saveFigmaUsage(usage)
	}
}

func figmaUsageSnapshot() map[string]any {
	figmaUsageMu.Lock()
	defer figmaUsageMu.Unlock()
	usage := loadFigmaUsage()
	return map[string]any{
		"day": usage.Day, "fileReads": usage.Reads, "filesCreated": usage.Creates,
		"fileReadLimit": figmaDemoReadsPerDay, "fileCreateLimit": figmaDemoCreatesPerDay,
	}
}

func loadFigmaUsage() figmaDayUsage {
	today := time.Now().UTC().Format("2006-01-02")
	raw, err := os.ReadFile(figmaUsagePath())
	if err != nil {
		return figmaDayUsage{Day: today}
	}
	var usage figmaDayUsage
	if json.Unmarshal(raw, &usage) != nil || usage.Day != today {
		return figmaDayUsage{Day: today}
	}
	return usage
}

func saveFigmaUsage(usage figmaDayUsage) {
	raw, _ := json.Marshal(usage)
	_ = os.WriteFile(figmaUsagePath(), raw, 0o600)
}

func figmaUsagePath() string {
	return filepath.Join(".", ".blink-figma-usage.json")
}
