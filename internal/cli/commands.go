package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

func init() {
	viper.SetDefault("api_host", defaultAPIHostValue)
	viper.SetDefault("web_host", "")
	_ = viper.BindEnv("api_host", "CREGHT_API_HOST")
	_ = viper.BindEnv("web_host", "CREGHT_WEB_HOST")
}

func Run(ctx context.Context, args []string) error {
	if hasVersionArg(args) {
		fmt.Fprintln(os.Stdout, version)
		return nil
	}

	cmd := newRootCommand(ctx, args)
	cmd.SetArgs(args)
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)
	return cmd.Execute()
}

func hasVersionArg(args []string) bool {
	for _, arg := range args {
		if arg == "-v" || arg == "--version" {
			return true
		}
	}
	return false
}

func newRootCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	var showVersion bool
	root := &cobra.Command{
		Use:           "creght",
		Short:         "Local bridge for Cregh site code",
		SilenceUsage:  true,
		SilenceErrors: true,
		Long: fmt.Sprintf(`Cregh CLI authenticates with Cregh, lists projects and sites,
pulls remote site files into a local directory, pushes local changes back to
Cregh, watches local files for sync, opens previews, and publishes sites.

Current API host: %s`, helpAPIHost()),
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion {
				fmt.Fprintln(cmd.OutOrStdout(), version)
				return nil
			}
			return cmd.Help()
		},
	}
	root.PersistentFlags().BoolVarP(&showVersion, "version", "v", false, "Print the installed CLI version.")

	root.AddCommand(loginCommand(ctx, rawArgs))
	root.AddCommand(legacyCommand(ctx, rawArgs, []string{"logout"}, "logout", "Remove the saved CLI token and API host configuration.", func(ctx context.Context, args []string) error {
		return runLogout(args)
	}, nil))
	root.AddCommand(projectCommand(ctx, rawArgs))
	root.AddCommand(siteFileCommand(ctx, rawArgs, "pull", "Download the current remote site files into a local directory.", runPull))
	root.AddCommand(siteFileCommand(ctx, rawArgs, "push", "Push the current local directory snapshot to the remote site.", runPush))
	root.AddCommand(siteFileCommand(ctx, rawArgs, "sync", "Watch local files and sync changes back to Cregh.", runSync))
	root.AddCommand(devCommand(ctx, rawArgs))
	root.AddCommand(siteCommand(ctx, rawArgs, "preview", "Open the remote preview URL for a site in the browser.", runPreview))
	root.AddCommand(publishCommand(ctx, rawArgs))
	root.AddCommand(cmsCommand(ctx, rawArgs))
	root.AddCommand(contentCommand(ctx, rawArgs))
	root.AddCommand(formCommand(ctx, rawArgs))
	root.AddCommand(uploadCommand(ctx, rawArgs))
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the installed CLI version.",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	})

	return root
}

func helpAPIHost() string {
	cfg, err := loadConfig()
	if err == nil && strings.TrimSpace(cfg.APIHost) != "" {
		return strings.TrimSpace(cfg.APIHost)
	}

	return defaultAPIHost()
}

func legacyCommand(ctx context.Context, rawArgs []string, path []string, use string, short string, run func(context.Context, []string) error, flags func(*pflag.FlagSet)) *cobra.Command {
	return legacyCommandPass(ctx, rawArgs, path, use, short, run, flags)
}

func legacyCommandPass(ctx context.Context, rawArgs []string, passAfterPath []string, use string, short string, run func(context.Context, []string) error, flags func(*pflag.FlagSet)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(ctx, originalArgsAfter(rawArgs, passAfterPath))
		},
	}
	if flags != nil {
		flags(cmd.Flags())
	}
	return cmd
}

func originalArgsAfter(rawArgs []string, path []string) []string {
	if len(rawArgs) < len(path) {
		return nil
	}
	for i, part := range path {
		if rawArgs[i] != part {
			return rawArgs
		}
	}
	return rawArgs[len(path):]
}

func loginCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	return legacyCommand(ctx, rawArgs, []string{"login"}, "login", "Authenticate this machine with Cregh and save a CLI token.", runLogin, func(flags *pflag.FlagSet) {
		flags.String("web", "", "Cregh web host. Defaults to CREGHT_WEB_HOST, localhost:5173 for local APIs, or https://creght.cn.")
	})
}

func projectCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "List and manage projects.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProject(ctx, originalArgsAfter(rawArgs, []string{"project"}))
		},
	}
	list := legacyCommand(ctx, rawArgs, []string{"project", "list"}, "list", "List available projects and sites.", runProjectList, nil)
	create := legacyCommand(ctx, rawArgs, []string{"project", "create"}, "create", "Create a project.", runProjectCreate, addProjectCreateFlags)
	cmd.AddCommand(list)
	cmd.AddCommand(create)
	return cmd
}

func siteCommand(ctx context.Context, rawArgs []string, name string, short string, run func(context.Context, []string) error) *cobra.Command {
	return legacyCommand(ctx, rawArgs, []string{name}, name, short, run, func(flags *pflag.FlagSet) {
		addSiteIDFlag(flags)
	})
}

func siteFileCommand(ctx context.Context, rawArgs []string, name string, short string, run func(context.Context, []string) error) *cobra.Command {
	return legacyCommand(ctx, rawArgs, []string{name}, name, short, run, func(flags *pflag.FlagSet) {
		addSiteIDFlag(flags)
		flags.String("dir", ".", "Local Cregh project directory.")
	})
}

func devCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	return legacyCommand(ctx, rawArgs, []string{"dev"}, "dev", "Bidirectionally sync local files with the Web editor and run Vite preview.", runDev, func(flags *pflag.FlagSet) {
		flags.String("web", "", "Cregh web host.")
		addSiteIDFlag(flags)
		flags.String("dir", ".", "Local Cregh project directory.")
		flags.Bool("no-preview", false, "Do not start a local Vite preview.")
		flags.String("preview-host", "localhost", "Local Vite preview host.")
		flags.Int("preview-port", 5173, "Preferred local Vite preview port.")
	})
}

func publishCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	return legacyCommand(ctx, rawArgs, []string{"publish"}, "publish", "Publish a site to make the current remote site version live.", runPublish, func(flags *pflag.FlagSet) {
		addSiteIDFlag(flags)
		flags.String("note", "", "Optional publish note.")
	})
}

func uploadCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	return legacyCommand(ctx, rawArgs, []string{"upload"}, "upload", "Upload a local file as a Cregh site asset and print its URL.", runUpload, func(flags *pflag.FlagSet) {
		addSiteIDFlag(flags)
		flags.String("file", "", "Local file path to upload.")
		flags.String("name", "", "Uploaded file name.")
		flags.String("mimetype", "", "File MIME type.")
		flags.String("cache-control", "", "Cache-Control metadata for uploaded object.")
		flags.Bool("json", false, "Print upload metadata as JSON.")
	})
}

func cmsCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cms",
		Short: "Manage CMS collections.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCMS(ctx, originalArgsAfter(rawArgs, []string{"cms"}))
		},
	}
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"cms"}, "collections", "List CMS collections.", runCMS, addListFlags))
	collection := &cobra.Command{
		Use:   "collection",
		Short: "Manage a CMS collection.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCMS(ctx, originalArgsAfter(rawArgs, []string{"cms"}))
		},
	}
	collection.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"cms"}, "get", "Get a CMS collection.", runCMS, addGetFlags))
	collection.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"cms"}, "create", "Create a CMS collection.", runCMS, addSchemaCreateFlags))
	collection.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"cms"}, "update", "Update a CMS collection.", runCMS, addSchemaUpdateFlags))
	collection.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"cms"}, "delete", "Delete a CMS collection.", runCMS, addGetFlags))
	cmd.AddCommand(collection)
	return cmd
}

func contentCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "content",
		Short: "Manage CMS content entries.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContent(ctx, originalArgsAfter(rawArgs, []string{"content"}))
		},
	}
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"content"}, "list", "List content entries.", runContent, addContentListFlags))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"content"}, "get", "Get a content entry.", runContent, addContentGetFlags))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"content"}, "create", "Create a content entry.", runContent, addContentCreateFlags))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"content"}, "update", "Update a content entry.", runContent, addContentUpdateFlags))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"content"}, "delete", "Delete a content entry.", runContent, addContentDeleteFlags))
	return cmd
}

func formCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "form",
		Short: "Manage forms and form submissions.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runForm(ctx, originalArgsAfter(rawArgs, []string{"form"}))
		},
	}
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"form"}, "list", "List forms.", runForm, addListFlags))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"form"}, "get", "Get a form.", runForm, addGetFlags))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"form"}, "create", "Create a form.", runForm, addFormCreateFlags))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"form"}, "update", "Update a form.", runForm, addFormUpdateFlags))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"form"}, "delete", "Delete a form.", runForm, addGetFlags))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"form"}, "logs", "List form submission logs.", runForm, addFormLogsFlags))
	logCmd := &cobra.Command{
		Use:   "log",
		Short: "Manage a form submission log.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runForm(ctx, originalArgsAfter(rawArgs, []string{"form"}))
		},
	}
	logCmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"form"}, "get", "Get a form submission log.", runForm, addFormLogFlags))
	logCmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"form"}, "delete", "Delete a form submission log.", runForm, addFormLogFlags))
	cmd.AddCommand(logCmd)
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"form"}, "submit", "Submit a form payload.", runForm, addFormSubmitFlags))
	return cmd
}

func addSiteIDFlag(flags *pflag.FlagSet) {
	flags.String("site_id", "", "Site reference in <project_id>/<site_id> format.")
}

func addProjectCreateFlags(flags *pflag.FlagSet) {
	flags.String("name", "", "Project name.")
	flags.String("from_id", "", "Existing project id to copy.")
	flags.Int64("tpl_id", 0, "Template id to use.")
}

func addListFlags(flags *pflag.FlagSet) {
	addSiteIDFlag(flags)
	flags.Int("limit", 100, "Result limit.")
	flags.Int("offset", 0, "Result offset.")
}

func addGetFlags(flags *pflag.FlagSet) {
	addSiteIDFlag(flags)
	flags.String("id", "", "Resource id.")
	flags.String("key", "", "Resource key.")
}

func addSchemaCreateFlags(flags *pflag.FlagSet) {
	addSiteIDFlag(flags)
	flags.String("key", "", "Resource key.")
	flags.String("name", "", "Resource name.")
	flags.String("desc", "", "Resource description.")
	flags.String("schema", "", "JSON schema or resource JSON file.")
}

func addSchemaUpdateFlags(flags *pflag.FlagSet) {
	addSchemaCreateFlags(flags)
	flags.String("id", "", "Resource id.")
	flags.String("new-key", "", "New resource key.")
}

func addContentBaseFlags(flags *pflag.FlagSet) {
	addSiteIDFlag(flags)
	flags.String("collection", "", "Collection key or id.")
}

func addContentListFlags(flags *pflag.FlagSet) {
	addContentBaseFlags(flags)
	flags.Int("limit", 20, "Result limit.")
	flags.Int("offset", 0, "Result offset.")
	flags.String("search_key", "", "Search key.")
	flags.String("order_by", "", "Order by.")
	flags.String("filter", "", "JSON request body or filter file.")
}

func addContentGetFlags(flags *pflag.FlagSet) {
	addContentBaseFlags(flags)
	flags.String("id", "", "Content id.")
	flags.String("slug", "", "Content slug.")
}

func addContentCreateFlags(flags *pflag.FlagSet) {
	addContentBaseFlags(flags)
	flags.String("data", "", "Content JSON file.")
	flags.String("slug", "", "Content slug.")
	flags.Int("sort", 0, "Content sort.")
}

func addContentUpdateFlags(flags *pflag.FlagSet) {
	addContentCreateFlags(flags)
	flags.String("id", "", "Content id.")
	flags.Bool("publish", true, "Publish content update.")
}

func addContentDeleteFlags(flags *pflag.FlagSet) {
	addContentBaseFlags(flags)
	flags.String("id", "", "Content id.")
}

func addFormCreateFlags(flags *pflag.FlagSet) {
	addSchemaCreateFlags(flags)
	flags.String("setting", "", "Form setting JSON file.")
}

func addFormUpdateFlags(flags *pflag.FlagSet) {
	addSchemaUpdateFlags(flags)
	flags.String("setting", "", "Form setting JSON file.")
}

func addFormLogsFlags(flags *pflag.FlagSet) {
	addGetFlags(flags)
	flags.Int("limit", 20, "Result limit.")
	flags.Int("offset", 0, "Result offset.")
}

func addFormLogFlags(flags *pflag.FlagSet) {
	addGetFlags(flags)
	flags.String("log_id", "", "Form log id.")
}

func addFormSubmitFlags(flags *pflag.FlagSet) {
	addSiteIDFlag(flags)
	flags.String("key", "", "Form key.")
	flags.String("data", "", "Form payload JSON file.")
	flags.String("from_url", "", "Form source URL.")
	flags.String("uid", "", "Submitter uid.")
	flags.String("ua", "", "Submitter user agent.")
	flags.String("ip", "", "Submitter IP.")
}
