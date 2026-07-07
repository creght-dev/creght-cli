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
)

func frontendRoot(root string) string {
	return filepath.Join(root, workspaceFrontendDir)
}

func backendRoot(root string) string {
	return filepath.Join(root, workspaceBackendDir)
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

// workspaceSyncRoots returns the local directories whose files are synced as
// ordinary site files. With the standard layout the frontend and backend trees
// are separate top-level directories; the flat legacy layout syncs the whole
// workspace root.
func workspaceSyncRoots(root string) []string {
	if hasFrontendLayout(root) {
		return []string{frontendRoot(root), backendRoot(root)}
	}
	return []string{root}
}

// isWorkspaceSyncablePath reports whether a local path is within one of the
// workspace sync roots and therefore mirrors a remote site file.
func isWorkspaceSyncablePath(root string, path string) bool {
	for _, syncRoot := range workspaceSyncRoots(root) {
		if isPathInside(syncRoot, path) {
			return true
		}
	}
	return false
}

// isBackendRemotePath reports whether a remote site path lives under the
// /backend/ prefix (for example /backend/func/booking.ts).
func isBackendRemotePath(remotePath string) bool {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(strings.TrimSpace(remotePath), "/")))
	if clean == "." || clean == "" {
		return false
	}
	parts := strings.SplitN(clean, "/", 2)
	return parts[0] == workspaceBackendDir
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

// remotePathToWorkspaceLocal maps a remote site path to its local workspace
// path. Backend files keep their /backend/ prefix (remote /backend/func/x.ts ->
// local backend/func/x.ts) while all other files are written under frontend/
// (remote /page/x.tsx -> local frontend/page/x.tsx).
func remotePathToWorkspaceLocal(root string, remotePath string) (string, error) {
	if isBackendRemotePath(remotePath) {
		return remotePathToLocal(root, remotePath)
	}
	return remotePathToLocal(frontendRoot(root), remotePath)
}

// localWorkspacePathToRemote is the inverse of remotePathToWorkspaceLocal. Files
// under backend/ keep that prefix in the remote path; files under frontend/ (or
// the flat workspace root) have that root stripped.
func localWorkspacePathToRemote(root string, localPath string) (string, error) {
	if isWorkspaceBackendPath(root, localPath) {
		return localPathToRemote(root, localPath)
	}
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

func isPathInside(root string, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

func writeRemoteFilesToWorkspace(root string, files []creght.File) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	for _, file := range files {
		if file.IsDir {
			continue
		}

		localPath, err := remotePathToWorkspaceLocal(root, file.Path)
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
  talizen.config.ts. These map to remote site paths with the frontend/ root
  stripped (frontend/page/x.tsx <-> /page/x.tsx).
- backend/ contains site files that keep their backend/ prefix in the remote
  path (backend/func/booking.ts <-> /backend/func/booking.ts). Func backend
  code lives under backend/func/; for example backend/func/booking.ts is the
  Func with key booking and backend/func/profile/settings.ts is profile/settings.

Func code is just ordinary site source under backend/func/. It is synced,
versioned, and published together with the site through the normal pull, push,
sync, and dev flows. The only dedicated Func endpoint is invocation, used by
creght func run to test a Func with sample input.

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
