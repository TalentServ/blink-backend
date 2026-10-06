package zipkit

import "strings"

var skipDirNames = map[string]struct{}{
	".git": {}, ".idea": {}, ".vscode": {}, ".tools": {}, ".github": {},
	"python312": {}, "site-packages": {}, "node_modules": {}, "target": {}, "dist": {},
	"__pycache__": {}, ".venv": {}, ".venv-ai-sdlc": {}, ".pytest_cache": {}, ".mypy_cache": {},
	"runtime-data": {}, "htmlcov": {}, "services": {}, "docs": {}, "scripts": {},
	"tests": {}, "examples": {}, "fixtures": {}, "blink_demo": {}, "blink_backend": {}, "blink-backend": {},
}

func SkipDirectory(name, parentName string) bool {
	if name == "" {
		return true
	}
	if strings.HasPrefix(name, "test_") {
		return true
	}
	if parentName == ".cursor" {
		return name != "commands"
	}
	_, ok := skipDirNames[name]
	return ok
}

func SkipFile(name string) bool {
	if name == "" {
		return true
	}
	if name == ".DS_Store" || name == ".env" || name == "conftest.py" {
		return true
	}
	if strings.HasPrefix(name, ".env.") && !strings.Contains(name, "example") {
		return true
	}
	if strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".pyc") || strings.HasSuffix(name, ".log") {
		return true
	}
	// test_strategy.py is a production router dependency despite its historical
	// test_ prefix. Excluding it breaks the exported Cursor workspace.
	if strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py") && name != "test_strategy.py" {
		return true
	}
	return strings.HasSuffix(name, "_test.py") ||
		strings.HasSuffix(name, "test_helpers.py") ||
		strings.HasSuffix(name, "_e2e_harness.py")
}
