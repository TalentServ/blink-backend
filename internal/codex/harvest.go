package codex

import (
	"encoding/json"
	"path"
	"strings"
)

// PartitionProductFiles splits harvested files into commitable product files and governance rejects.
func PartitionProductFiles(files map[string]string) (product map[string]string, blocked []string) {
	product = map[string]string{}
	for raw, content := range files {
		p := normalizeHarvestPath(raw)
		if p == "" {
			continue
		}
		if IsGovernancePath(p) {
			blocked = append(blocked, p)
			continue
		}
		product[p] = content
	}
	return product, blocked
}

// IsGovernancePath is true for files that must never be committed to a customer PR.
func IsGovernancePath(p string) bool {
	p = normalizeHarvestPath(p)
	if p == "" {
		return true
	}
	lower := strings.ToLower(p)
	base := path.Base(lower)
	if strings.Contains(lower, ".ai-sdlc/") || strings.HasPrefix(lower, ".ai-sdlc/") || base == ".ai-sdlc" {
		return true
	}
	if base == "agents.md" {
		return true
	}
	if strings.Contains(lower, "capabilities/") || strings.HasPrefix(lower, "capabilities/") {
		return true
	}
	if strings.Contains(lower, "/outputs/") || strings.HasPrefix(lower, "outputs/") || strings.Contains(lower, "workspace/outputs/") {
		return true
	}
	if strings.Contains(lower, ".cursor/") || strings.HasPrefix(lower, ".cursor/") {
		return true
	}
	if strings.HasPrefix(base, ".env") || strings.HasSuffix(lower, ".pem") {
		return true
	}
	if strings.HasPrefix(lower, "automation_sdlc/") {
		return true
	}
	if p == "blink-result.json" || strings.HasSuffix(lower, "/blink-result.json") {
		return true
	}
	return false
}

func normalizeHarvestPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, "workspace/")
	p = strings.TrimPrefix(p, "repos/")
	parts := strings.SplitN(p, "/", 2)
	if len(parts) == 2 {
		switch parts[0] {
		case "frontend", "backend", "agent", "docs", "all", "ui", "web", "api", "server":
			p = parts[1]
		}
	}
	return strings.TrimPrefix(p, "/")
}

func mergeEnvelopeFiles(files map[string]string, envelope map[string]any) map[string]string {
	if files == nil {
		files = map[string]string{}
	}
	if envelope == nil {
		return files
	}
	for _, key := range []string{"files", "overlayFiles"} {
		arr, _ := envelope[key].([]any)
		for _, item := range arr {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			p, _ := m["path"].(string)
			c, _ := m["content"].(string)
			p = strings.TrimSpace(strings.TrimPrefix(p, "/"))
			if p == "" {
				continue
			}
			files[p] = c
		}
	}
	return files
}

func parseResultEnvelope(raw string, fallbackText string) map[string]any {
	if strings.TrimSpace(raw) != "" {
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) == nil && m != nil {
			return m
		}
	}
	return parseFallbackJSON(fallbackText)
}
