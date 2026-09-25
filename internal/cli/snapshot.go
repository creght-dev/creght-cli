package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Version snapshots.
//
// pull --version_no writes one site version's complete source into a directory:
// the files of that version and nothing else. It exists for readers who need a
// version as a whole — someone who copied a public template and wants to merge
// the template's next version into their copy — and it is the one pull a
// non-member of a public-copy project can run, since the platform shares that
// project's versions but not its workspace.
//
// A snapshot is not a workspace. It records no base state, and push, diff and rm
// refuse to run in it, so a template's files cannot be pushed back by accident.
// Pulling another version into the same directory makes it match that version
// exactly: files the new version lacks are deleted. Only .git and .creght at the
// directory root are left alone, so the directory can be a git work tree that
// commits one version after another.

// snapshotInfo is recorded in .creght/state.json of a snapshot directory.
type snapshotInfo struct {
	VersionNo int64  `json:"version_no"`
	VersionID int64  `json:"version_id,omitempty"`
	Note      string `json:"note,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// snapshotKeptRootEntries survive a snapshot pull untouched.
var snapshotKeptRootEntries = map[string]bool{".git": true, stateDirName: true}

// refuseSnapshotWorkspace stops a command that would treat a snapshot directory
// as an editable workspace.
func refuseSnapshotWorkspace(root string, command string) error {
	state, hasState, err := loadWorkspaceState(root)
	if err != nil || !hasState || state.Snapshot == nil {
		return err
	}
	return fmt.Errorf(
		"%s holds a read-only snapshot of version %d of %s (pulled with --version_no), so %s is disabled there; pull the site into another directory to edit it, or run creght pull --version_no=<version_no> here to switch versions",
		root, state.Snapshot.VersionNo, state.SiteID, command,
	)
}

func parseSnapshotVersionNo(raw string) (int64, error) {
	versionNo, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || versionNo <= 0 {
		return 0, fmt.Errorf("invalid --version_no %q; expected a positive <version_no> from creght version list", raw)
	}
	return versionNo, nil
}

// pullVersionSnapshot writes version versionNo of the site into root.
func pullVersionSnapshot(ctx context.Context, projectID string, realSiteID string, root string, versionNo int64) error {
	siteRef := projectID + "/" + realSiteID
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve dir: %w", err)
	}
	previous, err := checkSnapshotTarget(root)
	if err != nil {
		return err
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	info := snapshotInfo{VersionNo: versionNo}
	// The version's id and note are only labels; the file list below is what
	// decides whether the version exists, so a failure here is not fatal.
	if state, err := client.GetSitePublishState(ctx, projectID, realSiteID); err == nil {
		if version, ok := state.FindVersionByNo(versionNo); ok {
			info.VersionID = version.ID
			info.Note = strings.TrimSpace(version.Note)
			if !version.CreatedAt.IsZero() {
				info.CreatedAt = version.CreatedAt.Format(time.RFC3339)
			}
		}
	}

	list, err := client.GetFileListAtVersion(ctx, projectID, realSiteID, strconv.FormatInt(versionNo, 10))
	if err != nil {
		return err
	}
	files := snapshotFiles(list.List)
	if len(files) == 0 {
		// The platform answers an unknown version with an empty list.
		return fmt.Errorf("version %d of %s has no files; check the number with creght version list --site_id=%s", versionNo, siteRef, siteRef)
	}

	deleted, err := writeSnapshotFiles(root, files)
	if err != nil {
		return err
	}
	if err := saveSnapshotState(root, siteRef, previous.APIHost, info); err != nil {
		return err
	}

	label := fmt.Sprintf("version %d", versionNo)
	if info.VersionID > 0 {
		label = versionLabel(versionNo, info.VersionID)
	}
	fmt.Printf("Pulled %s of %s into %s: %d file(s)", label, siteRef, root, len(files))
	if deleted > 0 {
		fmt.Printf(", removed %d file(s) not in this version", deleted)
	}
	fmt.Println()
	if info.Note != "" {
		fmt.Printf("note: %s\n", info.Note)
	}
	fmt.Println("This is a read-only snapshot: push is disabled here. Pull another version with --version_no to switch.")
	return nil
}

// checkSnapshotTarget accepts a missing or empty directory, or one that already
// holds a snapshot. An editable workspace or a directory with unrelated files is
// refused: a snapshot pull deletes whatever the version does not contain.
func checkSnapshotTarget(root string) (workspaceState, error) {
	state, hasState, err := loadWorkspaceState(root)
	if err != nil {
		return workspaceState{}, err
	}
	if hasState {
		if state.Snapshot == nil {
			return workspaceState{}, fmt.Errorf("%s is an editable workspace of %s; pull a version into a separate --dir so the workspace is not overwritten", root, state.SiteID)
		}
		return state, nil
	}

	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return workspaceState{}, nil
	}
	if err != nil {
		return workspaceState{}, fmt.Errorf("read %s: %w", root, err)
	}
	for _, entry := range entries {
		if !snapshotKeptRootEntries[entry.Name()] {
			return workspaceState{}, fmt.Errorf("%s is not empty and is not a creght version snapshot; pull a version into a new or empty directory, since the pull deletes files the version does not contain", root)
		}
	}
	return workspaceState{}, nil
}

// snapshotFiles keys a version's files by site path. Platform-generated
// read-only files are left out, as they are for version diff.
func snapshotFiles(list []creght.File) map[string]string {
	files := make(map[string]string, len(list))
	for _, file := range list {
		if file.IsDir || file.Readonly {
			continue
		}
		files[normalizeSitePath(file.Path)] = file.Body
	}
	return files
}

// writeSnapshotFiles makes root hold exactly files: stale files go first, so a
// path that turned from a file into a directory (or back) can be written, then
// every file of the version is written. It returns how many files it deleted.
func writeSnapshotFiles(root string, files map[string]string) (int, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return 0, fmt.Errorf("create dir: %w", err)
	}

	var stale []string
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if filepath.Dir(path) == root && snapshotKeptRootEntries[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		remotePath, err := localPathToRemote(root, path)
		if err != nil {
			return err
		}
		if _, ok := files[remotePath]; !ok {
			stale = append(stale, path)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("scan %s: %w", root, err)
	}
	for _, path := range stale {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return 0, fmt.Errorf("delete %s: %w", path, err)
		}
	}
	// Deepest first, so a parent is empty by the time it is tried.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		// Fails harmlessly on directories that still hold files.
		_ = os.Remove(dir)
	}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		localPath, err := remotePathToLocal(root, p)
		if err != nil {
			return 0, err
		}
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			return 0, fmt.Errorf("create parent dir for %s: %w", p, err)
		}
		if err := os.WriteFile(localPath, []byte(files[p]), 0o644); err != nil {
			return 0, fmt.Errorf("write %s: %w", p, err)
		}
	}
	return len(stale), nil
}

func saveSnapshotState(root string, siteRef string, recordedAPIHost string, info snapshotInfo) error {
	state := workspaceState{
		SiteID:    siteRef,
		APIHost:   keepOrRecordAPIHost(recordedAPIHost),
		UpdatedAt: time.Now().Format(time.RFC3339Nano),
		// No base: a snapshot is never synced, so nothing may merge against it.
		Files:    map[string]stateEntry{},
		Snapshot: &info,
	}
	if err := os.MkdirAll(filepath.Dir(statePath(root)), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := os.WriteFile(statePath(root), append(body, '\n'), 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}
