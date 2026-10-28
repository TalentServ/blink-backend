package storyrun

import (
	"strings"
	"testing"
)

func TestClassifyTier1(t *testing.T) {
	got := Classify("Update the settings tooltip so the save hint is clearer.")
	if got.Tier != 1 || got.Status != "preview_ready" {
		t.Fatalf("got tier %d status %s", got.Tier, got.Status)
	}
}

func TestClassifyStopsForMigration(t *testing.T) {
	got := Classify("Add a production database migration for the billing table.")
	if got.Tier != 3 || got.Status != "needs_person" {
		t.Fatalf("got tier %d status %s", got.Tier, got.Status)
	}
}

func TestClassifyStopsForDatabase(t *testing.T) {
	got := Classify("Store the preference in the database for this screen.")
	if got.Tier != 2 || got.Status != "needs_person" {
		t.Fatalf("got tier %d status %s", got.Tier, got.Status)
	}
}

func TestClassifyEmptyIsBlocked(t *testing.T) {
	got := Classify("  ")
	if got.Status != "blocked" {
		t.Fatalf("status %s", got.Status)
	}
}

func TestPreviewEscapesRequirement(t *testing.T) {
	html := PreviewHTML("Tip", `Click <script>alert("x")</script> to save.`, nil)
	if strings.Contains(html, "<script>alert") {
		t.Fatal("requirement script was embedded raw")
	}
	if !strings.Contains(html, "Unmerged preview") {
		t.Fatal("preview shell missing")
	}
}
