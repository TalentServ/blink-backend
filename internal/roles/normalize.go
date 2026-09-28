package roles

import "strings"

var legacyRoleMap = map[string]string{
	"PRODUCT_OWNER":     "product_owner",
	"PROJECT_MANAGER":   "business_analyst",
	"TECH_LEAD":         "tech_lead",
	"QA_LEAD":           "qa_lead",
	"BUSINESS_ANALYST":  "business_analyst",
	"DESIGNER":          "ux_designer",
	"DEVOPS":            "sre",
	"STAKEHOLDER":       "business_analyst",
	"PO":                "product_owner",
	"BA":                "business_analyst",
	"SC":                "tech_lead",
}

// NormalizeRoleCode maps legacy Blink/Java IDs to Framework snake_case role IDs.
func NormalizeRoleCode(code string) string {
	c := strings.TrimSpace(code)
	if c == "" {
		return c
	}
	lower := strings.ToLower(c)
	if mapped, ok := legacyRoleMap[strings.ToUpper(c)]; ok {
		return mapped
	}
	if mapped, ok := legacyRoleMap[c]; ok {
		return mapped
	}
	return lower
}

func RoleDisplayName(code string) string {
	normalized := NormalizeRoleCode(code)
	for _, row := range Catalog() {
		if row["roleCode"] == normalized {
			if name, ok := row["roleName"].(string); ok {
				return name
			}
		}
	}
	return strings.ReplaceAll(normalized, "_", " ")
}
