package zipkit

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
)

var ensureMu sync.Mutex

const (
	maxRuntimeArchiveBytes = 256 << 20
	maxRuntimeFileBytes    = 64 << 20
)

// LooksReal reports whether dir is an automation_sdlc kit, not an empty folder.
func LooksReal(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return false
	}
	for _, marker := range []string{"framework_prompts", "app", "Makefile", "pyproject.toml", "prompts"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// Resolve returns a real kit directory from the configured path, or "".
func Resolve(configured string) string {
	p := resolveAutomationSDLC(configured)
	if LooksReal(p) {
		return p
	}
	return ""
}

// Ensure returns a configured local kit. It deliberately does not fetch source
// from GitHub: the hosted workspace kit is sourced from the deployed AWS Lambda
// package by EnsureFromLambda.
func Ensure(dest string) (string, error) {
	ensureMu.Lock()
	defer ensureMu.Unlock()

	if resolved := resolveAutomationSDLC(dest); LooksReal(resolved) {
		return resolved, nil
	}
	return "", fmt.Errorf("automation_sdlc kit is missing at %s", strings.TrimSpace(dest))
}

// EnsureFromLambda materializes the downloadable workspace kit from the
// deployed ZIP Lambda package. This keeps workspace downloads AWS-owned and
// does not use the separate GitHub integration.
func EnsureFromLambda(
	ctx context.Context,
	dest, functionName, region, accessKeyID, secretAccessKey string,
) (string, error) {
	ensureMu.Lock()
	defer ensureMu.Unlock()

	if resolved := resolveAutomationSDLC(dest); LooksReal(resolved) {
		return resolved, nil
	}

	target := strings.TrimSpace(dest)
	if target == "" {
		target = "automation_sdlc"
	}
	functionName = strings.TrimSpace(functionName)
	if functionName == "" {
		return "", fmt.Errorf("automation_sdlc kit is missing and BLINK_AGENT_RUNTIME_LAMBDA_FUNCTION is empty")
	}
	if strings.TrimSpace(accessKeyID) == "" || strings.TrimSpace(secretAccessKey) == "" {
		return "", fmt.Errorf("automation_sdlc kit is missing and AWS credentials are not configured")
	}

	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	tmp := abs + ".partial"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}

	client := lambda.New(lambda.Options{
		Region: strings.TrimSpace(region),
		Credentials: credentials.NewStaticCredentialsProvider(
			strings.TrimSpace(accessKeyID),
			strings.TrimSpace(secretAccessKey),
			"",
		),
		HTTPClient: &http.Client{Timeout: 90 * time.Second},
	})
	function, err := client.GetFunction(ctx, &lambda.GetFunctionInput{
		FunctionName: aws.String(functionName),
	})
	if err != nil {
		return "", fmt.Errorf("read deployed Lambda package: %w", err)
	}
	if function.Configuration != nil && string(function.Configuration.PackageType) != "" &&
		string(function.Configuration.PackageType) != "Zip" {
		return "", fmt.Errorf(
			"blink-agent-runtime must use Lambda package type Zip to supply the workspace kit (got %s)",
			function.Configuration.PackageType,
		)
	}
	location := ""
	if function.Code != nil {
		location = strings.TrimSpace(aws.ToString(function.Code.Location))
	}
	if location == "" {
		return "", fmt.Errorf("deployed Lambda package location is unavailable")
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return "", fmt.Errorf("request deployed Lambda package: %w", err)
	}
	response, err := (&http.Client{Timeout: 90 * time.Second}).Do(request)
	if err != nil {
		return "", fmt.Errorf("download deployed Lambda package: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download deployed Lambda package: HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxRuntimeArchiveBytes {
		return "", fmt.Errorf("deployed Lambda package is too large (%d bytes)", response.ContentLength)
	}

	archivePath := tmp + ".zip"
	archive, err := os.Create(archivePath)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(archive, io.LimitReader(response.Body, maxRuntimeArchiveBytes+1))
	closeErr := archive.Close()
	if copyErr != nil {
		_ = os.Remove(archivePath)
		return "", fmt.Errorf("read deployed Lambda package: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(archivePath)
		return "", closeErr
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return "", err
	}
	if info.Size() > maxRuntimeArchiveBytes {
		_ = os.Remove(archivePath)
		return "", fmt.Errorf("deployed Lambda package exceeds %d bytes", maxRuntimeArchiveBytes)
	}

	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		_ = os.Remove(archivePath)
		return "", fmt.Errorf("open deployed Lambda package: %w", err)
	}
	err = extractRuntimeKit(zr, tmp)
	_ = zr.Close()
	_ = os.Remove(archivePath)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	_ = os.RemoveAll(abs)
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	return abs, nil
}

func extractRuntimeKit(zr *zip.ReadCloser, destination string) error {
	hasFramework := false
	for _, file := range zr.File {
		relative, ok := runtimeKitPath(file.Name)
		if !ok || file.FileInfo().IsDir() {
			continue
		}
		if file.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("deployed Lambda package contains a symlink: %s", file.Name)
		}
		if file.UncompressedSize64 > maxRuntimeFileBytes {
			return fmt.Errorf("deployed Lambda package file is too large: %s", file.Name)
		}
		target := filepath.Join(destination, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		input, err := file.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err == nil {
			var copied int64
			copied, err = io.Copy(output, io.LimitReader(input, maxRuntimeFileBytes+1))
			if err == nil && copied > maxRuntimeFileBytes {
				err = fmt.Errorf("deployed Lambda package file is too large: %s", file.Name)
			}
			closeErr := output.Close()
			if err == nil {
				err = closeErr
			}
		}
		_ = input.Close()
		if err != nil {
			return err
		}
		if strings.HasPrefix(relative, "ai-sdlc/") {
			hasFramework = true
		}
	}
	if !hasFramework || !LooksReal(destination) {
		return fmt.Errorf("deployed Lambda package does not contain a usable automation_sdlc kit")
	}
	return nil
}

func runtimeKitPath(name string) (string, bool) {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(name)))
	clean = strings.TrimPrefix(clean, "./")
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", false
	}
	switch {
	case strings.HasPrefix(clean, "ai-sdlc/"), strings.HasPrefix(clean, ".cursor/"), strings.HasPrefix(clean, "app/"):
		return clean, true
	case clean == "scripts/mcp-npx.sh" || clean == "scripts/mcp-npx.ps1":
		return clean, true
	default:
		return "", false
	}
}

// ListKitFiles returns relative slash-separated files to copy from a kit root.
func ListKitFiles(root string) ([]string, error) {
	if !LooksReal(root) {
		return nil, fmt.Errorf("automation_sdlc kit is missing at %s", root)
	}
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return nil
		}
		name := d.Name()
		parent := ""
		if parentRel := filepath.Dir(rel); parentRel != "." {
			parent = filepath.Base(parentRel)
		}
		if d.IsDir() {
			if SkipDirectory(name, parent) {
				return filepath.SkipDir
			}
			return nil
		}
		if SkipFile(name) {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files, err
}

// ListAutomationSdlcFiles is the kit minus .cursor (uploaded under automation_sdlc/).
func ListAutomationSdlcFiles(root string) ([]string, error) {
	all, err := ListKitFiles(root)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for _, rel := range all {
		if rel == ".cursor" || strings.HasPrefix(rel, ".cursor/") {
			continue
		}
		out = append(out, rel)
	}
	return out, nil
}

// ListCursorCommandFiles returns .cursor/commands/* relative paths for the workspace .cursor tree.
func ListCursorCommandFiles(root string) ([]string, error) {
	commands := filepath.Join(root, ".cursor", "commands")
	st, err := os.Stat(commands)
	if err != nil || !st.IsDir() {
		return nil, nil
	}
	var files []string
	err = filepath.WalkDir(commands, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		if SkipFile(d.Name()) {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files, err
}
