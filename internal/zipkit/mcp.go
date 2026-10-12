package zipkit

import (
	"strings"
)

const (
	unixWrapper    = "${workspaceFolder}/automation_sdlc/scripts/mcp-npx.sh"
	windowsWrapper = "${workspaceFolder}/automation_sdlc/scripts/mcp-npx.ps1"
)

// SiteHints are non-secret wizard values prefilling .env.mcp.example.
type SiteHints struct {
	JiraURL             string
	JiraUsername        string
	ConfluenceURL       string
	ConfluenceUsername  string
	JiraCloudID         string
}

type osProfile struct {
	command string
	prefix  []string
}

var (
	osPortable = osProfile{
		command: "powershell.exe",
		prefix:  []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", windowsWrapper},
	}
	osWindows = osPortable
	osUnix    = osProfile{command: unixWrapper, prefix: nil}
)

func mcpJSON(providers []string, profile osProfile, hints SiteHints) string {
	keys := []string{"filesystem"}
	set := normalizeProviders(providers)
	if set["atlassian"] {
		keys = append(keys, "atlassian")
	}
	for _, p := range []string{"github", "figma"} {
		if set[p] {
			keys = append(keys, p)
		}
	}
	var b strings.Builder
	b.WriteString("{\n  \"mcpServers\": {\n")
	for i, key := range keys {
		appendServer(&b, key, profile, hints)
		if i < len(keys)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("  }\n}\n")
	return b.String()
}

func mcpSetupReadme() string {
	return `# Cursor MCP setup

Cursor loads ` + "`.cursor/mcp.json`" + `. The default file uses the **Windows** PowerShell
wrapper (` + "`mcp-npx.ps1`" + `). macOS/Linux users should switch to ` + "`mcp.unix.json`" + `.

The wrapper:

1. Resolves your unzip folder at runtime (no hard-coded machine paths)
2. Loads secrets from ` + "`automation_sdlc/.env.mcp`" + ` (GitHub / Figma tokens only)
3. When Jira, Confluence, or Atlassian is connected in Blink, ` + "`.cursor/mcp.json`" + ` already has the official Atlassian MCP (` + "`https://mcp.atlassian.com/v2/mcp`" + `). Cursor signs you in — no API token.
4. Runs local MCP packages (filesystem, GitHub, Figma) via ` + "`npm exec`" + `

## Files

| Path | Purpose |
| --- | --- |
| ` + "`.cursor/mcp.json`" + ` | **Default = Windows** (PowerShell + ` + "`mcp-npx.ps1`" + `) |
| ` + "`.cursor/mcp.windows.json`" + ` | Same as default |
| ` + "`.cursor/mcp.unix.json`" + ` | macOS / Linux (` + "`mcp-npx.sh`" + `) |
| ` + "`automation_sdlc/env.mcp.example`" + ` | **Visible** copy (Cursor often hides ` + "`.env*`" + ` files) |
| ` + "`automation_sdlc/.env.mcp.example`" + ` | Same content — copy → ` + "`.env.mcp`" + ` and fill tokens |
| ` + "`automation_sdlc/scripts/mcp-npx.ps1`" + ` | Windows launcher |
| ` + "`automation_sdlc/scripts/mcp-npx.sh`" + ` | macOS / Linux launcher |

## First-time setup (2 minutes)

1. Unzip and **open the ` + "`*_workspace`" + ` folder** in Cursor (File → Open Folder).
2. Copy env file and fill **tokens** (URLs/emails may already be filled from Blink).
   GitHub MCP needs ` + "`GITHUB_PERSONAL_ACCESS_TOKEN`" + ` in ` + "`.env.mcp`" + ` — signing in on Blink
   does not write that token into the zip.

**Windows**
` + "```powershell" + `
Copy-Item automation_sdlc\env.mcp.example automation_sdlc\.env.mcp
notepad automation_sdlc\.env.mcp
` + "```" + `

**macOS / Linux**
` + "```bash" + `
cp .cursor/mcp.unix.json .cursor/mcp.json
cp automation_sdlc/env.mcp.example automation_sdlc/.env.mcp
# edit automation_sdlc/.env.mcp — add tokens
` + "```" + `

If the file tree hides dotfiles, look for ` + "`automation_sdlc/env.mcp.example`" + ` (no leading dot).

3. Restart Cursor (or reload MCP servers).
4. Confirm servers under **Cursor Settings → MCP**.
   Look for ` + "`atlassian`" + ` (not a token-based jira/confluence server). Enable it and sign in with Atlassian when Cursor prompts.

Requires **Node.js 18+** (` + "`node`" + ` / ` + "`npm`" + ` on PATH).

Never commit ` + "`automation_sdlc/.env.mcp`" + `.
`
}

func envMcpExample(providers []string, hints SiteHints) string {
	set := normalizeProviders(providers)
	var b strings.Builder
	b.WriteString("# AI-SDLC MCP env — copy to automation_sdlc/.env.mcp (never commit)\n")
	b.WriteString("# mcp-npx.sh / mcp-npx.ps1 load this file automatically.\n")
	b.WriteString("# Cursor mcp.json keeps secrets as ${env:VAR}; fill token values here.\n")
	b.WriteString("# Non-secret URLs/emails may be pre-filled from the Blink wizard.\n\n")
	if set["github"] {
		b.WriteString("# After unzip: paste a GitHub PAT here for Cursor MCP (Blink login does not copy it).\n")
		b.WriteString("GITHUB_PERSONAL_ACCESS_TOKEN=\n\n")
	}
	if set["figma"] {
		b.WriteString("# After unzip: paste a Figma PAT here for Cursor MCP (Blink login does not copy it).\n")
		b.WriteString("FIGMA_ACCESS_TOKEN=\n\n")
	}
	if set["atlassian"] || set["jira"] || set["confluence"] {
		b.WriteString("# Jira / Confluence / Atlassian in Blink → official Atlassian MCP in .cursor/mcp.json.\n")
		b.WriteString("# Cursor OAuth — do not paste an Atlassian API token here.\n")
		if site := atlassianSiteHost(hints.JiraURL, hints.ConfluenceURL); site != "" {
			b.WriteString("# Connected site: " + site + "\n")
		}
		b.WriteString("\n")
	}
	if !set["github"] && !set["figma"] && !set["jira"] && !set["confluence"] && !set["atlassian"] {
		b.WriteString("# No cloud MCP servers were connected in the wizard.\n")
		b.WriteString("# Connect GitHub / Figma / Jira / Atlassian and re-download, or add servers manually.\n")
	}
	return b.String()
}

func normalizeProviders(connected []string) map[string]bool {
	out := map[string]bool{"filesystem": true}
	for _, raw := range connected {
		id := strings.ToLower(strings.TrimSpace(raw))
		switch id {
		case "github", "figma", "jira", "confluence":
			out[id] = true
		case "atlassian":
			out["atlassian"] = true
			out["jira"] = true
		case "bitbucket":
			out["atlassian"] = true
		}
		if out["jira"] || out["confluence"] {
			out["atlassian"] = true
		}
	}
	return out
}

func appendServer(b *strings.Builder, key string, os osProfile, hints SiteHints) {
	switch key {
	case "filesystem":
		b.WriteString("    \"filesystem\": {\n")
		b.WriteString("      \"command\": \"" + escapeJSON(os.command) + "\",\n")
		appendArgsStart(b, os, "-y", "@modelcontextprotocol/server-filesystem", "${workspaceFolder}")
		b.WriteString(",\n      \"env\": {}\n    }")
	case "atlassian":
		b.WriteString("    \"atlassian\": {\n")
		b.WriteString("      \"url\": \"" + escapeJSON(atlassianMCPURL(hints)) + "\"\n")
		b.WriteString("    }")
	case "github":
		b.WriteString("    \"github\": {\n")
		b.WriteString("      \"command\": \"" + escapeJSON(os.command) + "\",\n")
		appendArgsStart(b, os, "-y", "@modelcontextprotocol/server-github")
		b.WriteString(",\n      \"env\": {\n")
		b.WriteString("        \"GITHUB_PERSONAL_ACCESS_TOKEN\": \"${env:GITHUB_PERSONAL_ACCESS_TOKEN}\"\n")
		b.WriteString("      }\n    }")
	case "figma":
		b.WriteString("    \"figma\": {\n")
		b.WriteString("      \"command\": \"" + escapeJSON(os.command) + "\",\n")
		appendArgsStart(b, os, "-y", "@modelcontextprotocol/server-figma")
		b.WriteString(",\n      \"env\": {\n")
		b.WriteString("        \"FIGMA_ACCESS_TOKEN\": \"${env:FIGMA_ACCESS_TOKEN}\"\n")
		b.WriteString("      }\n    }")
	}
}

func atlassianMCPURL(hints SiteHints) string {
	base := "https://mcp.atlassian.com/v2/mcp"
	if site := atlassianSiteHost(hints.JiraURL, hints.ConfluenceURL); site != "" {
		return base + "?site=" + site
	}
	return base
}

func atlassianSiteHost(urls ...string) string {
	for _, raw := range urls {
		v := strings.TrimSpace(raw)
		v = strings.TrimPrefix(v, "https://")
		v = strings.TrimPrefix(v, "http://")
		if i := strings.Index(v, "/"); i >= 0 {
			v = v[:i]
		}
		v = strings.TrimSpace(strings.ToLower(v))
		if v == "" || strings.Contains(v, "your_org.atlassian.net") {
			continue
		}
		return v
	}
	return ""
}

func appendArgsStart(b *strings.Builder, os osProfile, packageArgs ...string) {
	b.WriteString("      \"args\": [\n")
	for _, p := range os.prefix {
		b.WriteString("        \"" + escapeJSON(p) + "\",\n")
	}
	for i, a := range packageArgs {
		b.WriteString("        \"" + escapeJSON(a) + "\"")
		if i < len(packageArgs)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("      ]")
}

func escapeJSON(v string) string {
	return strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `"`, `\"`)
}
