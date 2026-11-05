package githubgit

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommitFilesTreatsEmptyRepo409AsBootstrap(t *testing.T) {
	t.Parallel()

	var putContents int
	var createdBlobs int
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/ws/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		if putContents == 0 {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"Git Repository is empty.","documentation_url":"https://docs.github.com/rest/git/refs#get-a-reference","status":"409"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": map[string]any{"sha": "base-sha"},
		})
	})
	mux.HandleFunc("/repos/acme/ws/git/commits/base-sha", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tree": map[string]any{"sha": "tree-base"},
		})
	})
	mux.HandleFunc("/repos/acme/ws/contents/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		putContents++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"commit": map[string]any{"sha": "init-sha", "html_url": "https://github.com/acme/ws/commit/init-sha"},
		})
	})
	mux.HandleFunc("/repos/acme/ws/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		createdBlobs++
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": "blob-sha"})
	})
	mux.HandleFunc("/repos/acme/ws/git/trees", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": "tree-sha"})
	})
	mux.HandleFunc("/repos/acme/ws/git/commits", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sha":      "commit-sha",
			"html_url": "https://github.com/acme/ws/commit/commit-sha",
		})
	})
	mux.HandleFunc("/repos/acme/ws/git/refs/heads/main", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/repos/acme/ws", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New("token")
	c.baseURL = srv.URL
	got, err := c.CommitFiles(context.Background(), "acme", "ws", "main", "init overlay", []File{
		{Path: ".cursor/ai-sdlc/governance/note.yaml", Content: "kind: note\n"},
		{Path: ".cursor/ai-sdlc/workflow-state/x.yaml", Content: "started: true\n"},
	})
	if err != nil {
		t.Fatalf("CommitFiles empty-repo 409: %v", err)
	}
	if putContents == 0 {
		t.Fatal("expected Contents API bootstrap for empty repository")
	}
	if createdBlobs == 0 {
		t.Fatal("expected remaining files via Git Data API after bootstrap")
	}
	if got == nil || got.SHA == "" {
		t.Fatal("expected commit result")
	}
}

func TestCommitFilesEmptyRepoErrorMessageIs409(t *testing.T) {
	t.Parallel()
	// Documents the live GitHub symptom before bootstrap existed.
	raw := `{"message":"Git Repository is empty.","documentation_url":"https://docs.github.com/rest/git/refs#get-a-reference","status":"409"}`
	if !isEmptyGitHubRepo(http.StatusConflict, []byte(raw)) {
		t.Fatal("409 empty-repo body should be treated as empty")
	}
	if isEmptyGitHubRepo(http.StatusConflict, []byte(`{"message":"other conflict"}`)) {
		t.Fatal("unrelated 409 must stay fail-closed")
	}
	if !isEmptyGitHubRepo(http.StatusNotFound, nil) {
		t.Fatal("404 missing ref is still a missing-branch bootstrap")
	}
}

func TestCurrentUserLoginResolvesPersonalRepositoryOwner(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "calculator-owner"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New("token")
	c.baseURL = srv.URL
	login, err := c.CurrentUserLogin(context.Background())
	if err != nil {
		t.Fatalf("CurrentUserLogin: %v", err)
	}
	if login != "calculator-owner" {
		t.Fatalf("CurrentUserLogin = %q, want calculator-owner", login)
	}
}

func TestCommitBranchAndDraftPRClosesExistingPull(t *testing.T) {
	t.Parallel()

	const branch = "blink/implement-STORY"
	var closed int
	var createdPRs int
	var forcedReset bool
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/app/git/ref/heads/main", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]any{"sha": "base-sha"}})
	})
	mux.HandleFunc("/repos/acme/app/pulls", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if !strings.Contains(r.URL.Query().Get("head"), "blink/implement-STORY") {
				t.Errorf("head filter = %q", r.URL.Query().Get("head"))
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 4, "html_url": "https://github.com/acme/app/pull/4"}})
		case http.MethodPost:
			createdPRs++
			if closed == 0 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"A pull request already exists for acme:blink/implement-STORY."}]}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number":   9,
				"html_url": "https://github.com/acme/app/pull/9",
			})
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/repos/acme/app/pulls/4", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["state"] != "closed" {
			t.Errorf("close state = %v", body["state"])
		}
		closed++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"number":4,"state":"closed"}`))
	})
	mux.HandleFunc("/repos/acme/app/git/refs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Reference already exists"}`))
	})
	mux.HandleFunc("/repos/acme/app/git/ref/heads/"+branch, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]any{"sha": "base-sha"}})
	})
	mux.HandleFunc("/repos/acme/app/git/refs/heads/"+branch, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]any{"sha": "base-sha"}})
			return
		}
		if r.Method != http.MethodPatch {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["force"] == true && body["sha"] == "base-sha" {
			forcedReset = true
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/repos/acme/app/git/commits/base-sha", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tree": map[string]any{"sha": "tree-base"}})
	})
	mux.HandleFunc("/repos/acme/app/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": "blob-sha"})
	})
	mux.HandleFunc("/repos/acme/app/git/trees", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": "tree-sha"})
	})
	mux.HandleFunc("/repos/acme/app/git/commits", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": "commit-sha", "html_url": "https://github.com/acme/app/commit/commit-sha"})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New("token")
	c.baseURL = srv.URL
	got, err := c.CommitBranchAndDraftPR(context.Background(), "acme", "app", branch, "main", "title", "body", "msg", []File{
		{Path: "src/app.ts", Content: "export const n = 1\n"},
	})
	if err != nil {
		t.Fatalf("CommitBranchAndDraftPR: %v", err)
	}
	if closed == 0 {
		t.Fatal("expected the existing draft PR to be closed")
	}
	if !forcedReset {
		t.Fatal("expected the implement branch to reset to the default branch")
	}
	if got.Number != 9 || got.URL != "https://github.com/acme/app/pull/9" {
		t.Fatalf("new draft = %+v", got)
	}
	if createdPRs < 1 {
		t.Fatal("expected a new draft PR")
	}
}

func TestGetPullRequestDiffAndFiles(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/app/pulls/9", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number": 9, "title": "Demo", "body": "body", "state": "open", "draft": true,
			"html_url": "https://github.com/acme/app/pull/9",
			"base": map[string]any{"ref": "main"},
			"head": map[string]any{"sha": "abc123", "ref": "blink/implement-X"},
		})
	})
	mux.HandleFunc("/repos/acme/app/pulls/9/files", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"filename": "src/main.go", "status": "modified", "additions": 2, "deletions": 1, "patch": "@@ -1 +1 @@\n-a\n+b\n",
		}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New("token")
	c.baseURL = srv.URL
	diff, err := c.GetPullRequestDiffAndFiles(context.Background(), "acme", "app", 9)
	if err != nil {
		t.Fatal(err)
	}
	if diff.HeadSHA != "abc123" || len(diff.Files) != 1 || diff.Files[0].Filename != "src/main.go" {
		t.Fatalf("unexpected diff: %+v", diff)
	}
}

func TestCreatePullRequestComment(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/app/issues/9/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", 405)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"html_url": "https://github.com/acme/app/pull/9#issuecomment-1"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New("token")
	c.baseURL = srv.URL
	url, err := c.CreatePullRequestComment(context.Background(), "acme", "app", 9, "advisory review")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(url, "issuecomment") {
		t.Fatalf("url=%q", url)
	}
}

func TestCommitOntoExistingBranch(t *testing.T) {
	t.Parallel()
	var blobs int
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/app/git/ref/heads/blink/fix", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]any{"sha": "head-sha"}})
	})
	mux.HandleFunc("/repos/acme/app/git/commits/head-sha", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tree": map[string]any{"sha": "tree-base"}})
	})
	mux.HandleFunc("/repos/acme/app/git/blobs", func(w http.ResponseWriter, r *http.Request) {
		blobs++
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": "blob"})
	})
	mux.HandleFunc("/repos/acme/app/git/trees", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": "tree"})
	})
	mux.HandleFunc("/repos/acme/app/git/commits", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": "new-sha", "html_url": "https://github.com/acme/app/commit/new-sha"})
	})
	mux.HandleFunc("/repos/acme/app/git/refs/heads/blink/fix", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			http.Error(w, "method", 405)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New("token")
	c.baseURL = srv.URL
	got, err := c.CommitOntoExistingBranch(context.Background(), "acme", "app", "blink/fix", "fix findings", []File{
		{Path: "src/a.go", Content: "package a\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if blobs == 0 || got.SHA == "" {
		t.Fatalf("unexpected commit: blobs=%d got=%+v", blobs, got)
	}
}

func TestExtractTextFilesFiltersBinariesAndPrefix(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, body string) {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("repo-sha/src/main.go", "package main\n")
	add("repo-sha/node_modules/x/index.js", "nope")
	add("repo-sha/photo.png", "xxxxx")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := ExtractTextFiles(buf.Bytes(), 50, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "src/main.go" {
		t.Fatalf("unexpected extract: %+v", files)
	}
}
