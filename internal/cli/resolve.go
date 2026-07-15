package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
)

// runResolve lists files containing conflict markers left by creght pull, or
// resolves one file by keeping the local or remote side. Purely local; the
// result is uploaded by a later creght push.
func runResolve(_ context.Context, args []string) error {
	positionals, flagArgs := splitFlagArgs(args)
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	dir := fs.String("dir", ".", "local directory")
	list := fs.Bool("list", false, "list files containing conflict markers")
	ours := fs.Bool("ours", false, "keep the local side of every conflict")
	theirs := fs.Bool("theirs", false, "keep the remote side of every conflict")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	root, _, hasState, err := findWorkspaceState(*dir, !flagWasSet(fs, "dir"))
	if err != nil {
		return err
	}
	if !hasState {
		return fmt.Errorf("%s is not inside a creght workspace (missing .creght/state.json)", *dir)
	}

	if !flagWasSet(fs, "dir") {
		printWorkspaceNotice(root)
	}

	if len(positionals) == 0 || *list {
		return listConflictedFiles(root)
	}
	if len(positionals) > 1 {
		return fmt.Errorf("resolve accepts at most one <path> argument")
	}
	if *ours == *theirs {
		return fmt.Errorf("pass exactly one of --ours (keep local side) or --theirs (keep remote side), or edit the conflict markers by hand")
	}

	remotePath, err := resolveWorkspacePath(root, positionals[0])
	if err != nil {
		return err
	}
	localPath, err := remotePathToLocal(root, remotePath)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", remotePath, err)
	}
	resolved, count, err := resolveConflictBody(string(body), *ours)
	if err != nil {
		return fmt.Errorf("%s: %w", remotePath, err)
	}
	if err := os.WriteFile(localPath, []byte(resolved), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", remotePath, err)
	}

	side := "local"
	if *theirs {
		side = "remote"
	}
	fmt.Printf("Resolved %d conflict(s) in %s (kept %s side)\n", count, remotePath, side)
	return nil
}

func listConflictedFiles(root string) error {
	found := 0
	err := walkWorkspaceFiles(root, func(path string) error {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !isUTF8FileBody(body) || !hasConflictMarkers(string(body)) {
			return nil
		}
		remotePath, err := localPathToRemote(root, path)
		if err != nil {
			return err
		}
		found++
		fmt.Printf("conflict %s\n", remotePath)
		return nil
	})
	if err != nil {
		return err
	}
	if found == 0 {
		fmt.Println("No conflict markers found")
	} else {
		fmt.Printf("%d file(s) with conflict markers; run creght resolve <path> --ours|--theirs or edit them by hand, then push\n", found)
	}
	return nil
}
