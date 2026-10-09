package zipkit

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeKit(t *testing.T) string {
	t.Helper()
	kit := t.TempDir()
	if err := os.WriteFile(filepath.Join(kit, "Makefile"), []byte("all:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(kit, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kit, "app", "main.py"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdDir := filepath.Join(kit, ".cursor", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "setup-new-workspace.md"), []byte("# setup\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	aiDir := filepath.Join(kit, ".cursor", "ai-sdlc")
	if err := os.MkdirAll(aiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(aiDir, "placeholder.yaml"), []byte("skip-me: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return kit
}

func TestListAutomationSdlcFilesSkipsCursor(t *testing.T) {
	kit := writeKit(t)
	files, err := ListAutomationSdlcFiles(kit)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range files {
		if rel == ".cursor" || strings.HasPrefix(rel, ".cursor/") {
			t.Fatalf("automation_sdlc upload must not include %s", rel)
		}
	}
	found := false
	for _, rel := range files {
		if rel == "app/main.py" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected app/main.py, got %v", files)
	}
}

func TestListCursorCommandFiles(t *testing.T) {
	kit := writeKit(t)
	files, err := ListCursorCommandFiles(kit)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != ".cursor/commands/setup-new-workspace.md" {
		t.Fatalf("got %v", files)
	}
}

func TestExtractRuntimeKitIncludesOnlyWorkspaceKitFiles(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "runtime.zip")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	for name, body := range map[string]string{
		"ai-sdlc/tools/setup/runner.py":        "print('framework')\n",
		"app/main.py":                          "print('runtime')\n",
		".cursor/commands/setup-new-workspace.md": "# setup\n",
		"site-packages/fastapi/__init__.py":     "# dependency\n",
	} {
		entry, entryErr := writer.Create(name)
		if entryErr != nil {
			t.Fatal(entryErr)
		}
		if _, entryErr = entry.Write([]byte(body)); entryErr != nil {
			t.Fatal(entryErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	destination := filepath.Join(t.TempDir(), "automation_sdlc")
	if err := extractRuntimeKit(reader, destination); err != nil {
		t.Fatal(err)
	}
	if !LooksReal(destination) {
		t.Fatal("extracted Lambda package should be a usable kit")
	}
	if _, err := os.Stat(filepath.Join(destination, "ai-sdlc", "tools", "setup", "runner.py")); err != nil {
		t.Fatalf("framework file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "site-packages")); !os.IsNotExist(err) {
		t.Fatalf("runtime dependencies must not be copied into the downloadable kit: %v", err)
	}
}
