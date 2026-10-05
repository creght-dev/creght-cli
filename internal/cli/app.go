package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/creght-dev/creght-cli/internal/creght"
	"github.com/creght-dev/creght-cli/pkg/sitesync"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	defaultAPIHostValue = "https://creght.cn"
	defaultWebHostValue = "https://creght.cn"
)

var version = "dev"

func envAPIHost() (string, bool) {
	v, ok := os.LookupEnv("CREGHT_API_HOST")
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	return v, v != ""
}

func defaultAPIHost() string {
	if v, ok := envAPIHost(); ok {
		return v
	}

	if v := strings.TrimSpace(viper.GetString("api_host")); v != "" {
		return v
	}

	return defaultAPIHostValue
}

func defaultWebHost(apiHost string) string {
	if v := strings.TrimSpace(viper.GetString("web_host")); v != "" {
		return v
	}

	u, err := url.Parse(apiHost)
	if err == nil {
		host := u.Hostname()
		if host == "localhost" || host == "127.0.0.1" {
			u.Host = "localhost:5173"
			u.Path = ""
			u.RawQuery = ""
			u.Fragment = ""
			return strings.TrimRight(u.String(), "/")
		}
	}

	return defaultWebHostValue
}

func clientFromConfig() (*creght.Client, Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, Config{}, err
	}

	client := creght.NewClient(cfg.APIHost, cfg.Token)
	client.SetAuthHint(authHint(cfg))
	return client, cfg, nil
}

func runLogin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	webHost := fs.String("web", "", "Creght web host")
	err := fs.Parse(args)
	if err != nil {
		return err
	}
	if err := refuseEnvToken("login"); err != nil {
		return err
	}

	client, cfg, err := clientFromConfig()
	if err != nil {
		return err
	}

	resolvedWebHost := strings.TrimSpace(*webHost)
	if resolvedWebHost == "" {
		resolvedWebHost = defaultWebHost(cfg.APIHost)
	}

	session, err := client.CreateCLIAuthSession(ctx, resolvedWebHost)
	if err != nil {
		return err
	}

	fmt.Printf("Open this URL to authorize Creght CLI:\n%s\n", session.VerifyURL)
	_ = openBrowser(session.VerifyURL)

	deadline := time.Now().Add(time.Duration(session.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		result, err := client.GetCLIAuthSession(ctx, session.Code)
		if err != nil {
			return err
		}
		if result.Status == "approved" {
			cfg.Token = result.Token
			err = saveConfig(cfg)
			if err != nil {
				return err
			}

			fmt.Printf("Logged in to %s.\n", canonicalAPIHost(cfg.APIHost))
			printImplicitHostNotice(canonicalAPIHost(cfg.APIHost))
			return nil
		}
		if result.Status == "expired" {
			return fmt.Errorf("authorization expired")
		}
	}

	return fmt.Errorf("authorization timed out")
}

// printImplicitHostNotice explains why the saved default did not move after a
// login against a discovered host. The token was saved for that host, but the
// default stayed put — without a word about it, the next bare command talking to
// a different host looks like a bug.
func printImplicitHostNotice(loggedInHost string) {
	saved, err := loadRawConfig()
	if err != nil {
		return
	}
	savedHost := canonicalAPIHost(saved.APIHost)
	if savedHost == "" || savedHost == loggedInHost {
		return
	}

	switch resolved := resolveAPIHost(saved.APIHost); resolved.Source {
	case apiHostSourceEnv:
		fmt.Printf("Default API host is still %s; keep setting CREGHT_API_HOST, "+
			"or run creght config set api_host=%s to switch it.\n", savedHost, loggedInHost)
	case apiHostSourceWorkspace:
		fmt.Printf("Default API host is still %s; commands under %s auto-discover %s, "+
			"or run creght config set api_host=%s to switch the default.\n",
			savedHost, resolved.Workspace, resolved.Host, loggedInHost)
	}
}

// runLogout revokes the token server-side, drops it from git's credential store,
// then removes the local config — in that order, because each step needs what the
// previous one still has.
//
// Revoking first matters: `creght logout` used to only delete the local file, so
// the token stayed valid until its TTL expired and any other copy of it kept
// working. Reporting "Logged out." while the credential still authenticates is a
// lie worth avoiding, which is why a failed revoke aborts instead of pressing on.
func runLogout(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	localOnly := fs.Bool("local_only", false, "only forget the local credentials; do not revoke the token server-side")
	err := fs.Parse(args)
	if err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("logout does not accept positional arguments")
	}
	// The borrowed token is not ours to revoke, and the saved logins are not
	// the ones in use, so neither the server nor config.json is touched.
	if err := refuseEnvToken("logout"); err != nil {
		return err
	}

	client, cfg, err := clientFromConfig()
	if err != nil {
		return err
	}

	if !*localOnly {
		if cfg.Token == "" {
			fmt.Println("No saved login for", canonicalAPIHost(cfg.APIHost))
		} else if err := client.Logout(ctx); err != nil {
			// Keep the local config so the revoke can be retried; deleting it now
			// would leave a live token with no way to reach it.
			return fmt.Errorf("revoke token on %s: %w\n"+
				"The token is still valid server-side. Retry when reachable, "+
				"or run `creght logout --local_only` to only forget it locally",
				canonicalAPIHost(cfg.APIHost), err)
		}
	}

	host := canonicalAPIHost(cfg.APIHost)
	if err := deleteConfig(host); err != nil {
		return err
	}

	if *localOnly {
		fmt.Printf("Local credentials for %s removed. The token is still valid server-side until it expires.\n", host)
	} else {
		fmt.Printf("Logged out of %s.\n", host)
	}
	return nil
}

// runConfig backs `creght config`, the one way to move the saved default API
// host. CREGHT_API_HOST deliberately does not move it (see saveConfig), so
// without this command a default chosen at first login could never be changed.
func runConfig(_ context.Context, args []string) error {
	if len(args) == 0 {
		return runConfigGet(nil)
	}

	switch args[0] {
	case "get":
		return runConfigGet(args[1:])
	case "set":
		return runConfigSet(args[1:])
	default:
		return fmt.Errorf("unknown config subcommand: %s", args[0])
	}
}

func runConfigGet(args []string) error {
	fs := flag.NewFlagSet("config get", flag.ContinueOnError)
	err := fs.Parse(args)
	if err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("config get takes at most one key")
	}
	if fs.NArg() == 1 {
		if err := checkConfigKey(fs.Arg(0)); err != nil {
			return err
		}
	}

	saved, err := loadRawConfig()
	if err != nil {
		return err
	}
	savedHost := canonicalAPIHost(saved.APIHost)
	if savedHost == "" {
		savedHost = canonicalAPIHost(defaultAPIHostValue)
	}

	fmt.Printf("api_host\t%s\n", savedHost)
	// Only the source actually in effect is reported; naming the others would
	// suggest more than one host is in play.
	switch resolved := resolveAPIHost(saved.APIHost); resolved.Source {
	case apiHostSourceEnv:
		if resolved.Host != savedHost {
			fmt.Printf("  CREGHT_API_HOST=%s overrides it for this command only\n", resolved.Host)
		}
	case apiHostSourceWorkspace:
		if resolved.Host != savedHost {
			fmt.Printf("  workspace %s auto-discovers %s from .creght/state.json\n", resolved.Workspace, resolved.Host)
		}
	}

	return nil
}

func runConfigSet(args []string) error {
	fs := flag.NewFlagSet("config set", flag.ContinueOnError)
	err := fs.Parse(args)
	if err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("config set takes one <key>=<value> argument, e.g. creght config set api_host=https://creght.cn")
	}

	key, value, ok := strings.Cut(fs.Arg(0), "=")
	if !ok {
		return fmt.Errorf("config set expects <key>=<value>, e.g. creght config set api_host=https://creght.cn")
	}
	if err := checkConfigKey(key); err != nil {
		return err
	}

	apiHost := canonicalAPIHost(value)
	u, parseErr := url.Parse(apiHost)
	if apiHost == "" || parseErr != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("invalid api_host %q: want an absolute URL such as https://creght.cn", strings.TrimSpace(value))
	}

	// Written straight through rather than via saveConfig, which refuses to move
	// the default while CREGHT_API_HOST is set. Moving it is this command's job.
	cfg, err := loadRawConfig()
	if err != nil {
		return err
	}
	cfg.APIHost = apiHost
	cfg.Token = cfg.Tokens[apiHost]

	path, err := configPath()
	if err != nil {
		return err
	}
	if err := writeConfig(path, cfg); err != nil {
		return err
	}

	fmt.Printf("api_host\t%s\n", apiHost)
	if cfg.Token == "" {
		fmt.Println("No saved login for this host yet; run creght login.")
	}

	return nil
}

func checkConfigKey(key string) error {
	if strings.TrimSpace(key) != "api_host" {
		return fmt.Errorf("unknown config key %q; the only settable key is api_host", strings.TrimSpace(key))
	}

	return nil
}

func runProjectList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("project", flag.ContinueOnError)
	err := fs.Parse(args)
	if err != nil {
		return err
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	projects, err := client.GetProjectList(ctx)
	if err != nil {
		return err
	}

	for _, project := range projects.List {
		fmt.Printf("%s\t%s\n", project.ID, project.Name)
		for _, site := range project.SiteList {
			fmt.Printf("  %s/%s\t%s\n", project.ID, site.ID, site.Name)
		}
	}

	return nil
}

func runProject(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return runProjectList(ctx, args)
	}

	switch args[0] {
	case "list":
		return runProjectList(ctx, args[1:])
	case "create":
		return runProjectCreate(ctx, args[1:])
	default:
		return fmt.Errorf("unknown project subcommand: %s", args[0])
	}
}

func runProjectCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("project create", flag.ContinueOnError)
	name := fs.String("name", "", "project name")
	fromID := fs.String("from_id", "", "existing project id to copy")
	tplID := fs.Int64("tpl_id", 0, "template id to use")
	err := fs.Parse(args)
	if err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("project create does not accept positional arguments; use --name=<project_name>")
	}

	projectName := strings.TrimSpace(*name)
	if projectName == "" {
		return fmt.Errorf("project create requires --name")
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	id, err := client.CreateProject(ctx, creght.CreateProjectRequest{
		Name:   projectName,
		FromID: strings.TrimSpace(*fromID),
		TplID:  *tplID,
	})
	if err != nil {
		return err
	}

	fmt.Printf("Created project %s\t%s\n", id, projectName)
	printProjectSites(id, lookupProjectSites(ctx, client, id))
	return nil
}

func runPull(ctx context.Context, args []string) error {
	positionals, flagArgs := splitFlagArgs(args)
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	dir := fs.String("dir", ".", "local directory")
	force := fs.Bool("force", false, "overwrite local files with the remote workspace")
	versionNo := fs.String("version_no", "", "pull that site version as a read-only snapshot")
	err := fs.Parse(flagArgs)
	if err != nil {
		return err
	}
	resolvedDir, resolvedSiteID, err := resolveSiteWorkspace(*dir, *siteID, !flagWasSet(fs, "dir"), false)
	if err != nil {
		return err
	}
	*dir, *siteID = resolvedDir, resolvedSiteID
	if !flagWasSet(fs, "dir") {
		printWorkspaceNotice(*dir)
	}
	warnAPIHostMismatch(*dir)

	projectID, realSiteID, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	site := sitesync.Site{ProjectID: projectID, SiteID: realSiteID}

	if len(positionals) > 1 {
		return fmt.Errorf("pull accepts at most one <path> argument")
	}
	if flagWasSet(fs, "version_no") {
		if len(positionals) == 1 {
			return fmt.Errorf("pull --version_no pulls a whole version; it does not take a <path> (use creght version cat <version_no> <path> for one file)")
		}
		no, err := parseSnapshotVersionNo(*versionNo)
		if err != nil {
			return err
		}
		syncClient, cfg, err := syncClientFromConfig()
		if err != nil {
			return err
		}
		res, err := syncClient.Pull(ctx, *dir, site, sitesync.PullOptions{VersionNo: no})
		if err != nil {
			return withAuthHint(err, cfg)
		}
		printSnapshotPull(site, res)
		return nil
	}
	if err := refuseSnapshotWorkspace(*dir, "pull without --version_no"); err != nil {
		return err
	}
	if len(positionals) == 1 {
		return pullOneFile(ctx, projectID, realSiteID, *dir, positionals[0], *force)
	}

	syncClient, cfg, err := syncClientFromConfig()
	if err != nil {
		return err
	}
	res, err := syncClient.Pull(ctx, *dir, site, sitesync.PullOptions{Force: *force})
	if err != nil {
		return withAuthHint(err, cfg)
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	editorURL := siteEditorURL(defaultWebHost(cfg.APIHost), projectID, realSiteID)
	previewURL, _ := previewURL(ctx, client, realSiteID)
	for _, path := range res.Merged {
		fmt.Printf("merged %s\n", path)
	}
	for _, path := range res.Conflicted {
		fmt.Printf("conflict %s: wrote conflict markers\n", path)
	}
	if res.BackupDir != "" {
		fmt.Printf("Backed up overwritten local files to %s\n", res.BackupDir)
	}
	fmt.Printf("Pulled %d changes into %s\n", res.Changed, *dir)
	if res.AgentsFileCreated {
		fmt.Printf("Generated AGENTS.md for Creght agent context\n")
	}
	fmt.Printf("Editor: %s\n", editorURL)
	if previewURL != "" {
		fmt.Printf("Preview: %s\n", previewURL)
	}

	if len(res.Conflicted) > 0 {
		return fmt.Errorf("pulled with %d conflicted file(s); edit the conflict markers or run creght resolve, then push", len(res.Conflicted))
	}
	return nil
}

func printSnapshotPull(site sitesync.Site, res sitesync.PullResult) {
	snap := res.Snapshot
	label := fmt.Sprintf("version %d", snap.VersionNo)
	if snap.VersionID > 0 {
		label = versionLabel(snap.VersionNo, snap.VersionID)
	}
	fmt.Printf("Pulled %s of %s into %s: %d file(s)", label, site, res.Dir, snap.Files)
	if snap.Removed > 0 {
		fmt.Printf(", removed %d file(s) not in this version", snap.Removed)
	}
	fmt.Println()
	if snap.Note != "" {
		fmt.Printf("note: %s\n", snap.Note)
	}
	fmt.Println("This is a read-only snapshot: push is disabled here. Pull another version with --version_no to switch.")
}

func siteEditorURL(webHost string, projectID string, siteID string) string {
	return fmt.Sprintf("%s/teditor/project/%s/site/%s", strings.TrimRight(webHost, "/"), url.PathEscape(projectID), url.PathEscape(siteID))
}

func runPush(ctx context.Context, args []string) error {
	positionals, flagArgs := splitFlagArgs(args)
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	dir := fs.String("dir", ".", "local directory")
	allowDelete := fs.Bool("delete", false, "delete remote files/functions that were removed locally")
	force := fs.Bool("force", false, "overwrite remote with the local workspace snapshot")
	skipConflicts := fs.Bool("skip-conflicts", false, "push non-conflicting files and keep conflicted ones for a later pull")
	err := fs.Parse(flagArgs)
	if err != nil {
		return err
	}
	resolvedDir, resolvedSiteID, err := resolveSiteWorkspace(*dir, *siteID, !flagWasSet(fs, "dir"), true)
	if err != nil {
		return err
	}
	*dir, *siteID = resolvedDir, resolvedSiteID
	if !flagWasSet(fs, "dir") {
		printWorkspaceNotice(*dir)
	}
	warnAPIHostMismatch(*dir)

	projectID, realSiteID, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	if err := refuseSnapshotWorkspace(*dir, "push"); err != nil {
		return err
	}

	if len(positionals) > 1 {
		return fmt.Errorf("push accepts at most one <path> argument")
	}
	if len(positionals) == 1 {
		return pushOneFile(ctx, projectID, realSiteID, *dir, positionals[0], *force)
	}

	syncClient, cfg, err := syncClientFromConfig()
	if err != nil {
		return err
	}
	if _, err := syncClient.Push(ctx, *dir, sitesync.PushOptions{Delete: *allowDelete, Force: *force, SkipConflicts: *skipConflicts}); err != nil {
		return withAuthHint(err, cfg)
	}

	absDir, err := filepath.Abs(*dir)
	if err != nil {
		absDir = *dir
	}
	fmt.Printf("Pushed %s -> %s/%s\n", absDir, projectID, realSiteID)
	// The push is already done, so a preview host that cannot be resolved is a
	// missing line, not a failed command.
	if client, _, err := clientFromConfig(); err == nil {
		if preview, err := previewURL(ctx, client, realSiteID); err == nil && preview != "" {
			fmt.Printf("Preview: %s\n", preview)
		}
	}
	return nil
}

func runDiff(ctx context.Context, args []string) error {
	positionals, flagArgs := splitFlagArgs(args)
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	dir := fs.String("dir", ".", "local directory")
	allowDelete := fs.Bool("delete", false, "show remote deletions for files/functions removed locally")
	jsonOut := fs.Bool("json", false, "output the change plan as JSON")
	err := fs.Parse(flagArgs)
	if err != nil {
		return err
	}
	resolvedDir, resolvedSiteID, err := resolveSiteWorkspace(*dir, *siteID, !flagWasSet(fs, "dir"), true)
	if err != nil {
		return err
	}
	*dir, *siteID = resolvedDir, resolvedSiteID
	if !flagWasSet(fs, "dir") && !*jsonOut {
		printWorkspaceNotice(*dir)
	}
	warnAPIHostMismatch(*dir)

	projectID, realSiteID, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}
	if err := refuseSnapshotWorkspace(*dir, "diff"); err != nil {
		return err
	}

	if len(positionals) > 1 {
		return fmt.Errorf("diff accepts at most one <path> argument")
	}
	if len(positionals) == 1 {
		return diffOneFile(ctx, projectID, realSiteID, *dir, positionals[0])
	}

	client, cfg, err := clientFromConfig()
	if err != nil {
		return err
	}

	syncer, err := NewSyncer(client, projectID, realSiteID, *dir, cfg.APIHost, os.Stdout)
	if err != nil {
		return err
	}

	// The same plan pkg/sitesync's Diff returns; the text form also needs the
	// skipped deletes the JSON leaves out, so it is read here directly.
	plan, err := syncer.Plan(ctx, *allowDelete)
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(plan.Result())
	}
	plan.Print(os.Stdout)
	if plan.HasConflicts() {
		return fmt.Errorf("diff has conflicts; run creght pull to merge remote changes, then resolve any conflict markers before pushing")
	}
	return nil
}

func runPublish(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	note := fs.String("note", "", "publish note")
	err := fs.Parse(args)
	if err != nil {
		return err
	}

	if fs.NArg() != 0 {
		return fmt.Errorf("publish does not accept positional arguments; use --site_id=<project_id>/<site_id>")
	}

	projectID, realSiteID, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	result, err := client.PublishSite(ctx, projectID, realSiteID, *note)
	if err != nil {
		return err
	}

	printPublishResult(os.Stdout, projectID, realSiteID, result, liveURLScheme(ctx, client))
	return nil
}

// printPublishResult reports what went live and where, naming the published
// domains as URLs so they can be opened straight from the terminal.
func printPublishResult(out io.Writer, projectID string, realSiteID string, result creght.PublishVersionResult, scheme string) {
	fmt.Fprintf(out, "Published %s/%s\n", projectID, realSiteID)
	if result.VersionID == 0 {
		return
	}
	if len(result.Targets) == 0 {
		fmt.Fprintf(out, "%s is live\n", versionLabel(result.VersionNo, result.VersionID))
		return
	}

	fmt.Fprintf(out, "%s is live on:\n", versionLabel(result.VersionNo, result.VersionID))
	for _, target := range result.Targets {
		if address := domainURL(scheme, target); address != "" {
			fmt.Fprintf(out, "  %s\n", address)
		}
	}
}

// liveURLScheme reports the scheme published domains are served over. The
// publish panel returns bare hostnames, and this only decorates them, so a
// deployment that cannot be reached falls back to https rather than failing a
// publish that already succeeded.
func liveURLScheme(ctx context.Context, client *creght.Client) string {
	info, err := client.GetSystemInfo(ctx)
	if err != nil {
		return "https"
	}

	return schemeOf(info.SelfAPIHost)
}

func releaseTag(rawVersion string) (string, error) {
	v := strings.TrimSpace(rawVersion)
	if v == "" || v == "dev" {
		return "", fmt.Errorf("cannot publish version %q", rawVersion)
	}
	if strings.HasPrefix(v, "v") {
		return v, nil
	}

	return "v" + v, nil
}

func gitRun(ctx context.Context, args ...string) error {
	_, err := gitOutput(ctx, args...)
	return err
}

func gitOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return "", err
		}
		return "", errors.New(msg)
	}

	return strings.TrimSpace(string(out)), nil
}

func parseSiteRef(ref string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(ref), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("site_id must be <project_id>/<site_id>")
	}

	return parts[0], parts[1], nil
}

// warnAPIHostMismatch flags a sync about to run against a different deployment
// than the workspace was pulled from.
//
// The override still applies — an explicit CREGHT_API_HOST outranks the recorded
// host by design, and that escape hatch is the reason it ranks higher at all.
// But aiming one site's files at another deployment should not happen silently,
// so it costs a line on stderr, where it cannot corrupt `diff --json`.
func warnAPIHostMismatch(root string) {
	state, hasState, err := loadWorkspaceState(root)
	if err != nil || !hasState {
		return
	}
	recorded := canonicalAPIHost(strings.TrimSpace(state.APIHost))
	if recorded == "" {
		// Pulled by a CLI that did not record the host; there is nothing to
		// disagree with.
		return
	}

	resolved := currentAPIHost()
	if resolved.Host == recorded {
		return
	}
	if resolved.Source == apiHostSourceEnv {
		fmt.Fprintf(os.Stderr, "warning: workspace %s was pulled from %s; CREGHT_API_HOST points this command at %s\n",
			root, recorded, resolved.Host)
		return
	}
	fmt.Fprintf(os.Stderr, "warning: workspace %s was pulled from %s, but this command uses %s (%s)\n",
		root, recorded, resolved.Host, resolved.describe())
}

// printWorkspaceNotice reports which discovered workspace root a command
// operates on when it is not the current directory, so accidentally acting on
// an ancestor workspace is visible before anything happens.
func printWorkspaceNotice(root string) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return
	}
	rootResolved, cwdResolved := absRoot, cwd
	if r, err := filepath.EvalSymlinks(absRoot); err == nil {
		rootResolved = r
	}
	if c, err := filepath.EvalSymlinks(cwd); err == nil {
		cwdResolved = c
	}
	if rootResolved != cwdResolved {
		fmt.Printf("workspace: %s\n", absRoot)
	}
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func previewURL(ctx context.Context, client *creght.Client, siteID string) (string, error) {
	info, err := client.GetSystemInfo(ctx)
	if err != nil {
		return "", err
	}

	return previewURLForHost(info.SelfAPIHost, siteID), nil
}

// previewURLForHost derives a site's preview address from the deployment's own
// API host: the preview environment is a subdomain of it.
func previewURLForHost(apiHost string, siteID string) string {
	if strings.TrimSpace(apiHost) == "" {
		return ""
	}

	u, err := url.Parse(apiHost)
	if err != nil || u.Host == "" {
		return ""
	}
	u.Host = siteID + ".preview." + u.Host
	u.Path = "/"
	u.RawQuery = ""
	u.Fragment = ""

	return u.String()
}

// openBrowser is a variable so tests can observe what --open would launch
// without a browser window appearing.
var openBrowser = func(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}

	return cmd.Start()
}
