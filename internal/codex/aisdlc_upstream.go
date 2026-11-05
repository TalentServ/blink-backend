package codex

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultAISDLCTarball = "https://codeload.github.com/ai-sdlc-framework/ai-sdlc/tar.gz/refs/heads/main"
	upstreamSkillGov     = "ai-sdlc-plugin/skills/ai-sdlc-governance/SKILL.md"
	upstreamSkillRubric  = "ai-sdlc-plugin/skills/decision-rubric/SKILL.md"
	upstreamDeveloper    = "ai-sdlc-plugin/agents/developer.md"
	upstreamCodeReviewer = "ai-sdlc-plugin/agents/code-reviewer.md"
	upstreamTestReviewer = "ai-sdlc-plugin/agents/test-reviewer.md"
	upstreamSecReviewer  = "ai-sdlc-plugin/agents/security-reviewer.md"
)

var requiredUpstreamRel = []string{
	upstreamSkillGov,
	upstreamSkillRubric,
	upstreamDeveloper,
	upstreamCodeReviewer,
	upstreamTestReviewer,
	upstreamSecReviewer,
}

var (
	upstreamOnce sync.Once
	upstreamRoot string
	upstreamErr  error
)

// LooksLikeAISDLC is true when the official plugin skills and agents are present.
func LooksLikeAISDLC(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	for _, rel := range requiredUpstreamRel {
		st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || st.IsDir() {
			return false
		}
	}
	return true
}

// ResolveAISDLCRoot returns a local path containing the official plugin files.
// It prefers configuredPath, then common workspace locations, then a tarball cache.
func ResolveAISDLCRoot(configuredPath, tarballURL string) (string, error) {
	upstreamOnce.Do(func() {
		upstreamRoot, upstreamErr = resolveAISDLCRoot(configuredPath, tarballURL)
	})
	return upstreamRoot, upstreamErr
}

func resolveAISDLCRoot(configuredPath, tarballURL string) (string, error) {
	candidates := []string{
		strings.TrimSpace(configuredPath),
		filepath.Join("..", "ai-sdlc"),
		filepath.Join("D:", "Blink", "repos", "ai-sdlc"),
	}
	for _, c := range candidates {
		if LooksLikeAISDLC(c) {
			return absPath(c), nil
		}
		if abs, err := filepath.Abs(c); err == nil && LooksLikeAISDLC(abs) {
			return abs, nil
		}
	}
	cache := filepath.Join(os.TempDir(), "blink-ai-sdlc-plugin")
	if LooksLikeAISDLC(cache) {
		return cache, nil
	}
	if tarballURL == "" {
		tarballURL = defaultAISDLCTarball
	}
	if err := materializeAISDLCPlugin(tarballURL, cache); err != nil {
		return "", fmt.Errorf("ai-sdlc plugin unavailable (set BLINK_AI_SDLC_PATH or allow tarball bootstrap): %w", err)
	}
	if !LooksLikeAISDLC(cache) {
		return "", fmt.Errorf("ai-sdlc tarball cache is missing required plugin files")
	}
	return cache, nil
}

func materializeAISDLCPlugin(tarballURL, dest string) error {
	req, err := http.NewRequest(http.MethodGet, tarballURL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return fmt.Errorf("download ai-sdlc tarball HTTP %d", res.StatusCode)
	}
	wanted := map[string]struct{}{}
	for _, rel := range requiredUpstreamRel {
		wanted[rel] = struct{}{}
	}
	gz, err := gzip.NewReader(res.Body)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	written := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		rel := stripTarballPrefix(hdr.Name)
		if _, ok := wanted[rel]; !ok {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(tr, 2<<20))
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
		written++
	}
	if written < len(requiredUpstreamRel) {
		return fmt.Errorf("extracted %d/%d required plugin files", written, len(requiredUpstreamRel))
	}
	return nil
}

func stripTarballPrefix(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimPrefix(name, "./")
	parts := strings.SplitN(name, "/", 2)
	if len(parts) == 2 && (strings.HasPrefix(parts[0], "ai-sdlc-") || parts[0] == "ai-sdlc") {
		return parts[1]
	}
	return name
}

func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func resetAISDLCResolverForTest() {
	upstreamOnce = sync.Once{}
	upstreamRoot = ""
	upstreamErr = nil
}
