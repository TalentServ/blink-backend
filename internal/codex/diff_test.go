package codex

import "testing"

func TestDiffMapsAndHunkStage(t *testing.T) {
	before := map[string]string{"src/a.go": "package a\n\nfunc Old() {}\n"}
	after := map[string]string{"src/a.go": "package a\n\nfunc New() {}\n", "src/b.go": "package b\n"}
	diffs := DiffMaps(before, after)
	if len(diffs) != 2 {
		t.Fatalf("diffs: %#v", diffs)
	}
	var changed FileDiff
	for _, d := range diffs {
		if d.Path == "src/a.go" {
			changed = d
		}
	}
	if changed.Additions == 0 || changed.Deletions == 0 || len(changed.Hunks) == 0 {
		t.Fatalf("expected hunks: %+v", changed)
	}
	staged := ApplySelectedHunks(before["src/a.go"], changed.Hunks, map[int]bool{changed.Hunks[0].Index: true})
	if staged == before["src/a.go"] {
		t.Fatal("staging a hunk should change the file")
	}
}

func TestApplyGitActionRevertAndCommit(t *testing.T) {
	base := map[string]string{"a.txt": "one\n"}
	uncommitted := map[string]string{"a.txt": "two\n", "b.txt": "new\n"}
	staged := map[string]string{}
	var err error
	base, uncommitted, staged, _, err = ApplyGitAction(base, uncommitted, staged, "stage", "b.txt", -1)
	if err != nil || staged["b.txt"] != "new\n" {
		t.Fatalf("stage: %v %#v", err, staged)
	}
	base, uncommitted, staged, commit, err := ApplyGitAction(base, uncommitted, staged, "commit_staged", "", -1)
	if err != nil || commit["b.txt"] != "new\n" || base["b.txt"] != "new\n" {
		t.Fatalf("commit: %v base=%#v commit=%#v", err, base, commit)
	}
	if _, ok := uncommitted["b.txt"]; ok {
		t.Fatal("committed file should leave the uncommitted map when it matches")
	}
	_, uncommitted, staged, _, err = ApplyGitAction(base, uncommitted, staged, "revert", "a.txt", -1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := uncommitted["a.txt"]; ok {
		t.Fatal("revert should drop the path back to base")
	}
}
