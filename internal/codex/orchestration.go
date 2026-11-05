package codex

import (
	"context"
	"fmt"

	"github.com/ai-sdlc-framework/ai-sdlc/sdk-go/agents"
)

// StoryPlan describes one story execution and its prerequisite stories.
type StoryPlan struct {
	IssueID   string
	DependsOn []string
}

// BuildHybridStoryPlan reuses the AI-SDLC dependency-aware orchestration
// resource for Blink story execution. The actual provider remains injected by
// the caller, so this package does not become coupled to OpenAI.
func BuildHybridStoryPlan(stories []StoryPlan) (*agents.OrchestrationPlan, error) {
	seen := map[string]struct{}{}
	steps := make([]agents.OrchestrationStep, 0, len(stories))
	for _, story := range stories {
		if story.IssueID == "" {
			return nil, fmt.Errorf("story issue id is required")
		}
		if _, exists := seen[story.IssueID]; exists {
			return nil, fmt.Errorf("duplicate story issue id: %s", story.IssueID)
		}
		seen[story.IssueID] = struct{}{}
		steps = append(steps, agents.OrchestrationStep{
			Name:      story.IssueID,
			Agent:     "blink-codex",
			DependsOn: append([]string(nil), story.DependsOn...),
		})
	}
	for _, step := range steps {
		for _, dependency := range step.DependsOn {
			if _, exists := seen[dependency]; !exists {
				return nil, fmt.Errorf("story %s depends on unknown story %s", step.Name, dependency)
			}
		}
	}
	return agents.Hybrid(steps...), nil
}

// ExecuteHybridStoryPlan executes dependency waves through the official
// AI-SDLC executor. A semaphore adds Blink's platform-wide concurrency cap.
func ExecuteHybridStoryPlan(
	ctx context.Context,
	stories []StoryPlan,
	maxConcurrent int,
	execute func(context.Context, StoryPlan) (map[string]interface{}, error),
) (*agents.OrchestrationResult, error) {
	plan, err := BuildHybridStoryPlan(stories)
	if err != nil {
		return nil, err
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	slots := make(chan struct{}, maxConcurrent)
	byID := make(map[string]StoryPlan, len(stories))
	for _, story := range stories {
		byID[story.IssueID] = story
	}
	return agents.ExecuteOrchestration(ctx, plan, func(ctx context.Context, step *agents.OrchestrationStep) (*agents.StepResult, error) {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-slots }()
		output, err := execute(ctx, byID[step.Name])
		if err != nil {
			return &agents.StepResult{StepName: step.Name, Succeeded: false, Error: err.Error()}, nil
		}
		return &agents.StepResult{StepName: step.Name, Succeeded: true, Output: output}, nil
	})
}
