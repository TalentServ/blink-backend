package githubgit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
