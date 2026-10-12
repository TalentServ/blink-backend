package zipkit

import "strings"
import "testing"

func TestMcpJSONAddsOfficialAtlassianWhenJiraConnected(t *testing.T) {
	body := mcpJSON([]string{"jira"}, osPortable, SiteHints{})
	if !strings.Contains(body, `"atlassian"`) {
		t.Fatal("jira integration must add the official Atlassian MCP")
	}
	if !strings.Contains(body, "https://mcp.atlassian.com/v2/mcp") {
		t.Fatalf("missing official Atlassian MCP URL, got:\n%s", body)
	}
	if strings.Contains(body, "mcp-atlassian") || strings.Contains(body, `"jira"`) {
		t.Fatalf("Blink Jira must not emit token-based mcp-atlassian, got:\n%s", body)
	}
}

func TestMcpJSONAcceptsAtlassianAlias(t *testing.T) {
	body := mcpJSON([]string{"atlassian"}, osUnix, SiteHints{})
	if !strings.Contains(body, "https://mcp.atlassian.com/v2/mcp") {
		t.Fatalf("atlassian alias should emit official MCP, got:\n%s", body)
	}
}

func TestMcpJSONOmitsAtlassianWhenNotConnected(t *testing.T) {
	body := mcpJSON([]string{"github"}, osPortable, SiteHints{})
	if strings.Contains(body, "mcp.atlassian.com") {
		t.Fatal("Atlassian MCP must not appear unless Jira/Atlassian is connected")
	}
}

func TestMcpJSONPinsAtlassianSiteFromJiraHint(t *testing.T) {
	body := mcpJSON([]string{"jira"}, osPortable, SiteHints{JiraURL: "https://talentserv.atlassian.net/jira"})
	if !strings.Contains(body, "https://mcp.atlassian.com/v2/mcp?site=talentserv.atlassian.net") {
		t.Fatalf("expected pinned Atlassian site, got:\n%s", body)
	}
}
