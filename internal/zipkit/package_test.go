package zipkit

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageWorkspaceRejectsMissingKit(t *testing.T) {
	_, err := PackageWorkspace("demo_workspace", "# req\n", nil, nil, nil, SiteHints{}, filepath.Join(t.TempDir(), "missing-kit"))
	if err == nil {
		t.Fatal("expected error when automation_sdlc is missing")
	}
	if !strings.Contains(err.Error(), "automation_sdlc") {
		t.Fatalf("error should mention missing kit, got %v", err)
	}
}

func TestPackageWorkspaceIncludesRealKit(t *testing.T) {
	kit := t.TempDir()
	if err := os.WriteFile(filepath.Join(kit, "Makefile"), []byte("kit:\n\t@echo real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(kit, "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "REAL_KIT.txt"), []byte("not-dummy-kit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bundle, err := PackageWorkspace("demo_workspace", "# req\n", nil, map[string]string{
		".cursor/ai-sdlc/setup/setup-result.yaml": "status: ok\n",
	}, nil, SiteHints{}, kit)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.ZipBytes) < 200 {
		t.Fatalf("zip too small (%d bytes); dummy stub is ~11KB of placeholders", len(bundle.ZipBytes))
	}
	if strings.Contains(string(bundle.ZipBytes), "Blink could not find this folder") {
		t.Fatal("zip still contains dummy missing-folder README")
	}

	zr, err := zip.NewReader(bytes.NewReader(bundle.ZipBytes), int64(len(bundle.ZipBytes)))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "app/REAL_KIT.txt") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("zip did not include the real kit file")
	}
}

func TestPackageWorkspacePrefersKitCommandsAndMergedOverlay(t *testing.T) {
	kit := t.TempDir()
	if err := os.WriteFile(filepath.Join(kit, "Makefile"), []byte("all:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(kit, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kit, "app", "REAL_KIT.txt"), []byte("kit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(kit, "ai-sdlc", "tools", "orchestration")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "test_strategy.py"), []byte("def standalone_test_plan_required(state): return False\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdDir := filepath.Join(kit, ".cursor", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "setup-new-workspace.md"), []byte("# hosted setup\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	overlay := MergeOverlays(
		map[string]string{".cursor/ai-sdlc/spec.yaml": "from-s3\n"},
		map[string]string{".cursor/ai-sdlc/setup/setup-result.yaml": "status: ok\n"},
	)
	bundle, err := PackageWorkspace("demo_workspace", "# req\n", nil, overlay, nil, SiteHints{}, kit)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(bundle.ZipBytes), int64(len(bundle.ZipBytes)))
	if err != nil {
		t.Fatal(err)
	}

	names := map[string]string{}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		if f.Method != zip.Store {
			t.Fatalf("%s method=%d want Store", f.Name, f.Method)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		names[f.Name] = string(body)
	}

	cmdPath := "demo_workspace/.cursor/commands/setup-new-workspace.md"
	if !strings.Contains(names[cmdPath], "hosted setup") {
		t.Fatalf("missing kit commands in zip: %q", names[cmdPath])
	}
	nestedCmd := "demo_workspace/automation_sdlc/.cursor/commands/setup-new-workspace.md"
	if _, ok := names[nestedCmd]; ok {
		t.Fatal("commands must not be nested under automation_sdlc/.cursor")
	}
	runtimeModule := "demo_workspace/automation_sdlc/ai-sdlc/tools/orchestration/test_strategy.py"
	if _, ok := names[runtimeModule]; !ok {
		t.Fatalf("required stage-router module %q was excluded from the workspace kit", runtimeModule)
	}
	if names["demo_workspace/.cursor/ai-sdlc/spec.yaml"] != "from-s3\n" {
		t.Fatalf("s3 overlay missing, got %q", names["demo_workspace/.cursor/ai-sdlc/spec.yaml"])
	}
	if names["demo_workspace/.cursor/ai-sdlc/setup/setup-result.yaml"] != "status: ok\n" {
		t.Fatal("setup overlay missing")
	}
	if strings.Contains(string(bundle.ZipBytes), "blink-go-stub") {
		t.Fatal("zip must not include silent stub overlay")
	}
}

func TestLooksReal(t *testing.T) {
	empty := t.TempDir()
	if LooksReal(empty) {
		t.Fatal("empty dir should not look like a kit")
	}
	kit := t.TempDir()
	if err := os.WriteFile(filepath.Join(kit, "Makefile"), []byte("all:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !LooksReal(kit) {
		t.Fatal("dir with Makefile should look like a kit")
	}
}
