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
	webHost := fs.String("web", "", "Cregh web host")
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

	fmt.Printf("Open this URL to authorize Cregh CLI:\n%s\n", session.VerifyURL)
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
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	dir := fs.String("dir", ".", "local directory")
	err := fs.Parse(args)
	if err != nil {
		return err
	}

	projectID, realSiteID, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}

	client, cfg, err := clientFromConfig()
	if err != nil {
		return err
	}

	files, err := client.GetFileList(ctx, projectID, realSiteID)
	if err != nil {
		return err
	}

	err = writeRemoteFiles(*dir, files.List)
	if err != nil {
		return err
	}

	editorURL := siteEditorURL(defaultWebHost(cfg.APIHost), projectID, realSiteID)
	createdAgents, err := ensurePulledAgentsFile(*dir, files.List, projectID, realSiteID, editorURL)
	if err != nil {
		return err
	}

	previewURL, _ := previewURL(ctx, client, realSiteID)
	fmt.Printf("Pulled %d files into %s\n", len(files.List), *dir)
	if createdAgents {
		fmt.Printf("Generated AGENTS.md for Cregh agent context\n")
	}
	fmt.Printf("Editor: %s\n", editorURL)
	if previewURL != "" {
		fmt.Printf("Preview: %s\n", previewURL)
	}

	return nil
}

func siteEditorURL(webHost string, projectID string, siteID string) string {
	return fmt.Sprintf("%s/editor/project/%s/site/%s", strings.TrimRight(webHost, "/"), url.PathEscape(projectID), url.PathEscape(siteID))
}

func runSync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	dir := fs.String("dir", ".", "local directory")
	err := fs.Parse(args)
	if err != nil {
		return err
	}

	projectID, realSiteID, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	syncer, err := NewSyncer(client, projectID, realSiteID, *dir)
	if err != nil {
		return err
	}

	previewURL, _ := previewURL(ctx, client, realSiteID)
	if previewURL != "" {
		fmt.Printf("Preview: %s\n", previewURL)
	}

	return syncer.Run(ctx)
}

func runPush(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	dir := fs.String("dir", ".", "local directory")
	err := fs.Parse(args)
	if err != nil {
		return err
	}

	projectID, realSiteID, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	syncer, err := NewSyncer(client, projectID, realSiteID, *dir)
	if err != nil {
		return err
	}

	if err := syncer.Push(ctx); err != nil {
		return err
	}

	fmt.Printf("Pushed %s -> %s/%s\n", syncer.dir, projectID, realSiteID)
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
