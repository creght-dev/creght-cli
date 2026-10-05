package workspace

import (
	"fmt"
	"os"
)

// ConflictedFiles lists the site paths of workspace files that still hold
// conflict markers left by a pull.
func ConflictedFiles(root string) ([]string, error) {
	var found []string
	err := WalkWorkspaceFiles(root, func(path string) error {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !IsUTF8FileBody(body) || !HasConflictMarkers(string(body)) {
			return nil
		}
		remotePath, err := LocalPathToRemote(root, path)
		if err != nil {
			return err
		}
		found = append(found, remotePath)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// ResolveFile rewrites one file's conflict blocks keeping the local side
// (keepLocal) or the remote side, and returns how many blocks it resolved.
// Purely local; a later push uploads the result.
func ResolveFile(root string, remotePath string, keepLocal bool) (int, error) {
	localPath, err := RemotePathToLocal(root, remotePath)
	if err != nil {
		return 0, err
	}
	body, err := os.ReadFile(localPath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", remotePath, err)
	}
	resolved, count, err := ResolveConflictBody(string(body), keepLocal)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", remotePath, err)
	}
	if err := os.WriteFile(localPath, []byte(resolved), 0o644); err != nil {
		return 0, fmt.Errorf("write %s: %w", remotePath, err)
	}
	return count, nil
}
