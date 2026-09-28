package httpapi

import "testing"

func TestPickWorkspaceRepoWithPersonalOwnerAcceptsBarePersonalRepositoryName(t *testing.T) {
	owner, repo, err := pickWorkspaceRepoWithPersonalOwner(
		"",
		"calculator-owner",
		[]repoRef{{Name: "calculator-workspace"}},
		"calculator-workspace",
	)
	if err != nil {
		t.Fatalf("pickWorkspaceRepoWithPersonalOwner: %v", err)
	}
	if owner != "calculator-owner" || repo != "calculator-workspace" {
		t.Fatalf("got %q/%q, want calculator-owner/calculator-workspace", owner, repo)
	}
}

func TestPickWorkspaceRepoWithPersonalOwnerKeepsConfiguredOrganization(t *testing.T) {
	owner, repo, err := pickWorkspaceRepoWithPersonalOwner(
		"acme",
		"personal-user",
		[]repoRef{{Name: "calculator-workspace"}},
		"calculator-workspace",
	)
	if err != nil {
		t.Fatalf("pickWorkspaceRepoWithPersonalOwner: %v", err)
	}
	if owner != "acme" || repo != "calculator-workspace" {
		t.Fatalf("got %q/%q, want acme/calculator-workspace", owner, repo)
	}
}
