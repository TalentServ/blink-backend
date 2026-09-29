package zipkit

import (
	"fmt"
	"strings"
)

// ToMarkdown builds requirement.md from pasted text or an uploaded file.
// Pasted text wins. Otherwise the file is read the same way as the extract endpoint.
func ToMarkdown(projectName string, fileName string, fileBytes []byte, pastedText string) (string, error) {
	title := strings.TrimSpace(projectName)
	if title == "" {
		title = "Project"
	}
	if strings.TrimSpace(pastedText) != "" {
		return withTitle(title, strings.TrimSpace(pastedText)), nil
	}
	if len(fileBytes) > 0 {
		extracted, err := ExtractRequirement(fileName, fileBytes)
		if err != nil || strings.TrimSpace(extracted) == "" {
			name := fileName
			if name == "" {
				name = "upload"
			}
			return fmt.Sprintf("# %s\n\nRequirement document uploaded: `%s`\n\nBlink could not extract text from this file. Replace this page with the full\nrequirements before running the SDLC workflow.\n", title, name), nil
		}
		return withTitle(title, strings.TrimSpace(extracted)), nil
	}
	return "", fmt.Errorf("Upload a document or paste requirements.")
}

func withTitle(title, body string) string {
	if strings.HasPrefix(body, "#") {
		return body
	}
	return "# " + title + "\n\n" + body + "\n"
}
