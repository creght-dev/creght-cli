package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/creght-dev/creght-cli/internal/creght"
)

// creght export / creght import move a whole project to another Creght
// deployment (e.g. a self-hosted one) as a package. Both need maintainer
// access; on a self-hosted deployment the admin token has it.

func runExport(ctx context.Context, args []string) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		printExportUsage()
		return nil
	}
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	out := fs.String("out", "", "output file (default <project_id>-<time>.creght.zip)")
	with := fs.String("with", "", "optional data to include: members,form_logs")
	siteIDs := fs.String("site_ids", "", "only export these sites (comma separated)")
	projectFlag := fs.String("project_id", "", "project id")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	projectID := strings.TrimSpace(*projectFlag)
	if projectID == "" && fs.NArg() > 0 {
		projectID = fs.Arg(0)
	}
	if projectID == "" {
		printExportUsage()
		return fmt.Errorf("project id is required")
	}
	file := strings.TrimSpace(*out)
	if file == "" {
		file = fmt.Sprintf("%s-%s.creght.zip", projectID, time.Now().Format("20060102-150405"))
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	tmp := file + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	prog := newTransferProgress("downloading", 0)
	n, err := client.ExportProjectPackage(ctx, projectID, splitList(*siteIDs), splitList(*with), progressWriter{w: f, p: prog})
	prog.done()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		return err
	}
	fmt.Printf("exported %s (%.1f MB)\n", file, float64(n)/1024/1024)
	return nil
}

func runImport(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printImportUsage()
		return nil
	}
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	name := fs.String("name", "", "name of the new project (default: the exported project's name)")
	skipMembers := fs.Bool("skip-members", false, "do not import site members even if the package has them")
	bindDomains := fs.Bool("bind-domains", false, "bind the package's custom domains to the new sites (self-hosted creght)")
	jsonOut := fs.Bool("json", false, "print the import result as JSON")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		printImportUsage()
		return fmt.Errorf("exactly one package file is required")
	}
	path := fs.Arg(0)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	client, cfg, err := clientFromConfig()
	if err != nil {
		return err
	}
	var size int64
	if info, err := f.Stat(); err == nil {
		size = info.Size()
	}
	// The upload finishes before the server is done importing; the line then
	// stays at 100% until the server answers.
	prog := newTransferProgress("uploading", size)
	raw, err := client.ImportProjectPackage(ctx, filepath.Base(path), progressReader{r: f, p: prog}, creght.ImportPackageOptions{
		NewProjectName: *name, SkipMembers: *skipMembers, BindDomains: *bindDomains,
	})
	prog.done()
	if err != nil {
		return err
	}
	if *jsonOut {
		fmt.Println(string(raw))
		return nil
	}
	var res struct {
		ProjectID string `json:"project_id"`
		Sites     []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"sites"`
		MissingSecrets           []string `json:"missing_secrets"`
		BoundDomains             []string `json:"bound_domains"`
		Domains                  []string `json:"domains"`
		SkippedDomains           []string `json:"skipped_domains"`
		Members                  int      `json:"members"`
		MembersNeedResetPassword bool     `json:"members_need_reset_password"`
		Assets                   int      `json:"assets"`
		MissingAssets            []string `json:"missing_assets"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		fmt.Println(string(raw))
		return nil
	}
	fmt.Printf("imported into %s as project %s\n", cfg.APIHost, res.ProjectID)
	for _, s := range res.Sites {
		fmt.Printf("  site %s/%s  %s\n", res.ProjectID, s.ID, s.Name)
	}
	fmt.Printf("  assets: %d", res.Assets)
	if len(res.MissingAssets) > 0 {
		fmt.Printf(" (%d not in the package, still pointing at the old URL)", len(res.MissingAssets))
	}
	fmt.Println()
	if len(res.BoundDomains) > 0 {
		fmt.Printf("  bound domains: %s\n", strings.Join(res.BoundDomains, ", "))
	} else if len(res.Domains) > 0 {
		fmt.Printf("  domains in the package (not bound): %s\n", strings.Join(res.Domains, ", "))
	}
	if len(res.SkippedDomains) > 0 {
		fmt.Printf("  domains already used here, skipped: %s\n", strings.Join(res.SkippedDomains, ", "))
	}
	if res.MembersNeedResetPassword {
		fmt.Println("  members: not in the package — site members need to register again or reset their password")
	} else {
		fmt.Printf("  members: %d\n", res.Members)
	}
	if len(res.MissingSecrets) > 0 {
		fmt.Printf("  fill these secrets before using the related features: %s\n", strings.Join(res.MissingSecrets, ", "))
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// reorderFlags lets the flag package accept positional arguments before flags,
// e.g. `creght import site.zip --bind-domains`.
func reorderFlags(args []string) []string {
	var flags, pos []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else if len(flags) > 0 && !strings.Contains(flags[len(flags)-1], "=") && needsValue(flags[len(flags)-1]) {
			flags = append(flags, a)
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

func needsValue(flagArg string) bool {
	switch strings.TrimLeft(flagArg, "-") {
	case "out", "with", "site_ids", "project_id", "name":
		return true
	}
	return false
}

func printExportUsage() {
	fmt.Println(`Usage: creght export <project_id> [--out file.zip] [--with members,form_logs] [--site_ids a,b]

Download a project as a package (code, CMS, forms, tables, members, assets) that
"creght import" can load into another Creght deployment, e.g. a self-hosted one.
Secrets (integration keys, OAuth client secrets, secret env values) are never exported.
Requires maintainer access.`)
}

func printImportUsage() {
	fmt.Println(`Usage: creght import <file.zip> [--name "New name"] [--bind-domains] [--skip-members] [--json]

Create a new project from a package made by "creght export". Point the CLI at the
target deployment first, e.g.

  CREGHT_API_HOST=https://admin.example.com CREGHT_TOKEN=<admin token> creght import site.zip --bind-domains

--bind-domains binds the package's custom domains to the new sites (self-hosted creght).`)
}
