package integrations

import "testing"

func TestNormalizeFigmaScopesDropsInvalidAppScopes(t *testing.T) {
	t.Parallel()
	got := normalizeFigmaScopes(
		"current_user:read,file_comments:read,file_comments:write,file_content:read," +
			"file_dev_resources:read,file_dev_resources:write,file_metadata:read,file_versions:read," +
			"folders:read,folder_metadata:read,library_assets:read,library_content:read," +
			"selections:read,team_library_content:read,webhooks:read,webhooks:write,files:read,projects:read",
	)
	want := "current_user:read,file_content:read,file_metadata:read,webhooks:write"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestNormalizeFigmaScopesEmptyFallsBackToDefault(t *testing.T) {
	t.Parallel()
	if got := normalizeFigmaScopes(""); got != figmaDefaultScopes {
		t.Fatalf("got %q", got)
	}
}
