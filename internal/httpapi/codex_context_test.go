package httpapi

import (
	"strings"
	"testing"
)

func TestHydrateCodexContextFromWizardState(t *testing.T) {
	wizard := []byte(`{
		"productScope": {
			"markdown": "A calculator",
			"stories": [{
				"id": "CALC-1",
				"title": "Add numbers",
				"asA": "user",
				"iWant": "to add",
				"soThat": "I get a sum",
				"acceptanceCriteria": ["returns 2 for 1+1"]
			}]
		},
		"specification": {"markdown": "Spec body that should appear"},
		"technicalPlan": {"markdown": "Plan body"},
		"repositories": [{"name": "app", "htmlUrl": "https://github.com/acme/app", "purpose": "backend"}]
	}`)
	got := hydrateCodexContext(wizard, codexBody{IssueID: "CALC-1"}, "CALC-1")
	if got.Admission != nil {
		t.Fatalf("admission: %v", got.Admission)
	}
	if got.Story == nil || got.Story.Title != "Add numbers" {
		t.Fatalf("story not hydrated: %#v", got.Story)
	}
	if !strings.Contains(got.Vars["PROJECT_CONTEXT"], "Add numbers") {
		t.Fatalf("missing story in context: %s", got.Vars["PROJECT_CONTEXT"])
	}
	if !strings.Contains(got.Vars["ACCEPTANCE_CRITERIA"], "returns 2") {
		t.Fatalf("missing AC: %s", got.Vars["ACCEPTANCE_CRITERIA"])
	}
	if !strings.Contains(got.Vars["PROJECT_CONTEXT"], "Spec body") {
		t.Fatalf("missing spec: %s", got.Vars["PROJECT_CONTEXT"])
	}
}

func TestHydrateCodexContextMissingIssue(t *testing.T) {
	got := hydrateCodexContext(nil, codexBody{}, "")
	if got.Admission == nil {
		t.Fatal("expected admission failure")
	}
}
