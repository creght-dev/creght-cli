package cli

import (
	"bysir/creght-cli/internal/creght"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

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

func isPathInside(root string, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

// walkWorkspaceFiles walks every syncable local file under root, calling fn
// with the local path. Local paths mirror remote site paths exactly
// (page/Index.tsx <-> /page/Index.tsx, backend/func/booking.ts <->
// /backend/func/booking.ts).
func walkWorkspaceFiles(root string, fn func(localPath string) error) error {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	ignore, err := loadCreghtIgnore(root)
	if err != nil {
		return err
	}
	if err := rejectLegacyFrontendLayout(root, ignore); err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if shouldSkipLocalPath(root, path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		remotePath, err := localPathToRemote(root, path)
		if err != nil {
			return err
		}
		if ignore.matches(remotePath) {
			return nil
		}
		return fn(path)
	})
}

// rejectLegacyFrontendLayout errors on workspaces pulled by older CLI versions
// that nested site files under frontend/; syncing those paths as-is would
// create bogus remote /frontend/* files and delete the real remote files.
func rejectLegacyFrontendLayout(root string, ignore *creghtIgnore) error {
	frontendRoot := filepath.Join(root, "frontend")
	info, err := os.Stat(frontendRoot)
	if err != nil || !info.IsDir() {
		return nil
	}
	if ignore.matches("/frontend") || ignore.matches("/frontend/__creght_ignore_probe__") {
		return nil
	}

	hasSyncableFile := false
	hasFile := false
	err = filepath.WalkDir(frontendRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if shouldSkipLocalPath(root, path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		hasFile = true
		remotePath, err := localPathToRemote(root, path)
		if err != nil {
			return err
		}
		if !ignore.matches(remotePath) {
			hasSyncableFile = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return err
	}
	if hasSyncableFile || !hasFile {
		return fmt.Errorf("workspace %s uses the legacy frontend/ layout; local paths now mirror remote site paths exactly. Move files out of frontend/ to the workspace root (frontend/page/Index.tsx -> page/Index.tsx) or re-pull into a fresh directory", root)
	}
	return nil
}

func writeRemoteFilesToWorkspace(root string, files []creght.File) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	ignore, err := loadCreghtIgnore(root)
	if err != nil {
		return err
	}

	for _, file := range files {
		if file.IsDir || ignore.matches(file.Path) {
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

func ensurePulledAgentsFile(root string, files []creght.File, projectID string, siteID string, editorURL string) (bool, error) {
	ignore, err := loadCreghtIgnore(root)
	if err != nil {
		return false, err
	}
	if ignore.matches("/AGENTS.md") {
		return false, nil
	}
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

https://github.com/creght-dev/skills/blob/main/readme.md

Project ID: %s
Site ID: %s
Editor URL: %s

Workspace layout:

Local paths mirror remote site paths exactly: page/Index.tsx is the remote
/page/Index.tsx, talizen.config.ts is /talizen.config.ts, and so on. Func
backend code lives under backend/func/; for example backend/func/booking.ts is
the Func with key booking and backend/func/profile/settings.ts is
profile/settings.

Func code is just ordinary site source under backend/func/. It is synced,
versioned, and published together with the site through the normal pull, push,
and dev flows. The only dedicated Func endpoint is invocation, used by
creght func run to test a Func with sample input.

Use the Creght CLI for ongoing maintenance:

The workspace's .creght/state.json records the site ID. From the workspace root
or any child directory, pull, diff, and push discover that state file by walking
upward, so do not repeat --site_id or --dir unless targeting a different
workspace explicitly.

`+"```bash"+`
creght pull
creght diff
creght push
`+"```"+`

pull three-way merges remote and local edits; overlapping edits leave git-style
conflict markers in the file. Use creght resolve --list to find them and
creght resolve <path> --ours|--theirs (or edit by hand) before pushing.
`, projectID, siteID, editorURL)
}

// writeBackupFiles copies path->body contents into a fresh directory under
// .creght/backup/, mirroring the workspace layout, and returns that directory.
// label distinguishes what was backed up (e.g. "local", "remote").
func writeBackupFiles(root string, label string, files map[string]string) (string, error) {
	backupRoot := filepath.Join(root, stateDirName, "backup")
	if err := os.MkdirAll(backupRoot, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	dir, err := os.MkdirTemp(backupRoot, time.Now().Format("20060102-150405")+"-"+label+"-")
	if err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	for path, body := range files {
		localPath, err := remotePathToLocal(dir, path)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			return "", fmt.Errorf("create backup parent dir for %s: %w", path, err)
		}
		if err := os.WriteFile(localPath, []byte(body), 0o644); err != nil {
			return "", fmt.Errorf("write backup %s: %w", path, err)
		}
	}
	return dir, nil
}

// backupOverwrittenLocalFiles backs up local files that are about to be
// overwritten by incoming content and whose current content diverged from the
// recorded base (uncommitted local work). Returns "" when nothing needed it.
func backupOverwrittenLocalFiles(root string, state workspaceState, hasState bool, localFiles map[string]snapshotEntry, incoming map[string]string) (string, error) {
	toBackup := map[string]string{}
	for path, newBody := range incoming {
		local, ok := localFiles[path]
		if !ok || local.Body == newBody {
			continue
		}
		if hasState {
			if base, ok := state.Files[path]; ok && base.Hash == local.Hash {
				continue
			}
		}
		toBackup[path] = local.Body
	}
	if len(toBackup) == 0 {
		return "", nil
	}
	return writeBackupFiles(root, "local", toBackup)
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
