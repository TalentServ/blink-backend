package prapp

import (
	"strings"
	"testing"
)

func TestParsePullURL(t *testing.T) {
	got, ok := ParsePullURL("https://github.com/acme/web/pull/14/files")
	if !ok || got.Owner != "acme" || got.Repo != "web" || got.Number != 14 {
		t.Fatalf("parse: %+v ok=%v", got, ok)
	}
	if _, ok := ParsePullURL("https://example.com/acme/web/pull/14"); ok {
		t.Fatal("accepted a non-github url")
	}
}

func TestCandidatePathsPreferChangedPage(t *testing.T) {
	got := CandidatePaths([]string{"src/App.java", "docs/index.html", "web/home.html", "public/index.html"})
	if len(got) == 0 || got[0] != "public/index.html" {
		t.Fatalf("first page = %v", got)
	}
	for _, p := range got {
		if strings.Contains(p, "docs/") {
			t.Fatalf("docs page included: %v", got)
		}
	}
}

func TestAssembleInlinesSameRepoAssets(t *testing.T) {
	doc := `<!DOCTYPE html><html><head><link rel="stylesheet" href="./app.css"></head><body><img src="logo.png"><script src="app.js"></script></body></html>`
	files := map[string]string{
		"web/app.css":  "body{color:#123}",
		"web/app.js":   "document.title='from-pr'",
		"web/logo.png": "png",
	}
	out := Assemble("web/index.html", doc, func(p string) ([]byte, bool) {
		body, ok := files[p]
		return []byte(body), ok
	})
	if !strings.Contains(out, "body{color:#123}") || !strings.Contains(out, "document.title='from-pr'") {
		t.Fatalf("assets not inlined: %s", out)
	}
	if strings.Contains(out, `src="app.js"`) || strings.Contains(out, `href="./app.css"`) {
		t.Fatalf("original asset urls remain: %s", out)
	}
	if !strings.Contains(out, "Unmerged pull request") {
		t.Fatal("banner missing")
	}
	if strings.Contains(out, "data:image/png;base64,") == false {
		t.Fatal("image was not inlined")
	}
}

func TestResolveRejectsEscape(t *testing.T) {
	if _, ok := Resolve("web/index.html", "https://evil.example/app.js"); ok {
		t.Fatal("fetched an absolute url")
	}
	if _, ok := Resolve("web/index.html", "../secret.html"); ok {
		t.Fatal("escaped the repository")
	}
	got, ok := Resolve("web/index.html", "./app.js")
	if !ok || got != "web/app.js" {
		t.Fatalf("resolve = %s ok=%v", got, ok)
	}
}
