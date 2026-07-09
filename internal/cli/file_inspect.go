package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
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
// path (e.g. "/page/Index.tsx"). It accepts a remote path ("/page/x.tsx"), a
// workspace path ("frontend/page/x.tsx", "backend/func/x.ts") or a
// frontend-relative path ("page/x.tsx").
func resolveRemotePath(root string, input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.HasPrefix(input, "/") {
		clean := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(input, "/")))
		if clean == "." || strings.HasPrefix(clean, "..") {
			return "", fmt.Errorf("unsafe path: %s", input)
		}
		return "/" + clean, nil
	}
	clean := filepath.ToSlash(filepath.Clean(input))
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("unsafe path: %s", input)
	}
	parts := strings.SplitN(clean, "/", 2)
	if parts[0] == workspaceFrontendDir || parts[0] == workspaceBackendDir {
		return localWorkspacePathToRemote(root, filepath.Join(root, clean))
	}
	return "/" + clean, nil
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
// pulling the whole workspace.
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
		return fmt.Errorf("cat requires exactly one <path> argument, e.g. creght cat page/Index.tsx --site_id=a/b")
	}

	remotePath, err := resolveRemotePath(*dir, positionals[0])
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
	remotePath, err := resolveRemotePath(dir, rawPath)
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
// so an agent can decide how to resolve without parsing human text.
type diffJSONEntry struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Action string `json:"action,omitempty"`
}

type diffJSONOutput struct {
	HasConflicts bool            `json:"has_conflicts"`
	Files        []diffJSONEntry `json:"files"`
}

func printPlanJSON(plan syncPlan) error {
	out := diffJSONOutput{HasConflicts: plan.hasConflicts()}
	for _, a := range plan.FileActions {
		out.Files = append(out.Files, diffJSONEntry{Path: a.remotePath, Status: "local-change", Action: a.action.Action})
	}
	for _, c := range plan.Conflicts {
		out.Files = append(out.Files, diffJSONEntry{Path: c.Path, Status: "conflict"})
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

// pullOneFile downloads a single remote file into the workspace and updates just
// that file's base state. Without --force it refuses when the file changed both
// locally and remotely.
func pullOneFile(ctx context.Context, projectID string, realSiteID string, dir string, rawPath string, force bool) error {
	remotePath, err := resolveRemotePath(dir, rawPath)
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
	remoteSnap := remoteFileSnapshot(files.List)
	remoteEntry, ok := remoteSnap[remotePath]
	if !ok {
		return fmt.Errorf("remote file not found: %s", remotePath)
	}

	if !force {
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
		if hasState && baseOK && localOK && local.Hash != base.Hash && remoteEntry.Hash != local.Hash {
			return fmt.Errorf("%s changed both locally and remotely; use --force to overwrite the local file", remotePath)
		}
	}

	if err := writePulledFile(dir, remoteEntry); err != nil {
		return err
	}
	if err := putStateFileEntry(dir, projectID+"/"+realSiteID, remoteEntry); err != nil {
		return err
	}
	fmt.Printf("Pulled %s\n", remotePath)
	return nil
}

// unifiedLineDiff produces a simple LCS-based interleaved line diff. Lines are
// prefixed with "  " (context), "- " (only in a/remote) or "+ " (only in
// b/local). No hunk compaction — kept minimal and easy for an agent to parse.
func unifiedLineDiff(aName string, bName string, a string, b string) string {
	aLines := strings.Split(a, "\n")
	bLines := strings.Split(b, "\n")
	n, m := len(aLines), len(bLines)

	// dp[i][j] = LCS length of aLines[i:] and bLines[j:]
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if aLines[i] == bLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("--- " + aName + "\n")
	sb.WriteString("+++ " + bName + "\n")
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case aLines[i] == bLines[j]:
			sb.WriteString("  " + aLines[i] + "\n")
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			sb.WriteString("- " + aLines[i] + "\n")
			i++
		default:
			sb.WriteString("+ " + bLines[j] + "\n")
			j++
		}
	}
	for ; i < n; i++ {
		sb.WriteString("- " + aLines[i] + "\n")
	}
	for ; j < m; j++ {
		sb.WriteString("+ " + bLines[j] + "\n")
	}
	return sb.String()
}
