package codex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	CapabilityRootWorkspace = "/workspace/capabilities/ai-sdlc"
	CapabilityDirRelative   = "capabilities/ai-sdlc"
)

// SkillSpec describes one capability skill staged into the sandbox.
type SkillSpec struct {
	Name        string
	Description string
	RelPath     string
}

// DefaultSkills are official plugin files used as read-only reference.
func DefaultSkills() []SkillSpec {
	return []SkillSpec{
		{Name: "ai-sdlc-governance", Description: "Official AI-SDLC governance skill.", RelPath: upstreamSkillGov},
		{Name: "decision-rubric", Description: "Official decision rubric.", RelPath: upstreamSkillRubric},
		{Name: "developer", Description: "Official developer agent (reference only).", RelPath: upstreamDeveloper},
		{Name: "code-reviewer", Description: "Official code reviewer (reference only).", RelPath: upstreamCodeReviewer},
		{Name: "test-reviewer", Description: "Official test reviewer (reference only).", RelPath: upstreamTestReviewer},
		{Name: "security-reviewer", Description: "Official security reviewer (reference only).", RelPath: upstreamSecReviewer},
	}
}

func skillsForKind(kind WorkflowKind) []SkillSpec {
	all := DefaultSkills()
	switch kind {
	case WorkflowPRReview:
		return []SkillSpec{all[0], all[1], all[3], all[4], all[5]}
	case WorkflowFixReviewFindings, WorkflowImplementStep, WorkflowImplementBatch:
		return []SkillSpec{all[0], all[1], all[2]}
	default:
		return all
	}
}

// StagedFile is a sandbox workspace file (absolute path under /workspace).
type StagedFile struct {
	Path    string
	Content []byte
}

// BuildCapabilityFiles stages official plugin markdown under /workspace/capabilities/ai-sdlc.
func BuildCapabilityFiles(aisdlcRoot string, skills []SkillSpec) ([]StagedFile, error) {
	if strings.TrimSpace(aisdlcRoot) == "" {
		return nil, fmt.Errorf("ai-sdlc root is empty")
	}
	if len(skills) == 0 {
		skills = DefaultSkills()
	}
	out := make([]StagedFile, 0, len(skills)+2)
	for _, skill := range skills {
		raw, err := os.ReadFile(filepath.Join(aisdlcRoot, filepath.FromSlash(skill.RelPath)))
		if err != nil {
			return nil, fmt.Errorf("load official skill %s: %w", skill.Name, err)
		}
		out = append(out, StagedFile{
			Path:    path.Join(CapabilityRootWorkspace, skill.Name, "SKILL.md"),
			Content: []byte(formatOfficialSkill(skill, string(raw))),
		})
	}
	return out, nil
}

func formatOfficialSkill(skill SkillSpec, body string) string {
	if strings.HasPrefix(strings.TrimSpace(body), "---") {
		return strings.TrimSpace(body) + "\n\n## Blink harness note\n\nThis file is read-only reference. Follow the Blink Codex harness instructions, not local gh/pnpm/make/codex-exec steps.\n"
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + skill.Name + "\n")
	b.WriteString("description: " + yamlSingleLine(skill.Description) + "\n")
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n\n## Blink harness note\n\nThis file is read-only reference. Follow the Blink Codex harness instructions, not local gh/pnpm/make/codex-exec steps.\n")
	return b.String()
}

func yamlSingleLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\"", "'")
	return s
}

// BuildEphemeralGovernanceFiles writes .ai-sdlc JSON + AGENTS.md outside repo trees.
func BuildEphemeralGovernanceFiles(bundle *GovernanceBundle) []StagedFile {
	if bundle == nil {
		return nil
	}
	files := []StagedFile{
		{Path: "/workspace/AGENTS.md", Content: []byte(ephemeralAgentsMD())},
	}
	if bundle.Pipeline != nil {
		files = append(files, StagedFile{Path: "/workspace/.ai-sdlc/pipeline.json", Content: mustJSON(bundle.Pipeline)})
	}
	if bundle.AgentRole != nil {
		files = append(files, StagedFile{Path: "/workspace/.ai-sdlc/agent-role.json", Content: mustJSON(bundle.AgentRole)})
	}
	if bundle.QualityGate != nil {
		files = append(files, StagedFile{Path: "/workspace/.ai-sdlc/quality-gate.json", Content: mustJSON(bundle.QualityGate)})
	}
	if bundle.Adapter != nil {
		files = append(files, StagedFile{Path: "/workspace/.ai-sdlc/adapter-binding.json", Content: mustJSON(bundle.Adapter)})
	}
	return files
}

func mustJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}

func ephemeralAgentsMD() string {
	return `# AGENTS.md (ephemeral Blink sandbox)

This file is outside every product repo. Do not copy it into /workspace/repos.

- Follow the Blink Codex harness instructions.
- Official AI-SDLC skills are under /workspace/capabilities/ai-sdlc/.
- Write /workspace/outputs/blink-result.json when finished.
`
}

// EncodeInlineBase64 encodes file bytes for HostedEnvironmentFileParamInline.Data.
func EncodeInlineBase64(content []byte) string {
	return base64.StdEncoding.EncodeToString(content)
}

// CapabilityDirectoryList returns the OpenAI capability_directories value.
func CapabilityDirectoryList() []string {
	return []string{CapabilityRootWorkspace}
}

// ValidateSkillBundle is used by tests to assert SKILL.md shape.
func ValidateSkillBundle(files []StagedFile) error {
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f.Path, "/SKILL.md") {
			found++
			text := string(f.Content)
			if !strings.Contains(text, "name:") || !strings.Contains(text, "description:") {
				return fmt.Errorf("%s frontmatter incomplete", f.Path)
			}
		}
	}
	if found == 0 {
		return fmt.Errorf("no SKILL.md files staged")
	}
	return nil
}
