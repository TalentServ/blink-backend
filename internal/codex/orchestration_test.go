package codex

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestExecuteHybridStoryPlanRespectsDependenciesAndConcurrency(t *testing.T) {
	var mu sync.Mutex
	active, maxActive := 0, 0
	started := map[string]time.Time{}
	_, err := ExecuteHybridStoryPlan(context.Background(), []StoryPlan{
		{IssueID: "A"},
		{IssueID: "B"},
		{IssueID: "C", DependsOn: []string{"A"}},
	}, 2, func(_ context.Context, story StoryPlan) (map[string]interface{}, error) {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		started[story.IssueID] = time.Now()
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return map[string]interface{}{"issueId": story.IssueID}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if maxActive > 2 {
		t.Fatalf("max concurrency = %d, want <= 2", maxActive)
	}
	if !started["C"].After(started["A"]) {
		t.Fatalf("dependent story C started before A")
	}
}

func TestBuildHybridStoryPlanRejectsUnknownDependency(t *testing.T) {
	_, err := BuildHybridStoryPlan([]StoryPlan{{IssueID: "A", DependsOn: []string{"missing"}}})
	if err == nil {
		t.Fatal("expected unknown dependency error")
	}
}
