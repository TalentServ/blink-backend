package zipkit

import (
	"archive/zip"
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	NextSDLCCommand = "/configure-stakeholders"
)

var reservedTopLevel = map[string]struct{}{
	"requirement.md": {}, ".cursor": {}, "automation_sdlc": {},
	"blink_demo": {}, "blink_backend": {}, "blink-backend": {},
}

// RepoFolder is a configured product repo under the workspace root.
type RepoFolder struct {
	Name        string
	Purpose     string
	Description string
}

// WorkspaceEntry is a top-level structure item for X-Blink-Workspace-Structure.
type WorkspaceEntry struct {
	Name string
	Kind string
}

// Bundle is the packaged download zip.
type Bundle struct {
	ZipBytes  []byte
	Filename  string
	Structure []WorkspaceEntry
	FileCount int
}

// PackageWorkspace builds the customer workspace zip.
// Prefer copying from automationSDLCPath when it exists; always include requirements
// markdown and .cursor overlay / MCP files.
func PackageWorkspace(
	rootName string,
	markdown string,
	repos []RepoFolder,
	overlay map[string]string,
	mcpProviders []string,
	siteHints SiteHints,
	automationSDLCPath string,
) (*Bundle, error) {
	root := strings.TrimSpace(rootName)
	if root == "" {
		root = "project_workspace"
	}
	root = strings.ReplaceAll(root, `\`, "/")
	root = strings.Trim(root, "/")

	md := strings.TrimSpace(markdown)
	if md == "" {
		md = "# requirement\n"
	}

	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	structure := make([]WorkspaceEntry, 0, 8)
	files := 0

	if err := putDir(zw, root+"/"); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := putText(zw, zipPath(root, "requirement.md"), md); err != nil {
		_ = zw.Close()
		return nil, err
	}
	files++
	structure = append(structure, WorkspaceEntry{Name: "requirement.md", Kind: "file"})

	sdlc := resolveAutomationSDLC(automationSDLCPath)
	if sdlc == "" || !LooksReal(sdlc) {
		_ = zw.Close()
		return nil, fmt.Errorf("automation_sdlc kit is missing — configure BLINK_AUTOMATION_SDLC_PATH or permit retrieval from the deployed Lambda package")
	}
	copied, err := copyTree(zw, sdlc, zipPath(root, "automation_sdlc"), map[string]struct{}{".cursor": {}}, map[string]struct{}{".env.mcp.example": {}}, &files)
	if err != nil {
		_ = zw.Close()
		return nil, err
	}
	if copied == 0 {
		_ = zw.Close()
		return nil, fmt.Errorf("automation_sdlc kit at %s had no files to copy", sdlc)
	}
	structure = append(structure, WorkspaceEntry{Name: "automation_sdlc", Kind: "directory"})

	if err := addCursorOverlay(zw, root, sdlc, &files, &structure); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := addAgentOverlay(zw, root, overlay, &files, &structure); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := addMcpConfig(zw, root, sdlc, mcpProviders, siteHints, &files, &structure); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := addConfiguredRepos(zw, root, repos, &files, &structure); err != nil {
		_ = zw.Close()
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return &Bundle{
		ZipBytes:  buf.Bytes(),
		Filename:  root + ".zip",
		Structure: structure,
		FileCount: files,
	}, nil
}

// EncodeStructure joins structure entries for the X-Blink-Workspace-Structure header.
func EncodeStructure(structure []WorkspaceEntry) string {
	parts := make([]string, 0, len(structure))
	for _, e := range structure {
		parts = append(parts, e.Name+":"+e.Kind)
	}
	return strings.Join(parts, ",")
}

// SanitizeOverlayPath accepts only .cursor/ai-sdlc/... file paths.
func SanitizeOverlayPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	normalized := strings.ReplaceAll(path, `\`, "/")
	normalized = strings.TrimLeft(normalized, "/")
	if strings.ContainsAny(normalized, "\x00\r\n") ||
		!strings.HasPrefix(normalized, ".cursor/ai-sdlc/") ||
		strings.HasSuffix(normalized, "/") ||
		strings.Contains(normalized, "//") {
		return ""
	}
	segments := strings.Split(normalized, "/")
	if len(segments) < 3 {
		return ""
	}
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." ||
			strings.HasSuffix(seg, ".lock") || seg == ".cache" || strings.Contains(seg, ":") {
			return ""
		}
	}
	return normalized
}

func addCursorOverlay(zw *zip.Writer, root, sdlc string, files *int, structure *[]WorkspaceEntry) error {
	skipFiles := map[string]struct{}{
		"mcp.json": {}, "mcp.windows.json": {}, "mcp.unix.json": {}, "MCP_SETUP.md": {},
	}
	copied := 0
	if overlay := resolveCursorOverlay(sdlc); overlay != "" {
		n, err := copyTree(zw, overlay, zipPath(root, ".cursor"), nil, skipFiles, files)
		if err != nil {
			return err
		}
		copied = n
	}
	if copied > 0 {
		*structure = append(*structure, WorkspaceEntry{Name: ".cursor", Kind: "directory"})
	}
	return nil
}

func addAgentOverlay(zw *zip.Writer, root string, overlay map[string]string, files *int, structure *[]WorkspaceEntry) error {
	added := false
	for path, content := range overlay {
		rel := SanitizeOverlayPath(path)
		if rel == "" {
			continue
		}
		if err := putText(zw, zipPath(root, rel), content); err != nil {
			return err
		}
		*files++
		added = true
	}
	if added {
		*structure = append(*structure, WorkspaceEntry{Name: ".cursor/ai-sdlc", Kind: "directory"})
	}
	return nil
}

func addMcpConfig(zw *zip.Writer, root, sdlc string, providers []string, hints SiteHints, files *int, structure *[]WorkspaceEntry) error {
	items := []struct {
		path, content string
	}{
		{zipPath(root, ".cursor/mcp.json"), mcpJSON(providers, osPortable)},
		{zipPath(root, ".cursor/mcp.windows.json"), mcpJSON(providers, osWindows)},
		{zipPath(root, ".cursor/mcp.unix.json"), mcpJSON(providers, osUnix)},
	}
	setup := mcpSetupReadme()
	items = append(items,
		struct{ path, content string }{zipPath(root, ".cursor/MCP_SETUP.md"), setup},
		struct{ path, content string }{zipPath(root, "MCP_SETUP.md"), setup},
	)
	envExample := envMcpExample(providers, hints)
	items = append(items,
		struct{ path, content string }{zipPath(root, "automation_sdlc/.env.mcp.example"), envExample},
		struct{ path, content string }{zipPath(root, "automation_sdlc/env.mcp.example"), envExample},
	)
	for _, it := range items {
		if err := putText(zw, it.path, it.content); err != nil {
			return err
		}
		*files++
	}
	if err := addMcpWrapperScripts(zw, root, sdlc, files); err != nil {
		return err
	}
	ensureStruct(structure, ".cursor", "directory")
	ensureStruct(structure, ".cursor/mcp.json", "file")
	ensureStruct(structure, "automation_sdlc/env.mcp.example", "file")
	ensureStruct(structure, "MCP_SETUP.md", "file")
	return nil
}

func addMcpWrapperScripts(zw *zip.Writer, root, sdlc string, files *int) error {
	for _, name := range []string{"mcp-npx.sh", "mcp-npx.ps1"} {
		entry := zipPath(root, "automation_sdlc/scripts/"+name)
		if sdlc != "" {
			src := filepath.Join(sdlc, "scripts", name)
			if st, err := os.Stat(src); err == nil && st.Mode().IsRegular() {
				if err := putFile(zw, entry, src); err != nil {
					return err
				}
				*files++
				continue
			}
		}
		stub := `#!/usr/bin/env bash
echo "mcp-npx.sh missing from automation_sdlc/scripts — re-download or copy from AI-SDLC." >&2
exit 1
`
		if strings.HasSuffix(name, ".ps1") {
			stub = "Write-Error \"mcp-npx.ps1 missing from automation_sdlc/scripts — re-download or copy from AI-SDLC.\"\nexit 1\n"
		}
		if err := putText(zw, entry, stub); err != nil {
			return err
		}
		*files++
	}
	return nil
}

func addConfiguredRepos(zw *zip.Writer, root string, repos []RepoFolder, files *int, structure *[]WorkspaceEntry) error {
	used := map[string]struct{}{}
	for k := range reservedTopLevel {
		used[k] = struct{}{}
	}
	for _, repo := range repos {
		name := slugifyRepo(repo.Name)
		if name == "" {
			continue
		}
		if _, exists := used[name]; exists {
			continue
		}
		used[name] = struct{}{}
		var b strings.Builder
		b.WriteString("# " + name + "\n\n")
		if strings.TrimSpace(repo.Purpose) != "" {
			b.WriteString(strings.TrimSpace(repo.Purpose) + "\n\n")
		}
		if strings.TrimSpace(repo.Description) != "" {
			b.WriteString(strings.TrimSpace(repo.Description) + "\n")
		}
		if err := putText(zw, zipPath(root, name+"/README.md"), b.String()); err != nil {
			return err
		}
		*files++
		*structure = append(*structure, WorkspaceEntry{Name: name, Kind: "directory"})
	}
	return nil
}

func resolveAutomationSDLC(configured string) string {
	candidates := []string{}
	if strings.TrimSpace(configured) != "" {
		candidates = append(candidates, configured)
	}
	cwd, _ := os.Getwd()
	if cwd != "" {
		candidates = append(candidates,
			filepath.Join(cwd, "automation_sdlc"),
			filepath.Join(cwd, "..", "automation_sdlc"),
			filepath.Join(filepath.Dir(cwd), "automation_sdlc"),
		)
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			return abs
		}
	}
	return ""
}

func resolveCursorOverlay(sdlc string) string {
	if sdlc != "" {
		nested := filepath.Join(sdlc, ".cursor")
		if hasCursorCommands(nested) {
			return nested
		}
	}
	cwd, _ := os.Getwd()
	if cwd != "" {
		ws := filepath.Join(cwd, ".cursor")
		if hasCursorCommands(ws) {
			return ws
		}
		if parent := filepath.Dir(cwd); parent != "" && parent != cwd {
			sib := filepath.Join(parent, ".cursor")
			if hasCursorCommands(sib) {
				return sib
			}
		}
	}
	if sdlc != "" {
		nested := filepath.Join(sdlc, ".cursor")
		if hasCursorContent(nested) {
			return nested
		}
	}
	return ""
}

func hasCursorCommands(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "commands"))
	return err == nil && st.IsDir()
}

func hasCursorContent(dir string) bool {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return false
	}
	found := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		found = true
		return io.EOF
	})
	return found
}

func copyTree(zw *zip.Writer, srcRoot, zipPrefix string, extraSkipDirs, skipFileNames map[string]struct{}, files *int) (int, error) {
	copied := 0
	err := filepath.WalkDir(srcRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		name := d.Name()
		parent := ""
		if parentRel := filepath.Dir(rel); parentRel != "." {
			parent = filepath.Base(parentRel)
		}
		if d.IsDir() {
			if SkipDirectory(name, parent) {
				return filepath.SkipDir
			}
			if extraSkipDirs != nil {
				if _, ok := extraSkipDirs[name]; ok {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if SkipFile(name) {
			return nil
		}
		if skipFileNames != nil {
			if _, ok := skipFileNames[name]; ok {
				return nil
			}
		}
		relSlash := filepath.ToSlash(rel)
		if err := putFile(zw, zipPrefix+"/"+relSlash, path); err != nil {
			return err
		}
		copied++
		*files++
		return nil
	})
	return copied, err
}

func putText(zw *zip.Writer, name, content string) error {
	return putBytes(zw, name, []byte(content), 0o644)
}

func putDir(zw *zip.Writer, name string) error {
	name = strings.ReplaceAll(name, `\`, "/")
	if !strings.HasSuffix(name, "/") {
		name += "/"
	}
	h := &zip.FileHeader{
		Name:   name,
		Method: zip.Store,
	}
	h.SetModTime(time.Unix(0, 0).UTC())
	h.SetMode(os.ModeDir | 0o755)
	_, err := zw.CreateHeader(h)
	return err
}

func putFile(zw *zip.Writer, name, src string) error {
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(src); err == nil {
		mode = st.Mode()
	}
	return putBytes(zw, name, body, mode)
}

func putBytes(zw *zip.Writer, name string, body []byte, mode os.FileMode) error {
	name = strings.ReplaceAll(name, `\`, "/")
	if mode == 0 {
		mode = 0o644
	}
	h := &zip.FileHeader{
		Name:               name,
		Method:             zip.Store,
		CRC32:              crc32.ChecksumIEEE(body),
		CompressedSize64:   uint64(len(body)),
		UncompressedSize64: uint64(len(body)),
	}
	h.SetModTime(time.Unix(0, 0).UTC())
	h.SetMode(mode)
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func zipPath(root, relative string) string {
	return root + "/" + strings.ReplaceAll(relative, `\`, "/")
}

func ensureStruct(structure *[]WorkspaceEntry, name, kind string) {
	for _, e := range *structure {
		if e.Name == name {
			return
		}
	}
	*structure = append(*structure, WorkspaceEntry{Name: name, Kind: kind})
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slugifyRepo(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	slug := strings.ToLower(strings.TrimSpace(value))
	slug = nonAlnum.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 80 {
		slug = strings.TrimRight(slug[:80], "-")
	}
	return slug
}
