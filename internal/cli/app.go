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

			fmt.Println("Logged in.")
			return nil
		}
		if result.Status == "expired" {
			return fmt.Errorf("authorization expired")
		}
	}

	return fmt.Errorf("authorization timed out")
}

func runLogout(args []string) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	err := fs.Parse(args)
	if err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("logout does not accept positional arguments")
	}

	if err := deleteConfig(); err != nil {
		return err
	}

	fmt.Println("Logged out.")
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

	remoteSnap := remoteFileSnapshot(files.List)
	var outcome pullOutcome
	if *force {
		state, hasState, err := loadWorkspaceState(*dir)
		if err != nil {
			return err
		}
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
		outcome.changed = len(files.List)
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
	state, hasState, err := loadWorkspaceState(root)
	if err != nil {
		return outcome, err
	}
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

	if err := client.PublishSite(ctx, projectID, realSiteID, *note); err != nil {
		return err
	}

	fmt.Printf("Published %s/%s\n", projectID, realSiteID)
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
