package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
		Short:         "Local bridge for Creght site code",
		SilenceUsage:  true,
		SilenceErrors: true,
		Long: fmt.Sprintf(`Creght CLI authenticates with Creght, lists projects and sites,
pulls remote site files (including Func code) into a local workspace with
three-way merge, pushes local changes back to Creght, resolves conflicts,
opens previews, and publishes sites.

Current API host: %s
Set a different one with the CREGHT_API_HOST environment variable, e.g.
  CREGHT_API_HOST=http://localhost:8433 creght project list

Credentials file: %s
It stores one token per API host; creght logout removes the token for the
current API host only.`, helpAPIHost(), helpConfigPath()),
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
	root.AddCommand(legacyCommand(ctx, rawArgs, []string{"logout"}, "logout", "Remove the saved CLI login for the current API host.", func(ctx context.Context, args []string) error {
		return runLogout(ctx, args)
	}, nil))
	root.AddCommand(projectCommand(ctx, rawArgs))
	root.AddCommand(siteFileCommand(ctx, rawArgs, "pull", "Download site files into a local workspace.", runPull,
		withLong(`Download a Creght site into a local workspace and record a base snapshot in
.creght/state.json for safe 3-way sync.

Local paths mirror remote site paths exactly (e.g. page/x.tsx <-> /page/x.tsx);
Func keys map to backend/func/ (e.g. booking <-> backend/func/booking.ts).

Without --dir, pull discovers .creght/state.json from the current directory or
its parents and reuses its site_id. The first pull still requires --site_id.
Without a path it merges the whole site; with an optional <path> it pulls just
that one file and updates only its base state.

Files changed only remotely are updated; local-only edits are kept. Files
changed on both sides are three-way merged against the base: non-overlapping
edits merge automatically, overlapping edits write git-style conflict markers
into the file (see creght resolve). Local content that pull overwrites is
first backed up under .creght/backup/. --force skips merging and overwrites
local with the remote copy.

Paths matched by a workspace-root .creghtignore are not pulled. The ignore
file supports gitignore-style comments, negation, and *, ?, and ** wildcards,
and is itself never synced.`),
		withExample(`  creght pull --site_id=<pid>/<sid> --dir=./mysite
  creght pull
  creght pull page/Index.tsx
  creght pull page/Index.tsx --site_id=<pid>/<sid> --dir=./mysite --force`)))
	root.AddCommand(siteFileCommand(ctx, rawArgs, "diff", "Show local site file changes before pushing.", runDiff,
		withLong(`Show what push would change, comparing three versions: the base snapshot in
.creght/state.json, current local files, and current remote files.

Without --dir, diff discovers .creght/state.json from the current directory or
its parents and reuses its site_id.

--json prints a machine-readable plan: {"has_conflicts":bool,"files":[{"path",
"status","action"}]} where status is local-change | remote-only | conflict |
no-base. Conflict entries additionally carry "reason", "auto_mergeable"
(whether creght pull would merge them without markers), "base_to_local_diff"
and "base_to_remote_diff", so an agent can resolve without extra round-trips.
With <path> it prints a git-style unified diff (remote vs local, hunks with 3
context lines) of that one file.`),
		withExample(`  creght diff
  creght diff --json
  creght diff page/Index.tsx
  creght diff --site_id=<pid>/<sid> --dir=./mysite`)))
	root.AddCommand(siteFileCommand(ctx, rawArgs, "push", "Safely push local site file changes to Creght.", runPush,
		withLong(`Upload local changes, comparing base/local/remote. Files changed only remotely
are kept; local deletions are skipped unless --delete; files changed both
locally and remotely report a conflict — run creght pull to merge them, or use
--skip-conflicts to push everything else and leave the conflicted files for a
later pull. Files still containing conflict markers are refused (see creght
resolve). --force overwrites remote with the local snapshot, backing up
diverged remote copies under .creght/backup/ first.

With an optional <path> it pushes just that one file and updates only its base
state (use --force to overwrite a remote copy that moved since the last pull).
Paths matched by the workspace-root .creghtignore are not uploaded or deleted
remotely; the ignore file itself is never synced.

Without --dir, push discovers .creght/state.json from the current directory or
its parents and reuses its site_id, so creght push works anywhere inside a
pulled workspace.

push does not publish. Use creght publish to promote changes to the live site.`),
		withExample(`  creght push
  creght push --delete
  creght push --skip-conflicts
  creght push page/Index.tsx
  creght push --site_id=<pid>/<sid> --dir=./mysite`)))
	root.AddCommand(legacyCommand(ctx, rawArgs, []string{"resolve"}, "resolve [path]", "List or resolve conflict markers left by creght pull.", runResolve, func(flags *pflag.FlagSet) {
		flags.String("dir", ".", "Local Creght project directory.")
		flags.Bool("list", false, "List files containing conflict markers.")
		flags.Bool("ours", false, "Keep the local side of every conflict in <path>.")
		flags.Bool("theirs", false, "Keep the remote side of every conflict in <path>.")
	},
		withLong(`When creght pull finds overlapping local and remote edits it writes git-style
conflict markers (<<<<<<< local / ======= / >>>>>>> remote) into the file.
push refuses to upload files that still contain markers.

Without <path> (or with --list) resolve lists the files that contain markers.
With <path> and --ours it keeps the local side of every conflict; --theirs
keeps the remote side. Editing the markers by hand works too. After resolving,
run creght push.`),
		withExample(`  creght resolve --list
  creght resolve page/Index.tsx --ours
  creght resolve page/Index.tsx --theirs`)))
	root.AddCommand(legacyCommand(ctx, rawArgs, []string{"cat"}, "cat <path>", "Print one site file's content to stdout (remote by default, or local).", runCat, func(flags *pflag.FlagSet) {
		addSiteIDFlag(flags)
		flags.String("dir", ".", "Local Creght project directory.")
		flags.String("ref", "remote", "Which version to read: remote | local.")
	},
		withLong(`Print a single site file's content to stdout without pulling the whole
workspace. --ref remote (default) reads the live remote file; --ref local reads
the workspace copy.

Inside a pulled workspace, cat discovers .creght/state.json like pull/push, so
--site_id is optional there. <path> accepts a workspace-root path (/page/x.tsx)
or a relative path (page/x.tsx), resolved from the current directory when run
inside the workspace.`),
		withExample(`  creght cat page/Index.tsx
  creght cat page/Index.tsx --ref=local
  creght cat page/Index.tsx --site_id=<pid>/<sid>`)))
	root.AddCommand(legacyCommand(ctx, rawArgs, []string{"importmap"}, "importmap", "Print a site's effective importMap (platform built-ins + talizen.config).", runImportMap, func(flags *pflag.FlagSet) {
		addSiteIDFlag(flags)
		flags.String("dir", ".", "Local Creght project directory.")
		flags.String("ref", "remote", "Which config to read: remote | local.")
	},
		withLong(`Print the importMap that is actually in effect for a site: the platform
built-in imports (from the render config) overlaid with the project's
talizen.config importMap.imports, exactly how the renderer composes it.
Config imports override built-ins with the same specifier.

--ref remote (default) reads the live remote talizen.config; --ref local reads
the workspace copy. Inside a pulled workspace, importmap discovers
.creght/state.json like pull/push, so --site_id is optional there.

Output is JSON: {"imports":{<specifier>:<url>},"sources":{<specifier>:"builtin"
| "<config path>"}} where sources records where each specifier came from.`),
		withExample(`  creght importmap
  creght importmap --ref=local
  creght importmap --site_id=<pid>/<sid>`)))
	root.AddCommand(siteCommand(ctx, rawArgs, "preview", "Open the remote preview URL for a site in the browser.", runPreview))
	root.AddCommand(publishCommand(ctx, rawArgs))
	root.AddCommand(cmsCommand(ctx, rawArgs))
	root.AddCommand(contentCommand(ctx, rawArgs))
	root.AddCommand(formCommand(ctx, rawArgs))
	root.AddCommand(tableCommand(ctx, rawArgs))
	root.AddCommand(funcCommand(ctx, rawArgs))
	root.AddCommand(uploadCommand(ctx, rawArgs))
	root.AddCommand(gitCommand(ctx, rawArgs))
	root.AddCommand(versionCommand(ctx, rawArgs))

	return root
}

// versionCommand keeps bare `creght version` printing the CLI version, and hangs
// the site-version subcommands off it.
func versionCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the installed CLI version, or manage site versions.",
		Long: strings.TrimSpace(`Without a subcommand, print the installed CLI version.

The subcommands manage site versions: immutable snapshots of a site's source
files, the platform equivalent of a git commit. Create one whenever a piece of
work is done, then publish the one you want the live site to serve.

A snapshot covers source files only. CMS content and the platform state under
/platform/** (CMS/form/table/auth definitions) are live, so publishing an older
version does not roll those back.

Inside a pulled workspace --site_id is optional; these commands discover
.creght/state.json from the current directory or its parents like pull/push do.`),
		Example: strings.TrimSpace(`  creght version
  creght version create --note="Add pricing page"
  creght version list
  creght version publish 12`),
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	}
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"version"}, "create", "Snapshot the remote site source into a new version, without publishing it.", runVersion, addVersionCreateFlags,
		withLong(`Snapshot the site's current remote source files into a new immutable version
and leave the live site untouched. This is the "commit" step: run it whenever a
piece of work is finished, then publish the version when you want it served.

A version records the remote workspace, so local edits that were never pushed
would be missing from it. Inside a pulled workspace create therefore compares
local files against the site first and refuses to run while anything is
unpushed, listing what to push. --allow-dirty skips that check and snapshots
the remote site as it is.

The platform rejects a snapshot identical to the newest version, so repeated
creates never pile up duplicates.`),
		withExample(`  creght version create --note="Add pricing page"
  creght version create --note="Checkpoint" --allow-dirty
  creght version create --site_id=<pid>/<sid> --note="Add pricing page"`)))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"version"}, "list", "List site versions, newest first, and show which one is live.", runVersion, addVersionListFlags,
		withLong(`List the site's recent versions, newest first. A * marks the version the live
site currently serves; the footer names the hostnames serving it and any domain
pinned to a different version.

The VERSION column is the per-site number that creght version publish takes;
ID is the platform-wide version id. When the site's source has changed since the
newest version, the footer says so — run creght version create to capture it.

--json prints the raw publish state, including every version, the live version
id, the pinned domains, and the exact files that changed since the newest
version.`),
		withExample(`  creght version list
  creght version list --limit=10
  creght version list --json`)))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"version"}, "publish <version_no>", "Make an existing site version the live one.", runVersion, addVersionPublishFlags,
		withLong(`Point the live site at an existing version, forward to a newer one or back to
an older one. Every domain follows it except domains pinned to a specific
version.

<version_no> is the per-site number from the VERSION column of creght version
list. Pass id:<version_id> instead to select a version by id, which is how to
reach versions older than the window list returns.

Publishing the version already being served is a no-op that just refreshes its
caches.

This changes only which snapshot the live site serves. It restores nothing: the
editable site workspace and your local files are untouched, and creght pull
still fetches the current workspace rather than the published version's files.
So after rolling production back, the workspace still holds the newer source and
the next publish would ship it again — to revert the code itself, edit locally
and push. CMS content and /platform/** definitions are live and never move with
a version either.

To snapshot the current source and publish it in one step, use creght publish.`),
		withExample(`  creght version publish 12
  creght version publish 12 --note="Roll back nav change"
  creght version publish id:456`)))
	return cmd
}

func helpAPIHost() string {
	cfg, err := loadConfig()
	if err == nil && strings.TrimSpace(cfg.APIHost) != "" {
		return strings.TrimSpace(cfg.APIHost)
	}

	return defaultAPIHost()
}

// helpConfigPath reports where the CLI keeps its saved login tokens, so
// `creght -h` can point at the real file instead of a per-OS description.
func helpConfigPath() string {
	path, err := configPath()
	if err != nil {
		return filepath.Join("<user config dir>", "creght", "config.json")
	}

	return path
}

// cmdOpt customizes a command built by legacyCommand/siteFileCommand, so
// `creght <cmd> --help` can carry the authoritative usage docs.
type cmdOpt func(*cobra.Command)

// withLong sets the long description shown by `creght <cmd> --help`.
func withLong(long string) cmdOpt { return func(c *cobra.Command) { c.Long = strings.TrimSpace(long) } }

// withExample sets the example block shown by `creght <cmd> --help`.
func withExample(example string) cmdOpt {
	return func(c *cobra.Command) { c.Example = strings.TrimSpace(example) }
}

func legacyCommand(ctx context.Context, rawArgs []string, path []string, use string, short string, run func(context.Context, []string) error, flags func(*pflag.FlagSet), opts ...cmdOpt) *cobra.Command {
	return legacyCommandPass(ctx, rawArgs, path, use, short, run, flags, opts...)
}

func legacyCommandPass(ctx context.Context, rawArgs []string, passAfterPath []string, use string, short string, run func(context.Context, []string) error, flags func(*pflag.FlagSet), opts ...cmdOpt) *cobra.Command {
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
	for _, opt := range opts {
		opt(cmd)
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
	return legacyCommand(ctx, rawArgs, []string{"login"}, "login", "Authenticate this machine with Creght and save a CLI token for the current API host.", runLogin, func(flags *pflag.FlagSet) {
		flags.String("web", "", "Creght web host. Defaults to CREGHT_WEB_HOST, localhost:5173 for local APIs, or https://creght.cn.")
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

func siteFileCommand(ctx context.Context, rawArgs []string, name string, short string, run func(context.Context, []string) error, opts ...cmdOpt) *cobra.Command {
	return legacyCommand(ctx, rawArgs, []string{name}, name, short, run, func(flags *pflag.FlagSet) {
		flags.String("site_id", "", "Site reference in <project_id>/<site_id> format. Optional inside a pulled workspace.")
		flags.String("dir", ".", "Local Creght project directory.")
		if name == "pull" {
			flags.Bool("force", false, "Overwrite local files with the remote workspace.")
		}
		if name == "push" {
			flags.Bool("delete", false, "Delete remote files/functions that were removed locally.")
			flags.Bool("force", false, "Overwrite remote with the local workspace snapshot.")
			flags.Bool("skip-conflicts", false, "Push non-conflicting files and keep conflicted ones for a later pull.")
		}
		if name == "diff" {
			flags.Bool("delete", false, "Show remote deletions for files/functions removed locally.")
			flags.Bool("json", false, "Output the change plan as machine-readable JSON.")
		}
	}, opts...)
}

func publishCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	return legacyCommand(ctx, rawArgs, []string{"publish"}, "publish", "Publish a site to make the current remote site version live.", runPublish, func(flags *pflag.FlagSet) {
		addSiteIDFlag(flags)
		flags.String("note", "", "Optional publish note.")
	})
}

func uploadCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	return legacyCommand(ctx, rawArgs, []string{"upload"}, "upload", "Upload a local file as a Creght site asset and print its URL.", runUpload, func(flags *pflag.FlagSet) {
		addSiteIDFlag(flags)
		flags.String("file", "", "Local file path to upload.")
		flags.String("name", "", "Uploaded file name.")
		flags.String("mimetype", "", "File MIME type.")
		flags.String("cache-control", "", "Cache-Control metadata for uploaded object.")
		flags.Bool("json", false, "Print upload metadata as JSON.")
	},
		withLong(`Upload a build-time local file (downloaded stock image, generated favicon,
mockup, texture, exported illustration) as a Creght-hosted asset. With --json
the command prints {"file_url":"<cdn-url>"}.

For assets created inside a Func at runtime (e.g. AI-generated images), use
ctx.assets.upload({filename, mimeType, base64}) in the Func instead; persist the
returned CDN URL/metadata, never base64 payloads.`),
		withExample(`  creght upload --site_id=<pid>/<sid> --file=./image.png
  creght upload --site_id=<pid>/<sid> --file=./image.png --json`))
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

// gitCommand exposes the site's version history through git. The site is
// reachable as a read-only git remote, so `git log`, `git show <ver>:<path>`,
// `git diff` and `git blame` work without the CLI reimplementing any of them.
func gitCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Use a site's version history as a read-only git remote.",
		Long: strings.TrimSpace(`A site's versions are also served as a git repository, one commit per version:

  refs/heads/main        newest version
  refs/tags/v<no>        each version, so ` + "`git show v195:page/Price.tsx`" + ` works
  refs/heads/published   the version production currently serves

That makes ` + "`git diff published..main`" + ` answer "what would go live if I published
now" at source level, which is more precise than comparing rendered output.

The remote is read-only: push still goes through ` + "`creght push`" + `. Accepting a git
push has to settle what happens to unversioned editor work first.

Credentials come from ` + "`creght login`" + `, not a git password. ` + "`creght git clone`" + `
leaves them in the new repository, so git never asks.`),
		Example: strings.TrimSpace(`  creght git clone --site_id=<project_id>/<site_id>
  creght git url
  git show v195:page/Price.tsx
  git diff published..main`),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"git"}, "clone", "Clone a site over git, leaving credentials in the new repo only.", runGit, func(flags *pflag.FlagSet) {
		flags.String("site_id", "", "site id, or project_id/site_id")
		flags.String("dir", "", "workspace directory")
	}, withLong(strings.TrimSpace(`Clone a site's version history over git.

Credentials are written into the new repository's own config, so later fetch and
pull keep working without touching the global git config or the OS keychain
(osxkeychain on macOS, Git Credential Manager on Windows).

That is why this command exists rather than a one-off machine-wide setup step:
`+"`git clone -c`"+` persists into the repository it creates, so cloning through
creght is enough and nothing outside the new repo is modified.`))))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"git"}, "url", "Print the git clone URL for a site.", runGit, func(flags *pflag.FlagSet) {
		flags.String("site_id", "", "site id, or project_id/site_id")
		flags.String("dir", "", "workspace directory")
	}))
	// Invoked by git itself, not by hand.
	credential := legacyCommandPass(ctx, rawArgs, []string{"git"}, "credential", "git credential helper (invoked by git, not by hand).", runGit, func(flags *pflag.FlagSet) {})
	credential.Hidden = true
	cmd.AddCommand(credential)
	return cmd
}

func contentCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "content",
		Short: "Manage CMS content entries.",
		Long: `Manage CMS content entries.

For create/update, --data points at a full content object whose business fields
are wrapped under a "body" key (top-level slug/sort are optional; matching flags
win when passed). Bare body files are rejected with a format error. Example
content.json:

  {"slug":"post-1","sort":1,"body":{"title":"Hi","tags":["skill"]}}

update applies a partial update, so --data is optional for it: pass --slug/--sort
alone to rename or reorder without re-submitting the body. create still needs
--data.

--sort works on both create and update; bigger sort shows first in the CMS list.
Omitting it lets create append the entry last and leaves update's current value
untouched. On create, --sort=0 also means "append last" — the platform reads a
zero sort as auto-assign, so a literal 0 can only be stored via update --sort=0.
Use content update --sort to reorder; deleting and recreating an entry changes
its id and cannot be undone.

Detail commands print JSON to stdout; use --out=<file> to save. content update
exits non-zero with "not updated: <reason>" when no field actually changed —
treat that as a signal to check the payload, not as success.`,
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

func tableCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "table",
		Short: "Manage project JSON tables and records for Func.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTable(ctx, originalArgsAfter(rawArgs, []string{"table"}))
		},
	}
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"table"}, "list", "List project JSON tables.", runTable, addListFlags))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"table"}, "get", "Get a project JSON table.", runTable, addGetFlags))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"table"}, "create", "Create a project JSON table.", runTable, addSchemaCreateFlags))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"table"}, "update", "Update a project JSON table.", runTable, addSchemaUpdateFlags))
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"table"}, "delete", "Delete a project JSON table.", runTable, addGetFlags))
	recordCmd := &cobra.Command{
		Use:   "record",
		Short: "Manage project JSON table records.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTable(ctx, originalArgsAfter(rawArgs, []string{"table"}))
		},
	}
	recordCmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"table"}, "list", "List table records.", runTable, addTableRecordListFlags))
	recordCmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"table"}, "get", "Get a table record.", runTable, addTableRecordGetFlags))
	recordCmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"table"}, "create", "Create a table record.", runTable, addTableRecordCreateFlags))
	recordCmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"table"}, "update", "Update a table record.", runTable, addTableRecordUpdateFlags))
	recordCmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"table"}, "delete", "Delete a table record.", runTable, addTableRecordGetFlags))
	cmd.AddCommand(recordCmd)
	return cmd
}

func funcCommand(ctx context.Context, rawArgs []string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "func",
		Short: "Run project Func backend code with sample input.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFunc(ctx, originalArgsAfter(rawArgs, []string{"func"}))
		},
	}
	cmd.AddCommand(legacyCommandPass(ctx, rawArgs, []string{"func"}, "run", "Run a project Func with sample input.", runFunc, addFuncRunFlags))
	return cmd
}

func addSiteIDFlag(flags *pflag.FlagSet) {
	flags.String("site_id", "", "Site reference in <project_id>/<site_id> format.")
}

// addVersionSiteFlags mirrors pull/push so site-version commands can be run from
// inside a pulled workspace without repeating --site_id.
func addVersionSiteFlags(flags *pflag.FlagSet) {
	flags.String("site_id", "", "Site reference in <project_id>/<site_id> format. Optional inside a pulled workspace.")
	flags.String("dir", ".", "Local Creght project directory.")
}

func addVersionCreateFlags(flags *pflag.FlagSet) {
	addVersionSiteFlags(flags)
	flags.String("note", "", "Note describing what this version contains.")
	flags.Bool("allow-dirty", false, "Snapshot the remote site even when local changes are unpushed.")
	flags.Bool("json", false, "Print the created version as JSON.")
}

func addVersionListFlags(flags *pflag.FlagSet) {
	addVersionSiteFlags(flags)
	flags.Int("limit", 0, "Max versions to print. 0 prints every version returned.")
	flags.Bool("json", false, "Print the raw publish state as JSON.")
}

// addVersionPublishFlags deliberately has no --version flag: the root command
// treats a bare --version anywhere in the args as "print the CLI version", which
// would swallow it. The version to publish is a positional argument instead.
func addVersionPublishFlags(flags *pflag.FlagSet) {
	addVersionSiteFlags(flags)
	flags.String("note", "", "Optional publish note.")
	flags.Bool("json", false, "Print the publish result as JSON.")
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
	flags.String("out", "", "Write JSON output to file instead of stdout.")
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

func addTableRecordBaseFlags(flags *pflag.FlagSet) {
	addSiteIDFlag(flags)
	flags.String("table", "", "Table key or id.")
}

func addTableRecordListFlags(flags *pflag.FlagSet) {
	addTableRecordBaseFlags(flags)
	flags.Int("limit", 20, "Result limit.")
	flags.Int("offset", 0, "Result offset.")
	flags.String("order_by", "", "Order by.")
	flags.String("where", "", "Simple equality filter JSON file.")
	flags.String("filter", "", "Structured filter JSON file.")
}

func addTableRecordGetFlags(flags *pflag.FlagSet) {
	addTableRecordBaseFlags(flags)
	flags.String("id", "", "Record id.")
	flags.String("out", "", "Write JSON output to file instead of stdout.")
}

func addTableRecordCreateFlags(flags *pflag.FlagSet) {
	addTableRecordBaseFlags(flags)
	flags.String("data", "", "Record JSON file.")
	flags.Int("sort", 0, "Record sort.")
}

func addTableRecordUpdateFlags(flags *pflag.FlagSet) {
	addTableRecordCreateFlags(flags)
	flags.String("id", "", "Record id.")
}

func addFuncRunFlags(flags *pflag.FlagSet) {
	addSiteIDFlag(flags)
	flags.String("key", "", "Func key or key.method.")
	flags.String("method", "", "Method name.")
	flags.String("input", "", "Input JSON file.")
	flags.Int("timeout_ms", 0, "Timeout in milliseconds.")
}
