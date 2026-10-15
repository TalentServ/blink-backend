package zipkit

import "strings"

const productScopePrefix = ".cursor/ai-sdlc/product-scope/"

// MergeOverlays copies base then overlay. Overlay wins on the same path.
// Paths that fail SanitizeOverlayPath are dropped. Product scope is one
// revision: when overlay carries it, base product-scope files are not mixed in.
func MergeOverlays(base, overlay map[string]string) map[string]string {
	overlayOwnsScope := false
	for path := range overlay {
		if strings.HasPrefix(SanitizeOverlayPath(path), productScopePrefix) {
			overlayOwnsScope = true
			break
		}
	}
	out := make(map[string]string)
	for i, src := range []map[string]string{base, overlay} {
		for path, content := range src {
			rel := SanitizeOverlayPath(path)
			if rel == "" || IsPrematureWorkPlanOverlay(rel) {
				continue
			}
			if i == 0 && overlayOwnsScope && strings.HasPrefix(rel, productScopePrefix) {
				continue
			}
			out[rel] = content
		}
	}
	return out
}

// IsPrematureWorkPlanOverlay reports classify/spec/plan markdown that must not
// land in a downloaded workspace before /sdlc-start.
func IsPrematureWorkPlanOverlay(path string) bool {
	rel := strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
	if !strings.Contains(rel, "/workflow-state/") {
		return false
	}
	switch rel[strings.LastIndex(rel, "/")+1:] {
	case "classification.md", "specification.md", "technical-plan.md":
		return true
	default:
		return false
	}
}

func OverlayCount(files map[string]string) int {
	if files == nil {
		return 0
	}
	return len(files)
}
