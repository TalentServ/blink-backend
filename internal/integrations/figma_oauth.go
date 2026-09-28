package integrations

import "strings"

// Read scopes plus webhook writes so Figma can automatically notify Blink
// about changes to the selected design.
const figmaDefaultScopes = "current_user:read,file_content:read,file_metadata:read,webhooks:write"

func normalizeFigmaScopes(raw string) string {
	allowed := map[string]struct{}{
		"current_user:read":  {},
		"file_content:read":  {},
		"file_metadata:read": {},
		"webhooks:write":     {},
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';'
	})
	out := make([]string, 0, 3)
	seen := map[string]struct{}{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := allowed[part]; !ok {
			continue
		}
		if _, dup := seen[part]; dup {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	if len(out) == 0 {
		return figmaDefaultScopes
	}
	return strings.Join(out, ",")
}
