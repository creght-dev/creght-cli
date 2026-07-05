package cli

import (
	"bysir/creght-cli/internal/creght"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	workspaceFrontendDir = "frontend"
	workspaceBackendDir  = "backend"
	workspaceFuncDir     = "func"
)

func frontendRoot(root string) string {
	return filepath.Join(root, workspaceFrontendDir)
}

func funcRoot(root string) string {
	return filepath.Join(root, workspaceBackendDir, workspaceFuncDir)
}

func hasFrontendLayout(root string) bool {
	info, err := os.Stat(frontendRoot(root))
	return err == nil && info.IsDir()
}

func siteSyncRoot(root string) string {
	if hasFrontendLayout(root) {
		return frontendRoot(root)
	}
	return root
}

func remotePathToLocal(root string, remotePath string) (string, error) {
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "" || remotePath == "/" {
		return "", fmt.Errorf("invalid remote path: %q", remotePath)
	}

	clean := filepath.Clean(strings.TrimPrefix(remotePath, "/"))
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("unsafe remote path: %s", remotePath)
	}

	return filepath.Join(root, clean), nil
}

func localPathToRemote(root string, localPath string) (string, error) {
	rel, err := filepath.Rel(root, localPath)
	if err != nil {
		return "", fmt.Errorf("relative path: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path is outside sync dir: %s", localPath)
	}

	return "/" + filepath.ToSlash(rel), nil
}

func remotePathToWorkspaceLocal(root string, remotePath string) (string, error) {
	return remotePathToLocal(frontendRoot(root), remotePath)
}

func localWorkspacePathToRemote(root string, localPath string) (string, error) {
	return localPathToRemote(siteSyncRoot(root), localPath)
}

func isWorkspaceBackendPath(root string, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	return len(parts) > 0 && parts[0] == workspaceBackendDir
}

func isWorkspaceFuncPath(root string, path string) bool {
	rel, err := filepath.Rel(funcRoot(root), path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	return true
}

func isPathInside(root string, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

func funcKeyToLocalPath(root string, key string) (string, error) {
	key = normalizeLocalFuncKey(key)
	if key == "" {
		return "", fmt.Errorf("func key is required")
	}
	if strings.Contains(key, "\x00") || strings.Contains(key, "..") {
		return "", fmt.Errorf("invalid func key: %s", key)
	}
	clean := filepath.Clean(strings.TrimPrefix(key, "/"))
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("unsafe func key: %s", key)
	}
	if filepath.Ext(clean) == "" {
		clean += ".ts"
	}
	return filepath.Join(funcRoot(root), clean), nil
}

func localPathToFuncKey(root string, path string) (string, error) {
	rel, err := filepath.Rel(funcRoot(root), path)
	if err != nil {
		return "", fmt.Errorf("relative func path: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path is outside func dir: %s", path)
	}
	rel = filepath.ToSlash(rel)
	ext := filepath.Ext(rel)
	switch ext {
	case ".ts", ".js", ".tsx", ".jsx", ".mjs", ".mts":
		rel = strings.TrimSuffix(rel, ext)
	}
	return normalizeLocalFuncKey(rel), nil
}

func normalizeLocalFuncKey(key string) string {
	key = strings.TrimSpace(key)
	key = strings.TrimSuffix(key, filepath.Ext(key))
	key = strings.Trim(key, "/")
	if key == "" {
		return ""
	}
	return "/" + filepath.ToSlash(key)
}

func writeRemoteFiles(root string, files []creght.File) error {
	err := os.MkdirAll(root, 0o755)
	if err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	for _, file := range files {
		if file.IsDir {
			continue
		}

		localPath, err := remotePathToLocal(root, file.Path)
		if err != nil {
			return err
		}

		err = os.MkdirAll(filepath.Dir(localPath), 0o755)
		if err != nil {
			return fmt.Errorf("create parent dir for %s: %w", file.Path, err)
		}

		err = os.WriteFile(localPath, []byte(file.Body), 0o644)
		if err != nil {
			return fmt.Errorf("write %s: %w", file.Path, err)
		}
	}

	return nil
}

func writeRemoteFilesToWorkspace(root string, files []creght.File) error {
	return writeRemoteFiles(frontendRoot(root), files)
}

func writeProjectFuncsToWorkspace(root string, funcs []creght.ProjectFunc) error {
	if err := os.MkdirAll(funcRoot(root), 0o755); err != nil {
		return fmt.Errorf("create func dir: %w", err)
	}
	for _, fn := range funcs {
		if strings.TrimSpace(fn.Body) == "" {
			continue
		}
		localPath, err := funcKeyToLocalPath(root, fn.Key)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			return fmt.Errorf("create func parent for %s: %w", fn.Key, err)
		}
		if err := os.WriteFile(localPath, []byte(fn.Body), 0o644); err != nil {
			return fmt.Errorf("write func %s: %w", fn.Key, err)
		}
	}
	return nil
}

func ensurePulledAgentsFile(root string, files []creght.File, projectID string, siteID string, editorURL string) (bool, error) {
	for _, file := range files {
		if file.IsDir {
			continue
		}
		if strings.TrimSpace(file.Path) == "/AGENTS.md" {
			return false, nil
		}
	}

	localPath, err := remotePathToLocal(root, "/AGENTS.md")
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(localPath); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("check AGENTS.md: %w", err)
	}

	body := pulledAgentsFileBody(projectID, siteID, editorURL)
	if err := os.WriteFile(localPath, []byte(body), 0o644); err != nil {
		return false, fmt.Errorf("write AGENTS.md: %w", err)
	}

	return true, nil
}

func pulledAgentsFileBody(projectID string, siteID string, editorURL string) string {
	return fmt.Sprintf(`# Creght Project Agent Notes

This is a Creght project pulled by the Creght CLI.

Before editing this project, read the Creght skill. If the skill is not installed,
install it from this manual:

https://github.com/creght/skills/blob/main/readme.md

Project ID: %s
Site ID: %s
Editor URL: %s

Workspace layout:

- frontend/ contains Creght site files such as page/, component/, and
  talizen.config.ts.
- backend/func/ contains project Func backend code. For example,
  backend/func/booking.ts maps to Func key booking, and
  backend/func/profile/settings.ts maps to Func key profile/settings.

Use the Creght CLI for ongoing maintenance:

`+"```bash"+`
creght pull --site_id=%s/%s
creght push --site_id=%s/%s
creght sync --site_id=%s/%s
`+"```"+`
`, projectID, siteID, editorURL, projectID, siteID, projectID, siteID, projectID, siteID)
}

func shouldSkipLocalPath(root string, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return false
	}

	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts {
		if shouldSkipLocalPathPart(part) {
			return true
		}
	}

	return false
}

func shouldSkipLocalPathPart(base string) bool {
	if base == "" {
		return false
	}
	if strings.HasPrefix(base, ".") {
		return true
	}

	switch base {
	case "node_modules", "bower_components", "vendor", "dist", "build", "coverage":
		return true
	default:
		return false
	}
}

func isUTF8FileBody(body []byte) bool {
	return utf8.Valid(body)
}
