package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testAISDLCRoot(t *testing.T) string {
	t.Helper()
	candidates := []string{
		filepath.Join("..", "..", "..", "ai-sdlc"),
		filepath.Join("D:", "Blink", "repos", "ai-sdlc"),
	}
	for _, c := range candidates {
		if LooksLikeAISDLC(c) {
			return c
		}
		if abs, err := filepath.Abs(c); err == nil && LooksLikeAISDLC(abs) {
			return abs
		}
	}
	root := t.TempDir()
	mustWrite := func(rel, body string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(upstreamSkillGov, "---\nname: gov\ndescription: governance\n---\n# Gov\n")
	mustWrite(upstreamSkillRubric, "---\nname: rubric\ndescription: rubric\n---\n# Rubric\n")
	mustWrite(upstreamDeveloper, "# developer\nDo not copy this push advice.\n")
	mustWrite(upstreamCodeReviewer, "# code-reviewer\n")
	mustWrite(upstreamTestReviewer, "# test-reviewer\n")
	mustWrite(upstreamSecReviewer, "# security-reviewer\n")
	return root
}

func TestLoadOfficialSkills(t *testing.T) {
	root := testAISDLCRoot(t)
	files, err := BuildCapabilityFiles(root, DefaultSkills())
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSkillBundle(files); err != nil {
		t.Fatal(err)
	}
	foundGov := false
	for _, f := range files {
		if strings.Contains(f.Path, "ai-sdlc-governance") {
			foundGov = true
		}
		if strings.Contains(string(f.Content), "make ai-sdlc-validate-stage") {
			t.Fatalf("official skill still coupled via makefile in %s", f.Path)
		}
	}
	if !foundGov {
		t.Fatal("expected official governance skill")
	}
}

func TestBuildInstructionsDoesNotLoadMakefilePrompts(t *testing.T) {
	out, err := BuildInstructions(testAISDLCRoot(t), PromptImplementStep, PromptVars{"ISSUE": "X-1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "make ai-sdlc-validate-stage") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "X-1") {
		t.Fatal(out)
	}
}

func TestEphemeralGovernanceStaysOutsideRepos(t *testing.T) {
	bundle, err := BuildImplementGovernance("p", "i", "go")
	if err != nil {
		t.Fatal(err)
	}
	files := BuildEphemeralGovernanceFiles(bundle)
	for _, f := range files {
		if strings.HasPrefix(f.Path, "/workspace/repos/") {
			t.Fatalf("governance leaked into repo tree: %s", f.Path)
		}
	}
}
