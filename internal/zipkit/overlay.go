package zipkit

import "strings"

// MergeOverlays copies base then overlay. Overlay wins on the same path.
// Paths that fail SanitizeOverlayPath are dropped.
func MergeOverlays(base, overlay map[string]string) map[string]string {
	out := make(map[string]string)
	for _, src := range []map[string]string{base, overlay} {
		for path, content := range src {
			rel := SanitizeOverlayPath(path)
			if rel == "" || IsPrematureWorkPlanOverlay(rel) {
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
