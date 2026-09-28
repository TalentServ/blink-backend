package canonical

import "strings"

// Nine-stage spine (Home excluded). Ship consolidates workspace/implementation/review/release.
var StageOrder = []string{
	"project-stakeholders",
	"integrations",
	"requirements",
	"stakeholder-qa",
	"project-shape",
	"repositories",
	"technology-per-repo",
	"sdlc-plan",
	"ship",
}

func stageIndex(step string) int {
	for i, s := range StageOrder {
		if s == step {
			return i
		}
	}
	// Legacy ship substages map to ship index.
	switch step {
	case "generation", "implementation", "review-pr", "release":
		return len(StageOrder) - 1
	case "welcome", "home":
		return -1
	default:
		return -1
	}
}

// mapCompletedThroughIndex converts wizard STEP_ORDER indices (welcome=0) to nine-stage indices.
func mapCompletedThroughIndex(completedThrough *int) int {
	if completedThrough == nil || *completedThrough <= 0 {
		return 0
	}
	t := *completedThrough
	// welcome only
	if t == 0 {
		return 0
	}
	// Legacy 13-step spine (through index up to release)
	if t >= 12 {
		return len(StageOrder) - 1
	}
	// v7 spine: welcome + 9 stages (ship at index 9)
	if t >= 9 {
		return len(StageOrder) - 1
	}
	// Exclude welcome from stage count
	idx := t - 1
	if idx < 0 {
		return 0
	}
	if idx >= len(StageOrder) {
		return len(StageOrder) - 1
	}
	return idx
}

func normalizeWizardStep(step string) string {
	switch step {
	case "generation", "implementation", "review-pr", "release":
		return "ship"
	case "sdlc-scope":
		return "requirements"
	case "welcome", "":
		return "project-stakeholders"
	default:
		return step
	}
}

type Eligibility struct {
	SpineVersion         string   `json:"spineVersion"`
	CurrentStep          string   `json:"currentStep"`
	CompletedThrough     string   `json:"completedThrough"`
	CompletedIndex       int      `json:"completedIndex"`
	AllowedSteps         []string `json:"allowedSteps"`
	ShipSubstage         string   `json:"shipSubstage,omitempty"`
	AllowedShipSubstages []string `json:"allowedShipSubstages,omitempty"`
	MaxShipSubstage      string   `json:"maxShipSubstage,omitempty"`
}

func ComputeEligibility(wizardStep *string, completedThrough *int, wizardState []byte) Eligibility {
	step := "project-stakeholders"
	if wizardStep != nil && *wizardStep != "" {
		step = normalizeWizardStep(*wizardStep)
	}
	completedIdx := mapCompletedThroughIndex(completedThrough)
	completedStep := StageOrder[0]
	if completedIdx < len(StageOrder) {
		completedStep = StageOrder[completedIdx]
	} else if len(StageOrder) > 0 {
		completedStep = StageOrder[len(StageOrder)-1]
	}

	curIdx := stageIndex(step)
	if curIdx < 0 {
		curIdx = 0
	}
	maxNav := completedIdx
	if curIdx > maxNav {
		maxNav = curIdx
	}
	allowed := make([]string, 0, maxNav+1)
	for i := 0; i <= maxNav && i < len(StageOrder); i++ {
		allowed = append(allowed, StageOrder[i])
	}

	substage := ""
	if wizardStep != nil {
		switch *wizardStep {
		case "generation", "review-resolve", "project-preview", "ide-and-tools":
			substage = "workspace"
		case "implementation":
			substage = "implementation"
		case "review-pr":
			substage = "review-pr"
		case "release", "platform-delivery":
			substage = "release"
		}
	}
	if step == "ship" && substage == "" {
		substage = "workspace"
	}
	root := parseWizard(wizardState)
	if ws := wizardString(root, "shipSubstage"); ws != "" && step == "ship" {
		substage = strings.ToLower(strings.TrimSpace(ws))
	}
	if cs := wizardString(root, "canonicalShipSubstage"); cs != "" && step == "ship" && substage == "workspace" {
		substage = strings.ToLower(strings.TrimSpace(cs))
	}

	return Eligibility{
		SpineVersion:     "nine-stage-v1",
		CurrentStep:      step,
		CompletedThrough: completedStep,
		CompletedIndex:   completedIdx,
		AllowedSteps:     allowed,
		ShipSubstage:     substage,
	}
}
