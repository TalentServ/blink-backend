package codex

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/nisha-ts-40599/blink-backend/internal/githubgit"
)

const (
	maxInlineFiles      = 50
	maxInlineFileBytes  = 5 << 20
	maxInlineTotalBytes = 10 << 20
	maxArchiveBytes     = 50 << 20
	maxRepoFilesFetch   = 200
	maxRepoFileBytes    = 512 << 10
)

// RepoSnapshot describes one GitHub repo to stage under /workspace/repos/<hint>.
type RepoSnapshot struct {
	Owner    string
	Repo     string
	Ref      string
	Hint     string
	TechHint []string
}

// WorkspacePlan is the staged sandbox input.
type WorkspacePlan struct {
	Files          []StagedFile
	UseArchive     bool
	ArchiveBytes   []byte
	ArchiveName    string
	SetupCommands  []string
	PackagesPython []string
	PackagesNpm    []string
	PackagesSystem []string
	RepoRoots      []string
	Baseline       map[string]string
}

const blinkExportScript = `#!/usr/bin/env python3
import json, os, subprocess
roots = []
base = "/workspace/repos"
if os.path.isdir(base):
    roots = [os.path.join(base, n) for n in os.listdir(base) if os.path.isdir(os.path.join(base, n))]
files = []
for root in roots:
    hint = os.path.basename(root)
    try:
        out = subprocess.check_output(["git", "-C", root, "status", "--porcelain"], text=True)
    except Exception:
        continue
    for line in out.splitlines():
        if len(line) < 4:
            continue
        rel = line[3:].strip()
        low = rel.lower()
        if low.startswith(".ai-sdlc") or rel == "AGENTS.md" or low.startswith("capabilities/") or low.startswith(".cursor/") or low.startswith(".env"):
            continue
        path = os.path.join(root, rel)
        if not os.path.isfile(path):
            continue
        with open(path, "r", encoding="utf-8", errors="replace") as fh:
            files.append({"path": rel, "content": fh.read(), "repoHint": hint})
os.makedirs("/workspace/outputs", exist_ok=True)
result = {"summary": "git harvest", "files": files, "filesChanged": [f["path"] for f in files]}
with open("/workspace/outputs/blink-result.json", "w", encoding="utf-8") as fh:
    json.dump(result, fh)
print(json.dumps({"harvested": len(files)}))
`

// StageWorkspace builds sandbox files from GitHub tarballs + overlays + capabilities + prior outputs.
func StageWorkspace(
	ctx context.Context,
	gh *githubgit.Client,
	repos []RepoSnapshot,
	extraFiles []StagedFile,
	overlayFiles map[string]string,
	priorFiles map[string]string,
	techStack []string,
) (*WorkspacePlan, error) {
	plan := &WorkspacePlan{
		SetupCommands: []string{
			"mkdir -p /workspace/outputs /workspace/repos /workspace/capabilities /workspace/.ai-sdlc /usr/local/bin",
		},
		PackagesPython: inferPythonPackages(techStack),
		PackagesNpm:    inferNpmPackages(techStack),
		PackagesSystem: inferSystemPackages(techStack),
		Baseline:       map[string]string{},
	}

	files := make([]StagedFile, 0, 64)
	files = append(files, extraFiles...)
	files = append(files, StagedFile{Path: "/workspace/bin/blink-export", Content: []byte(blinkExportScript)})

	for _, repo := range repos {
		hint := sanitizeHint(repo.Hint)
		root := path.Join("/workspace/repos", hint)
		plan.RepoRoots = append(plan.RepoRoots, root)
		if gh == nil || strings.TrimSpace(repo.Owner) == "" || strings.TrimSpace(repo.Repo) == "" {
			continue
		}
		ref := strings.TrimSpace(repo.Ref)
		if ref == "" {
			if branch, err := gh.DefaultBranch(ctx, repo.Owner, repo.Repo); err == nil {
				ref = branch
			}
		}
		extracted := false
		if raw, err := gh.DownloadTarball(ctx, repo.Owner, repo.Repo, ref); err == nil && len(raw) > 0 {
			items, xerr := githubgit.ExtractTextFiles(raw, maxRepoFilesFetch, maxRepoFileBytes)
			if xerr == nil {
				for _, item := range items {
					rel := filepathToSlash(item.Path)
					files = append(files, StagedFile{
						Path:    path.Join(root, rel),
						Content: item.Content,
					})
					if rel != "" && !IsGovernancePath(rel) {
						plan.Baseline[rel] = string(item.Content)
					}
				}
				extracted = true
			}
		}
		if !extracted && ref != "" {
			sha := ref
			if resolved, err := gh.BranchSHA(ctx, repo.Owner, repo.Repo, ref); err == nil && resolved != "" {
				sha = resolved
			}
			if paths, err := gh.TreePaths(ctx, repo.Owner, repo.Repo, sha); err == nil {
				paths = filterStagePaths(paths)
				if len(paths) > maxRepoFilesFetch {
					paths = paths[:maxRepoFilesFetch]
				}
				for _, p := range paths {
					raw, err := gh.FileBytes(ctx, repo.Owner, repo.Repo, sha, p)
					if err != nil || len(raw) == 0 || len(raw) > maxRepoFileBytes || !utf8.Valid(raw) {
						continue
					}
					rel := filepathToSlash(p)
					files = append(files, StagedFile{Path: path.Join(root, rel), Content: raw})
					if rel != "" && !IsGovernancePath(rel) {
						plan.Baseline[rel] = string(raw)
					}
				}
			}
		}
	}

	for p, content := range overlayFiles {
		p = filepathToSlash(strings.TrimPrefix(strings.TrimSpace(p), "/"))
		if p == "" || IsGovernancePath(p) {
			continue
		}
		files = append(files, StagedFile{Path: path.Join("/workspace", p), Content: []byte(content)})
	}
	for p, content := range priorFiles {
		p = filepathToSlash(strings.TrimPrefix(strings.TrimSpace(p), "/"))
		if p == "" || IsGovernancePath(p) {
			continue
		}
		abs := p
		if !strings.HasPrefix(p, "/workspace/") {
			if strings.HasPrefix(p, "repos/") {
				abs = path.Join("/workspace", p)
			} else if len(plan.RepoRoots) == 1 {
				abs = path.Join(plan.RepoRoots[0], p)
			} else {
				abs = path.Join("/workspace/repos/all", p)
			}
		}
		files = append(files, StagedFile{Path: abs, Content: []byte(content)})
	}

	total := 0
	for _, f := range files {
		if len(f.Content) > maxInlineFileBytes {
			plan.UseArchive = true
		}
		total += len(f.Content)
	}
	if len(files) > maxInlineFiles || total > maxInlineTotalBytes {
		plan.UseArchive = true
	}

	if plan.UseArchive {
		archive, err := tarGzip(files)
		if err != nil {
			return nil, err
		}
		if len(archive) > maxArchiveBytes {
			return nil, fmt.Errorf("workspace archive is %d bytes (limit %d)", len(archive), maxArchiveBytes)
		}
		plan.ArchiveBytes = archive
		plan.ArchiveName = "blink-workspace.tar.gz"
		plan.SetupCommands = append(plan.SetupCommands,
			"tar -xzf /workspace/blink-workspace.tar.gz -C /",
			"rm -f /workspace/blink-workspace.tar.gz",
		)
		plan.Files = nil
	} else {
		plan.Files = files
	}

	plan.SetupCommands = append(plan.SetupCommands,
		"cp /workspace/bin/blink-export /usr/local/bin/blink-export && chmod +x /usr/local/bin/blink-export /workspace/bin/blink-export",
	)
	for _, root := range plan.RepoRoots {
		plan.SetupCommands = append(plan.SetupCommands,
			fmt.Sprintf("git -C %s init -q && git -C %s add -A && git -C %s -c user.email=blink@local -c user.name=blink commit -qm baseline --allow-empty", root, root, root),
		)
	}
	return plan, nil
}

func tarGzip(files []StagedFile) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		name := strings.TrimPrefix(f.Path, "/")
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(f.Content))}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.Content); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func filterStagePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = filepathToSlash(p)
		if githubgitKeep(p) {
			out = append(out, p)
		}
	}
	return out
}

func githubgitKeep(p string) bool {
	lower := strings.ToLower(filepathToSlash(p))
	base := path.Base(lower)
	if strings.Contains(lower, "/node_modules/") || strings.HasPrefix(lower, "node_modules/") {
		return false
	}
	if strings.Contains(lower, "/.git/") || strings.HasPrefix(lower, ".git/") {
		return false
	}
	if strings.Contains(lower, "/dist/") || strings.Contains(lower, "/build/") || strings.Contains(lower, "/.next/") {
		return false
	}
	if strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") ||
		strings.HasSuffix(lower, ".gif") || strings.HasSuffix(lower, ".webp") || strings.HasSuffix(lower, ".ico") ||
		strings.HasSuffix(lower, ".pdf") || strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".wasm") ||
		strings.HasSuffix(lower, ".exe") || strings.HasSuffix(lower, ".dll") {
		return false
	}
	if base == "package-lock.json" || base == "yarn.lock" || base == "pnpm-lock.yaml" {
		return false
	}
	return true
}

func sanitizeHint(hint string) string {
	hint = strings.ToLower(strings.TrimSpace(hint))
	switch hint {
	case "frontend", "ui", "web":
		return "frontend"
	case "backend", "api", "server":
		return "backend"
	case "agent", "runtime":
		return "agent"
	case "docs", "documentation":
		return "docs"
	case "":
		return "all"
	default:
		var b strings.Builder
		for _, r := range hint {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				b.WriteRune(r)
			}
		}
		if b.Len() == 0 {
			return "all"
		}
		return b.String()
	}
}

func filepathToSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

func inferPythonPackages(tech []string) []string {
	return uniqueStrings([]string{"jsonschema==4.23.0", "PyYAML==6.0.2"})
}

func inferNpmPackages(tech []string) []string {
	joined := strings.ToLower(strings.Join(tech, " "))
	if strings.Contains(joined, "react") || strings.Contains(joined, "vite") || strings.Contains(joined, "typescript") || strings.Contains(joined, "node") {
		return []string{"typescript"}
	}
	return nil
}

func inferSystemPackages(tech []string) []string {
	joined := strings.ToLower(strings.Join(tech, " "))
	out := []string{"git", "jq"}
	if strings.Contains(joined, "go") {
		out = append(out, "golang-go")
	}
	return uniqueStrings(out)
}

func uniqueStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// InlineBase64Map converts staged files to path -> base64.
func InlineBase64Map(files []StagedFile) map[string]string {
	out := make(map[string]string, len(files))
	for _, f := range files {
		out[f.Path] = base64.StdEncoding.EncodeToString(f.Content)
	}
	return out
}
