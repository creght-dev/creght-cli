package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/creght-dev/creght-cli/pkg/sitesync"
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
	side := sitesync.Theirs
	if *ours {
		side = sitesync.Ours
	}
	count, err := sitesync.Resolve(root, remotePath, side)
	if err != nil {
		return err
	}

	kept := "local"
	if *theirs {
		kept = "remote"
	}
	fmt.Printf("Resolved %d conflict(s) in %s (kept %s side)\n", count, remotePath, kept)
	return nil
}

func listConflictedFiles(root string) error {
	found, err := sitesync.Conflicts(root)
	if err != nil {
		return err
	}
	for _, remotePath := range found {
		fmt.Printf("conflict %s\n", remotePath)
	}
	if len(found) == 0 {
		fmt.Println("No conflict markers found")
	} else {
		fmt.Printf("%d file(s) with conflict markers; run creght resolve <path> --ours|--theirs or edit them by hand, then push\n", len(found))
	}
	return nil
}
