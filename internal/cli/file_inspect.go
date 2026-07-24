package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"
)

// splitFlagArgs separates positional arguments from flag arguments. Flags are
// expected in `--name=value` form (the convention used across the CLI), so any
// token starting with "-" is treated as a flag and everything else as a
// positional. This lets single-file commands accept the path in any position
// (e.g. `creght cat page/Index.tsx --site_id=a/b`).
func splitFlagArgs(args []string) (positionals []string, flagArgs []string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
		} else {
			positionals = append(positionals, a)
		}
	}
	return positionals, flagArgs
}

// resolveRemotePath normalizes a user-supplied path into a canonical remote site
// path (e.g. "/page/Index.tsx"). Local workspace paths mirror remote paths, so
// it accepts either a remote path ("/page/x.tsx") or a workspace-relative path
// ("page/x.tsx").
func resolveRemotePath(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", fmt.Errorf("empty path")
	}
	clean := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(input, "/")))
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("unsafe path: %s", input)
	}
	return "/" + clean, nil
}

// resolveWorkspacePath converts a user-supplied <path> into a canonical remote
// site path. A path starting with "/" is always workspace-root relative. A
// relative path resolves against the current working directory when that lies
// inside the workspace root — so `creght push Index.tsx` from page/ means
// /page/Index.tsx, like git — and falls back to workspace-root relative
// otherwise (e.g. when operating on another workspace via --dir).
func resolveWorkspacePath(root string, input string) (string, error) {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "/") {
		return resolveRemotePath(input)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return resolveRemotePath(input)
	}
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = resolved
	}
	cwd, err := os.Getwd()
	if err != nil {
		return resolveRemotePath(input)
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	if !isPathInside(absRoot, cwd) {
		return resolveRemotePath(input)
	}
	rel, err := filepath.Rel(absRoot, filepath.Join(cwd, filepath.FromSlash(input)))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return resolveRemotePath(input)
	}
	return resolveRemotePath(filepath.ToSlash(rel))
}

func findRemoteFile(files []creght.File, remotePath string) (creght.File, bool) {
	for _, f := range files {
		if !f.IsDir && f.Path == remotePath {
			return f, true
		}
	}
	return creght.File{}, false
}

// runCat prints a single site file's content to stdout, from the remote site
// (default) or the local workspace. Reading one remote file no longer requires
// pulling the whole workspace. Inside a pulled workspace it discovers
// .creght/state.json like pull/push, so --site_id is optional there.
func runCat(ctx context.Context, args []string) error {
	positionals, flagArgs := splitFlagArgs(args)
	fs := flag.NewFlagSet("cat", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	dir := fs.String("dir", ".", "local directory")
	ref := fs.String("ref", "remote", "which version to read: remote | local")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("cat requires exactly one <path> argument, e.g. creght cat page/Index.tsx")
	}

	resolvedDir, resolvedSiteID, err := resolveSiteWorkspace(*dir, *siteID, !flagWasSet(fs, "dir"), false)
	if err != nil {
		return err
	}
	*dir, *siteID = resolvedDir, resolvedSiteID

	remotePath, err := resolveWorkspacePath(*dir, positionals[0])
	if err != nil {
		return err
	}

	switch *ref {
	case "local":
		localFiles, err := localFileSnapshot(*dir)
		if err != nil {
			return err
		}
		entry, ok := localFiles[remotePath]
		if !ok {
			return fmt.Errorf("local file not found: %s", remotePath)
		}
		fmt.Print(entry.Body)
		return nil
	case "remote":
		projectID, realSiteID, err := parseSiteRef(*siteID)
		if err != nil {
			return err
		}
		client, _, err := clientFromConfig()
		if err != nil {
			return err
		}
		files, err := client.GetFileList(ctx, projectID, realSiteID)
		if err != nil {
			return err
		}
		file, ok := findRemoteFile(files.List, remotePath)
		if !ok {
			return fmt.Errorf("remote file not found: %s", remotePath)
		}
		fmt.Print(file.Body)
		return nil
	default:
		return fmt.Errorf("--ref must be remote or local, got %q", *ref)
	}
}

// diffOneFile prints a line diff (remote vs local) for a single file.
func diffOneFile(ctx context.Context, projectID string, realSiteID string, dir string, rawPath string) error {
	remotePath, err := resolveWorkspacePath(dir, rawPath)
	if err != nil {
		return err
	}
	ignore, err := loadCreghtIgnore(dir)
	if err != nil {
		return err
	}
	if ignore.matches(remotePath) {
		fmt.Printf("ignored %s by %s\n", remotePath, creghtIgnoreFileName)
		return nil
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	files, err := client.GetFileList(ctx, projectID, realSiteID)
	if err != nil {
		return err
	}
	remoteBody := ""
	if file, ok := findRemoteFile(files.List, remotePath); ok {
		remoteBody = file.Body
	}

	localFiles, err := localFileSnapshot(dir)
	if err != nil {
		return err
	}
	localBody := ""
	if entry, ok := localFiles[remotePath]; ok {
		localBody = entry.Body
	}

	if remoteBody == localBody {
		fmt.Printf("no changes: %s\n", remotePath)
		return nil
	}
	fmt.Print(unifiedLineDiff("remote:"+remotePath, "local:"+remotePath, remoteBody, localBody))
	return nil
}

// diffJSONEntry / diffJSONOutput are the machine-readable form of a sync plan,
// so an agent can decide how to resolve without parsing human text. Conflict
// entries carry the base->local and base->remote diffs plus whether a pull
// would auto-merge them, when the base content is recorded.
type diffJSONEntry struct {
	Path             string `json:"path"`
	Status           string `json:"status"`
	Action           string `json:"action,omitempty"`
	Reason           string `json:"reason,omitempty"`
	AutoMergeable    *bool  `json:"auto_mergeable,omitempty"`
	BaseToLocalDiff  string `json:"base_to_local_diff,omitempty"`
	BaseToRemoteDiff string `json:"base_to_remote_diff,omitempty"`
}

type diffJSONOutput struct {
	HasConflicts bool            `json:"has_conflicts"`
	Files        []diffJSONEntry `json:"files"`
}

type conflictJSONDetail struct {
	reason           string
	autoMergeable    *bool
	baseToLocalDiff  string
	baseToRemoteDiff string
}

// conflictJSONDetails computes per-conflict detail for diff --json from the
// plan context and the current remote snapshot.
func conflictJSONDetails(root string, planCtx syncPlanContext, remote map[string]snapshotEntry) map[string]conflictJSONDetail {
	details := map[string]conflictJSONDetail{}
	for _, c := range planCtx.plan.Conflicts {
		detail := conflictJSONDetail{reason: c.Reason}
		base, baseOK := planCtx.state.Files[c.Path]
		local, localOK := planCtx.localFiles[c.Path]
		remoteEntry, remoteOK := remote[c.Path]
		if baseOK && localOK && remoteOK && !hasConflictMarkers(local.Body) {
			if baseText, ok := readBaseObject(root, base.Hash); ok {
				detail.baseToLocalDiff = unifiedLineDiff("base:"+c.Path, "local:"+c.Path, baseText, local.Body)
				detail.baseToRemoteDiff = unifiedLineDiff("base:"+c.Path, "remote:"+c.Path, baseText, remoteEntry.Body)
				_, clean := merge3(baseText, local.Body, remoteEntry.Body)
				detail.autoMergeable = &clean
			}
		}
		details[c.Path] = detail
	}
	return details
}

func printPlanJSON(plan syncPlan, conflictDetails map[string]conflictJSONDetail) error {
	out := diffJSONOutput{HasConflicts: plan.hasConflicts()}
	for _, a := range plan.FileActions {
		out.Files = append(out.Files, diffJSONEntry{Path: a.remotePath, Status: "local-change", Action: a.action.Action})
	}
	for _, c := range plan.Conflicts {
		entry := diffJSONEntry{Path: c.Path, Status: "conflict", Reason: c.Reason}
		if detail, ok := conflictDetails[c.Path]; ok {
			entry.Reason = detail.reason
			entry.AutoMergeable = detail.autoMergeable
			entry.BaseToLocalDiff = detail.baseToLocalDiff
			entry.BaseToRemoteDiff = detail.baseToRemoteDiff
		}
		out.Files = append(out.Files, entry)
	}
	for _, p := range plan.RemoteOnlyUpdates {
		out.Files = append(out.Files, diffJSONEntry{Path: p, Status: "remote-only"})
	}
	for _, p := range plan.NoBaseRemoteDiffs {
		out.Files = append(out.Files, diffJSONEntry{Path: p, Status: "no-base"})
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(body))
	return nil
}

// pullOneFile downloads a single remote file into the workspace and updates
// just that file's base state. When the file changed both locally and remotely
// it three-way merges against the recorded base; overlapping edits write
// conflict markers. --force overwrites local with the remote copy.
func pullOneFile(ctx context.Context, projectID string, realSiteID string, dir string, rawPath string, force bool) error {
	remotePath, err := resolveWorkspacePath(dir, rawPath)
	if err != nil {
		return err
	}
	ignore, err := loadCreghtIgnore(dir)
	if err != nil {
		return err
	}
	if ignore.matches(remotePath) {
		fmt.Printf("ignored %s by %s\n", remotePath, creghtIgnoreFileName)
		return nil
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	files, err := client.GetFileList(ctx, projectID, realSiteID)
	if err != nil {
		return err
	}
	remoteSnap := filterIgnoredSnapshot(ignore, remoteFileSnapshot(files.List))
	remoteEntry, ok := remoteSnap[remotePath]
	if !ok {
		return fmt.Errorf("remote file not found: %s", remotePath)
	}

	state, hasState, err := loadWorkspaceState(dir)
	if err != nil {
		return err
	}
	localFiles, err := localFileSnapshot(dir)
	if err != nil {
		return err
	}
	base, baseOK := state.Files[remotePath]
	local, localOK := localFiles[remotePath]

	writeEntry := remoteEntry
	conflicted := false
	if !force && localOK && local.Hash != remoteEntry.Hash {
		if !hasState || !baseOK {
			return fmt.Errorf("%s exists locally with no base state; move it aside or use --force to overwrite it", remotePath)
		}
		if local.Hash != base.Hash {
			// Changed both locally and remotely: merge against the base.
			if hasConflictMarkers(local.Body) {
				return fmt.Errorf("%s contains unresolved conflict markers; edit the file or run creght resolve", remotePath)
			}
			baseText, ok := readBaseObject(dir, base.Hash)
			if !ok {
				return fmt.Errorf("%s changed both locally and remotely and no base content is recorded; use --force to overwrite the local file", remotePath)
			}
			merged, clean := merge3(baseText, local.Body, remoteEntry.Body)
			writeEntry.Body = merged
			conflicted = !clean
		}
	}

	if localOK && local.Body != writeEntry.Body && (!baseOK || base.Hash != local.Hash) {
		backupDir, err := writeBackupFiles(dir, "local", map[string]string{remotePath: local.Body})
		if err != nil {
			return err
		}
		fmt.Printf("Backed up local file to %s\n", backupDir)
	}

	if err := writePulledFile(dir, writeEntry); err != nil {
		return err
	}
	if err := putStateFileEntry(dir, projectID+"/"+realSiteID, remoteEntry); err != nil {
		return err
	}
	if conflicted {
		fmt.Printf("conflict %s: wrote conflict markers\n", remotePath)
		return fmt.Errorf("pulled with conflicts; edit the conflict markers or run creght resolve, then push")
	}
	if writeEntry.Body != remoteEntry.Body {
		fmt.Printf("merged %s\n", remotePath)
	}
	fmt.Printf("Pulled %s\n", remotePath)
	return nil
}

// pushOneFile uploads a single local file. Without --force it refuses when the
// remote copy moved since the last pull (pull merges it first); with --force it
// overwrites remote after backing up the diverged remote copy.
func pushOneFile(ctx context.Context, projectID string, realSiteID string, dir string, rawPath string, force bool) error {
	remotePath, err := resolveWorkspacePath(dir, rawPath)
	if err != nil {
		return err
	}
	ignore, err := loadCreghtIgnore(dir)
	if err != nil {
		return err
	}
	if ignore.matches(remotePath) {
		fmt.Printf("ignored %s by %s\n", remotePath, creghtIgnoreFileName)
		return nil
	}

	siteRef := projectID + "/" + realSiteID
	state, hasState, err := loadWorkspaceState(dir)
	if err != nil {
		return err
	}
	if !hasState {
		return fmt.Errorf("%s is not a creght workspace (missing .creght/state.json); run creght pull first", dir)
	}
	if strings.TrimSpace(state.SiteID) != "" && state.SiteID != siteRef {
		return fmt.Errorf("workspace state belongs to %s, not %s", state.SiteID, siteRef)
	}

	localFiles, err := localFileSnapshot(dir)
	if err != nil {
		return err
	}
	local, ok := localFiles[remotePath]
	if !ok {
		return fmt.Errorf("local file not found: %s (to delete remote files, use creght push --delete)", remotePath)
	}
	if !force && hasConflictMarkers(local.Body) {
		return fmt.Errorf("%s contains unresolved conflict markers; edit the file or run creght resolve", remotePath)
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	files, err := client.GetFileList(ctx, projectID, realSiteID)
	if err != nil {
		return err
	}
	remoteEntry, remoteOK := remoteFileSnapshot(files.List)[remotePath]
	if remoteOK && remoteEntry.Readonly {
		return fmt.Errorf("%s is readonly", remotePath)
	}
	if remoteOK && remoteEntry.Hash == local.Hash {
		if err := putStateFileEntry(dir, siteRef, remoteEntry); err != nil {
			return err
		}
		fmt.Printf("%s is already up to date\n", remotePath)
		return nil
	}

	base, baseOK := state.Files[remotePath]
	if !force && remoteOK {
		switch {
		case !baseOK:
			return fmt.Errorf("no base state for %s; run creght pull %s first, or use --force to overwrite the remote file", remotePath, rawPath)
		case remoteEntry.Hash != base.Hash && local.Hash == base.Hash:
			return fmt.Errorf("%s changed remotely and has no local edits; run creght pull %s instead, or use --force to overwrite it", remotePath, rawPath)
		case remoteEntry.Hash != base.Hash:
			return fmt.Errorf("%s changed both locally and remotely; run creght pull %s to merge, or use --force to overwrite the remote file", remotePath, rawPath)
		}
	}
	if force && remoteOK && (!baseOK || remoteEntry.Hash != base.Hash) {
		backupDir, err := writeBackupFiles(dir, "remote", map[string]string{remotePath: remoteEntry.Body})
		if err != nil {
			return err
		}
		fmt.Printf("Backed up remote file to %s\n", backupDir)
	}

	action := createFileAction(remotePath, local.Body)
	if remoteOK {
		action = updateFileAction(remoteEntry, local.Body)
	}
	if _, err := client.DoSiteAction(ctx, projectID, realSiteID, newClientID(), []creght.SiteActionChange{action.action}); err != nil {
		return err
	}
	if err := putStateFileEntry(dir, siteRef, snapshotEntry{Path: remotePath, Hash: local.Hash, Body: local.Body}); err != nil {
		return err
	}
	fmt.Printf("Pushed %s\n", remotePath)
	return nil
}

// unifiedLineDiff produces a git-style unified diff: only hunks around changed
// lines, with 3 lines of context and "@@ -a,b +c,d @@" headers, so a one-line
// change in a long file prints a few lines instead of the whole file. The diff
// itself comes from go-udiff, the diff implementation extracted from
// x/tools (gopls).
func unifiedLineDiff(aName string, bName string, a string, b string) string {
	return udiff.Unified(aName, bName, a, b)
}
