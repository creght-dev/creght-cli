package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"flag"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func runTpl(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printTplUsage()
		return nil
	}

	switch args[0] {
	case "list":
		return runTplList(ctx, args[1:])
	case "get":
		return runTplGet(ctx, args[1:])
	case "categories":
		return runTplCategories(ctx, args[1:])
	case "use":
		return runTplUse(ctx, args[1:])
	case "help", "-h", "--help":
		printTplUsage()
		return nil
	default:
		return fmt.Errorf("unknown tpl command: %s", args[0])
	}
}

func printTplUsage() {
	fmt.Println(`creght tpl

Usage:
  creght tpl list [--category_id=<id>] [--recommend] [--limit=50] [--offset=0] [--json]
  creght tpl get <id> [--json]
  creght tpl categories [--json]
  creght tpl use <id> --name=<project_name> [--json]

Notes:
  list/get show each template's description, categories, and preview URL — open
  the preview URL to see the template rendered. use creates a new project from
  the template and prints its sites ready for creght pull.`)
}

// tplLocaleText picks one display string out of a locales map, preferring the
// platform's default locale order, then any remaining locale deterministically.
func tplLocaleText(locales map[string]string) string {
	for _, k := range []string{"zh-CN", "zh-HK", "en"} {
		if v := strings.TrimSpace(locales[k]); v != "" {
			return v
		}
	}

	keys := make([]string, 0, len(locales))
	for k := range locales {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := strings.TrimSpace(locales[k]); v != "" {
			return v
		}
	}
	return ""
}

func tplCategoryNames(categories []creght.TplCategory) []string {
	var names []string
	for _, c := range categories {
		if name := tplLocaleText(c.NameLocales); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func tplPreviewURL(t creght.ProjectTpl) string {
	if t.PreviewURL != "" {
		return t.PreviewURL
	}
	return t.Tpl.PreviewURL
}

func runTplList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tpl list", flag.ContinueOnError)
	categoryID := fs.Int64("category_id", 0, "filter by template category id")
	recommend := fs.Bool("recommend", false, "list the platform's recommended templates")
	limit := fs.Int("limit", 50, "result limit")
	offset := fs.Int("offset", 0, "result offset")
	jsonOut := fs.Bool("json", false, "print the raw template list as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	var res creght.ProjectTplListResponse
	if *recommend {
		res, err = client.GetTplProjectRecommendList(ctx)
	} else {
		query := paginationQuery(*limit, *offset)
		if *categoryID != 0 {
			query.Set("category_id", strconv.FormatInt(*categoryID, 10))
		}
		res, err = client.GetTplProjectList(ctx, query)
	}
	if err != nil {
		return err
	}

	if *jsonOut {
		return printJSON(res)
	}

	if len(res.List) == 0 {
		fmt.Println("No templates found.")
		return nil
	}

	for _, t := range res.List {
		printTplSummary(t)
		fmt.Println()
	}
	fmt.Printf("total: %d\n", res.Total)
	fmt.Println("Details: creght tpl get <id>. Start a project: creght tpl use <id> --name=<project_name>.")
	return nil
}

func printTplSummary(t creght.ProjectTpl) {
	fmt.Printf("#%s\t%s\n", t.Tpl.ID, tplLocaleText(t.Tpl.NameLocales))
	if desc := tplLocaleText(t.Tpl.DescLocales); desc != "" {
		fmt.Printf("  %s\n", desc)
	}
	if names := tplCategoryNames(t.Tpl.Categories); len(names) > 0 {
		fmt.Printf("  categories: %s\n", strings.Join(names, ", "))
	}
	if preview := tplPreviewURL(t); preview != "" {
		fmt.Printf("  preview: %s\n", preview)
	}
	if t.Tpl.UseCount > 0 {
		fmt.Printf("  used: %d\n", t.Tpl.UseCount)
	}
}

func runTplGet(ctx context.Context, args []string) error {
	positionals, flagArgs := splitFlagArgs(args)
	fs := flag.NewFlagSet("tpl get", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print the raw template detail as JSON")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("tpl get requires exactly one <id> argument")
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	t, err := client.GetTplProjectDetail(ctx, positionals[0])
	if err != nil {
		return err
	}
	if t.Tpl.ID == "" {
		return fmt.Errorf("template %s not found", positionals[0])
	}

	if *jsonOut {
		return printJSON(t)
	}

	printTplSummary(t)
	if len(t.SiteList) > 0 {
		fmt.Println("sites:")
		for _, s := range t.SiteList {
			line := fmt.Sprintf("  %s\t%s", s.ID, s.Name)
			if s.FreeDomain != "" {
				line += "\t" + s.FreeDomain
			}
			fmt.Println(line)
		}
	}
	if len(t.CmsList) > 0 {
		fmt.Println("cms collections:")
		for _, c := range t.CmsList {
			fmt.Printf("  %s\t%s\n", c.Key, c.Name)
		}
	}
	fmt.Printf("\nStart a project from it: creght tpl use %s --name=<project_name>\n", t.Tpl.ID)
	return nil
}

func runTplCategories(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tpl categories", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print the raw category list as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	categories, err := client.GetTplCategoryList(ctx)
	if err != nil {
		return err
	}

	if *jsonOut {
		return printJSON(map[string]any{"list": categories})
	}

	if len(categories) == 0 {
		fmt.Println("No categories found.")
		return nil
	}

	// Two levels: print top-level categories with their children indented.
	children := map[string][]creght.TplCategory{}
	var roots []creght.TplCategory
	for _, c := range categories {
		if c.Pid == "" || c.Pid == "0" {
			roots = append(roots, c)
		} else {
			children[string(c.Pid)] = append(children[string(c.Pid)], c)
		}
	}
	for _, c := range roots {
		fmt.Printf("#%s\t%s\n", c.ID, tplLocaleText(c.NameLocales))
		for _, child := range children[string(c.ID)] {
			fmt.Printf("  #%s\t%s\n", child.ID, tplLocaleText(child.NameLocales))
		}
	}
	fmt.Println("\nFilter templates: creght tpl list --category_id=<id>")
	return nil
}

func runTplUse(ctx context.Context, args []string) error {
	positionals, flagArgs := splitFlagArgs(args)
	fs := flag.NewFlagSet("tpl use", flag.ContinueOnError)
	name := fs.String("name", "", "name for the new project")
	jsonOut := fs.Bool("json", false, "print the created project and sites as JSON")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("tpl use requires exactly one <id> argument")
	}
	tplID, err := strconv.ParseInt(positionals[0], 10, 64)
	if err != nil {
		return fmt.Errorf("template id must be a number, got %q", positionals[0])
	}

	projectName := strings.TrimSpace(*name)
	if projectName == "" {
		return fmt.Errorf("tpl use requires --name=<project_name>")
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	id, err := client.CreateProject(ctx, creght.CreateProjectRequest{
		Name:  projectName,
		TplID: tplID,
	})
	if err != nil {
		return err
	}

	sites := lookupProjectSites(ctx, client, id)

	if *jsonOut {
		type siteOut struct {
			SiteID string `json:"site_id"`
			Name   string `json:"name"`
		}
		out := struct {
			ProjectID string    `json:"project_id"`
			Name      string    `json:"name"`
			TplID     int64     `json:"tpl_id"`
			Sites     []siteOut `json:"sites"`
		}{ProjectID: id, Name: projectName, TplID: tplID, Sites: []siteOut{}}
		for _, s := range sites {
			out.Sites = append(out.Sites, siteOut{SiteID: id + "/" + s.ID, Name: s.Name})
		}
		return printJSON(out)
	}

	fmt.Printf("Created project %s\t%s (from template #%d)\n", id, projectName, tplID)
	printProjectSites(id, sites)
	return nil
}

// lookupProjectSites finds the sites of a just-created project. Best-effort:
// a lookup failure returns nil rather than failing the creation output.
func lookupProjectSites(ctx context.Context, client *creght.Client, projectID string) []creght.Site {
	projects, err := client.GetProjectList(ctx)
	if err != nil {
		return nil
	}
	for _, p := range projects.List {
		if p.ID == projectID {
			return p.SiteList
		}
	}
	return nil
}

func printProjectSites(projectID string, sites []creght.Site) {
	if len(sites) == 0 {
		fmt.Println("Run creght project list to see its sites.")
		return
	}
	fmt.Println("sites:")
	for _, s := range sites {
		fmt.Printf("  %s/%s\t%s\n", projectID, s.ID, s.Name)
	}
	fmt.Printf("\nNext: creght pull --site_id=%s/%s --dir=<local_dir>\n", projectID, sites[0].ID)
}
