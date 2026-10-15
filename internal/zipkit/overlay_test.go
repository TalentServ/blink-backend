package zipkit

import "testing"

func TestMergeOverlaysS3ThenSetupWins(t *testing.T) {
	s3 := map[string]string{
		".cursor/ai-sdlc/spec.yaml":        "from-s3",
		".cursor/ai-sdlc/plan.yaml":        "plan-s3",
		"../secret":                        "nope",
		".cursor/commands/setup.md":        "not-an-overlay",
	}
	setup := map[string]string{
		".cursor/ai-sdlc/spec.yaml": "from-setup",
		".cursor/ai-sdlc/setup/setup-result.yaml": "ok\n",
	}
	merged := MergeOverlays(s3, setup)
	if merged[".cursor/ai-sdlc/spec.yaml"] != "from-setup" {
		t.Fatalf("setup should win, got %q", merged[".cursor/ai-sdlc/spec.yaml"])
	}
	if merged[".cursor/ai-sdlc/plan.yaml"] != "plan-s3" {
		t.Fatalf("s3 plan should remain, got %q", merged[".cursor/ai-sdlc/plan.yaml"])
	}
	if _, ok := merged[".cursor/commands/setup.md"]; ok {
		t.Fatal("commands path is not an ai-sdlc overlay")
	}
	if OverlayCount(merged) != 3 {
		t.Fatalf("count=%d files=%v", OverlayCount(merged), merged)
	}
}

func TestMergeOverlaysSetupProductScopeReplacesS3Revision(t *testing.T) {
	merged := MergeOverlays(map[string]string{
		".cursor/ai-sdlc/product-scope/product-scope.yaml": "status: PROPOSED\n",
		".cursor/ai-sdlc/product-scope/proposed/S001.yaml": "stale\n",
		".cursor/ai-sdlc/spec.yaml":                        "from-s3",
	}, map[string]string{
		".cursor/ai-sdlc/product-scope/product-scope.yaml": "status: CONFIRMED\n",
	})
	if merged[".cursor/ai-sdlc/product-scope/product-scope.yaml"] != "status: CONFIRMED\n" {
		t.Fatalf("setup scope should win, got %q", merged[".cursor/ai-sdlc/product-scope/product-scope.yaml"])
	}
	if _, ok := merged[".cursor/ai-sdlc/product-scope/proposed/S001.yaml"]; ok {
		t.Fatal("s3 product-scope files must not mix into the setup revision")
	}
	if merged[".cursor/ai-sdlc/spec.yaml"] != "from-s3" {
		t.Fatal("non-scope s3 overlays should remain")
	}

	kept := MergeOverlays(map[string]string{
		".cursor/ai-sdlc/product-scope/product-scope.yaml": "status: CONFIRMED\n",
	}, map[string]string{".cursor/ai-sdlc/setup/setup-result.yaml": "ok\n"})
	if kept[".cursor/ai-sdlc/product-scope/product-scope.yaml"] != "status: CONFIRMED\n" {
		t.Fatal("s3 scope should remain when setup has none")
	}
}

func TestMergeOverlaysDropsPrematureWorkPlanFiles(t *testing.T) {
	merged := MergeOverlays(map[string]string{
		".cursor/ai-sdlc/workflow-state/CALC-1/classification.md":  "# classify\n",
		".cursor/ai-sdlc/workflow-state/CALC-1/specification.md":   "# spec\n",
		".cursor/ai-sdlc/workflow-state/CALC-1/technical-plan.md":  "# plan\n",
		".cursor/ai-sdlc/product-scope/product-scope.yaml":         "status: PROPOSED\n",
	}, nil)
	if _, ok := merged[".cursor/ai-sdlc/workflow-state/CALC-1/classification.md"]; ok {
		t.Fatal("classification.md must not enter the download zip")
	}
	if merged[".cursor/ai-sdlc/product-scope/product-scope.yaml"] != "status: PROPOSED\n" {
		t.Fatal("product-scope overlay should remain")
	}
}
