package canonical

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ShapeFingerprint matches the Blink UI shapeFingerprint (topology::repos::tech string).
func ShapeFingerprint(wizardState []byte) string {
	root := parseWizard(wizardState)
	topology := wizardString(root, "topology")
	repoModel := wizardString(root, "repositoryModel")
	arch := wizardString(root, "architectureStyle")

	repoParts := make([]string, 0)
	for _, item := range wizardArray(root, "repositories") {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		name, _ := m["name"].(string)
		repoParts = append(repoParts, fmt.Sprintf("%s:%s", id, strings.TrimSpace(name)))
	}
	techParts := make([]string, 0)
	for _, item := range wizardArray(root, "repoTechnologies") {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		techParts = append(techParts, fmt.Sprintf("%s:%s:%s:%s:%s:%s",
			strField(m, "repoId"),
			strField(m, "language"),
			strField(m, "framework"),
			strField(m, "database"),
			strField(m, "buildTool"),
			strField(m, "status"),
		))
	}
	return strings.Join([]string{topology, repoModel, arch, strings.Join(repoParts, "|"), strings.Join(techParts, "|")}, "::")
}

// WorkPlanPackageDigest matches the UI workPlanPackageDigest (FNV-1a over JSON subset).
func WorkPlanPackageDigest(wizardState []byte) string {
	root := parseWizard(wizardState)
	tier := ""
	if wc, ok := root["workClassification"].(map[string]any); ok {
		tier, _ = wc["tier"].(string)
	}
	specMD := ""
	if spec, ok := root["specification"].(map[string]any); ok {
		if md, _ := spec["markdown"].(string); md != "" {
			specMD = truncateRunes(md, 500)
		}
	}
	planMD := ""
	if plan, ok := root["technicalPlan"].(map[string]any); ok {
		if md, _ := plan["markdown"].(string); md != "" {
			planMD = truncateRunes(md, 500)
		}
	}
	ac, _ := root["acceptanceCriteriaAcknowledged"].(bool)
	payload, _ := json.Marshal(map[string]any{
		"tier": tier,
		"spec": specMD,
		"plan": planMD,
		"ac":   ac,
	})
	return Fnv1aHex(string(payload))
}

// GroomSessionDigest matches the UI groomSessionDigest for G-GROOM invalidation.
func GroomSessionDigest(wizardState []byte) string {
	root := parseWizard(wizardState)
	type qRow struct {
		ID             string `json:"id"`
		Mandatory      bool   `json:"mandatory"`
		AssignedRoleID string `json:"assignedRoleId"`
	}
	type rRow struct {
		QuestionID string `json:"questionId"`
		Status     string `json:"status"`
		Response   string `json:"response"`
	}
	questions := make([]qRow, 0)
	for _, item := range wizardArray(root, "questions") {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		questions = append(questions, qRow{
			ID:             strField(m, "id"),
			Mandatory:      wizardBool(m, "mandatory"),
			AssignedRoleID: strField(m, "assignedRoleId"),
		})
	}
	sort.Slice(questions, func(i, j int) bool { return questions[i].ID < questions[j].ID })

	responses := make([]rRow, 0)
	for _, item := range wizardArray(root, "responses") {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		responses = append(responses, rRow{
			QuestionID: strField(m, "questionId"),
			Status:     strField(m, "status"),
			Response:   strings.TrimSpace(strField(m, "response")),
		})
	}
	sort.Slice(responses, func(i, j int) bool { return responses[i].QuestionID < responses[j].QuestionID })

	payload, _ := json.Marshal(map[string]any{
		"questions":  questions,
		"responses":  responses,
		"groomDraft": strings.TrimSpace(wizardString(root, "groomDraft")),
	})
	return Fnv1aHex(string(payload))
}

func truncateRunes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}

// Fnv1aHex matches the Blink UI fnv1a helper (8-digit hex, not crypto).
func Fnv1aHex(raw string) string {
	var hash uint32 = 2166136261
	for i := 0; i < len(raw); i++ {
		hash ^= uint32(raw[i])
		hash *= 16777619
	}
	return fmt.Sprintf("%08x", hash)
}
