package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"errors"
	"flag"
	"fmt"
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

	return creght.NewClient(cfg.APIHost, cfg.Token), cfg, nil
}

func runLogin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	webHost := fs.String("web", "", "Creght web host")
	err := fs.Parse(args)
	if err != nil {
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
			if _, fromEnv := envAPIHost(); fromEnv {
				// The token was saved for this host, but the default was not
				// moved. Say so, or the next bare command silently talking to a
				// different host looks like a bug.
				if saved, err := loadRawConfig(); err == nil && canonicalAPIHost(saved.APIHost) != canonicalAPIHost(cfg.APIHost) {
					fmt.Printf("Default API host is still %s; keep setting CREGHT_API_HOST, "+
						"or run creght config set api_host=%s to switch it.\n",
						canonicalAPIHost(saved.APIHost), canonicalAPIHost(cfg.APIHost))
				}
			}
			return nil
		}
		if result.Status == "expired" {
			return fmt.Errorf("authorization expired")
		}
	}

	return fmt.Errorf("authorization timed out")
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

	if err := deleteConfig(); err != nil {
		return err
	}

	if *localOnly {
		fmt.Println("Local credentials removed. The token is still valid server-side until it expires.")
	} else {
		fmt.Println("Logged out.")
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
	if envHost, ok := envAPIHost(); ok && canonicalAPIHost(envHost) != savedHost {
		fmt.Printf("  CREGHT_API_HOST=%s overrides it for this command only\n", canonicalAPIHost(envHost))
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
	return nil
}

func runPull(ctx context.Context, args []string) error {
	positionals, flagArgs := splitFlagArgs(args)
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	dir := fs.String("dir", ".", "local directory")
	force := fs.Bool("force", false, "overwrite local files with the remote workspace")
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

	projectID, realSiteID, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}

	if len(positionals) > 1 {
		return fmt.Errorf("pull accepts at most one <path> argument")
	}
	if len(positionals) == 1 {
		return pullOneFile(ctx, projectID, realSiteID, *dir, positionals[0], *force)
	}

	client, cfg, err := clientFromConfig()
	if err != nil {
		return err
	}

	files, err := client.GetFileList(ctx, projectID, realSiteID)
	if err != nil {
		return err
	}

	remoteSnap, err := remoteFileSnapshotForWorkspace(*dir, files.List)
	if err != nil {
		return err
	}
	var outcome pullOutcome
	if *force {
		state, hasState, err := loadWorkspaceState(*dir)
		if err != nil {
			return err
		}
		ignore, err := loadCreghtIgnore(*dir)
		if err != nil {
			return err
		}
		state.Files = filterIgnoredState(ignore, state.Files)
		localFiles, err := localFileSnapshot(*dir)
		if err != nil {
			return err
		}
		incoming := map[string]string{}
		for path, entry := range remoteSnap {
			incoming[path] = entry.Body
		}
		outcome.backupDir, err = backupOverwrittenLocalFiles(*dir, state, hasState, localFiles, incoming)
		if err != nil {
			return err
		}
		if err := writeRemoteFilesToWorkspace(*dir, files.List); err != nil {
			return err
		}
		if err := saveWorkspaceState(*dir, projectID+"/"+realSiteID, remoteSnap); err != nil {
			return err
		}
		outcome.changed = len(remoteSnap)
	} else {
		outcome, err = safePullWorkspace(*dir, projectID+"/"+realSiteID, remoteSnap)
		if err != nil {
			return err
		}
	}

	editorURL := siteEditorURL(defaultWebHost(cfg.APIHost), projectID, realSiteID)
	createdAgents, err := ensurePulledAgentsFile(*dir, nil, projectID, realSiteID, editorURL)
	if err != nil {
		return err
	}

	previewURL, _ := previewURL(ctx, client, realSiteID)
	for _, path := range outcome.merged {
		fmt.Printf("merged %s\n", path)
	}
	for _, path := range outcome.conflicted {
		fmt.Printf("conflict %s: wrote conflict markers\n", path)
	}
	if outcome.backupDir != "" {
		fmt.Printf("Backed up overwritten local files to %s\n", outcome.backupDir)
	}
	fmt.Printf("Pulled %d changes into %s\n", outcome.changed, *dir)
	if createdAgents {
		fmt.Printf("Generated AGENTS.md for Creght agent context\n")
	}
	fmt.Printf("Editor: %s\n", editorURL)
	if previewURL != "" {
		fmt.Printf("Preview: %s\n", previewURL)
	}

	if len(outcome.conflicted) > 0 {
		return fmt.Errorf("pulled with %d conflicted file(s); edit the conflict markers or run creght resolve, then push", len(outcome.conflicted))
	}
	return nil
}

// pullOutcome summarizes what a pull did, for reporting.
type pullOutcome struct {
	changed    int
	merged     []string // both sides changed, auto-merged cleanly
	conflicted []string // both sides changed, conflict markers written
	backupDir  string   // where overwritten local work was saved, if any
}

func safePullWorkspace(root string, siteID string, remoteFiles map[string]snapshotEntry) (pullOutcome, error) {
	var outcome pullOutcome
	ignore, err := loadCreghtIgnore(root)
	if err != nil {
		return outcome, err
	}
	remoteFiles = filterIgnoredSnapshot(ignore, remoteFiles)
	state, hasState, err := loadWorkspaceState(root)
	if err != nil {
		return outcome, err
	}
	state.Files = filterIgnoredState(ignore, state.Files)
	if hasState && strings.TrimSpace(state.SiteID) != "" && state.SiteID != siteID {
		return outcome, fmt.Errorf("workspace state belongs to %s, not %s", state.SiteID, siteID)
	}

	localFiles, err := localFileSnapshot(root)
	if err != nil {
		return outcome, err
	}

	filePlan := buildPullEntryPlan("file", state.Files, hasState, localFiles, remoteFiles, func(hash string) (string, bool) {
		return readBaseObject(root, hash)
	})
	if len(filePlan.Conflicts) > 0 {
		for _, conflict := range filePlan.Conflicts {
			fmt.Printf("conflict %s %s: %s\n", conflict.Kind, conflict.Path, conflict.Reason)
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
	outcome.backupDir, err = backupOverwrittenLocalFiles(root, state, hasState, localFiles, incoming)
	if err != nil {
		return outcome, err
	}

	for _, entry := range filePlan.Writes {
		if err := writePulledFile(root, entry); err != nil {
			return outcome, err
		}
	}
	for _, entry := range filePlan.CleanMerges {
		if err := writePulledFile(root, entry); err != nil {
			return outcome, err
		}
		outcome.merged = append(outcome.merged, entry.Path)
	}
	for _, entry := range filePlan.ConflictWrites {
		if err := writePulledFile(root, entry); err != nil {
			return outcome, err
		}
		outcome.conflicted = append(outcome.conflicted, entry.Path)
	}
	for _, path := range filePlan.Deletes {
		if err := deletePulledFile(root, path); err != nil {
			return outcome, err
		}
	}
	if err := saveWorkspaceState(root, siteID, remoteFiles); err != nil {
		return outcome, err
	}
	outcome.changed = len(filePlan.Writes) + len(filePlan.CleanMerges) + len(filePlan.ConflictWrites) + len(filePlan.Deletes)
	return outcome, nil
}

func writePulledFile(root string, entry snapshotEntry) error {
	localPath, err := remotePathToLocal(root, entry.Path)
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
	localPath, err := remotePathToLocal(root, remotePath)
	if err != nil {
		return err
	}
	if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete %s: %w", remotePath, err)
	}
	return nil
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

	projectID, realSiteID, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}

	if len(positionals) > 1 {
		return fmt.Errorf("push accepts at most one <path> argument")
	}
	if len(positionals) == 1 {
		return pushOneFile(ctx, projectID, realSiteID, *dir, positionals[0], *force)
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	syncer, err := NewSyncer(client, projectID, realSiteID, *dir)
	if err != nil {
		return err
	}

	if *force {
		if err := syncer.Push(ctx); err != nil {
			return err
		}
	} else if err := syncer.PushSafe(ctx, *allowDelete, *skipConflicts); err != nil {
		return err
	}

	fmt.Printf("Pushed %s -> %s/%s\n", syncer.dir, projectID, realSiteID)
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

	projectID, realSiteID, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}

	if len(positionals) > 1 {
		return fmt.Errorf("diff accepts at most one <path> argument")
	}
	if len(positionals) == 1 {
		return diffOneFile(ctx, projectID, realSiteID, *dir, positionals[0])
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	syncer, err := NewSyncer(client, projectID, realSiteID, *dir)
	if err != nil {
		return err
	}

	planCtx, err := syncer.buildPlanContext(ctx, *allowDelete)
	if err != nil {
		return err
	}
	plan := planCtx.plan
	if *jsonOut {
		return printPlanJSON(plan, conflictJSONDetails(*dir, planCtx, syncer.currentRemoteFileSnapshot()))
	}
	printSyncPlan(plan, true)
	if plan.hasConflicts() {
		return fmt.Errorf("diff has conflicts; run creght pull to merge remote changes, then resolve any conflict markers before pushing")
	}
	return nil
}

func runPreview(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("preview", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	err := fs.Parse(args)
	if err != nil {
		return err
	}

	_, realSiteID, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	url, err := previewURL(ctx, client, realSiteID)
	if err != nil {
		return err
	}
	if url == "" {
		return fmt.Errorf("preview URL is unavailable")
	}

	fmt.Printf("Opening preview: %s\n", url)
	return openBrowser(url)
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

	fmt.Printf("Published %s/%s\n", projectID, realSiteID)
	if result.VersionID != 0 {
		if len(result.Targets) > 0 {
			fmt.Printf("%s is live on %s\n", versionLabel(result.VersionNo, result.VersionID), strings.Join(result.Targets, ", "))
		} else {
			fmt.Printf("%s is live\n", versionLabel(result.VersionNo, result.VersionID))
		}
	}
	return nil
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
	if strings.TrimSpace(info.SelfAPIHost) == "" {
		return "", nil
	}

	u, err := url.Parse(info.SelfAPIHost)
	if err != nil {
		return "", err
	}
	u.Host = siteID + ".preview." + u.Host
	u.Path = "/"
	u.RawQuery = ""
	u.Fragment = ""

	return u.String(), nil
}

func openBrowser(rawURL string) error {
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
