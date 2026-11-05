package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nisha-ts-40599/blink-backend/internal/codex"
)

const contextBudget = 24 * 1024

type wizardStory struct {
	ID                  string   `json:"id"`
	EpicID              string   `json:"epicId"`
	Title               string   `json:"title"`
	Objective           string   `json:"objective"`
	AsA                 string   `json:"asA"`
	IWant               string   `json:"iWant"`
	SoThat              string   `json:"soThat"`
	AcceptanceCriteria  []string `json:"acceptanceCriteria"`
}

type wizardStateView struct {
	ProductScope *struct {
		Markdown string        `json:"markdown"`
		Stories  []wizardStory `json:"stories"`
	} `json:"productScope"`
	Specification *struct {
		Markdown           string   `json:"markdown"`
		AcceptanceCriteria []string `json:"acceptanceCriteria"`
		Title              string   `json:"title"`
		Summary            string   `json:"summary"`
	} `json:"specification"`
	TechnicalPlan *struct {
		Markdown string `json:"markdown"`
		Summary  string `json:"summary"`
		Steps    []struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"steps"`
	} `json:"technicalPlan"`
	WorkClassification *struct {
		Markdown  string `json:"markdown"`
		WorkType  string `json:"workType"`
		Risk      string `json:"riskSummary"`
	} `json:"workClassification"`
	ArchitectureStyle string   `json:"architectureStyle"`
	RepoTechnologies  []any    `json:"repoTechnologies"`
	Repositories      []repoRef `json:"repositories"`
}

type hydratedCodexContext struct {
	IssueID    string
	Story      *wizardStory
	Vars       codex.PromptVars
	TechStack  []string
	Repos      []repoRef
	Admission  error
}

func hydrateCodexContext(wizard json.RawMessage, body codexBody, issueID string) hydratedCodexContext {
	out := hydratedCodexContext{
		IssueID:   strings.TrimSpace(issueID),
		TechStack: append([]string{}, body.TechStack...),
		Repos:     append([]repoRef{}, body.Repositories...),
		Vars:      codex.PromptVars{},
	}
	var view wizardStateView
	if len(wizard) > 0 {
		_ = json.Unmarshal(wizard, &view)
	}
	if len(out.Repos) == 0 && len(view.Repositories) > 0 {
		out.Repos = view.Repositories
	}
	if len(out.TechStack) == 0 {
		out.TechStack = techStackFromWizard(view.RepoTechnologies)
	}

	var story *wizardStory
	if view.ProductScope != nil {
		for i := range view.ProductScope.Stories {
			s := view.ProductScope.Stories[i]
			if strings.EqualFold(strings.TrimSpace(s.ID), out.IssueID) || strings.EqualFold(strings.TrimSpace(s.Title), out.IssueID) {
				story = &s
				break
			}
		}
	}
	out.Story = story

	ac := []string{}
	if story != nil {
		ac = append(ac, story.AcceptanceCriteria...)
	}
	if view.Specification != nil && len(ac) == 0 {
		ac = append(ac, view.Specification.AcceptanceCriteria...)
	}
	if strings.TrimSpace(body.Acceptance) != "" && len(ac) == 0 {
		ac = []string{body.Acceptance}
	}

	var ctxBuilder strings.Builder
	if story != nil {
		fmt.Fprintf(&ctxBuilder, "Story %s: %s\n", story.ID, story.Title)
		if story.AsA != "" {
			fmt.Fprintf(&ctxBuilder, "As a %s, I want %s so that %s\n", story.AsA, story.IWant, story.SoThat)
		}
		if story.Objective != "" {
			fmt.Fprintf(&ctxBuilder, "Objective: %s\n", story.Objective)
		}
	}
	if view.ProductScope != nil && view.ProductScope.Markdown != "" {
		ctxBuilder.WriteString("\n## Product scope\n")
		ctxBuilder.WriteString(truncateStr(view.ProductScope.Markdown, contextBudget))
	}
	if view.Specification != nil && view.Specification.Markdown != "" {
		ctxBuilder.WriteString("\n## Specification\n")
		ctxBuilder.WriteString(truncateStr(view.Specification.Markdown, contextBudget))
	}
	if view.TechnicalPlan != nil {
		if view.TechnicalPlan.Markdown != "" {
			ctxBuilder.WriteString("\n## Technical plan\n")
			ctxBuilder.WriteString(truncateStr(view.TechnicalPlan.Markdown, contextBudget))
		}
		for _, step := range view.TechnicalPlan.Steps {
			if strings.EqualFold(strings.TrimSpace(step.ID), out.IssueID) || strings.Contains(strings.ToLower(step.Title), strings.ToLower(out.IssueID)) {
				fmt.Fprintf(&ctxBuilder, "\nMatching plan step: %s — %s\n%s\n", step.ID, step.Title, step.Detail)
			}
		}
	}
	if view.WorkClassification != nil && view.WorkClassification.Markdown != "" {
		ctxBuilder.WriteString("\n## Work classification\n")
		ctxBuilder.WriteString(truncateStr(view.WorkClassification.Markdown, 4000))
	}
	if view.ArchitectureStyle != "" {
		fmt.Fprintf(&ctxBuilder, "\nArchitecture: %s\n", view.ArchitectureStyle)
	}
	if strings.TrimSpace(body.ProjectContext) != "" && ctxBuilder.Len() == 0 {
		ctxBuilder.WriteString(body.ProjectContext)
	}

	if out.IssueID == "" || out.IssueID == "unassigned" {
		out.Admission = fmt.Errorf("issueId is required")
	} else if story == nil && (view.ProductScope == nil || len(view.ProductScope.Stories) == 0) && strings.TrimSpace(body.ProjectContext) == "" && ctxBuilder.Len() == 0 {
		out.Admission = fmt.Errorf("WizardState is missing story %s (save the project so Neon has product scope)", out.IssueID)
	} else if len(out.Repos) == 0 && len(body.Repositories) == 0 {
		// repos can still be resolved from the project roster later
	}

	out.Vars = codex.PromptVars{
		"PROJECT_CONTEXT":     ctxBuilder.String(),
		"ISSUE":               out.IssueID,
		"CURRENT_STEP":        firstNonEmpty(body.CurrentStep, "implement"),
		"REPOSITORY_CONTEXT":  firstNonEmpty(body.RepositoryContext, repoContext(out.Repos)),
		"TECH_STACK":          strings.Join(out.TechStack, ", "),
		"SECURITY_MODEL":      body.SecurityModel,
		"WORKFLOW_STATE":      body.WorkflowState,
		"ACCEPTANCE_CRITERIA": strings.Join(ac, "\n- "),
		"PR_CONTEXT":          "",
		"VALIDATION_RESULTS":  body.ValidationResults,
		"APPROVAL_RULES":      body.ApprovalRules,
	}
	if strings.TrimSpace(out.Vars["ACCEPTANCE_CRITERIA"]) != "" && !strings.HasPrefix(out.Vars["ACCEPTANCE_CRITERIA"], "- ") {
		out.Vars["ACCEPTANCE_CRITERIA"] = "- " + out.Vars["ACCEPTANCE_CRITERIA"]
	}
	return out
}

func repoContext(repos []repoRef) string {
	if len(repos) == 0 {
		return ""
	}
	parts := make([]string, 0, len(repos))
	for _, r := range repos {
		parts = append(parts, strings.TrimSpace(r.Name+" "+r.HTMLURL+" "+r.Purpose))
	}
	return strings.Join(parts, "\n")
}

func techStackFromWizard(raw []any) []string {
	out := []string{}
	for _, item := range raw {
		switch v := item.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				out = append(out, v)
			}
		case map[string]any:
			for _, key := range []string{"language", "framework", "name", "stack"} {
				if s, _ := v[key].(string); strings.TrimSpace(s) != "" {
					out = append(out, s)
				}
			}
		}
	}
	return uniqueKeep(out)
}

func uniqueKeep(in []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		k := strings.ToLower(s)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, s)
	}
	return out
}
