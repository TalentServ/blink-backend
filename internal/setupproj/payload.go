package setupproj

import (
	"strings"
	"unicode"
)

// RequiredFiles are the canonical overlay paths Java refuses to skip.
var RequiredFiles = []string{
	".cursor/ai-sdlc/setup/project-initialisation-input.yaml",
	".cursor/ai-sdlc/setup/setup-result.yaml",
	".cursor/ai-sdlc/setup/setup-response-contract.yaml",
	".cursor/ai-sdlc/governance/role-registry.yaml",
	".cursor/ai-sdlc/greenfield-project-spec.yaml",
	".cursor/ai-sdlc/intake/requirements/requirement-source-history.yaml",
}

func HasRequired(overlay map[string]string) bool {
	if overlay == nil {
		return false
	}
	for _, path := range RequiredFiles {
		if strings.TrimSpace(overlay[path]) == "" {
			return false
		}
	}
	return true
}

func hyphenSlug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if out == "" {
		return "PROJECT"
	}
	return out
}

func asString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func asSlice(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case []map[string]any:
		out := make([]any, 0, len(t))
		for _, item := range t {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func integrationCategory(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "github", "bitbucket":
		return "source_control"
	case "jira":
		return "issue_tracking"
	case "confluence":
		return "documentation"
	case "figma":
		return "design"
	default:
		return ""
	}
}

func firstNonEmpty(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := asString(m[k]); s != "" {
			return s
		}
	}
	return ""
}

// HostedPayload is the Java projector / agent apply body (no nested blinkContext).
func HostedPayload(projectName, projectCode, numericID, description, requirementText string, blinkContext map[string]any) map[string]any {
	code := strings.TrimSpace(projectCode)
	if code == "" {
		code = hyphenSlug(projectName)
	}
	out := map[string]any{
		"projectId":        code,
		"projectCode":      code,
		"numericProjectId": numericID,
		"projectName":      strings.TrimSpace(projectName),
		"command":          "setup-new-workspace",
		"mode":             "apply",
	}
	if strings.TrimSpace(requirementText) != "" {
		out["requirementText"] = strings.TrimSpace(requirementText)
	}
	if strings.TrimSpace(description) != "" {
		out["projectDescription"] = strings.TrimSpace(description)
		out["description"] = strings.TrimSpace(description)
	}
	copyBlinkContext(out, blinkContext)
	return out
}

func copyBlinkContext(out map[string]any, ctx map[string]any) {
	if ctx == nil {
		return
	}
	stakeholders := map[string]string{}
	for _, item := range asSlice(ctx["stakeholderAssignments"]) {
		row := asMap(item)
		if row == nil {
			continue
		}
		role := firstNonEmpty(row, "roleId", "role_id", "role")
		name := firstNonEmpty(row, "personName", "name")
		email := firstNonEmpty(row, "personEmail", "email")
		if role == "" || name == "" {
			continue
		}
		if email != "" {
			stakeholders[role] = name + " <" + email + ">"
		} else {
			stakeholders[role] = name
		}
	}
	if len(stakeholders) > 0 {
		out["stakeholders"] = stakeholders
	}

	topology := map[string]any{}
	if model := asString(ctx["repositoryModel"]); model != "" {
		topology["structure"] = model
		topology["repository_model"] = model
	}
	if desc := asString(ctx["topology"]); desc != "" {
		topology["description"] = desc
	}
	if len(topology) > 0 {
		out["topology"] = topology
	}
	if confirmation := asMap(ctx["topologyConfirmation"]); confirmation != nil {
		out["topologyConfirmation"] = confirmation
	}

	repos := make([]map[string]any, 0)
	for _, item := range asSlice(ctx["repositories"]) {
		row := asMap(item)
		if row == nil {
			continue
		}
		name := firstNonEmpty(row, "name")
		if name == "" {
			continue
		}
		id := hyphenSlug(name)
		entry := map[string]any{"repo_id": id, "name": name}
		if purpose := firstNonEmpty(row, "purpose", "role"); purpose != "" {
			entry["role"] = purpose
		}
		repos = append(repos, entry)
	}
	if len(repos) > 0 {
		out["repositories"] = repos
	}

	handles := make([]map[string]any, 0)
	for _, item := range asSlice(ctx["integrations"]) {
		row := asMap(item)
		if row == nil {
			continue
		}
		provider := firstNonEmpty(row, "provider", "id")
		cat := integrationCategory(provider)
		if cat == "" {
			continue
		}
		entry := map[string]any{
			"integration_id": provider,
			"category":       cat,
			"user_choice":    "connect_now",
		}
		if handle := firstNonEmpty(row, "organization", "projectKey", "baseUrl", "workspace", "account"); handle != "" {
			entry["handle"] = handle
		}
		handles = append(handles, entry)
	}
	if len(handles) > 0 {
		out["integration_handles"] = handles
	}
}
