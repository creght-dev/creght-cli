package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// Site versions are the platform's git-commit equivalent: an immutable snapshot
// of the site's source files. `version create` records one, `version list`
// shows them, and `version publish` points the live site at one of them.
//
// A snapshot covers source files only. CMS content and the platform state under
// /platform/** (CMS/form/table/auth definitions) are live, so publishing an
// older version does not roll those back.
func runVersion(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printVersionUsage()
		return nil
	}

	switch args[0] {
	case "create":
		return runVersionCreate(ctx, args[1:])
	case "list":
		return runVersionList(ctx, args[1:])
	case "publish":
		return runVersionPublish(ctx, args[1:])
	case "cat":
		return runVersionCat(ctx, args[1:])
	case "diff":
		return runVersionDiff(ctx, args[1:])
	case "rollback":
		return runVersionRollback(ctx, args[1:])
	case "help", "-h", "--help":
		printVersionUsage()
		return nil
	default:
		return fmt.Errorf("unknown version command: %s", args[0])
	}
}

func printVersionUsage() {
	fmt.Println(`creght version

Usage:
  creght version                                     Print the installed CLI version
  creght version create [--note=<note>]                Snapshot the remote site source into a new version
  creght version list [--limit=<n>] [--json]           List site versions, newest first
  creght version publish <version_no> [--note=<note>]  Make an existing version live
  creght version cat <version_no> <path>               Print a file as of that version
  creght version diff <version_no> [<version_no>]      Compare two versions, or one against live
  creght version rollback <version_no>                 Put the live files back to that version

Notes:
  Inside a pulled workspace --site_id is optional; it is read from
  .creght/state.json like pull/push do.

  <version_no> is the per-site number shown in the VERSION column of
  creght version list. Pass id:<version_id> to select by id instead.`)
}

// versionSiteFlags registers the site/workspace flags every version subcommand
// accepts, mirroring pull/push so --site_id stays optional inside a workspace.
func versionSiteFlags(fs *flag.FlagSet) (siteID *string, dir *string) {
	siteID = fs.String("site_id", "", "project_id/site_id")
	dir = fs.String("dir", ".", "local directory")
	return siteID, dir
}

// resolveVersionSite resolves the target site the same way pull/push do: prefer
// an explicit --site_id, otherwise discover .creght/state.json from --dir or its
// parents. The workspace is optional, so version commands still work from
// anywhere with an explicit --site_id.
func resolveVersionSite(fs *flag.FlagSet, siteID string, dir string, quiet bool) (projectID string, realSiteID string, root string, err error) {
	root, resolvedSiteID, err := resolveSiteWorkspace(dir, siteID, !flagWasSet(fs, "dir"), false)
	if err != nil {
		return "", "", "", err
	}
	if !flagWasSet(fs, "dir") && !quiet {
		printWorkspaceNotice(root)
	}
	projectID, realSiteID, err = parseSiteRef(resolvedSiteID)
	if err != nil {
		return "", "", "", err
	}

	return projectID, realSiteID, root, nil
}

func runVersionCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("version create", flag.ContinueOnError)
	siteID, dir := versionSiteFlags(fs)
	note := fs.String("note", "", "version note")
	allowDirty := fs.Bool("allow-dirty", false, "snapshot the remote workspace even when local changes are unpushed")
	jsonOut := fs.Bool("json", false, "output the created version as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("version create does not accept positional arguments; use --note=<note>")
	}

	projectID, realSiteID, root, err := resolveVersionSite(fs, *siteID, *dir, *jsonOut)
	if err != nil {
		return err
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	if !*allowDirty {
		if err := requirePushedWorkspace(ctx, client, projectID, realSiteID, root); err != nil {
			return err
		}
	}

	result, err := client.CreateSiteVersion(ctx, projectID, realSiteID, *note)
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(result)
	}

	fmt.Printf("Created %s of %s/%s\n", versionLabel(result.VersionNo, result.VersionID), projectID, realSiteID)
	if strings.TrimSpace(*note) != "" {
		fmt.Printf("note: %s\n", strings.TrimSpace(*note))
	}
	fmt.Printf("Not live yet; run creght version publish %s to serve it\n", publishSelector(result.VersionNo, result.VersionID))
	return nil
}

func runVersionList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("version list", flag.ContinueOnError)
	siteID, dir := versionSiteFlags(fs)
	limit := fs.Int("limit", 0, "max versions to print (0 prints all returned)")
	jsonOut := fs.Bool("json", false, "output the publish state as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("version list does not accept positional arguments")
	}

	projectID, realSiteID, _, err := resolveVersionSite(fs, *siteID, *dir, *jsonOut)
	if err != nil {
		return err
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	state, err := client.GetSitePublishState(ctx, projectID, realSiteID)
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(state)
	}

	printVersionList(os.Stdout, state, *limit)
	return nil
}

func printVersionList(out io.Writer, state creght.SitePublishState, limit int) {
	versions := state.Versions
	truncated := 0
	if limit > 0 && len(versions) > limit {
		truncated = len(versions) - limit
		versions = versions[:limit]
	}

	if len(versions) == 0 {
		fmt.Fprintln(out, "No versions yet; run creght version create to snapshot the site")
	} else {
		w := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
		fmt.Fprintln(w, "\tVERSION\tID\tCREATED\tFROM\tNOTE")
		for _, version := range versions {
			marker := " "
			if version.ID == state.CurrentVersionID {
				marker = "*"
			}
			fmt.Fprintf(
				w, "%s\t%s\t%d\t%s\t%s\t%s\n",
				marker, versionNoColumn(version.VersionNo), version.ID,
				formatVersionTime(version.CreatedAt), dashIfEmpty(version.From), dashIfEmpty(version.Note),
			)
		}
		_ = w.Flush()
		if truncated > 0 {
			fmt.Fprintf(out, "... %d more version(s); raise --limit to see them\n", truncated)
		}
	}

	if state.CurrentVersionID != 0 {
		live := versionLabel(state.CurrentVersionNo, state.CurrentVersionID)
		if len(state.PublishTargets) > 0 {
			fmt.Fprintf(out, "* live: %s, served by %s\n", live, strings.Join(state.PublishTargets, ", "))
		} else {
			fmt.Fprintf(out, "* live: %s\n", live)
		}
	} else {
		fmt.Fprintln(out, "Nothing published yet")
	}

	for _, domain := range state.Domains {
		if domain.Follow {
			continue
		}
		fmt.Fprintf(out, "pinned: %s -> %s\n", domain.Domain, versionLabel(domain.PublishVersionNo, domain.PublishVersionID))
	}

	if state.HasChanges {
		if len(state.WorkspaceChanges) > 0 {
			fmt.Fprintf(out, "%s pending on the site since the newest version; run creght version create to snapshot them\n", changeCount(len(state.WorkspaceChanges)))
		} else {
			fmt.Fprintln(out, "The site has changes since the newest version; run creght version create to snapshot them")
		}
	}
}

func runVersionPublish(ctx context.Context, args []string) error {
	positionals, flagArgs := splitFlagArgs(args)
	fs := flag.NewFlagSet("version publish", flag.ContinueOnError)
	siteID, dir := versionSiteFlags(fs)
	note := fs.String("note", "", "publish note")
	jsonOut := fs.Bool("json", false, "output the publish result as JSON")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	// The version is a positional rather than a flag on purpose: a --version flag
	// here would be swallowed by the top-level "print the CLI version" handling.
	if len(positionals) > 1 {
		return fmt.Errorf("version publish accepts exactly one <version_no> argument")
	}
	selector := ""
	if len(positionals) == 1 {
		selector = strings.TrimSpace(positionals[0])
	}
	if selector == "" {
		return fmt.Errorf("version publish requires a version; pass <version_no> (see creght version list) or id:<version_id>")
	}

	projectID, realSiteID, _, err := resolveVersionSite(fs, *siteID, *dir, *jsonOut)
	if err != nil {
		return err
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	state, err := client.GetSitePublishState(ctx, projectID, realSiteID)
	if err != nil {
		return err
	}
	target, err := resolveVersionSelector(state, selector)
	if err != nil {
		return err
	}

	result, err := client.PublishSiteVersion(ctx, projectID, realSiteID, target.ID, *note)
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(result)
	}

	label := versionLabel(result.VersionNo, result.VersionID)
	if !result.Changed {
		fmt.Printf("%s was already live on %s/%s; refreshed its caches\n", label, projectID, realSiteID)
	} else if len(result.Targets) > 0 {
		fmt.Printf("Published %s of %s/%s to %s\n", label, projectID, realSiteID, strings.Join(result.Targets, ", "))
	} else {
		fmt.Printf("Published %s of %s/%s\n", label, projectID, realSiteID)
	}
	if strings.TrimSpace(target.Note) != "" {
		fmt.Printf("note: %s\n", strings.TrimSpace(target.Note))
	}
	return nil
}

// resolveVersionSelector turns a user-facing selector into the version to
// publish. A bare number is the per-site version number shown by
// `version list`; id:<n> selects by version id for versions older than the
// window that endpoint returns.
func resolveVersionSelector(state creght.SitePublishState, selector string) (creght.SiteVersion, error) {
	selector = strings.TrimSpace(selector)
	if rawID, ok := trimVersionIDPrefix(selector); ok {
		versionID, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
		if err != nil || versionID <= 0 {
			return creght.SiteVersion{}, fmt.Errorf("invalid version id %q; expected id:<version_id> with a positive integer", selector)
		}
		// An id may predate the listed window, so publish it without a local
		// match; the server rejects ids that do not belong to this site.
		if version, ok := state.FindVersionByID(versionID); ok {
			return version, nil
		}
		return creght.SiteVersion{ID: versionID}, nil
	}

	versionNo, err := strconv.ParseInt(selector, 10, 64)
	if err != nil || versionNo <= 0 {
		return creght.SiteVersion{}, fmt.Errorf("invalid version %q; expected a positive <version_no> from creght version list, or id:<version_id>", selector)
	}
	version, ok := state.FindVersionByNo(versionNo)
	if !ok {
		return creght.SiteVersion{}, fmt.Errorf("version %d not found in the %s of this site; run creght version list to see them, or select by id with id:<version_id>", versionNo, versionWindow(len(state.Versions)))
	}

	return version, nil
}

func trimVersionIDPrefix(selector string) (string, bool) {
	for _, prefix := range []string{"id:", "ID:", "#"} {
		if rest, ok := strings.CutPrefix(selector, prefix); ok {
			return rest, true
		}
	}
	return "", false
}

// requirePushedWorkspace refuses to snapshot while the local workspace still has
// changes the site has never received. A version records the remote files, so
// unpushed local work would silently be left out of it.
func requirePushedWorkspace(ctx context.Context, client *creght.Client, projectID string, realSiteID string, dir string) error {
	_, hasState, err := loadWorkspaceState(dir)
	if err != nil {
		return err
	}
	if !hasState {
		// Not a pulled workspace, so there is no local copy to compare against.
		return nil
	}

	syncer, err := NewSyncer(client, projectID, realSiteID, dir)
	if err != nil {
		return err
	}
	// allowDelete so local deletions surface as pending changes rather than
	// being silently skipped; nothing is applied here.
	planCtx, err := syncer.buildPlanContext(ctx, true)
	if err != nil {
		return err
	}
	plan := planCtx.plan
	if !plan.hasChanges() && !plan.hasConflicts() {
		return nil
	}

	for _, action := range plan.FileActions {
		fmt.Printf("%s %s\n", siteActionLabel(action.action.Action), action.remotePath)
	}
	for _, conflict := range plan.Conflicts {
		fmt.Printf("conflict %s %s: %s\n", conflict.Kind, conflict.Path, conflict.Reason)
	}

	return fmt.Errorf(
		"%s not pushed yet and would be missing from this version; run creght push first, or pass --allow-dirty to snapshot the remote site as it is",
		changeCount(len(plan.FileActions)+len(plan.Conflicts)),
	)
}

// versionLabel names a version for humans. Legacy visual-editor sites have no
// per-site number, so it falls back to the id alone.
func versionLabel(versionNo int64, versionID int64) string {
	if versionNo > 0 {
		return fmt.Sprintf("version %d (id %d)", versionNo, versionID)
	}
	return fmt.Sprintf("version id %d", versionID)
}

// publishSelector renders the selector `version publish` would accept for a
// version, preferring the per-site number users see.
func publishSelector(versionNo int64, versionID int64) string {
	if versionNo > 0 {
		return strconv.FormatInt(versionNo, 10)
	}
	return "id:" + strconv.FormatInt(versionID, 10)
}

func versionNoColumn(versionNo int64) string {
	if versionNo > 0 {
		return strconv.FormatInt(versionNo, 10)
	}
	return "-"
}

func formatVersionTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func dashIfEmpty(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "-"
	}
	// Notes are free text; keep the table one row per version.
	return strings.Join(strings.Fields(v), " ")
}

func changeCount(n int) string {
	if n == 1 {
		return "1 change is"
	}
	return fmt.Sprintf("%d changes are", n)
}

func versionWindow(listed int) string {
	if listed == 0 {
		return "versions"
	}
	return fmt.Sprintf("%d most recent version(s)", listed)
}
