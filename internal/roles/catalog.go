package roles

// Catalog matches the Framework role catalog (snake_case IDs) used by agents and the UI fallback.
func Catalog() []map[string]any {
	return []map[string]any{
		{"roleCode": "product_owner", "roleName": "Product Owner", "required": true, "displayOrder": 1},
		{"roleCode": "business_analyst", "roleName": "Business Analyst", "required": true, "displayOrder": 2},
		{"roleCode": "ux_designer", "roleName": "UX Designer", "required": false, "displayOrder": 3},
		{"roleCode": "platform_architect", "roleName": "Platform Architect", "required": false, "displayOrder": 4},
		{"roleCode": "tech_lead", "roleName": "Tech Lead", "required": true, "displayOrder": 5},
		{"roleCode": "backend_developer", "roleName": "Backend Developer", "required": false, "displayOrder": 6},
		{"roleCode": "frontend_developer", "roleName": "Frontend Developer", "required": false, "displayOrder": 7},
		{"roleCode": "dba", "roleName": "DBA", "required": false, "displayOrder": 8},
		{"roleCode": "sre", "roleName": "SRE", "required": false, "displayOrder": 9},
		{"roleCode": "qa_lead", "roleName": "QA Lead", "required": false, "displayOrder": 10},
		{"roleCode": "qa_engineer", "roleName": "QA Engineer", "required": false, "displayOrder": 11},
		{"roleCode": "security_champion", "roleName": "Security Champion", "required": false, "displayOrder": 12},
		{"roleCode": "compliance_approver", "roleName": "Compliance Approver", "required": false, "displayOrder": 13},
		{"roleCode": "release_approver", "roleName": "Release Approver", "required": false, "displayOrder": 14},
	}
}
