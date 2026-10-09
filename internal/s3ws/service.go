package s3ws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/go-chi/chi/v5"
	"github.com/nisha-ts-40599/blink-backend/internal/config"
	"github.com/nisha-ts-40599/blink-backend/internal/project"
	"github.com/nisha-ts-40599/blink-backend/internal/zipkit"
)

const (
	workspaceManifest = ".blink-workspace.json"
	kitComplete       = "automation_sdlc/.blink-kit-complete"
)

type jobState struct {
	mu     sync.Mutex
	status string
	copied int
	total  int
}

func (j *jobState) setStatus(status string) {
	j.mu.Lock()
	j.status = status
	j.mu.Unlock()
}

func (j *jobState) setTotal(n int) {
	j.mu.Lock()
	j.total = n
	j.mu.Unlock()
}

func (j *jobState) addCopied() {
	j.mu.Lock()
	j.copied++
	j.mu.Unlock()
}

func (j *jobState) snapshot() (status string, copied, total int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status, j.copied, j.total
}

type Service struct {
	cfg    config.Config
	client *s3.Client
	once   sync.Once
	jobs   sync.Map // folder -> *jobState
}

func New(cfg config.Config) *Service {
	return &Service{cfg: cfg}
}

func (s *Service) Enabled() bool {
	return strings.TrimSpace(s.cfg.S3BucketName) != "" &&
		strings.TrimSpace(s.cfg.AWSAccessKeyID) != "" &&
		strings.TrimSpace(s.cfg.AWSSecretAccessKey) != ""
}

func (s *Service) enabled() bool { return s.Enabled() }

// FolderStatus returns the workspace status string for X-Blink-Folder-Status.
func (s *Service) FolderStatus(name string, id *int64) string {
	st := s.Progress(name, id)
	if v, ok := st["status"].(string); ok && v != "" {
		return v
	}
	return ""
}

func (s *Service) s3() *s3.Client {
	s.once.Do(func() {
		s.client = s3.New(s3.Options{
			Region: s.cfg.AWSRegion,
			Credentials: credentials.NewStaticCredentialsProvider(
				strings.TrimSpace(s.cfg.AWSAccessKeyID),
				strings.TrimSpace(s.cfg.AWSSecretAccessKey),
				"",
			),
		})
	})
	return s.client
}

func (s *Service) bucket() string { return strings.TrimSpace(s.cfg.S3BucketName) }

func (s *Service) publicURL(folder string) string {
	base := strings.TrimSpace(s.cfg.S3PublicBaseURL)
	if base == "" {
		return folder + "/"
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + folder + "/"
}

// ProvisionAsync copies the automation_sdlc kit into the project S3 prefix.
func (s *Service) ProvisionAsync(ctx context.Context, projectName string, id *int64) {
	if !s.enabled() || strings.TrimSpace(projectName) == "" {
		return
	}
	folder := project.Folder(projectName, id)
	job := &jobState{status: "preparing"}
	if existing, loaded := s.jobs.LoadOrStore(folder, job); loaded {
		if st, ok := existing.(*jobState); ok {
			status, _, _ := st.snapshot()
			if status == "preparing" || status == "ready" {
				return
			}
			job = &jobState{status: "preparing"}
			s.jobs.Store(folder, job)
		}
	}
	go s.runProvision(projectName, id, folder, job)
}

func (s *Service) runProvision(projectName string, id *int64, folder string, job *jobState) {
	key := folder + "/" + workspaceManifest
	body, _ := json.Marshal(map[string]string{
		"projectName": projectName,
		"workspace":   folder,
	})
	_, err := s.s3().PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket()),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		log.Printf("s3 provision manifest failed folder=%s: %v", folder, err)
		job.setStatus("failed")
		return
	}

	if s.hasObject(context.Background(), folder+"/"+kitComplete) {
		job.setTotal(1)
		job.addCopied()
		job.setStatus("ready")
		return
	}

	kit := zipkit.Resolve(s.cfg.AutomationSDLCPath)
	if kit == "" {
		kit, _ = zipkit.EnsureFromLambda(
			context.Background(),
			s.cfg.AutomationSDLCPath,
			s.cfg.AgentLambdaName,
			s.cfg.AWSRegion,
			s.cfg.AWSAccessKeyID,
			s.cfg.AWSSecretAccessKey,
		)
	}
	sdlcFiles, err := zipkit.ListAutomationSdlcFiles(kit)
	if err != nil {
		log.Printf("s3 provision kit list failed folder=%s: %v", folder, err)
		job.setStatus("failed")
		return
	}
	cmdFiles, err := zipkit.ListCursorCommandFiles(kit)
	if err != nil {
		log.Printf("s3 provision commands list failed folder=%s: %v", folder, err)
		job.setStatus("failed")
		return
	}
	if len(sdlcFiles) == 0 && len(cmdFiles) == 0 {
		log.Printf("s3 provision kit empty folder=%s kit=%s", folder, kit)
		job.setStatus("failed")
		return
	}
	job.setTotal(len(sdlcFiles) + len(cmdFiles) + 1)
	ctx := context.Background()
	for _, rel := range sdlcFiles {
		if err := s.putKitFile(ctx, kit, folder, rel); err != nil {
			log.Printf("s3 provision put failed folder=%s file=%s: %v", folder, rel, err)
			job.setStatus("failed")
			return
		}
		job.addCopied()
	}
	for _, rel := range cmdFiles {
		if err := s.putKitFile(ctx, kit, folder, rel); err != nil {
			log.Printf("s3 provision command put failed folder=%s file=%s: %v", folder, rel, err)
			job.setStatus("failed")
			return
		}
		job.addCopied()
	}
	_, err = s.s3().PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket()),
		Key:         aws.String(folder + "/" + kitComplete),
		Body:        bytes.NewReader([]byte("ok")),
		ContentType: aws.String("text/plain"),
	})
	if err != nil {
		log.Printf("s3 provision complete marker failed folder=%s: %v", folder, err)
		job.setStatus("failed")
		return
	}
	job.addCopied()
	job.setStatus("ready")
}

// Progress is the UI contract: status, filesCopied, filesTotal, percent, exists.
func (s *Service) Progress(name string, id *int64) map[string]any {
	folder := project.Folder(name, id)
	out := map[string]any{
		"workspaceKey":    folder,
		"workspaceUrl":    s.publicURL(folder),
		"status":          nil,
		"workspaceStatus": nil,
		"filesCopied":     0,
		"filesTotal":      0,
		"percent":         0,
		"exists":          false,
	}
	if !s.enabled() {
		return out
	}
	if v, ok := s.jobs.Load(folder); ok {
		if st, ok := v.(*jobState); ok {
			status, copied, total := st.snapshot()
			percent := 0
			if status == "ready" {
				percent = 100
			} else if total > 0 {
				percent = copied * 100 / total
				if percent > 99 {
					percent = 99
				}
			}
			out["status"] = status
			out["workspaceStatus"] = status
			out["filesCopied"] = copied
			out["filesTotal"] = total
			out["percent"] = percent
			out["exists"] = status == "ready"
			return out
		}
	}
	if s.hasObject(context.Background(), folder+"/"+kitComplete) {
		out["status"] = "ready"
		out["workspaceStatus"] = "ready"
		out["filesCopied"] = 1
		out["filesTotal"] = 1
		out["percent"] = 100
		out["exists"] = true
	}
	return out
}

// Status returns workspaceKey, workspaceUrl, and workspaceStatus (ready/preparing/failed).
func (s *Service) Status(name string, id *int64) map[string]any {
	return s.Progress(name, id)
}

func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	if !s.enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "bucket": nil, "workspaces": []any{}})
		return
	}
	ctx := r.Context()
	var folders []string
	var token *string
	for {
		out, err := s.s3().ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket()),
			Delimiter:         aws.String("/"),
			ContinuationToken: token,
		})
		if err != nil {
			writeErr(w, http.StatusBadGateway, "Could not list Blink S3 workspaces.")
			return
		}
		for _, p := range out.CommonPrefixes {
			prefix := aws.ToString(p.Prefix)
			if !strings.HasSuffix(prefix, "/") {
				continue
			}
			folder := strings.TrimSuffix(prefix, "/")
			if project.IsBlinkWorkspaceFolder(folder) {
				folders = append(folders, folder)
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		token = out.NextContinuationToken
	}

	workspaces := make([]map[string]any, 0)
	for _, folder := range folders {
		if !s.isBlinkOwned(ctx, folder) {
			continue
		}
		kit := s.hasObject(ctx, folder+"/"+kitComplete)
		workspaces = append(workspaces, map[string]any{
			"folder":      folder,
			"projectId":   parseProjectID(folder),
			"url":         s.publicURL(folder),
			"objectCount": 0,
			"totalBytes":  0,
			"kitComplete": kit,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    true,
		"bucket":     s.bucket(),
		"workspaces": workspaces,
	})
}

func (s *Service) DeleteOne(w http.ResponseWriter, r *http.Request) {
	if !s.enabled() {
		writeErr(w, http.StatusServiceUnavailable, "S3 workspace storage is not configured.")
		return
	}
	folder := strings.Trim(chi.URLParam(r, "folder"), "/")
	if !project.IsBlinkWorkspaceFolder(folder) {
		writeErr(w, http.StatusBadRequest, "Not a Blink workspace folder.")
		return
	}
	if !s.isBlinkOwned(r.Context(), folder) {
		writeErr(w, http.StatusBadRequest, "Folder is not a Blink-owned workspace (missing .blink-workspace.json / kit marker).")
		return
	}
	n, err := s.deletePrefix(r.Context(), folder+"/")
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Could not delete workspace.")
		return
	}
	s.jobs.Delete(folder)
	writeJSON(w, http.StatusOK, map[string]any{
		"deletedFolders": 1,
		"deletedObjects": n,
		"folders":        []string{folder},
	})
}

func (s *Service) DeleteAll(w http.ResponseWriter, r *http.Request) {
	if !s.enabled() {
		writeErr(w, http.StatusServiceUnavailable, "S3 workspace storage is not configured.")
		return
	}
	// Reuse List logic by collecting owned folders.
	ctx := r.Context()
	var folders []string
	var token *string
	for {
		out, err := s.s3().ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket()),
			Delimiter:         aws.String("/"),
			ContinuationToken: token,
		})
		if err != nil {
			writeErr(w, http.StatusBadGateway, "Could not list Blink S3 workspaces.")
			return
		}
		for _, p := range out.CommonPrefixes {
			prefix := aws.ToString(p.Prefix)
			folder := strings.TrimSuffix(prefix, "/")
			if project.IsBlinkWorkspaceFolder(folder) && s.isBlinkOwned(ctx, folder) {
				folders = append(folders, folder)
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		token = out.NextContinuationToken
	}

	var objects int64
	deleted := make([]string, 0, len(folders))
	for _, folder := range folders {
		if !s.isBlinkOwned(ctx, folder) {
			continue
		}
		n, err := s.deletePrefix(ctx, folder+"/")
		if err != nil {
			continue
		}
		objects += n
		deleted = append(deleted, folder)
		s.jobs.Delete(folder)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deletedFolders": len(deleted),
		"deletedObjects": objects,
		"folders":        deleted,
	})
}

func (s *Service) isBlinkOwned(ctx context.Context, folder string) bool {
	return s.hasObject(ctx, folder+"/"+workspaceManifest) || s.hasObject(ctx, folder+"/"+kitComplete)
}

func (s *Service) hasObject(ctx context.Context, key string) bool {
	_, err := s.s3().HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket()),
		Key:    aws.String(key),
	})
	if err == nil {
		return true
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return false
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		code := api.ErrorCode()
		if code == "NotFound" || code == "NoSuchKey" || code == "Forbidden" || code == "AccessDenied" {
			return false
		}
	}
	var httpErr interface{ HTTPStatusCode() int }
	if errors.As(err, &httpErr) {
		sc := httpErr.HTTPStatusCode()
		if sc == 403 || sc == 404 {
			return false
		}
	}
	return false
}

func (s *Service) deletePrefix(ctx context.Context, prefix string) (int64, error) {
	var deleted int64
	var token *string
	for {
		out, err := s.s3().ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket()),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return deleted, err
		}
		var objs []types.ObjectIdentifier
		for _, o := range out.Contents {
			if o.Key == nil || strings.TrimSpace(*o.Key) == "" {
				continue
			}
			objs = append(objs, types.ObjectIdentifier{Key: o.Key})
			if len(objs) == 1000 {
				if err := s.deleteBatch(ctx, objs); err != nil {
					return deleted, err
				}
				deleted += int64(len(objs))
				objs = objs[:0]
			}
		}
		if len(objs) > 0 {
			if err := s.deleteBatch(ctx, objs); err != nil {
				return deleted, err
			}
			deleted += int64(len(objs))
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		token = out.NextContinuationToken
	}
	return deleted, nil
}

func (s *Service) deleteBatch(ctx context.Context, objs []types.ObjectIdentifier) error {
	_, err := s.s3().DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(s.bucket()),
		Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(true)},
	})
	return err
}

// OverlayFile is a relative workspace path + text content from the agent runtime.
type OverlayFile struct {
	Path    string
	Content string
}

func contentTypeFor(rel string) string {
	lower := strings.ToLower(rel)
	switch {
	case strings.HasSuffix(lower, ".json"):
		return "application/json"
	case strings.HasSuffix(lower, ".yaml"), strings.HasSuffix(lower, ".yml"):
		return "application/yaml"
	case strings.HasSuffix(lower, ".md"):
		return "text/markdown; charset=utf-8"
	case strings.HasSuffix(lower, ".py"):
		return "text/x-python; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func (s *Service) putBytes(ctx context.Context, key string, body []byte, contentType string) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err := s.s3().PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket()),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	})
	return err
}

func (s *Service) putKitFile(ctx context.Context, kit, folder, rel string) error {
	abs := filepath.Join(kit, filepath.FromSlash(rel))
	raw, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	key := workspaceObjectKey(folder, rel)
	return s.putBytes(ctx, key, raw, contentTypeFor(rel))
}

// PutOverlayFiles writes agent overlay files under the project workspace prefix.
// Safe no-op when S3 is not configured. Skips empty/unsafe paths.
func (s *Service) PutOverlayFiles(ctx context.Context, projectName string, id *int64, files []OverlayFile) (int, error) {
	if !s.enabled() || strings.TrimSpace(projectName) == "" || len(files) == 0 {
		return 0, nil
	}
	folder := project.Folder(projectName, id)
	written := 0
	for _, f := range files {
		rel := strings.TrimSpace(strings.ReplaceAll(f.Path, "\\", "/"))
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" || strings.Contains(rel, "..") {
			continue
		}
		key := folder + "/" + rel
		if err := s.putBytes(ctx, key, []byte(f.Content), contentTypeFor(rel)); err != nil {
			return written, fmt.Errorf("put %s: %w", key, err)
		}
		written++
	}
	return written, nil
}

// PutFrameworkCommands copies kit .cursor/commands to the workspace .cursor/commands prefix.
func (s *Service) PutFrameworkCommands(ctx context.Context, projectName string, id *int64, kit string) error {
	if !s.enabled() || strings.TrimSpace(projectName) == "" || strings.TrimSpace(kit) == "" {
		return nil
	}
	files, err := zipkit.ListCursorCommandFiles(kit)
	if err != nil || len(files) == 0 {
		return err
	}
	folder := project.Folder(projectName, id)
	for _, rel := range files {
		if err := s.putKitFile(ctx, kit, folder, rel); err != nil {
			return err
		}
	}
	return nil
}

// PutFrameworkCommandsAsync uploads kit commands without blocking download.
func (s *Service) PutFrameworkCommandsAsync(projectName string, id *int64, kit string) {
	if !s.enabled() || strings.TrimSpace(projectName) == "" {
		return
	}
	go func() {
		_ = s.PutFrameworkCommands(context.Background(), projectName, id, kit)
	}()
}

// PutRequirement writes requirement.md at the workspace root (Java putRequirement).
func (s *Service) PutRequirement(ctx context.Context, projectName string, id *int64, markdown string) error {
	if !s.enabled() || strings.TrimSpace(projectName) == "" {
		return nil
	}
	body := strings.TrimSpace(markdown)
	if body == "" {
		body = "# requirement\n"
	}
	key := workspaceObjectKey(project.Folder(projectName, id), "requirement.md")
	return s.putBytes(ctx, key, []byte(body), "text/markdown; charset=utf-8")
}

// PutRequirementAsync writes requirement.md without blocking download.
func (s *Service) PutRequirementAsync(projectName string, id *int64, markdown string) {
	if !s.enabled() || strings.TrimSpace(projectName) == "" {
		return
	}
	go func() {
		_ = s.PutRequirement(context.Background(), projectName, id, markdown)
	}()
}

// PutCursorOverlay writes sanitized .cursor/ai-sdlc files to S3 (Java putCursorOverlay).
func (s *Service) PutCursorOverlay(ctx context.Context, projectName string, id *int64, files map[string]string) error {
	if !s.enabled() || strings.TrimSpace(projectName) == "" {
		return nil
	}
	folder := project.Folder(projectName, id)
	wrote := 0
	for path, content := range files {
		rel := zipkit.SanitizeOverlayPath(path)
		if rel == "" {
			continue
		}
		key := folder + "/" + rel
		if err := s.putBytes(ctx, key, []byte(content), contentTypeFor(rel)); err != nil {
			return fmt.Errorf("put %s: %w", key, err)
		}
		wrote++
	}
	if wrote == 0 {
		return s.putBytes(ctx, folder+"/.cursor/.keep", nil, "application/octet-stream")
	}
	return nil
}

// PutCursorOverlayAsync writes overlay files without blocking download.
func (s *Service) PutCursorOverlayAsync(projectName string, id *int64, files map[string]string) {
	if !s.enabled() || strings.TrimSpace(projectName) == "" || len(files) == 0 {
		return
	}
	go func() {
		_ = s.PutCursorOverlay(context.Background(), projectName, id, files)
	}()
}

// ListCursorOverlays reads existing .cursor/ai-sdlc files from S3 for zip merge.
func (s *Service) ListCursorOverlays(ctx context.Context, projectName string, id *int64) (map[string]string, error) {
	out := map[string]string{}
	if !s.enabled() || strings.TrimSpace(projectName) == "" {
		return out, nil
	}
	folder := project.Folder(projectName, id)
	prefix := folder + "/.cursor/ai-sdlc/"
	var token *string
	for {
		listed, err := s.s3().ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket()),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return out, err
		}
		for _, obj := range listed.Contents {
			key := aws.ToString(obj.Key)
			if key == "" || strings.HasSuffix(key, "/") {
				continue
			}
			rel := strings.TrimPrefix(key, folder+"/")
			if zipkit.SanitizeOverlayPath(rel) == "" {
				continue
			}
			got, err := s.s3().GetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(s.bucket()),
				Key:    aws.String(key),
			})
			if err != nil {
				continue
			}
			body, err := io.ReadAll(got.Body)
			_ = got.Body.Close()
			if err != nil {
				continue
			}
			out[rel] = string(body)
		}
		if !aws.ToBool(listed.IsTruncated) {
			break
		}
		token = listed.NextContinuationToken
	}
	return out, nil
}

// EnsureProvisioned kicks workspace provisioning if the kit copy is not finished.
func (s *Service) EnsureProvisioned(ctx context.Context, projectName string, id *int64) {
	if !s.enabled() || strings.TrimSpace(projectName) == "" {
		return
	}
	folder := project.Folder(projectName, id)
	if v, ok := s.jobs.Load(folder); ok {
		if st, ok := v.(*jobState); ok {
			status, _, _ := st.snapshot()
			if status == "preparing" || status == "ready" {
				return
			}
		}
	}
	if s.hasObject(context.Background(), folder+"/"+kitComplete) {
		return
	}
	s.ProvisionAsync(ctx, projectName, id)
}

func parseProjectID(folder string) any {
	if !project.IsBlinkWorkspaceFolder(folder) {
		return nil
	}
	base := strings.TrimSuffix(folder, "_workspace")
	i := strings.LastIndex(base, "_")
	if i < 0 || i == len(base)-1 {
		return nil
	}
	var id int64
	if _, err := fmt.Sscanf(base[i+1:], "%d", &id); err != nil || id <= 0 {
		return nil
	}
	return id
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}
