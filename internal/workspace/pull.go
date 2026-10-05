package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/creght-dev/creght-cli/internal/creght"
)

// PullOptions are pull's flags.
type PullOptions struct {
	// Force overwrites local files with the remote workspace instead of
	// merging; overwritten local work is backed up under .creght/backup/.
	Force bool
	// SkipAgentsFile leaves out the AGENTS.md a pull otherwise writes into a
	// workspace that has none.
	SkipAgentsFile bool
}

// PullResult reports what a pull did.
type PullResult struct {
	Dir     string
	Changed int
	// Merged are files changed on both sides whose edits merged cleanly.
	Merged []string
	// Conflicted are files changed on both sides with overlapping edits; they
	// now hold conflict markers to edit or resolve before pushing.
	Conflicted []string
	// BackupDir is where overwritten local work was saved, if any.
	BackupDir         string
	AgentsFileCreated bool
}

// Pull merges the site's remote workspace into dir (a new directory, or a
// workspace of the same site), three-way against the recorded base, and records
// the new base. apiHost is stamped on a workspace that has none recorded.
func Pull(ctx context.Context, client *creght.Client, projectID string, siteID string, dir string, apiHost string, opts PullOptions, out io.Writer) (PullResult, error) {
	if out == nil {
		out = io.Discard
	}
	siteRef := projectID + "/" + siteID
	if err := RefuseSnapshotWorkspace(dir, "pull without --version_no"); err != nil {
		return PullResult{}, err
	}
	state, hasState, err := LoadWorkspaceState(dir)
	if err != nil {
		return PullResult{}, err
	}
	if hasState && strings.TrimSpace(state.SiteID) != "" && state.SiteID != siteRef {
		return PullResult{}, fmt.Errorf("workspace state belongs to %s, not %s", state.SiteID, siteRef)
	}

	files, err := client.GetFileList(ctx, projectID, siteID)
	if err != nil {
		return PullResult{}, err
	}

	remoteSnap, err := RemoteFileSnapshotForWorkspace(dir, files.List)
	if err != nil {
		return PullResult{}, err
	}
	var outcome PullOutcome
	if opts.Force {
		ignore, err := LoadCreghtIgnore(dir)
		if err != nil {
			return PullResult{}, err
		}
		state.Files = FilterIgnoredState(ignore, state.Files)
		localFiles, err := LocalFileSnapshot(dir)
		if err != nil {
			return PullResult{}, err
		}
		incoming := map[string]string{}
		for path, entry := range remoteSnap {
			incoming[path] = entry.Body
		}
		outcome.backupDir, err = BackupOverwrittenLocalFiles(dir, state, hasState, localFiles, incoming)
		if err != nil {
			return PullResult{}, err
		}
		if err := WriteRemoteFilesToWorkspace(dir, files.List); err != nil {
			return PullResult{}, err
		}
		if err := SaveWorkspaceState(dir, siteRef, apiHost, remoteSnap); err != nil {
			return PullResult{}, err
		}
		outcome.changed = len(remoteSnap)
	} else {
		outcome, err = SafePullWorkspace(dir, siteRef, apiHost, remoteSnap, out)
		if err != nil {
			return PullResult{}, err
		}
	}

	result := PullResult{
		Dir:        dir,
		Changed:    outcome.changed,
		Merged:     outcome.merged,
		Conflicted: outcome.conflicted,
		BackupDir:  outcome.backupDir,
	}
	if !opts.SkipAgentsFile {
		result.AgentsFileCreated, err = EnsurePulledAgentsFile(dir, nil)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// pullOutcome summarizes what a pull did, for reporting.
type PullOutcome struct {
	changed    int
	merged     []string // both sides changed, auto-merged cleanly
	conflicted []string // both sides changed, conflict markers written
	backupDir  string   // where overwritten local work was saved, if any
}

func SafePullWorkspace(root string, siteID string, apiHost string, remoteFiles map[string]SnapshotEntry, out io.Writer) (PullOutcome, error) {
	var outcome PullOutcome
	ignore, err := LoadCreghtIgnore(root)
	if err != nil {
		return outcome, err
	}
	remoteFiles = FilterIgnoredSnapshot(ignore, remoteFiles)
	state, hasState, err := LoadWorkspaceState(root)
	if err != nil {
		return outcome, err
	}
	state.Files = FilterIgnoredState(ignore, state.Files)
	if hasState && strings.TrimSpace(state.SiteID) != "" && state.SiteID != siteID {
		return outcome, fmt.Errorf("workspace state belongs to %s, not %s", state.SiteID, siteID)
	}

	localFiles, err := LocalFileSnapshot(root)
	if err != nil {
		return outcome, err
	}

	filePlan := BuildPullEntryPlan("file", state.Files, hasState, localFiles, remoteFiles, func(hash string) (string, bool) {
		return ReadBaseObject(root, hash)
	})
	if len(filePlan.Conflicts) > 0 {
		for _, conflict := range filePlan.Conflicts {
			fmt.Fprintf(out, "conflict %s %s: %s\n", conflict.Kind, conflict.Path, conflict.Reason)
		}
		return outcome, fmt.Errorf("pull has conflicts; resolve local changes first, or use --force to overwrite local files")
	}

	incoming := map[string]string{}
	for _, entry := range filePlan.CleanMerges {
		incoming[entry.Path] = entry.Body
	}
	for _, entry := range filePlan.ConflictWrites {
		incoming[entry.Path] = entry.Body
	}
	outcome.backupDir, err = BackupOverwrittenLocalFiles(root, state, hasState, localFiles, incoming)
	if err != nil {
		return outcome, err
	}

	for _, entry := range filePlan.Writes {
		if err := WritePulledFile(root, entry); err != nil {
			return outcome, err
		}
	}
	for _, entry := range filePlan.CleanMerges {
		if err := WritePulledFile(root, entry); err != nil {
			return outcome, err
		}
		outcome.merged = append(outcome.merged, entry.Path)
	}
	for _, entry := range filePlan.ConflictWrites {
		if err := WritePulledFile(root, entry); err != nil {
			return outcome, err
		}
		outcome.conflicted = append(outcome.conflicted, entry.Path)
	}
	for _, path := range filePlan.Deletes {
		if err := deletePulledFile(root, path); err != nil {
			return outcome, err
		}
	}
	if err := SaveWorkspaceState(root, siteID, apiHost, remoteFiles); err != nil {
		return outcome, err
	}
	outcome.changed = len(filePlan.Writes) + len(filePlan.CleanMerges) + len(filePlan.ConflictWrites) + len(filePlan.Deletes)
	return outcome, nil
}

func WritePulledFile(root string, entry SnapshotEntry) error {
	localPath, err := RemotePathToLocal(root, entry.Path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("create parent dir for %s: %w", entry.Path, err)
	}
	if err := os.WriteFile(localPath, []byte(entry.Body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", entry.Path, err)
	}
	return nil
}

func deletePulledFile(root string, remotePath string) error {
	localPath, err := RemotePathToLocal(root, remotePath)
	if err != nil {
		return err
	}
	if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete %s: %w", remotePath, err)
	}
	return nil
}
