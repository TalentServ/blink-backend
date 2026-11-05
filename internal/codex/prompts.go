package codex

import (
	"fmt"
	"strings"
)

// PromptStem maps Blink workflow kinds to harness templates.
const (
	PromptImplementStep     = "implement-step"
	PromptAIDeveloperReview = "ai-developer-review"
	PromptPRReview          = "pr-review"
	PromptFixReviewFindings = "fix-review-findings"
)

// PromptVars are substituted into {{PLACEHOLDER}} tokens.
type PromptVars map[string]string

// RenderPrompt replaces {{KEY}} placeholders. Missing keys become empty strings.
func RenderPrompt(template string, vars PromptVars) string {
	out := template
	if vars == nil {
		vars = PromptVars{}
	}
	keys := []string{
		"PROJECT_CONTEXT", "ISSUE", "CURRENT_STEP", "REPOSITORY_CONTEXT",
		"TECH_STACK", "SECURITY_MODEL", "WORKFLOW_STATE", "ACCEPTANCE_CRITERIA",
		"PR_CONTEXT", "VALIDATION_RESULTS", "APPROVAL_RULES", "ENGINEERING_GUARDRAILS",
		"IMPLEMENTATION_EVIDENCE", "ARTIFACTS", "RISK_TIER",
	}
	seen := map[string]struct{}{}
	for _, k := range keys {
		seen[k] = struct{}{}
		out = strings.ReplaceAll(out, "{{"+k+"}}", vars[k])
	}
	for k, v := range vars {
		if _, ok := seen[k]; ok {
			continue
		}
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

// BuildInstructions returns the Blink-owned Codex harness prompt for a workflow stem.
func BuildInstructions(aisdlcRoot, stem string, vars PromptVars) (string, error) {
	if vars == nil {
		vars = PromptVars{}
	}
	body := RenderPrompt(blinkHarnessPrompt(stem), vars)
	if strings.TrimSpace(aisdlcRoot) != "" && !LooksLikeAISDLC(aisdlcRoot) {
		// Official files are mounted as capabilities; missing root is non-fatal here
		// because StageWorkspace / BuildCapabilityFiles fail closed separately.
	}
	return body, nil
}

func blinkHarnessPrompt(stem string) string {
	common := strings.TrimSpace(`
# Blink Codex Cloud harness (overrides official local-CLI agent files)

You are running in an OpenAI-hosted sandbox. blink-backend is the control plane.

## Environment
- Workspace root: /workspace
- Product repositories: /workspace/repos/<hint> (already extracted; each has a baseline git commit)
- Official AI-SDLC skills (read-only reference): /workspace/capabilities/ai-sdlc/
- Ephemeral governance (outside the repo, do not copy into the repo): /workspace/.ai-sdlc/ and /workspace/AGENTS.md
- Durable output: /workspace/outputs/
- Network access: DISABLED. Do not pip, npm i, go get, curl, wget, gh, git push, git remote, pnpm, pipeline-cli, make ai-sdlc-*, or codex exec.

## Hard rules
1. Edit and create product source and tests only under /workspace/repos/<hint>.
2. Do not write .ai-sdlc/, AGENTS.md, capabilities/, .cursor/, .env*, or *.pem into a repo tree.
3. Do not open pull requests, push, or talk to GitHub. blink-backend will commit after gates pass.
4. Official developer.md / reviewer markdown is reference for quality bar and JSON field names only. This harness overrides their local git/gh/pnpm/make instructions.
5. Prefer local verification only if the tool is already installed. If a command needs the network, skip it and record that in verifications.

## Context
- Issue: {{ISSUE}}
- Current step: {{CURRENT_STEP}}
- Tech stack: {{TECH_STACK}}
- Security model: {{SECURITY_MODEL}}
- Project context:
{{PROJECT_CONTEXT}}
- Repository context:
{{REPOSITORY_CONTEXT}}
- Acceptance criteria:
{{ACCEPTANCE_CRITERIA}}
- Workflow notes:
{{WORKFLOW_STATE}}
`)

	switch stem {
	case PromptPRReview, PromptAIDeveloperReview:
		return common + "\n\n" + strings.TrimSpace(`
## Task
Review the staged repository and PR context. Use the official code-reviewer, test-reviewer, and security-reviewer skills as the quality bar.

PR context:
{{PR_CONTEXT}}

Validation:
{{VALIDATION_RESULTS}}

Approval rules:
{{APPROVAL_RULES}}

Final assistant message MUST be a single JSON object (also write it to /workspace/outputs/blink-result.json):
{
  "approved": false,
  "recommendation": "READY|NEEDS_FIXES|BLOCKED",
  "summary": "...",
  "findings": [
    {"id": "F-1", "severity": "critical|major|minor|suggestion", "category": "code|test|security", "file": "path", "line": 1, "message": "..."}
  ]
}
Never claim human merge approval. AI review is advisory.
`)
	case PromptFixReviewFindings:
		return common + "\n\n" + strings.TrimSpace(`
## Task
Fix ONLY the human-approved review findings. Do not expand scope.

Approved findings:
{{IMPLEMENTATION_EVIDENCE}}

Final assistant message MUST be a single JSON object (also write it to /workspace/outputs/blink-result.json):
{
  "summary": "...",
  "commitMessage": "fix: ... ({{ISSUE}})",
  "filesChanged": ["path"],
  "files": [{"path": "repo-relative-path", "content": "full file contents", "repoHint": "frontend|backend|agent|docs|all"}],
  "verifications": {"build": "skipped-offline", "test": "...", "lint": "..."},
  "acceptanceCriteriaMet": ["..."],
  "notes": "..."
}
`)
	default:
		return common + "\n\n" + strings.TrimSpace(`
## Task
Implement the approved plan step for {{ISSUE}}. Apply code and tests under /workspace/repos/<hint>.

Final assistant message MUST be a single JSON object (also write it to /workspace/outputs/blink-result.json):
{
  "summary": "...",
  "commitMessage": "feat: ... ({{ISSUE}})",
  "filesChanged": ["path"],
  "files": [{"path": "repo-relative-path", "content": "full file contents", "repoHint": "frontend|backend|agent|docs|all"}],
  "verifications": {"build": "skipped-offline", "test": "...", "lint": "..."},
  "acceptanceCriteriaMet": ["..."],
  "notes": "..."
}

blink-export exists at /usr/local/bin/blink-export and can rewrite blink-result.json from git diff. You do not have to run it if the JSON above includes full file contents.
`)
	}
}

func blinkOutputContract(stem string) string {
	return fmt.Sprintf("workflow=%s", stem)
}
