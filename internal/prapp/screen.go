package prapp

import (
	"path"
	"regexp"
	"strings"
)

// Screen is the file the preview opens from a branch.
type Screen struct {
	File string
	Kind string
	Name string
}

var exportFn = regexp.MustCompile(`export\s+function\s+([A-Z]\w*)`)
var exportDefaultFn = regexp.MustCompile(`export\s+default\s+function\s+([A-Z]\w*)`)
var heading = regexp.MustCompile(`(?s)<h1[^>]*>\s*([^<]+)`)

// ChooseScreen picks one webpage from a branch snapshot. React App.tsx wins over a fragment.
func ChooseScreen(files map[string]string) (Screen, bool) {
	var htmlPath string
	apps := make([]string, 0, 2)
	var fragment string
	for file, text := range files {
		lower := strings.ToLower(file)
		if strings.HasSuffix(lower, ".html") && htmlScore(text) > 0 && (htmlPath == "" || file < htmlPath) {
			htmlPath = file
		}
		if strings.HasSuffix(lower, ".tsx") && !strings.Contains(lower, ".test.") && !strings.Contains(lower, ".spec.") {
			if strings.HasSuffix(lower, "/app.tsx") || lower == "app.tsx" {
				apps = append(apps, file)
			} else if fragment == "" && exportFn.MatchString(text) {
				fragment = file
			}
		}
	}
	if htmlPath != "" {
		return Screen{File: htmlPath, Kind: "html", Name: strings.TrimSuffix(path.Base(htmlPath), path.Ext(htmlPath))}, true
	}
	best := ""
	bestScore := -1
	for _, file := range apps {
		score := appScore(files[file])
		if score > bestScore || (score == bestScore && (best == "" || file < best)) {
			best, bestScore = file, score
		}
	}
	chosen := best
	if chosen == "" {
		chosen = fragment
	}
	if chosen == "" {
		return Screen{}, false
	}
	text := files[chosen]
	if strings.Contains(text, "export default") {
		name := "App"
		if m := exportDefaultFn.FindStringSubmatch(text); m != nil {
			name = m[1]
		}
		return Screen{File: chosen, Kind: "default", Name: name}, true
	}
	if m := exportFn.FindStringSubmatch(text); m != nil {
		return Screen{File: chosen, Kind: "named", Name: m[1]}, true
	}
	return Screen{}, false
}

// Heading returns the first h1 text, when the screen has one.
func Heading(text string) string {
	m := heading.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func htmlScore(text string) int {
	sample := strings.ToLower(text)
	if strings.Contains(sample, ".tsx") || strings.Contains(sample, ".jsx") || strings.Contains(sample, ".vue") {
		return 0
	}
	if strings.Contains(sample, "<html") || strings.Contains(sample, "<!doctype html") || strings.Contains(sample, "<body") {
		return 100
	}
	return 0
}

func appScore(text string) int {
	score := 30
	if regexp.MustCompile(`(?i)keypad`).MatchString(text) {
		score += 20
	}
	if regexp.MustCompile(`evaluateExpression|equals-button|function calculate\b`).MatchString(text) {
		score += 40
	}
	return score
}

// Rank scores one repository screen. A frontend keypad outranks a second page in a backend repository.
func Rank(repoName, text string) int {
	score := appScore(text)
	if html := htmlScore(text); html > score {
		score = html
	}
	lower := strings.ToLower(repoName)
	switch {
	case strings.Contains(lower, "frontend") || strings.Contains(lower, "web") || strings.Contains(lower, "-ui"):
		score += 15
	case strings.Contains(lower, "backend") || strings.Contains(lower, "server") || strings.Contains(lower, "-api"):
		score -= 15
	}
	return score
}
