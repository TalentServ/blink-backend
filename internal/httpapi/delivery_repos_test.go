package httpapi

import (
	"testing"

	"github.com/nisha-ts-40599/blink-backend/internal/githubgit"
)

func TestPickAppReposUsesOwnerAndSkipsWorkspace(t *testing.T) {
	got := pickAppRepos("octocat", []repoRef{
		{Name: "calculator-workspace-test", Purpose: "Workspace"},
		{Name: "calculator-app-test", Purpose: "Application"},
	})
	if len(got) != 1 {
		t.Fatalf("repos = %+v", got)
	}
	if got[0].Owner != "octocat" || got[0].Repo != "calculator-app-test" || got[0].Kind != "backend" {
		t.Fatalf("repo = %+v", got[0])
	}
}

func TestClassifyRepoHintKeepsScreenInFrontend(t *testing.T) {
	if got := classifyRepoHint("src/App.tsx", "backend"); got != "frontend" {
		t.Fatalf("screen hint = %s", got)
	}
	if got := classifyRepoHint("src/calculator/calculator.controller.ts", ""); got != "backend" {
		t.Fatalf("server hint = %s", got)
	}
	if got := classifyRepoHint("src/App.tsx", "app"); got != "frontend" {
		t.Fatalf("app hint = %s", got)
	}
}

func TestFilesForRepoPutsEveryPatchOnTheOnlyAppRepo(t *testing.T) {
	repos := []struct{ Owner, Repo, Kind string }{{Owner: "octocat", Repo: "calculator-app-test", Kind: "backend"}}
	byHint := map[string][]githubgit.File{
		"frontend": {{Path: "src/App.tsx", Content: "export {}"}},
		"backend":  {{Path: "src/Calc.java", Content: "class Calc {}"}},
	}
	files := filesForRepo(repos, byHint, 0, "backend")
	if len(files) != 2 {
		t.Fatalf("files = %+v", files)
	}
}
