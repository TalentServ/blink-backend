package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nisha-ts-40599/blink-backend/internal/githubgit"
	"github.com/nisha-ts-40599/blink-backend/internal/prapp"
)

const (
	implementAllBranch = "blink/implement-all"
	prAppFileLimit     = 400_000
	prAppFileCap       = 80
)

func (s *Server) previewPullRequestApp(w http.ResponseWriter, r *http.Request) {
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
		PullRequests []string `json:"pullRequests"`
	}
	_ = readJSON(r, &body)
	token, _, ok := s.integ.GitHubCreds(r.Context(), id)
	if !ok {
		token = strings.TrimSpace(os.Getenv("GITHUB_PREVIEW_TOKEN"))
		ok = token != ""
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "unavailable",
			"message": "Connect GitHub to open the application from the pull request.",
		})
		return
	}

	repos := previewRepos(body.PullRequests)
	if len(repos) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "unavailable",
			"message": "Run Implement first. Preview opens the application from the draft pull request.",
		})
		return
	}

	gh := githubgit.New(token)
	loaded := make([]loadedBranch, 0, len(repos))
	var lastError string
	for _, repo := range repos {
		item, err := s.loadImplementAll(r.Context(), gh, repo)
		if err != nil {
			lastError = err.Error()
			continue
		}
		loaded = append(loaded, item)
	}
	sort.SliceStable(loaded, func(i, j int) bool {
		return loaded[i].score > loaded[j].score
	})
	var page map[string]any
	for _, item := range loaded {
		got, err := s.materializePreview(r.Context(), item)
		if err != nil {
			lastError = err.Error()
			continue
		}
		page = got
		break
	}
	if page == nil {
		message := "blink/implement-all has no screen to open."
		if lastError != "" {
			message = "Could not open blink/implement-all. " + lastError
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "unavailable",
			"message": message,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ready",
		"message":     "This is the draft application. Nothing is merged.",
		"previewHtml": page["previewHtml"],
		"entryPath":   page["entryPath"],
		"pullRequest": page["pullRequest"],
		"pages":       []map[string]any{page},
	})
}

type loadedBranch struct {
	repo   prapp.PullRef
	files  map[string]string
	screen prapp.Screen
	score  int
}

func previewRepos(urls []string) []prapp.PullRef {
	seen := map[string]bool{}
	out := make([]prapp.PullRef, 0, 2)
	for _, raw := range urls {
		ref, ok := prapp.ParsePullURL(raw)
		if !ok {
			continue
		}
		key := strings.ToLower(ref.Owner + "/" + ref.Repo)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
		if len(out) == 4 {
			break
		}
	}
	return out
}

func (s *Server) loadImplementAll(ctx context.Context, gh *githubgit.Client, repo prapp.PullRef) (loadedBranch, error) {
	sha, err := gh.BranchSHA(ctx, repo.Owner, repo.Repo, implementAllBranch)
	if err != nil {
		return loadedBranch{}, err
	}
	paths, err := gh.TreePaths(ctx, repo.Owner, repo.Repo, sha)
	if err != nil {
		return loadedBranch{}, err
	}
	files := map[string]string{}
	for _, repoPath := range paths {
		if !previewSource(repoPath) || len(files) >= prAppFileCap {
			continue
		}
		got, ferr := gh.FileBytes(ctx, repo.Owner, repo.Repo, sha, repoPath)
		if ferr != nil || len(got) == 0 || len(got) > prAppFileLimit {
			continue
		}
		files[repoPath] = string(got)
	}
	screen, ok := prapp.ChooseScreen(files)
	if !ok {
		return loadedBranch{}, fmt.Errorf("%s/%s has no screen", repo.Owner, repo.Repo)
	}
	return loadedBranch{
		repo:   repo,
		files:  files,
		screen: screen,
		score:  prapp.Rank(repo.Repo, files[screen.File]),
	}, nil
}

func (s *Server) materializePreview(ctx context.Context, item loadedBranch) (map[string]any, error) {
	dir, err := os.MkdirTemp("", "blink-preview-src-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for repoPath, text := range item.files {
		target := filepath.Join(dir, filepath.FromSlash(repoPath))
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dir)) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, []byte(text), 0o644); err != nil {
			return nil, err
		}
	}
	screen := item.screen
	html, err := bundlePreview(ctx, dir, screen)
	if err != nil {
		return nil, err
	}
	title := screen.Name
	if heading := prapp.Heading(item.files[screen.File]); heading != "" {
		title = heading
	}
	return map[string]any{
		"title":       title,
		"previewHtml": html,
		"entryPath":   screen.File,
		"pullRequest": fmt.Sprintf("https://github.com/%s/%s/tree/%s", item.repo.Owner, item.repo.Repo, implementAllBranch),
	}, nil
}

func previewSource(repoPath string) bool {
	lower := strings.ToLower(strings.TrimSpace(repoPath))
	if lower == "" || strings.Contains(lower, "..") || strings.Contains(lower, "node_modules/") {
		return false
	}
	if strings.Contains(lower, ".test.") || strings.Contains(lower, ".spec.") {
		return false
	}
	switch {
	case strings.HasSuffix(lower, ".tsx"),
		strings.HasSuffix(lower, ".ts"),
		strings.HasSuffix(lower, ".jsx"),
		strings.HasSuffix(lower, ".js"),
		strings.HasSuffix(lower, ".css"),
		strings.HasSuffix(lower, ".html"),
		strings.HasSuffix(lower, ".svg"):
		return true
	default:
		return false
	}
}

func bundlePreview(ctx context.Context, dir string, screen prapp.Screen) (string, error) {
	root := strings.TrimSpace(os.Getenv("BLINK_PREVIEW_BUNDLER"))
	if root == "" {
		root = "preview-bundler"
	}
	script := filepath.Join(root, "bundle.mjs")
	node := "node"
	if bundled := strings.TrimSpace(os.Getenv("BLINK_PREVIEW_NODE")); bundled != "" {
		node = bundled
	}
	runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, node, script, dir, screen.File, screen.Kind, screen.Name)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if len(detail) > 300 {
			detail = detail[len(detail)-300:]
		}
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("preview build failed: %s", detail)
	}
	html := strings.TrimSpace(string(out))
	if !strings.Contains(strings.ToLower(html), "<html") && !strings.Contains(html, "<div id=\"root\">") {
		return "", fmt.Errorf("preview build returned no page")
	}
	return html, nil
}
