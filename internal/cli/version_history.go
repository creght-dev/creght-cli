package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"flag"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
)

// Reading site history.
//
// A version is an immutable snapshot of the site's source, but the live files carry
// no history of their own — so without these commands there is no way to see what a
// file used to contain, what changed between two versions, or to get an old state
// back. `version list` only ever showed numbers and notes.
//
// All three work off one server call: the file list accepts a version, and an empty
// version means the live files. Nothing is computed client-side that the server
// already knows.

// versionFiles reads a version's files, keyed by path. An empty version reads the
// live files, which is what `rollback` compares against.
func versionFiles(ctx context.Context, client *creght.Client, projectID, siteID, version string) (map[string]creght.File, error) {
	list, err := client.GetFileListAtVersion(ctx, projectID, siteID, version)
	if err != nil {
		return nil, err
	}

	files := make(map[string]creght.File, len(list.List))
	for _, file := range list.List {
		if file.IsDir {
			// Directories are not content; a version's shape is its files.
			continue
		}
		if file.Readonly {
			// Platform-generated files — /types/cms.d.ts and friends. The server only
			// synthesizes them for the live listing, never for a version, so comparing
			// the two would report them as added since and a rollback would plan to
			// delete them. They are not the site's source and cannot be written anyway.
			continue
		}
		files[normalizeSitePath(file.Path)] = file
	}
	return files, nil
}

func normalizeSitePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

func sortedPaths(sets ...map[string]creght.File) []string {
	seen := map[string]struct{}{}
	for _, set := range sets {
		for p := range set {
			seen[p] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// runVersionCat prints one file as of one version.
func runVersionCat(ctx context.Context, args []string) error {
	positionals, flagArgs := splitFlagArgs(args)
	fs := flag.NewFlagSet("version cat", flag.ContinueOnError)
	siteID, dir := versionSiteFlags(fs)
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if len(positionals) != 2 {
		return fmt.Errorf("usage: creght version cat <version_no> <path>")
	}
	version, target := positionals[0], normalizeSitePath(positionals[1])

	projectID, realSiteID, _, err := resolveVersionSite(fs, *siteID, *dir, true)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	files, err := versionFiles(ctx, client, projectID, realSiteID, version)
	if err != nil {
		return err
	}
	file, ok := files[target]
	if !ok {
		return fmt.Errorf("%s does not exist in version %s", target, version)
	}
	_, err = os.Stdout.WriteString(file.Body)
	return err
}

// runVersionDiff compares two versions, or one version against the live files.
func runVersionDiff(ctx context.Context, args []string) error {
	positionals, flagArgs := splitFlagArgs(args)
	fs := flag.NewFlagSet("version diff", flag.ContinueOnError)
	siteID, dir := versionSiteFlags(fs)
	nameOnly := fs.Bool("name_only", false, "list changed paths without bodies")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if len(positionals) < 1 || len(positionals) > 2 {
		return fmt.Errorf("usage: creght version diff <version_no> [<version_no>]  (one argument compares against the live files)")
	}

	from := positionals[0]
	to := "" // live
	if len(positionals) == 2 {
		to = positionals[1]
	}

	projectID, realSiteID, _, err := resolveVersionSite(fs, *siteID, *dir, true)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	before, err := versionFiles(ctx, client, projectID, realSiteID, from)
	if err != nil {
		return err
	}
	after, err := versionFiles(ctx, client, projectID, realSiteID, to)
	if err != nil {
		return err
	}

	toLabel := "live"
	if to != "" {
		toLabel = "v" + to
	}

	changes := 0
	for _, p := range sortedPaths(before, after) {
		b, hadBefore := before[p]
		a, hasAfter := after[p]
		switch {
		case hadBefore && !hasAfter:
			changes++
			fmt.Printf("deleted  %s\n", p)
		case !hadBefore && hasAfter:
			changes++
			fmt.Printf("added    %s\n", p)
		case b.Body != a.Body:
			changes++
			fmt.Printf("changed  %s\n", p)
		}
	}
	if changes == 0 {
		fmt.Printf("v%s and %s are identical\n", from, toLabel)
		return nil
	}
	if !*nameOnly {
		fmt.Printf("\n%d file(s) differ between v%s and %s; use `creght version cat` to read either side\n", changes, from, toLabel)
	}
	return nil
}

// runVersionRollback puts the live files back to an earlier version.
//
// This is the one that closes the loop: `version publish` rolls PRODUCTION back but
// leaves the editable files at the newest state, so an unwanted edit had no way home.
//
// History is not rewritten — the rollback is a normal write on top of the current
// state, so every version in between stays. Production is untouched until someone
// runs `version publish`.
func runVersionRollback(ctx context.Context, args []string) error {
	positionals, flagArgs := splitFlagArgs(args)
	fs := flag.NewFlagSet("version rollback", flag.ContinueOnError)
	siteID, dir := versionSiteFlags(fs)
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	dryRun := fs.Bool("dry_run", false, "show what would change and stop")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: creght version rollback <version_no>")
	}
	version := positionals[0]

	projectID, realSiteID, _, err := resolveVersionSite(fs, *siteID, *dir, true)
	if err != nil {
		return err
	}
	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	target, err := versionFiles(ctx, client, projectID, realSiteID, version)
	if err != nil {
		return err
	}
	if len(target) == 0 {
		// Replacing the whole site with nothing is never what anyone meant.
		return fmt.Errorf("version %s has no files; refusing to empty the site", version)
	}
	live, err := versionFiles(ctx, client, projectID, realSiteID, "")
	if err != nil {
		return err
	}

	changes, summary := rollbackPlan(target, live)

	if len(changes) == 0 {
		fmt.Printf("The live files already match v%s; nothing to do.\n", version)
		return nil
	}
	for _, line := range summary {
		fmt.Println(line)
	}
	fmt.Printf("\n%d change(s) to roll the live files back to v%s.\n", len(changes), version)
	if *dryRun {
		return nil
	}
	if !*yes {
		fmt.Print("Apply? This changes the preview site immediately (production is untouched). [y/N] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if !strings.EqualFold(strings.TrimSpace(answer), "y") {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// One request: the server applies a batch all-or-nothing, so a failure cannot
	// leave the site half rolled back.
	if _, err := client.DoSiteAction(ctx, projectID, realSiteID, "creght-cli", changes); err != nil {
		return err
	}

	fmt.Printf("Rolled the live files back to v%s.\n", version)
	fmt.Println("Run `creght version create` to record this state as a version, and `creght version publish` to put it into production.")
	return nil
}

// rollbackPlan turns "make live look like this version" into a batch of changes.
//
// Pure so the shape can be tested without a server: getting a delete wrong here
// removes a file from a live site, and getting a create wrong resurrects one.
func rollbackPlan(target map[string]creght.File, live map[string]creght.File) ([]creght.SiteActionChange, []string) {
	var changes []creght.SiteActionChange
	var summary []string

	for _, p := range sortedPaths(target, live) {
		want, inTarget := target[p]
		have, inLive := live[p]
		switch {
		case inTarget && !inLive:
			body := want.Body
			path := p
			changes = append(changes, creght.SiteActionChange{
				Action: "file_create",
				File:   creght.SiteActionFileSpec{Path: &path, Body: &body},
			})
			summary = append(summary, "restore  "+p)
		case inTarget && inLive && want.Body != have.Body:
			body := want.Body
			changes = append(changes, creght.SiteActionChange{
				Action: "file_update",
				File:   creght.SiteActionFileSpec{ID: have.ID, Body: &body},
			})
			summary = append(summary, "revert   "+p)
		case !inTarget && inLive:
			changes = append(changes, creght.SiteActionChange{
				Action: "file_delete",
				File:   creght.SiteActionFileSpec{ID: have.ID},
			})
			summary = append(summary, "delete   "+p)
		}
	}
	return changes, summary
}
