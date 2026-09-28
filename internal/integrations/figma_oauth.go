package integrations

import "strings"

// Read scopes plus the Professional writes Blink uses: webhooks so Figma can
// call Blink, and mcp:connect so a chosen screen can become a Figma file.
const figmaDefaultScopes = "current_user:read,file_content:read,file_metadata:read,webhooks:write,mcp:connect"

func normalizeFigmaScopes(raw string) string {
	allowed := map[string]struct{}{
		"current_user:read":  {},
		"file_content:read":  {},
		"file_metadata:read": {},
		"webhooks:write":     {},
		"mcp:connect":        {},
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
