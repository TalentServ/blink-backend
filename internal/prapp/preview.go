package prapp

import (
	"encoding/base64"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// PullRef is a GitHub pull request.
type PullRef struct {
	Owner  string
	Repo   string
	Number int
	URL    string
}

var pullURLPattern = regexp.MustCompile(`(?i)^https?://github\.com/([^/]+)/([^/]+)/pull/(\d+)`)

// ParsePullURL reads https://github.com/owner/repo/pull/123.
func ParsePullURL(raw string) (PullRef, bool) {
	raw = strings.TrimSpace(raw)
	m := pullURLPattern.FindStringSubmatch(raw)
	if m == nil {
		return PullRef{}, false
	}
	n, err := strconv.Atoi(m[3])
	if err != nil || n <= 0 {
		return PullRef{}, false
	}
	return PullRef{
		Owner:  m[1],
		Repo:   strings.TrimSuffix(m[2], ".git"),
		Number: n,
		URL:    "https://github.com/" + m[1] + "/" + strings.TrimSuffix(m[2], ".git") + "/pull/" + m[3],
	}, true
}

var fallbackPages = []string{
	"index.html",
	"public/index.html",
	"frontend/index.html",
	"web/index.html",
	"app/index.html",
	"dist/index.html",
	"build/index.html",
	"src/main/resources/static/index.html",
}

// CandidatePaths orders HTML pages to try. Pages changed by the pull request come first.
func CandidatePaths(changed []string) []string {
	var pages []string
	seen := map[string]bool{}
	add := func(p string) {
		p = cleanPath(p)
		if p == "" || seen[p] || !isPage(p) {
			return
		}
		seen[p] = true
		pages = append(pages, p)
	}
	var fromPR []string
	for _, p := range changed {
		if isPage(p) {
			fromPR = append(fromPR, cleanPath(p))
		}
	}
	sort.SliceStable(fromPR, func(i, j int) bool {
		return pageRank(fromPR[i]) < pageRank(fromPR[j])
	})
	for _, p := range fromPR {
		add(p)
	}
	for _, p := range fallbackPages {
		add(p)
	}
	return pages
}

func pageRank(p string) int {
	base := strings.ToLower(path.Base(p))
	if base == "index.html" || base == "index.htm" {
		return 0
	}
	return 1
}

func isPage(p string) bool {
	lower := strings.ToLower(cleanPath(p))
	if lower == "" || strings.HasPrefix(lower, "docs/") || strings.Contains(lower, "/docs/") {
		return false
	}
	return strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".htm")
}

func cleanPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "/")
	if p == "" || strings.Contains(p, "..") {
		return ""
	}
	return path.Clean(p)
}

// Resolve turns a relative URL in an HTML file into a path in the same repository.
func Resolve(entry, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "#") || strings.Contains(ref, "://") || strings.HasPrefix(ref, "//") || strings.HasPrefix(strings.ToLower(ref), "data:") || strings.HasPrefix(strings.ToLower(ref), "mailto:") || strings.HasPrefix(strings.ToLower(ref), "javascript:") {
		return "", false
	}
	if i := strings.IndexAny(ref, "?#"); i >= 0 {
		ref = ref[:i]
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	unescaped, err := url.PathUnescape(ref)
	if err != nil {
		return "", false
	}
	base := path.Dir(cleanPath(entry))
	if base == "." {
		base = ""
	}
	var joined string
	if strings.HasPrefix(unescaped, "/") {
		joined = cleanPath(unescaped)
	} else if base == "" {
		joined = cleanPath(unescaped)
	} else {
		joined = cleanPath(base + "/" + unescaped)
	}
	if joined == "" || joined == "." || strings.HasPrefix(joined, "../") {
		return "", false
	}
	return joined, true
}

// Assemble returns the pull request page with same-repo CSS, scripts, and images inlined.
func Assemble(entry, doc string, load func(repoPath string) ([]byte, bool)) string {
	doc = inlineTags(entry, doc, load)
	return injectBanner(doc)
}

func inlineTags(entry, doc string, load func(repoPath string) ([]byte, bool)) string {
	doc = linkPattern.ReplaceAllStringFunc(doc, func(tag string) string {
		ref := attr(tag, "href")
		rel := strings.ToLower(attr(tag, "rel"))
		if ref == "" || (rel != "" && !strings.Contains(rel, "stylesheet")) {
			return tag
		}
		if rel == "" && !strings.HasSuffix(strings.ToLower(ref), ".css") {
			return tag
		}
		repoPath, ok := Resolve(entry, ref)
		if !ok {
			return tag
		}
		body, ok := load(repoPath)
		if !ok || len(body) == 0 {
			return tag
		}
		return "<style>\n" + string(body) + "\n</style>"
	})
	doc = scriptPattern.ReplaceAllStringFunc(doc, func(tag string) string {
		ref := attr(tag, "src")
		if ref == "" {
			return tag
		}
		repoPath, ok := Resolve(entry, ref)
		if !ok {
			return tag
		}
		body, ok := load(repoPath)
		if !ok || len(body) == 0 {
			return tag
		}
		kind := "text/javascript"
		if strings.Contains(strings.ToLower(tag), "module") {
			kind = "module"
		}
		if kind == "module" {
			return "<script type=\"module\">\n" + string(body) + "\n</script>"
		}
		return "<script>\n" + string(body) + "\n</script>"
	})
	doc = imgPattern.ReplaceAllStringFunc(doc, func(tag string) string {
		ref := attr(tag, "src")
		repoPath, ok := Resolve(entry, ref)
		if !ok {
			return tag
		}
		body, ok := load(repoPath)
		if !ok || len(body) == 0 {
			return tag
		}
		mime := mimeFor(repoPath)
		data := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(body)
		return replaceAttr(tag, "src", data)
	})
	return doc
}

var (
	linkPattern   = regexp.MustCompile(`(?i)<link\b[^>]*>`)
	scriptPattern = regexp.MustCompile(`(?i)<script\b[^>]*\bsrc\s*=\s*["'][^"']+["'][^>]*>\s*</script>`)
	imgPattern    = regexp.MustCompile(`(?i)<img\b[^>]*>`)
	attrPattern   = regexp.MustCompile(`(?i)([:\w-]+)\s*=\s*("([^"]*)"|'([^']*)')`)
)

func attr(tag, name string) string {
	name = strings.ToLower(name)
	for _, m := range attrPattern.FindAllStringSubmatch(tag, -1) {
		if strings.ToLower(m[1]) != name {
			continue
		}
		if m[3] != "" || strings.Contains(m[2], `"`) {
			return m[3]
		}
		return m[4]
	}
	return ""
}

func replaceAttr(tag, name, value string) string {
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(name) + `\s*=\s*(".*?"|'.*?')`)
	escaped := strings.ReplaceAll(value, `"`, "&quot;")
	return re.ReplaceAllString(tag, name+`="`+escaped+`"`)
}

func mimeFor(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".webp":
		return "image/webp"
	case ".css":
		return "text/css"
	default:
		return "application/octet-stream"
	}
}

func injectBanner(doc string) string {
	banner := `<div style="position:sticky;top:0;z-index:2147483647;background:#1c1915;color:#f4f1ea;font:13px/1.4 Segoe UI,sans-serif;padding:8px 12px">Unmerged pull request. This is the application on that branch.</div>`
	lower := strings.ToLower(doc)
	if i := strings.Index(lower, "<body"); i >= 0 {
		if j := strings.Index(doc[i:], ">"); j >= 0 {
			at := i + j + 1
			return doc[:at] + banner + doc[at:]
		}
	}
	return banner + doc
}
