// Package githubgit commits overlay trees and opens draft PRs via the GitHub Git Data API.
package githubgit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	http    *http.Client
	token   string
	baseURL string
}

func New(token string) *Client {
	return &Client{
		http:    &http.Client{Timeout: 90 * time.Second},
		token:   strings.TrimSpace(token),
		baseURL: "https://api.github.com",
	}
}

type File struct {
	Path    string
	Content string
}

type CommitResult struct {
	SHA       string `json:"sha"`
	URL       string `json:"url"`
	Branch    string `json:"branch"`
	Owner     string `json:"owner"`
	Repo      string `json:"repo"`
	TreeCount int    `json:"treeCount"`
}

type DraftPRResult struct {
	URL    string `json:"url"`
	Number int    `json:"number"`
	Branch string `json:"branch"`
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	SHA    string `json:"sha"`
}

func ParseOwnerRepo(htmlURLOrSlug string) (owner, repo string, err error) {
	raw := strings.TrimSpace(htmlURLOrSlug)
	if raw == "" {
		return "", "", fmt.Errorf("empty repository reference")
	}
	if strings.Contains(raw, "://") {
		u, perr := url.Parse(raw)
		if perr != nil {
			return "", "", perr
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 {
			return "", "", fmt.Errorf("cannot parse owner/repo from %s", raw)
		}
		return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
	}
	parts := strings.Split(raw, "/")
	if len(parts) == 2 {
		return parts[0], parts[1], nil
	}
	return "", "", fmt.Errorf("expected owner/repo or html_url, got %q", raw)
}

func (c *Client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	if c.token == "" {
		return 0, nil, fmt.Errorf("GitHub token is empty")
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw, nil
}

func isEmptyGitHubRepo(status int, raw []byte) bool {
	if status == http.StatusNotFound {
		return true
	}
	if status != http.StatusConflict {
		return false
	}
	return strings.Contains(strings.ToLower(string(raw)), "git repository is empty")
}

func (c *Client) DefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	status, raw, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s", owner, repo), nil)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		return "", fmt.Errorf("GitHub repo lookup HTTP %d: %s", status, truncate(string(raw), 200))
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	branch, _ := m["default_branch"].(string)
	if strings.TrimSpace(branch) == "" {
		return "", fmt.Errorf("repository has no default_branch")
	}
	return branch, nil
}

// CurrentUserLogin resolves the owner for repositories created under the
// authenticated user's personal GitHub account.
func (c *Client) CurrentUserLogin(ctx context.Context) (string, error) {
	status, raw, err := c.do(ctx, http.MethodGet, "/user", nil)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		return "", fmt.Errorf("GitHub current-user lookup HTTP %d: %s", status, truncate(string(raw), 200))
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(raw, &user); err != nil {
		return "", fmt.Errorf("decode GitHub current user: %w", err)
	}
	if strings.TrimSpace(user.Login) == "" {
		return "", fmt.Errorf("GitHub current-user response has no login")
	}
	return strings.TrimSpace(user.Login), nil
}

func (c *Client) CommitFiles(ctx context.Context, owner, repo, branch, message string, files []File) (*CommitResult, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no files to commit")
	}
	if branch == "" {
		b, err := c.DefaultBranch(ctx, owner, repo)
		if err != nil {
			return nil, err
		}
		branch = b
	}
	refPath := fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", owner, repo, branch)
	status, raw, err := c.do(ctx, http.MethodGet, refPath, nil)
	if err != nil {
		return nil, err
	}
	if isEmptyGitHubRepo(status, raw) {
		first, rest := files[0], files[1:]
		init, err := c.commitViaContents(ctx, owner, repo, branch, message, first)
		if err != nil {
			return nil, err
		}
		if len(rest) == 0 {
			return init, nil
		}
		return c.CommitFiles(ctx, owner, repo, branch, message, rest)
	}
	baseSHA := ""
	if status == 200 {
		var ref map[string]any
		_ = json.Unmarshal(raw, &ref)
		if obj, ok := ref["object"].(map[string]any); ok {
			baseSHA, _ = obj["sha"].(string)
		}
	} else {
		return nil, fmt.Errorf("GitHub ref lookup HTTP %d: %s", status, truncate(string(raw), 200))
	}

	baseTreeSHA := ""
	if baseSHA != "" {
		status, raw, err = c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/commits/%s", owner, repo, baseSHA), nil)
		if err != nil {
			return nil, err
		}
		if status >= 400 {
			return nil, fmt.Errorf("GitHub commit lookup HTTP %d: %s", status, truncate(string(raw), 200))
		}
		var commit map[string]any
		_ = json.Unmarshal(raw, &commit)
		if tree, ok := commit["tree"].(map[string]any); ok {
			baseTreeSHA, _ = tree["sha"].(string)
		}
	}

	treeEntries := make([]map[string]any, 0, len(files))
	for _, f := range files {
		path := strings.TrimSpace(strings.TrimPrefix(f.Path, "/"))
		if path == "" || strings.Contains(path, "..") {
			return nil, fmt.Errorf("invalid path %q", f.Path)
		}
		status, raw, err = c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/blobs", owner, repo), map[string]any{
			"content":  base64.StdEncoding.EncodeToString([]byte(f.Content)),
			"encoding": "base64",
		})
		if err != nil {
			return nil, err
		}
		if status >= 400 {
			return nil, fmt.Errorf("GitHub blob create HTTP %d for %s: %s", status, path, truncate(string(raw), 200))
		}
		var blob map[string]any
		_ = json.Unmarshal(raw, &blob)
		sha, _ := blob["sha"].(string)
		if sha == "" {
			return nil, fmt.Errorf("blob create returned empty sha for %s", path)
		}
		treeEntries = append(treeEntries, map[string]any{
			"path": path,
			"mode": "100644",
			"type": "blob",
			"sha":  sha,
		})
	}

	treeBody := map[string]any{"tree": treeEntries}
	if baseTreeSHA != "" {
		treeBody["base_tree"] = baseTreeSHA
	}
	status, raw, err = c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/trees", owner, repo), treeBody)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("GitHub tree create HTTP %d: %s", status, truncate(string(raw), 200))
	}
	var tree map[string]any
	_ = json.Unmarshal(raw, &tree)
	treeSHA, _ := tree["sha"].(string)
	if treeSHA == "" {
		return nil, fmt.Errorf("tree create returned empty sha")
	}

	commitBody := map[string]any{
		"message": message,
		"tree":    treeSHA,
	}
	if baseSHA != "" {
		commitBody["parents"] = []string{baseSHA}
	}
	status, raw, err = c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/commits", owner, repo), commitBody)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("GitHub commit create HTTP %d: %s", status, truncate(string(raw), 200))
	}
	var commit map[string]any
	_ = json.Unmarshal(raw, &commit)
	commitSHA, _ := commit["sha"].(string)
	if commitSHA == "" {
		return nil, fmt.Errorf("commit create returned empty sha")
	}
	htmlURL, _ := commit["html_url"].(string)

	if baseSHA == "" {
		status, raw, err = c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo), map[string]any{
			"ref": "refs/heads/" + branch,
			"sha": commitSHA,
		})
	} else {
		status, raw, err = c.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, repo, branch), map[string]any{
			"sha":   commitSHA,
			"force": false,
		})
	}
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("GitHub ref update HTTP %d: %s", status, truncate(string(raw), 240))
	}

	return &CommitResult{
		SHA:       commitSHA,
		URL:       htmlURL,
		Branch:    branch,
		Owner:     owner,
		Repo:      repo,
		TreeCount: len(files),
	}, nil
}

func (c *Client) commitViaContents(ctx context.Context, owner, repo, branch, message string, file File) (*CommitResult, error) {
	path := strings.TrimSpace(strings.TrimPrefix(file.Path, "/"))
	if path == "" || strings.Contains(path, "..") {
		return nil, fmt.Errorf("invalid path %q", file.Path)
	}
	body := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString([]byte(file.Content)),
	}
	if strings.TrimSpace(branch) != "" {
		body["branch"] = branch
	}
	status, raw, err := c.do(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/%s/contents/%s", owner, repo, path), body)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("GitHub contents init HTTP %d: %s", status, truncate(string(raw), 240))
	}
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	sha := ""
	htmlURL := ""
	if commit, ok := payload["commit"].(map[string]any); ok {
		sha, _ = commit["sha"].(string)
		htmlURL, _ = commit["html_url"].(string)
	}
	if sha == "" {
		return nil, fmt.Errorf("contents init returned empty commit sha")
	}
	if branch == "" {
		branch = "main"
	}
	return &CommitResult{
		SHA:       sha,
		URL:       htmlURL,
		Branch:    branch,
		Owner:     owner,
		Repo:      repo,
		TreeCount: 1,
	}, nil
}

func (c *Client) CommitBranchAndDraftPR(ctx context.Context, owner, repo, branch, baseBranch, title, body, message string, files []File) (*DraftPRResult, error) {
	if baseBranch == "" {
		b, err := c.DefaultBranch(ctx, owner, repo)
		if err != nil {
			return nil, err
		}
		baseBranch = b
	}
	// Create branch from default tip first.
	status, raw, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", owner, repo, baseBranch), nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("base branch lookup HTTP %d: %s", status, truncate(string(raw), 200))
	}
	var ref map[string]any
	_ = json.Unmarshal(raw, &ref)
	baseSHA := ""
	if obj, ok := ref["object"].(map[string]any); ok {
		baseSHA, _ = obj["sha"].(string)
	}
	if baseSHA == "" {
		return nil, fmt.Errorf("base branch has empty sha")
	}
	if err := c.closeOpenPullsForHead(ctx, owner, repo, branch); err != nil {
		return nil, err
	}

	status, raw, err = c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo), map[string]any{
		"ref": "refs/heads/" + branch,
		"sha": baseSHA,
	})
	if err != nil {
		return nil, err
	}
	if status == 422 {
		// Branch already exists from an earlier implement. Point it at the current default tip
		// so this run replaces that implementation instead of stacking on it.
		status, raw, err = c.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, repo, branch), map[string]any{
			"sha":   baseSHA,
			"force": true,
		})
		if err != nil {
			return nil, err
		}
		if status >= 400 {
			return nil, fmt.Errorf("reset implement branch HTTP %d: %s", status, truncate(string(raw), 200))
		}
	} else if status >= 400 {
		return nil, fmt.Errorf("create branch HTTP %d: %s", status, truncate(string(raw), 200))
	}

	commit, err := c.CommitFiles(ctx, owner, repo, branch, message, files)
	if err != nil {
		return nil, err
	}

	status, raw, err = c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls", owner, repo), map[string]any{
		"title": title,
		"head":  branch,
		"base":  baseBranch,
		"body":  body,
		"draft": true,
	})
	if err != nil {
		return nil, err
	}
	if status == 422 && strings.Contains(strings.ToLower(string(raw)), "pull request already exists") {
		if cerr := c.closeOpenPullsForHead(ctx, owner, repo, branch); cerr != nil {
			return nil, cerr
		}
		status, raw, err = c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls", owner, repo), map[string]any{
			"title": title,
			"head":  branch,
			"base":  baseBranch,
			"body":  body,
			"draft": true,
		})
		if err != nil {
			return nil, err
		}
	}
	if status >= 400 {
		return nil, fmt.Errorf("create draft PR HTTP %d: %s", status, truncate(string(raw), 240))
	}
	var pr map[string]any
	_ = json.Unmarshal(raw, &pr)
	prURL, _ := pr["html_url"].(string)
	num := 0
	switch n := pr["number"].(type) {
	case float64:
		num = int(n)
	}
	return &DraftPRResult{
		URL:    prURL,
		Number: num,
		Branch: branch,
		Owner:  owner,
		Repo:   repo,
		SHA:    commit.SHA,
	}, nil
}

// StackOnBranch commits files onto one long-lived branch without resetting it.
// Story branches stay separate. A draft pull request is opened once for the stacked branch.
func (c *Client) StackOnBranch(ctx context.Context, owner, repo, branch, baseBranch, title, body, message string, files []File) (*DraftPRResult, error) {
	if strings.TrimSpace(branch) == "" {
		return nil, fmt.Errorf("branch is required")
	}
	if baseBranch == "" {
		b, err := c.DefaultBranch(ctx, owner, repo)
		if err != nil {
			return nil, err
		}
		baseBranch = b
	}
	status, raw, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", owner, repo, branch), nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		baseStatus, baseRaw, baseErr := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", owner, repo, baseBranch), nil)
		if baseErr != nil {
			return nil, baseErr
		}
		if baseStatus >= 400 {
			return nil, fmt.Errorf("base branch lookup HTTP %d: %s", baseStatus, truncate(string(baseRaw), 200))
		}
		var baseRef map[string]any
		_ = json.Unmarshal(baseRaw, &baseRef)
		baseSHA := ""
		if obj, ok := baseRef["object"].(map[string]any); ok {
			baseSHA, _ = obj["sha"].(string)
		}
		if baseSHA == "" {
			return nil, fmt.Errorf("base branch has empty sha")
		}
		status, raw, err = c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo), map[string]any{
			"ref": "refs/heads/" + branch,
			"sha": baseSHA,
		})
		if err != nil {
			return nil, err
		}
		if status >= 400 {
			return nil, fmt.Errorf("create branch HTTP %d: %s", status, truncate(string(raw), 200))
		}
	} else if status >= 400 {
		return nil, fmt.Errorf("branch lookup HTTP %d: %s", status, truncate(string(raw), 200))
	}

	commit, err := c.CommitFiles(ctx, owner, repo, branch, message, files)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("state", "open")
	q.Set("head", owner+":"+branch)
	status, raw, err = c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls?%s", owner, repo, q.Encode()), nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("list pull requests HTTP %d: %s", status, truncate(string(raw), 200))
	}
	var pulls []map[string]any
	if err := json.Unmarshal(raw, &pulls); err != nil {
		return nil, fmt.Errorf("list pull requests: %w", err)
	}
	if len(pulls) > 0 {
		prURL, _ := pulls[0]["html_url"].(string)
		num := 0
		switch n := pulls[0]["number"].(type) {
		case float64:
			num = int(n)
		}
		return &DraftPRResult{
			URL: prURL, Number: num, Branch: branch, Owner: owner, Repo: repo, SHA: commit.SHA,
		}, nil
	}

	status, raw, err = c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls", owner, repo), map[string]any{
		"title": title,
		"head":  branch,
		"base":  baseBranch,
		"body":  body,
		"draft": true,
	})
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("create draft PR HTTP %d: %s", status, truncate(string(raw), 200))
	}
	var pr map[string]any
	_ = json.Unmarshal(raw, &pr)
	prURL, _ := pr["html_url"].(string)
	num := 0
	switch n := pr["number"].(type) {
	case float64:
		num = int(n)
	}
	return &DraftPRResult{
		URL: prURL, Number: num, Branch: branch, Owner: owner, Repo: repo, SHA: commit.SHA,
	}, nil
}

func (c *Client) closeOpenPullsForHead(ctx context.Context, owner, repo, branch string) error {
	q := url.Values{}
	q.Set("state", "open")
	q.Set("head", owner+":"+branch)
	status, raw, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls?%s", owner, repo, q.Encode()), nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("list pull requests HTTP %d: %s", status, truncate(string(raw), 200))
	}
	var pulls []map[string]any
	if err := json.Unmarshal(raw, &pulls); err != nil {
		return fmt.Errorf("list pull requests: %w", err)
	}
	for _, pr := range pulls {
		number := 0
		switch n := pr["number"].(type) {
		case float64:
			number = int(n)
		}
		if number <= 0 {
			continue
		}
		status, raw, err = c.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number), map[string]any{
			"state": "closed",
		})
		if err != nil {
			return err
		}
		if status >= 400 {
			return fmt.Errorf("close pull request #%d HTTP %d: %s", number, status, truncate(string(raw), 200))
		}
	}
	return nil
}

func (c *Client) CombinedStatus(ctx context.Context, owner, repo, ref string) (string, string, error) {
	status, raw, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/commits/%s/status", owner, repo, url.PathEscape(ref)), nil)
	if err != nil {
		return "", "", err
	}
	if status >= 400 {
		return "unknown", truncate(string(raw), 200), nil
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	state, _ := m["state"].(string)
	return state, string(raw), nil
}

// PullHead returns the head commit of a pull request.
func (c *Client) PullHead(ctx context.Context, owner, repo string, number int) (sha, ref string, err error) {
	status, raw, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(repo), number), nil)
	if err != nil {
		return "", "", err
	}
	if status == http.StatusNotFound {
		return "", "", fmt.Errorf("pull request not found")
	}
	if status >= 400 {
		return "", "", fmt.Errorf("pull request HTTP %d: %s", status, truncate(string(raw), 200))
	}
	var body struct {
		Head struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", "", err
	}
	if body.Head.SHA == "" {
		return "", "", fmt.Errorf("pull request has no head commit")
	}
	return body.Head.SHA, body.Head.Ref, nil
}

// PullChangedPaths lists files touched by a pull request.
func (c *Client) PullChangedPaths(ctx context.Context, owner, repo string, number int) ([]string, error) {
	status, raw, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100", url.PathEscape(owner), url.PathEscape(repo), number), nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("pull request files HTTP %d: %s", status, truncate(string(raw), 200))
	}
	var rows []struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Filename) != "" {
			out = append(out, row.Filename)
		}
	}
	return out, nil
}

// FileBytes reads one file at a commit. Missing files return an error containing "not found".
func (c *Client) FileBytes(ctx context.Context, owner, repo, ref, filePath string) ([]byte, error) {
	filePath = strings.TrimPrefix(strings.TrimSpace(filePath), "/")
	if filePath == "" || strings.Contains(filePath, "..") {
		return nil, fmt.Errorf("invalid path")
	}
	parts := strings.Split(filePath, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	apiPath := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", url.PathEscape(owner), url.PathEscape(repo), strings.Join(parts, "/"), url.QueryEscape(ref))
	status, raw, err := c.do(ctx, http.MethodGet, apiPath, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, fmt.Errorf("not found")
	}
	if status >= 400 {
		return nil, fmt.Errorf("file HTTP %d: %s", status, truncate(string(raw), 200))
	}
	var body struct {
		Encoding    string `json:"encoding"`
		Content     string `json:"content"`
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	if strings.EqualFold(body.Encoding, "base64") && strings.TrimSpace(body.Content) != "" {
		decoded, err := base64.StdEncoding.DecodeString(strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, body.Content))
		if err != nil {
			return nil, err
		}
		return decoded, nil
	}
	if body.DownloadURL != "" {
		return c.getAbsolute(ctx, body.DownloadURL)
	}
	return nil, fmt.Errorf("file has no content")
}

func (c *Client) getAbsolute(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("download HTTP %d", res.StatusCode)
	}
	return raw, nil
}

// BranchSHA returns the commit at refs/heads/<branch>.
func (c *Client) BranchSHA(ctx context.Context, owner, repo, branch string) (string, error) {
	branch = strings.TrimPrefix(strings.TrimSpace(branch), "/")
	if branch == "" || strings.Contains(branch, "..") {
		return "", fmt.Errorf("invalid branch")
	}
	status, raw, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", url.PathEscape(owner), url.PathEscape(repo), branch), nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		return "", fmt.Errorf("branch not found")
	}
	if status >= 400 {
		return "", fmt.Errorf("branch HTTP %d: %s", status, truncate(string(raw), 200))
	}
	var body struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	if body.Object.SHA == "" {
		return "", fmt.Errorf("branch has no commit")
	}
	return body.Object.SHA, nil
}

// TreePaths lists file paths in a git tree.
func (c *Client) TreePaths(ctx context.Context, owner, repo, sha string) ([]string, error) {
	status, raw, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/trees/%s?recursive=1", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(sha)), nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("tree HTTP %d: %s", status, truncate(string(raw), 200))
	}
	var body struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Tree))
	for _, item := range body.Tree {
		if item.Type == "blob" && strings.TrimSpace(item.Path) != "" {
			out = append(out, item.Path)
		}
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
