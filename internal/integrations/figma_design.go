package integrations

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var figmaFileKeyRe = regexp.MustCompile(`(?:file|design|proto)/([A-Za-z0-9]{10,})`)

type figmaScreen struct {
	NodeID       string `json:"nodeId"`
	Name         string `json:"name"`
	PageID       string `json:"pageId,omitempty"`
	PageName     string `json:"pageName,omitempty"`
	Type         string `json:"type,omitempty"`
	StoryID      string `json:"storyId,omitempty"`
	JiraKey      string `json:"jiraKey,omitempty"`
	Fingerprint  string `json:"fingerprint,omitempty"`
	ThumbnailURL string `json:"thumbnailUrl,omitempty"`
}

func (s *Service) FigmaFiles(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	stored, ok := s.load(r.Context(), parseID(req["projectId"]), "figma")
	token := firstNonEmpty(str(req["token"]), stored.AccessToken)
	projectID := firstNonEmpty(str(req["figmaProjectId"]), stored.ProjectKey)
	if !ok || token == "" || projectID == "" {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	status, body, err := s.do(r.Context(), http.MethodGet,
		"https://api.figma.com/v1/projects/"+projectID+"/files", figmaHeaders(token), nil)
	if err != nil || status != http.StatusOK {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	var root map[string]any
	_ = json.Unmarshal([]byte(body), &root)
	list, _ := root["files"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, raw := range list {
		item, _ := raw.(map[string]any)
		key := str(item["key"])
		if key == "" {
			continue
		}
		out = append(out, map[string]any{
			"key": key, "name": firstNonEmpty(str(item["name"]), key),
			"thumbnailUrl": str(item["thumbnail_url"]), "lastModified": str(item["last_modified"]),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) FigmaFrames(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	stored, _ := s.load(r.Context(), parseID(req["projectId"]), "figma")
	token := firstNonEmpty(str(req["token"]), stored.AccessToken)
	fileKey := parseFigmaFileKey(firstNonEmpty(str(req["fileKey"]), str(req["fileUrl"])))
	if token == "" || fileKey == "" {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	_, _, screens, err := s.readFigmaFile(r, token, fileKey)
	if err != nil {
		writeErr(w, statusFromFigmaErr(err), err.Error())
		return
	}
	out := make([]map[string]any, 0, len(screens))
	for _, screen := range screens {
		out = append(out, map[string]any{
			"nodeId": screen.NodeID, "name": screen.Name,
			"pageId": screen.PageID, "pageName": screen.PageName, "type": screen.Type,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) GetFigmaDesign(w http.ResponseWriter, r *http.Request) {
	projectID := parseID(r.URL.Query().Get("projectId"))
	if projectID <= 0 {
		writeErr(w, http.StatusBadRequest, "Project id is required.")
		return
	}
	row, ok := s.loadFigmaDesign(r, projectID, "")
	if !ok {
		writeJSON(w, http.StatusOK, emptyFigmaDesign(projectID))
		return
	}
	writeJSON(w, http.StatusOK, row.response(nil, nil))
}

func (s *Service) ClearFigmaDesign(w http.ResponseWriter, r *http.Request) {
	projectID := parseID(r.URL.Query().Get("projectId"))
	if projectID <= 0 {
		writeErr(w, http.StatusBadRequest, "Project id is required.")
		return
	}
	if s.pool != nil {
		_, _ = s.pool.Exec(r.Context(), `DELETE FROM figma_design_binding WHERE project_id = $1`, projectID)
	}
	writeJSON(w, http.StatusOK, emptyFigmaDesign(projectID))
}

func (s *Service) SaveFigmaDesign(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "Figma design binding is required.")
		return
	}
	projectID := parseID(req["projectId"])
	if projectID <= 0 {
		writeErr(w, http.StatusBadRequest, "Project id is required.")
		return
	}
	fileKey := parseFigmaFileKey(firstNonEmpty(str(req["fileKey"]), str(req["fileUrl"])))
	if fileKey == "" {
		writeErr(w, http.StatusBadRequest, "Select a Figma file or paste a file URL.")
		return
	}
	row := s.loadOrEmptyDesign(r, projectID, fileKey)
	row.FileKey = fileKey
	row.FileURL = firstNonEmpty(str(req["fileUrl"]), "https://www.figma.com/design/"+fileKey)
	row.FileName = firstNonEmpty(str(req["fileName"]), row.FileName, fileKey)
	if req["syncJira"] != nil {
		row.SyncJira = truthy(req["syncJira"])
	}
	if screens := screensFromRequest(req["screens"]); screens != nil {
		row.Screens = screens
	}
	row.WebhookStatus = firstNonEmpty(row.WebhookStatus, "manual-sync")
	if err := s.saveFigmaDesign(r, row); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save the Figma file.")
		return
	}
	writeJSON(w, http.StatusOK, row.response(nil, nil))
}

func (s *Service) IngestFigmaDesign(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "Figma ingest request is required.")
		return
	}
	projectID := parseID(req["projectId"])
	if projectID <= 0 {
		writeErr(w, http.StatusBadRequest, "Project id is required.")
		return
	}
	stored, ok := s.load(r.Context(), projectID, "figma")
	if !ok || strings.TrimSpace(stored.AccessToken) == "" {
		writeErr(w, http.StatusBadRequest, "Connect Figma before ingesting a design.")
		return
	}
	token, err := s.figmaAccessToken(r, &stored, false)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	s.ingestLoaded(w, r, projectID, token, stored.Organization, req, true)
}

func (s *Service) ingestLoaded(w http.ResponseWriter, r *http.Request, projectID int64, token, teamID string, req map[string]any, force bool) {
	fileKey := parseFigmaFileKey(firstNonEmpty(str(req["fileKey"]), str(req["fileUrl"])))
	existing := s.loadOrEmptyDesign(r, projectID, fileKey)
	if fileKey == "" {
		fileKey = existing.FileKey
		existing = s.loadOrEmptyDesign(r, projectID, fileKey)
	}
	if fileKey == "" {
		writeErr(w, http.StatusBadRequest, "Select a Figma file first.")
		return
	}
	name, version, screens, err := s.readFigmaFile(r, token, fileKey)
	if auth, ok := err.(figmaAuthErr); ok {
		stored, found := s.load(r.Context(), projectID, "figma")
		if found {
			if refreshed, rerr := s.figmaAccessToken(r, &stored, true); rerr == nil && refreshed != token {
				token = refreshed
				name, version, screens, err = s.readFigmaFile(r, token, fileKey)
			} else if rerr != nil && auth.expired {
				err = rerr
			}
		}
	}
	if err != nil {
		writeErr(w, statusFromFigmaErr(err), err.Error())
		return
	}
	screens = keepScreenLinks(existing.Screens, screens)
	screens = applyFigmaLinks(screens, req["stories"], req["jiraIssues"])
	changes := diffFigmaScreens(existing.Screens, screens)
	versionChanged := existing.FileVersion != "" && version != "" && existing.FileVersion != version
	if len(changes) == 0 && versionChanged {
		changes = markLinkedScreens(screens, "changed in Figma")
	}
	screens = s.fillFigmaThumbnails(r, token, fileKey, screens, false)
	row := existing
	row.ProjectID = projectID
	row.FileKey = fileKey
	row.FileName = firstNonEmpty(name, fileKey)
	row.FileURL = firstNonEmpty(str(req["fileUrl"]), row.FileURL, "https://www.figma.com/design/"+fileKey)
	if req["syncJira"] != nil {
		row.SyncJira = truthy(req["syncJira"])
	}
	row.FileVersion = version
	row.Screens = screens
	row.LastSyncedAt = time.Now().UTC().Format(time.RFC3339)
	unchanged := existing.FileVersion != "" && existing.FileVersion == version && !force
	var updates []any
	if unchanged {
		row.LastSyncSummary = "Figma version unchanged; Jira was not updated."
		changes = nil
	} else if row.SyncJira && len(changes) > 0 {
		updates = s.postFigmaComments(r, projectID, row, changes)
		row.LastSyncSummary = figmaSyncSummary(changes, updates)
	} else {
		row.LastSyncSummary = fmt.Sprintf("Design snapshot stored. %d screen%s.", len(screens), plural(len(screens)))
	}
	s.ensureFigmaWebhook(r, token, &row, teamID)
	if err := s.saveFigmaDesign(r, row); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save the Figma file.")
		return
	}
	writeJSON(w, http.StatusOK, row.response(changesToMaps(changes), updates))
}

func (s *Service) FigmaWebhook(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := readJSON(r, &payload); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "message": "Empty webhook."})
		return
	}
	event := firstNonEmpty(str(payload["event_type"]), str(payload["eventType"]), "FILE_UPDATE")
	passcode := firstNonEmpty(str(payload["passcode"]), str(payload["webhook_passcode"]))
	if passcode == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "message": "Missing webhook passcode."})
		return
	}
	if strings.EqualFold(event, "PING") || strings.EqualFold(event, "FILE_DELETE") {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "message": "Webhook " + event + " acknowledged."})
		return
	}
	projectID, fileKey := s.designByPasscode(r, passcode)
	if projectID == 0 || fileKey == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "message": "Unknown webhook passcode."})
		return
	}
	if row, ok := s.loadFigmaDesign(r, projectID, fileKey); ok {
		if synced, err := time.Parse(time.RFC3339, row.LastSyncedAt); err == nil && time.Since(synced) < 3*time.Minute {
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "message": "Figma update already applied."})
			return
		}
	}
	stored, ok := s.load(r.Context(), projectID, "figma")
	if !ok || strings.TrimSpace(stored.AccessToken) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "message": "Figma is not connected."})
		return
	}
	token, err := s.figmaAccessToken(r, &stored, false)
	if err != nil || token == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "message": "Figma is not connected."})
		return
	}
	req := map[string]any{"projectId": projectID, "fileKey": fileKey, "syncJira": true}
	s.ingestLoaded(w, r, projectID, token, stored.Organization, req, false)
}

func (s *Service) readFigmaFile(r *http.Request, token, fileKey string) (string, string, []figmaScreen, error) {
	if err := noteFigmaRead(); err != nil {
		return "", "", nil, err
	}
	rawURL := "https://api.figma.com/v1/files/" + fileKey + "?depth=5"
	headers := figmaHeaders(token)
	status, body, err := s.doLarge(r, http.MethodGet, rawURL, headers)
	if err != nil {
		return "", "", nil, fmt.Errorf("Could not reach Figma.")
	}
	if (status == http.StatusUnauthorized || status == http.StatusForbidden) && alternateFigmaHeaders(token) != nil {
		status, body, err = s.doLarge(r, http.MethodGet, rawURL, alternateFigmaHeaders(token))
		if err != nil {
			return "", "", nil, fmt.Errorf("Could not reach Figma.")
		}
	}
	if status == http.StatusTooManyRequests {
		return "", "", nil, figmaRateErr{}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", "", nil, figmaAuthErr{expired: figmaTokenRejected(body), detail: figmaErrText(body)}
	}
	if status == http.StatusNotFound {
		return "", "", nil, fmt.Errorf("That Figma file was not found. Bind the file again.")
	}
	if status < 200 || status >= 300 {
		return "", "", nil, fmt.Errorf("Could not refresh the Figma file.")
	}
	var root map[string]any
	if json.Unmarshal([]byte(body), &root) != nil {
		return "", "", nil, fmt.Errorf("Could not refresh the Figma file.")
	}
	name := firstNonEmpty(str(root["name"]), fileKey)
	version := figmaRevision(root)
	document, _ := root["document"].(map[string]any)
	pages, _ := document["children"].([]any)
	screens := make([]figmaScreen, 0)
	for _, pageRaw := range pages {
		page, _ := pageRaw.(map[string]any)
		children, _ := page["children"].([]any)
		for _, childRaw := range children {
			child, _ := childRaw.(map[string]any)
			kind := str(child["type"])
			if !isFigmaScreen(kind) {
				continue
			}
			nodeID := str(child["id"])
			if nodeID == "" {
				continue
			}
			screens = append(screens, figmaScreen{
				NodeID: nodeID, Name: firstNonEmpty(str(child["name"]), nodeID),
				PageID: str(page["id"]), PageName: firstNonEmpty(str(page["name"]), "Page"),
				Type: kind, Fingerprint: screenFingerprint(child),
			})
		}
	}
	return name, version, screens, nil
}

func (s *Service) fillFigmaThumbnails(r *http.Request, token, fileKey string, screens []figmaScreen, refresh bool) []figmaScreen {
	if len(screens) == 0 || strings.TrimSpace(token) == "" || fileKey == "" {
		return screens
	}
	needs := refresh
	if !needs {
		for _, screen := range screens {
			if strings.TrimSpace(screen.ThumbnailURL) == "" {
				needs = true
				break
			}
		}
	}
	if !needs {
		return screens
	}
	ids := make([]string, 0, 12)
	for _, screen := range screens {
		if screen.NodeID == "" || len(ids) >= 12 {
			continue
		}
		if !refresh && strings.TrimSpace(screen.ThumbnailURL) != "" {
			continue
		}
		ids = append(ids, screen.NodeID)
	}
	if len(ids) == 0 {
		return screens
	}
	escaped := make([]string, len(ids))
	for i, id := range ids {
		escaped[i] = url.QueryEscape(id)
	}
	rawURL := "https://api.figma.com/v1/images/" + url.PathEscape(fileKey) + "?ids=" + strings.Join(escaped, ",") + "&format=png&scale=1"
	status, body, err := s.do(r.Context(), http.MethodGet, rawURL, figmaHeaders(token), nil)
	if err != nil || status == http.StatusTooManyRequests || status < 200 || status >= 300 {
		return screens
	}
	var payload map[string]any
	if json.Unmarshal([]byte(body), &payload) != nil {
		return screens
	}
	images, _ := payload["images"].(map[string]any)
	if len(images) == 0 {
		return screens
	}
	for i := range screens {
		if imageURL := str(images[screens[i].NodeID]); imageURL != "" {
			screens[i].ThumbnailURL = imageURL
		}
	}
	return screens
}

func (s *Service) doLarge(r *http.Request, method, rawURL string, headers map[string]string) (int, string, error) {
	req, err := http.NewRequestWithContext(r.Context(), method, rawURL, nil)
	if err != nil {
		return 0, "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := s.http.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	return res.StatusCode, string(b), nil
}

type figmaDesignRow struct {
	ProjectID       int64
	FileKey         string
	FileName        string
	FileURL         string
	SyncJira        bool
	WebhookID       string
	WebhookPasscode string
	WebhookStatus   string
	FileVersion     string
	LastSyncedAt    string
	LastSyncSummary string
	Screens         []figmaScreen
}

func (row figmaDesignRow) response(changes, updates []any) map[string]any {
	screens := row.Screens
	if screens == nil {
		screens = []figmaScreen{}
	}
	if changes == nil {
		changes = []any{}
	}
	if updates == nil {
		updates = []any{}
	}
	return map[string]any{
		"bound": row.FileKey != "", "projectId": strconv.FormatInt(row.ProjectID, 10),
		"fileKey": row.FileKey, "fileName": row.FileName, "fileUrl": row.FileURL,
		"fileVersion": row.FileVersion, "syncJira": row.SyncJira,
		"webhookId": row.WebhookID, "webhookStatus": row.WebhookStatus,
		"lastSyncedAt": row.LastSyncedAt, "lastSyncSummary": row.LastSyncSummary,
		"markdown": "", "screens": screens, "changes": changes, "jiraUpdates": updates,
		"figmaUsage": figmaUsageSnapshot(),
	}
}

func emptyFigmaDesign(projectID int64) map[string]any {
	return figmaDesignRow{ProjectID: projectID, SyncJira: true}.response(nil, nil)
}

func (s *Service) loadOrEmptyDesign(r *http.Request, projectID int64, fileKey string) figmaDesignRow {
	if row, ok := s.loadFigmaDesign(r, projectID, fileKey); ok {
		return row
	}
	return figmaDesignRow{ProjectID: projectID, SyncJira: true}
}

func (s *Service) loadFigmaDesign(r *http.Request, projectID int64, fileKey string) (figmaDesignRow, bool) {
	row := figmaDesignRow{ProjectID: projectID, SyncJira: true}
	if s.pool == nil || projectID <= 0 {
		return row, false
	}
	var snapshot string
	var sync bool
	query := `SELECT file_key, COALESCE(file_name,''), COALESCE(file_url,''), sync_jira,
		COALESCE(webhook_id,''), COALESCE(webhook_passcode,''), COALESCE(webhook_status,''), COALESCE(file_version,''),
		COALESCE(last_synced_at,''), COALESCE(last_sync_summary,''), COALESCE(snapshot_json,'')
		FROM figma_design_binding WHERE project_id = $1`
	args := []any{projectID}
	if fileKey != "" {
		query += ` AND file_key = $2`
		args = append(args, fileKey)
	}
	query += ` ORDER BY updated_at DESC LIMIT 1`
	err := s.pool.QueryRow(r.Context(), query, args...).Scan(
		&row.FileKey, &row.FileName, &row.FileURL, &sync,
		&row.WebhookID, &row.WebhookPasscode, &row.WebhookStatus, &row.FileVersion,
		&row.LastSyncedAt, &row.LastSyncSummary, &snapshot,
	)
	if err != nil || row.FileKey == "" {
		return figmaDesignRow{ProjectID: projectID, SyncJira: true}, false
	}
	row.SyncJira = sync
	_ = json.Unmarshal([]byte(snapshot), &row.Screens)
	return row, true
}

func (s *Service) saveFigmaDesign(r *http.Request, row figmaDesignRow) error {
	if s.pool == nil {
		return fmt.Errorf("database unavailable")
	}
	raw, _ := json.Marshal(row.Screens)
	_, err := s.pool.Exec(r.Context(), `
		INSERT INTO figma_design_binding (
			project_id, file_key, file_name, file_url, sync_jira, webhook_id, webhook_passcode, webhook_status,
			file_version, last_synced_at, last_sync_summary, snapshot_json, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NOW())
		ON CONFLICT (project_id, file_key) DO UPDATE SET
			file_name = EXCLUDED.file_name,
			file_url = EXCLUDED.file_url,
			sync_jira = EXCLUDED.sync_jira,
			webhook_id = EXCLUDED.webhook_id,
			webhook_passcode = EXCLUDED.webhook_passcode,
			webhook_status = EXCLUDED.webhook_status,
			file_version = EXCLUDED.file_version,
			last_synced_at = EXCLUDED.last_synced_at,
			last_sync_summary = EXCLUDED.last_sync_summary,
			snapshot_json = EXCLUDED.snapshot_json,
			updated_at = NOW()`,
		row.ProjectID, row.FileKey, nullStr(clip(row.FileName, 255)), nullStr(clip(row.FileURL, 500)), row.SyncJira,
		nullStr(clip(row.WebhookID, 120)), nullStr(clip(row.WebhookPasscode, 120)), nullStr(clip(row.WebhookStatus, 120)), nullStr(clip(row.FileVersion, 240)),
		nullStr(clip(row.LastSyncedAt, 40)), nullStr(row.LastSyncSummary), string(raw),
	)
	return err
}

func parseFigmaFileKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if m := figmaFileKeyRe.FindStringSubmatch(raw); len(m) == 2 {
		return m[1]
	}
	if regexp.MustCompile(`^[A-Za-z0-9]{10,}$`).MatchString(raw) {
		return raw
	}
	return ""
}

func isFigmaScreen(kind string) bool {
	switch kind {
	case "FRAME", "COMPONENT", "COMPONENT_SET", "SECTION":
		return true
	default:
		return false
	}
}

func figmaRevision(root map[string]any) string {
	return strings.Trim(str(root["lastModified"])+"|"+str(root["version"]), "|")
}

func screenFingerprint(node map[string]any) string {
	var b strings.Builder
	writeFigmaFingerprint(&b, node, 0)
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

func writeFigmaFingerprint(b *strings.Builder, node map[string]any, depth int) {
	b.WriteString(str(node["name"]))
	b.WriteByte('|')
	b.WriteString(str(node["type"]))
	b.WriteByte('|')
	b.WriteString(str(node["characters"]))
	if box, ok := node["absoluteBoundingBox"].(map[string]any); ok {
		b.WriteByte('|')
		b.WriteString(str(box["width"]))
		b.WriteByte('|')
		b.WriteString(str(box["height"]))
	}
	if depth >= 6 {
		return
	}
	children, _ := node["children"].([]any)
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(len(children)))
	for _, childRaw := range children {
		child, _ := childRaw.(map[string]any)
		if child == nil {
			continue
		}
		b.WriteByte(';')
		writeFigmaFingerprint(b, child, depth+1)
	}
}

func screensFromRequest(v any) []figmaScreen {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]figmaScreen, 0, len(list))
	for _, raw := range list {
		item, _ := raw.(map[string]any)
		nodeID := str(item["nodeId"])
		if nodeID == "" {
			continue
		}
		out = append(out, figmaScreen{
			NodeID: nodeID, Name: firstNonEmpty(str(item["name"]), nodeID),
			PageID: str(item["pageId"]), PageName: str(item["pageName"]), Type: str(item["type"]),
			StoryID: str(item["storyId"]), JiraKey: str(item["jiraKey"]),
			Fingerprint: str(item["fingerprint"]), ThumbnailURL: str(item["thumbnailUrl"]),
		})
	}
	return out
}

func figmaHeaders(token string) map[string]string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(token), "figd_") {
		return map[string]string{"X-Figma-Token": token, "Accept": "application/json"}
	}
	return map[string]string{"Authorization": "Bearer " + token, "Accept": "application/json"}
}

func alternateFigmaHeaders(token string) map[string]string {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(token), "figd_") {
		return map[string]string{"Authorization": "Bearer " + token, "Accept": "application/json"}
	}
	return map[string]string{"X-Figma-Token": token, "Accept": "application/json"}
}

func figmaBasicAuth(clientID, clientSecret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(clientID+":"+clientSecret))
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true")
	default:
		return v != nil
	}
}

func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

type figmaRateErr struct{}

func (figmaRateErr) Error() string {
	return "Figma has used up file reads for this file. A Starter file only allows a few reads per month. Duplicate it into a Professional team where you have a Full or Dev seat, then sync once."
}

func statusFromFigmaErr(err error) int {
	if _, ok := err.(figmaRateErr); ok {
		return http.StatusTooManyRequests
	}
	if _, ok := err.(figmaAuthErr); ok {
		return http.StatusUnauthorized
	}
	return http.StatusBadGateway
}

type figmaAuthErr struct {
	expired bool
	detail  string
}

func (e figmaAuthErr) Error() string {
	if e.expired || e.detail == "" {
		return "Figma session expired. Reconnect Figma on Integrations, then sync again."
	}
	return "Figma could not open this file. " + e.detail
}

func figmaErrText(body string) string {
	return firstNonEmpty(jsonText(body, "err"), jsonText(body, "message"), jsonText(body, "error"))
}

func figmaTokenRejected(body string) bool {
	text := strings.ToLower(figmaErrText(body))
	if text == "" {
		return true
	}
	return strings.Contains(text, "token") || strings.Contains(text, "unauthorized") || strings.Contains(text, "invalid")
}

func (s *Service) figmaAccessToken(r *http.Request, stored *storedIntegration, force bool) (string, error) {
	if stored == nil {
		return "", fmt.Errorf("Connect Figma before ingesting a design.")
	}
	token := strings.TrimSpace(stored.AccessToken)
	needs := force || (stored.ExpiresAt != nil && time.Until(stored.ExpiresAt.UTC()) < 2*time.Minute)
	if !needs || strings.TrimSpace(stored.RefreshToken) == "" {
		if token == "" {
			return "", fmt.Errorf("Connect Figma before ingesting a design.")
		}
		return token, nil
	}
	clientID := strings.TrimSpace(s.cfg.FigmaClientID)
	clientSecret := strings.TrimSpace(s.cfg.FigmaClientSecret)
	if clientID == "" || clientSecret == "" {
		if token != "" && !force {
			return token, nil
		}
		return "", fmt.Errorf("Figma OAuth credentials are not configured on the server.")
	}
	form := url.Values{"refresh_token": {stored.RefreshToken}}
	status, body, err := s.do(r.Context(), http.MethodPost, "https://api.figma.com/v1/oauth/refresh",
		map[string]string{
			"Authorization": figmaBasicAuth(clientID, clientSecret),
			"Content-Type":  "application/x-www-form-urlencoded",
			"Accept":        "application/json",
		}, []byte(form.Encode()))
	if err != nil || status < 200 || status >= 300 {
		if token != "" && !force {
			return token, nil
		}
		return "", fmt.Errorf("Figma session expired. Reconnect Figma on Integrations, then sync again.")
	}
	access := jsonText(body, "access_token")
	if access == "" {
		if token != "" && !force {
			return token, nil
		}
		return "", fmt.Errorf("Figma session expired. Reconnect Figma on Integrations, then sync again.")
	}
	stored.AccessToken = access
	if rt := jsonText(body, "refresh_token"); rt != "" {
		stored.RefreshToken = rt
	}
	if n := jsonInt(body, "expires_in"); n > 0 {
		t := time.Now().UTC().Add(time.Duration(n) * time.Second)
		stored.ExpiresAt = &t
	}
	owner := strings.TrimSpace(r.Header.Get("X-Blink-Owner-Email"))
	_ = s.persistOwned(r.Context(), owner, *stored)
	return access, nil
}

type figmaChange struct {
	NodeID  string
	Name    string
	JiraKey string
	Detail  string
}

func keepScreenLinks(previous, current []figmaScreen) []figmaScreen {
	prior := map[string]figmaScreen{}
	for _, screen := range previous {
		prior[screen.NodeID] = screen
	}
	for i := range current {
		old, found := prior[current[i].NodeID]
		if !found {
			continue
		}
		current[i].StoryID = old.StoryID
		current[i].JiraKey = old.JiraKey
		if current[i].ThumbnailURL == "" {
			current[i].ThumbnailURL = old.ThumbnailURL
		}
	}
	return current
}

func applyFigmaLinks(screens []figmaScreen, storiesAny, issuesAny any) []figmaScreen {
	jiraByStory := map[string]string{}
	if issues, ok := issuesAny.([]any); ok {
		for _, raw := range issues {
			item, _ := raw.(map[string]any)
			id := str(item["sourceId"])
			key := str(item["jiraKey"])
			if id != "" && key != "" {
				jiraByStory[id] = key
			}
		}
	}
	stories, _ := storiesAny.([]any)
	used := map[string]bool{}
	for i := range screens {
		if screens[i].StoryID != "" {
			used[screens[i].StoryID] = true
		}
		if screens[i].JiraKey == "" && screens[i].StoryID != "" {
			screens[i].JiraKey = jiraByStory[screens[i].StoryID]
		}
	}
	var leftover []map[string]any
	for _, raw := range stories {
		item, _ := raw.(map[string]any)
		id := str(item["id"])
		if id != "" && !used[id] {
			leftover = append(leftover, item)
		}
	}
	for i := range screens {
		if screens[i].StoryID != "" || len(leftover) == 0 {
			continue
		}
		story := leftover[0]
		leftover = leftover[1:]
		screens[i].StoryID = str(story["id"])
		if screens[i].JiraKey == "" {
			screens[i].JiraKey = jiraByStory[screens[i].StoryID]
		}
	}
	return screens
}

func diffFigmaScreens(previous, current []figmaScreen) []figmaChange {
	before := map[string]figmaScreen{}
	for _, screen := range previous {
		before[screen.NodeID] = screen
	}
	after := map[string]bool{}
	var changes []figmaChange
	for _, screen := range current {
		after[screen.NodeID] = true
		prior, found := before[screen.NodeID]
		if !found {
			changes = append(changes, figmaChange{screen.NodeID, screen.Name, screen.JiraKey, "New screen \"" + screen.Name + "\""})
			continue
		}
		if prior.Name != screen.Name || (prior.Fingerprint != "" && screen.Fingerprint != "" && prior.Fingerprint != screen.Fingerprint) {
			key := firstNonEmpty(screen.JiraKey, prior.JiraKey)
			changes = append(changes, figmaChange{screen.NodeID, screen.Name, key, "Screen \"" + screen.Name + "\" changed in Figma"})
		}
	}
	for _, prior := range previous {
		if after[prior.NodeID] {
			continue
		}
		changes = append(changes, figmaChange{prior.NodeID, prior.Name, prior.JiraKey, "Screen \"" + prior.Name + "\" was removed"})
	}
	return changes
}

func markLinkedScreens(screens []figmaScreen, reason string) []figmaChange {
	var changes []figmaChange
	for _, screen := range screens {
		if screen.JiraKey == "" {
			continue
		}
		changes = append(changes, figmaChange{screen.NodeID, screen.Name, screen.JiraKey, "Screen \"" + screen.Name + "\" " + reason})
	}
	return changes
}

func figmaSyncSummary(changes []figmaChange, updates []any) string {
	if len(updates) == 0 {
		return fmt.Sprintf("%d screen change%s detected. Link a Jira ticket to post a comment.", len(changes), plural(len(changes)))
	}
	var posted []string
	var failed string
	for _, raw := range updates {
		item, _ := raw.(map[string]any)
		key := str(item["issueKey"])
		if str(item["status"]) == "failed" {
			if failed == "" {
				failed = firstNonEmpty(str(item["message"]), "Could not comment on "+key)
			}
			continue
		}
		if key != "" {
			posted = append(posted, key)
		}
	}
	if len(posted) > 0 {
		return "Posted a Figma update on " + strings.Join(posted, ", ") + "."
	}
	return firstNonEmpty(failed, fmt.Sprintf("%d screen change%s detected.", len(changes), plural(len(changes))))
}

func changesToMaps(changes []figmaChange) []any {
	out := make([]any, 0, len(changes))
	for _, change := range changes {
		out = append(out, map[string]any{
			"kind": "updated", "nodeId": change.NodeID, "name": change.Name,
			"jiraKey": change.JiraKey, "detail": change.Detail,
		})
	}
	return out
}

func (s *Service) postFigmaComments(r *http.Request, projectID int64, row figmaDesignRow, changes []figmaChange) []any {
	byIssue := map[string][]figmaChange{}
	var order []string
	for _, change := range changes {
		key := strings.TrimSpace(change.JiraKey)
		if key == "" {
			continue
		}
		if _, ok := byIssue[key]; !ok {
			order = append(order, key)
		}
		byIssue[key] = append(byIssue[key], change)
	}
	updates := make([]any, 0, len(order))
	for _, key := range order {
		text := figmaComment(row, byIssue[key])
		commentID, err := s.postJiraComment(r, projectID, key, text)
		if err != nil {
			updates = append(updates, map[string]any{"issueKey": key, "status": "failed", "message": err.Error()})
			continue
		}
		updates = append(updates, map[string]any{
			"issueKey": key, "status": "updated", "commentId": commentID,
			"message": "Posted design change on " + key,
		})
	}
	return updates
}

func figmaComment(row figmaDesignRow, changes []figmaChange) string {
	var b strings.Builder
	b.WriteString("[blink-design-sync]\n")
	b.WriteString("Figma design update for " + firstNonEmpty(row.FileName, row.FileKey) + "\n")
	b.WriteString(firstNonEmpty(row.FileURL, "https://www.figma.com/design/"+row.FileKey) + "\n")
	for _, change := range changes {
		b.WriteString("- " + change.Detail)
		if change.NodeID != "" {
			b.WriteString(" (" + change.NodeID + ")")
		}
		b.WriteByte('\n')
	}
	b.WriteString("Blink will keep this ticket in sync when the bound frame changes.")
	return b.String()
}

func (s *Service) postJiraComment(r *http.Request, projectID int64, issueKey, text string) (string, error) {
	ctxJ, err := s.resolveJiraOwned(r.Context(), projectID, map[string]any{}, r.Header.Get("X-Blink-Owner-Email"))
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]any{"body": adfDocument(text)})
	status, body, err := s.do(r.Context(), http.MethodPost,
		ctxJ.APIBase+"/rest/api/3/issue/"+issueKey+"/comment",
		withJSON(ctxJ.Headers), payload)
	if err != nil || status < 200 || status >= 300 {
		return "", fmt.Errorf("%s", firstNonEmpty(jiraAPIError(body, status), "Could not comment on "+issueKey))
	}
	return firstNonEmpty(jsonText(body, "id"), "posted"), nil
}

func (s *Service) ensureFigmaWebhook(r *http.Request, token string, row *figmaDesignRow, teamID string) {
	if row == nil || !row.SyncJira || row.WebhookID != "" {
		return
	}
	endpoint := figmaWebhookEndpoint(r)
	if endpoint == "" || strings.Contains(endpoint, "localhost") || strings.Contains(endpoint, "127.0.0.1") {
		row.WebhookStatus = "manual-sync (Figma cannot reach localhost)"
		return
	}
	if row.WebhookPasscode == "" {
		row.WebhookPasscode = strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	payload := map[string]any{
		"event_type": "FILE_UPDATE", "endpoint": endpoint, "passcode": row.WebhookPasscode,
		"description": "Blink design-to-Jira sync", "status": "ACTIVE",
		"context": "file", "context_id": row.FileKey,
	}
	if team := strings.TrimSpace(teamID); team != "" {
		payload["team_id"] = team
	}
	raw, _ := json.Marshal(payload)
	status, body, err := s.do(r.Context(), http.MethodPost, "https://api.figma.com/v2/webhooks", withJSON(figmaHeaders(token)), raw)
	if err != nil {
		row.WebhookStatus = "pending (" + err.Error() + ")"
		return
	}
	if status >= 200 && status < 300 {
		row.WebhookID = firstNonEmpty(jsonText(body, "id"), jsonText(body, "webhook_id"))
		row.WebhookStatus = "active"
		return
	}
	detail := strings.ToLower(firstNonEmpty(jsonText(body, "err"), jsonText(body, "message"), jsonText(body, "error"), body))
	if status == http.StatusForbidden && (strings.Contains(detail, "plan") || strings.Contains(detail, "starter") || strings.Contains(detail, "upgrade")) {
		row.WebhookStatus = "starter: automatic updates need a Professional team"
		return
	}
	if status == http.StatusForbidden && strings.Contains(detail, "scope") {
		row.WebhookStatus = "Reconnect Figma on Integrations so Blink can turn on automatic updates."
		return
	}
	row.WebhookStatus = fmt.Sprintf("pending (HTTP %d)", status)
}

func figmaWebhookEndpoint(r *http.Request) string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("BLINK_FIGMA_WEBHOOK_PUBLIC_BASE")), "/")
	if base == "" {
		base = strings.TrimRight(publicAPIBase(r), "/")
	}
	base = strings.TrimSuffix(base, "/api")
	if base == "" {
		return ""
	}
	return base + "/api/integrations/figma/webhooks"
}

func (s *Service) designByPasscode(r *http.Request, passcode string) (int64, string) {
	if s.pool == nil || passcode == "" {
		return 0, ""
	}
	var projectID int64
	var fileKey string
	err := s.pool.QueryRow(r.Context(),
		`SELECT project_id, file_key FROM figma_design_binding WHERE webhook_passcode = $1 ORDER BY updated_at DESC LIMIT 1`,
		passcode).Scan(&projectID, &fileKey)
	if err != nil {
		return 0, ""
	}
	return projectID, fileKey
}
