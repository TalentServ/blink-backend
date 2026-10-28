package storyrun

import (
	"regexp"
	"strings"
)

// Decision is the machine tier for one story. Tier 1 can continue without a person.
// Tier 2 and above stop until a person approves.
type Decision struct {
	Tier    int
	Status  string
	Reasons []string
}

var tier3Pattern = regexp.MustCompile(`(?i)\b(migration|schema|oauth|auth(entication|orization)?|payment|billing|production|iam|secret|credential|pii|personal data|drop table|delete all|kubernetes|breaking change)\b`)

var tier2Pattern = regexp.MustCompile(`(?i)\b(database|multi-service|cross-service|permission|integration|public api)\b`)

// Classify decides the tier from the requirement text alone.
// Empty text is blocked. Risky words stop for a person. Everything else is Tier 1.
func Classify(requirement string) Decision {
	text := strings.TrimSpace(requirement)
	if text == "" {
		return Decision{
			Tier:    0,
			Status:  "blocked",
			Reasons: []string{"Add a requirement before running the story."},
		}
	}
	if hit := firstMatch(tier3Pattern, text); hit != "" {
		return Decision{
			Tier:   3,
			Status: "needs_person",
			Reasons: []string{
				"The requirement mentions " + hit + ".",
				"Tier 3 stops for a person before any pull request is opened.",
			},
		}
	}
	if hit := firstMatch(tier2Pattern, text); hit != "" {
		return Decision{
			Tier:   2,
			Status: "needs_person",
			Reasons: []string{
				"The requirement mentions " + hit + ".",
				"Tier 2 stops for a plan approval before implementation.",
			},
		}
	}
	return Decision{
		Tier:   1,
		Status: "preview_ready",
		Reasons: []string{
			"One local change with no auth, migration, or production risk.",
			"Tier 1 can be previewed and implemented without an extra approval.",
		},
	}
}

func firstMatch(pattern *regexp.Regexp, text string) string {
	found := pattern.FindString(text)
	return strings.ToLower(strings.TrimSpace(found))
}
