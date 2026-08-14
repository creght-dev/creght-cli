# Creght CLI

Creght CLI is a thin local bridge for syncing site code between a local directory and Creght.

Creght remains responsible for cloud rendering, CMS, assets, and the preview environment. The CLI does not render sites locally; use `creght preview` to open the canonical remote preview.

## Install

Using npm:

```bash
npm install -g creght-cli
```

Build from source:

```bash
cd /Users/bysir/dev/bysir/creght-cli
go build -o creght ./cmd/creght
```

Optional:

```bash
mv ./creght /usr/local/bin/creght
```

## Login

For production:

```bash
creght login
```

For local development:

```bash
CREGHT_API_HOST=http://localhost:8433 creght login --web=http://localhost:5173
```

The command opens a browser authorization page. After authorization succeeds, the CLI stores the token in:

```text
~/Library/Application Support/creght/config.json
```

The config file contains the default API host and CLI tokens. Tokens are stored per API host, so logging in to `https://creght.cn`, `https://creght.com`, or a local backend does not overwrite the other hosts' login state.

`CREGHT_API_HOST` applies to the single command it is set on and never changes the saved default. A login prefixed with it saves a token for that host and leaves the default alone, so a later bare `creght project list` still talks to the default host. To switch hosts for good, see [Default API Host](#default-api-host).

When `--web` is omitted, the CLI uses `CREGHT_WEB_HOST` if set. For local API hosts such as `localhost` or `127.0.0.1`, it defaults to `http://localhost:5173`.
For production, the default API host and default web host are both `https://creght.cn`.

## Logout

Remove the saved CLI login for the current API host:

```bash
creght logout
```

When `CREGHT_API_HOST` is set, `logout` removes only that host's token. Other saved hosts remain logged in. If the last saved token is removed, the config file is deleted.

Logging out does not move the default API host either, unless the default is the host being logged out of.

## Default API Host

Commands run without `CREGHT_API_HOST` use the saved default API host, `https://creght.cn` until it is changed. Show it, and any override in effect:

```bash
creght config get
```

Change it:

```bash
creght config set api_host=https://creght.com
creght config set api_host=http://localhost:8433
```

This is the only thing that moves the default — neither `login` nor `logout` under `CREGHT_API_HOST` does. Tokens are kept per API host, so switching to a host already logged in to needs no new login.

## List Projects

```bash
creght project list
```

For local development:

```bash
CREGHT_API_HOST=http://localhost:8433 creght project list
```

Example output:

```text
project_id    Project Name
  project_id/site_id    Site Name
```

Use the `project_id/site_id` value with `pull` and `push`.

## Create Project

Create a new project:

```bash
creght project create --name="My Project"
```

For local development:

```bash
CREGHT_API_HOST=http://localhost:8433 creght project create --name="My Project"
```

You can also create from an existing project or template when the backend allows it:

```bash
creght project create --name="My Project" --from_id=<project_id>
creght project create --name="My Project" --tpl_id=<template_id>
```

## Pull Site Workspace

Download the current remote site workspace into a local directory:

```bash
creght pull --site_id=<project_id>/<site_id> --dir=./mysite
```

For local development:

```bash
CREGHT_API_HOST=http://localhost:8433 creght pull --site_id=<project_id>/<site_id> --dir=./mysite
```

The command writes a file-based workspace:

```text
mysite/
  AGENTS.md
  page/...
  component/...
  talizen.config.ts
  backend/
    func/
      booking.ts
      profile/settings.ts
```

Local paths mirror remote site paths exactly: `page/Index.tsx` maps to
`/page/Index.tsx` and `backend/func/booking.ts` maps to
`/backend/func/booking.ts`.

Every file in the workspace is an ordinary site file. Func backend code is
simply the set of site files under `backend/func/`; for example
`backend/func/booking.ts` is the Func with key `booking`, and
`backend/func/profile/settings.ts` is `profile/settings`.

The first pull requires `--site_id` and usually `--dir`. After that,
`.creght/state.json` records the site reference. From the workspace root or any
child directory, `pull`, `diff`, and `push` find that file by walking upward,
so normal ongoing usage does not repeat either option:

```bash
creght pull
creght diff
creght push
```

Pass `--dir` and `--site_id` explicitly when operating on another workspace.

Single-file `<path>` arguments resolve from the current directory, git-style:
`creght push Index.tsx` run inside `page/` pushes `/page/Index.tsx`, while
paths starting with `/` are always workspace-root paths. When the discovered
workspace root differs from the current directory, commands print
`workspace: <root>` before doing anything.

### Ignoring workspace files

Add a `.creghtignore` file at the workspace root to exclude local and remote
paths from `pull`, `diff`, and `push`. Ignored remote files are left untouched,
including when using `push --delete` or `push --force`. The `.creghtignore`
file itself is always local and is never synced.

The syntax follows common gitignore conventions: blank lines, `#` comments,
`!` negation, root-relative patterns, directory patterns, and `*`, `?`, and
`**` wildcards. For example:

```gitignore
# Ignore generated files
generated/*

# Keep one generated file in sync
!generated/keep.ts

*.local.ts
/scratch/
```

## Push Local Changes

Push the current local directory snapshot to Creght and exit:

```bash
cd ./mysite
creght push
```

For local development:

```bash
CREGHT_API_HOST=http://localhost:8433 creght push --site_id=<project_id>/<site_id> --dir=./mysite
```

The CLI scans the local workspace and diffs every file against the remote site
files. Local paths map to remote paths as-is, so `page/Index.tsx` becomes
`/page/Index.tsx` and `backend/func/**/*.ts` becomes `/backend/func/**/*.ts`.

All files flow through the same site file mechanism (`file_list` /
`site_action`). Func code is not a separate resource; creating, editing,
renaming, or deleting a file under `backend/func/` creates, updates, renames, or
deletes the corresponding site file, and Func code is versioned and published
together with the rest of the site.

## Resolve Conflicts

`pull` and `push` compare three versions of every file: the base snapshot
recorded at the last pull/push (hashes in `.creght/state.json`, contents under
`.creght/base/`), the current local file, and the current remote file.

When a file changed on both sides, `pull` three-way merges it:

- Non-overlapping edits merge automatically; the file is reported as `merged`
  and the result stays local until the next `push`.
- Overlapping edits write git-style conflict markers
  (`<<<<<<< local` / `=======` / `>>>>>>> remote`) into the file and `pull`
  exits non-zero.

List and resolve marker files:

```bash
creght resolve --list
creght resolve page/Index.tsx --ours    # keep the local side
creght resolve page/Index.tsx --theirs  # keep the remote side
```

Editing the markers by hand works too. `push` refuses to upload any file that
still contains conflict markers.

`push` reports a conflict for files changed on both sides — run `creght pull`
to merge them first, push the rest with `creght push --skip-conflicts`, or
overwrite remote with `creght push --force`. Before anything is overwritten
(local work by `pull`, diverged remote copies by `push --force`), the losing
content is backed up under `.creght/backup/<timestamp>-*/`.

`creght diff --json` marks each conflict with `reason`, `auto_mergeable`, and
`base_to_local_diff` / `base_to_remote_diff`, so agents can decide how to
resolve without extra round-trips.

## Open Preview

Open the remote preview URL for a site in the browser:

```bash
creght preview --site_id=<project_id>/<site_id>
```

For local development:

```bash
CREGHT_API_HOST=http://localhost:8433 creght preview --site_id=<project_id>/<site_id>
```

## Publish Site

Publish a site:

```bash
creght publish --site_id=<project_id>/<site_id>
```

With a publish note:

```bash
creght publish --site_id=<project_id>/<site_id> --note="Update homepage copy"
```

For local development:

```bash
CREGHT_API_HOST=http://localhost:8433 creght publish --site_id=<project_id>/<site_id>
```

`publish` reports the version it created, so you can publish it again later:

```text
Published <project_id>/<site_id>
version 14 (id 458) is live on demo.creght.cn
```

## Site Versions

A site version is an immutable snapshot of the site's source files — the
platform equivalent of a git commit. Create one whenever a piece of work is
done, then publish whichever version you want the live site to serve.

Snapshots cover source files only. CMS content and the platform state under
`/platform/**` (CMS/form/table/auth definitions) are live, so publishing an
older version does not roll those back.

Inside a pulled workspace `--site_id` is optional; the version commands
discover `.creght/state.json` from the current directory or its parents, like
`pull`/`push` do.

Snapshot the current site source without publishing it:

```bash
creght version create --note="Add pricing page"
```

A version records the *remote* files, so local edits that were never pushed
would be missing from it. `create` therefore compares the workspace against the
site first and refuses to run while anything is unpushed:

```text
update /page/Index.tsx
1 change is not pushed yet and would be missing from this version; run creght
push first, or pass --allow-dirty to snapshot the remote site as it is
```

Run `creght push` first, or pass `--allow-dirty` to snapshot the remote site as
it is. The platform rejects a snapshot identical to the newest version, so
repeated `create` calls never pile up duplicates.

List versions, newest first:

```bash
creght version list
```

```text
   VERSION  ID   CREATED           FROM                  NOTE
   12       456  2026-08-05 14:31  api_generate_version  Add pricing page
*  11       455  2026-08-05 11:02  publish               Fix nav
   10       442  2026-08-04 18:40  agent                 -
* live: version 11 (id 455), served by demo.creght.cn
pinned: www.example.com -> version 12 (id 456)
1 change is pending on the site since the newest version; run creght version create to snapshot them
```

`*` marks the version the live site serves. `VERSION` is the per-site number
that `version publish` takes; `ID` is the platform-wide version id. Use
`--limit=<n>` to shorten the list and `--json` to get the raw publish state,
including every version, the pinned domains, and the exact files that changed
since the newest version.

Make an existing version live, forward to a newer one or back to an older one:

```bash
creght version publish 12
creght version publish 12 --note="Roll back nav change"
```

Every domain follows it except domains pinned to a specific version.
Publishing the version already being served is a no-op that just refreshes its
caches.

Publishing a version changes only which snapshot the live site serves — it
restores nothing. The editable site workspace and your local files are
untouched, and `creght pull` still fetches the current workspace rather than the
published version's files. So after rolling production back to an older version,
the workspace still holds the newer source and the next `publish` would ship it
again; to revert the code itself, edit locally and push.

`12` is the per-site number from the `VERSION` column. To reach a version older
than the window `version list` returns, select it by id instead:

```bash
creght version publish id:456
```

Bare `creght version` still prints the installed CLI version.

## Manage CMS Collections

List CMS collections:

```bash
creght cms collections --site_id=<project_id>/<site_id>
```

Create a collection from a JSON Schema file:

```bash
creght cms collection create --site_id=<project_id>/<site_id> --key=blogs --name="Blogs" --schema=./blogs.schema.json
```

Update or delete by collection key or id:

```bash
creght cms collection get --site_id=<project_id>/<site_id> --key=blogs
creght cms collection update --site_id=<project_id>/<site_id> --key=blogs --schema=./blogs.schema.json
creght cms collection delete --site_id=<project_id>/<site_id> --key=blogs
```

`--schema` can point to either a raw JSON Schema object or a full collection JSON object containing fields such as `key`, `name`, `desc`, and `json_schema`.

## Manage CMS Content

List, get, create, update, and delete content entries:

```bash
creght content list --site_id=<project_id>/<site_id> --collection=blogs
creght content get --site_id=<project_id>/<site_id> --collection=blogs --slug=hello-world
creght content get --site_id=<project_id>/<site_id> --collection=blogs --slug=hello-world --out=./content.json
creght content create --site_id=<project_id>/<site_id> --collection=blogs --data=./content.json --slug=hello-world
creght content update --site_id=<project_id>/<site_id> --collection=blogs --id=<content_id> --data=./content.json
creght content delete --site_id=<project_id>/<site_id> --collection=blogs --id=<content_id>
```

`--data` must be a full content object whose business fields sit under an object-valued `body` key. This is the only accepted format — a bare body is rejected with a format error, because guessing between the two shapes silently dropped fields whenever the body itself contained a name like `tags`, `slug`, or `sort`.

```json
{
  "slug": "typography-v02",
  "sort": 15,
  "body": {
    "title": "Typography V.02",
    "description": "100vh",
    "tags": ["skill"]
  }
}
```

Top-level `slug` and `sort` are optional; the `--slug` / `--sort` flags override the file only when actually passed:

```bash
creght content create --site_id=<project_id>/<site_id> --collection=prompts --data=./content.json --slug=typography-v02
creght content update --site_id=<project_id>/<site_id> --collection=prompts --id=<content_id> --sort=15
```

`update` applies a partial update, so `--data` is optional there — pass `--slug` / `--sort` alone to rename or reorder without re-submitting the body, as in the command above. `create` still requires `--data`.

`sort` controls the order editors see in the CMS list, bigger first. Omitting it lets `create` append the entry last and leaves `update`'s current value alone. Use `content update --sort` to reorder; deleting and recreating an entry changes its id, and site versions do not snapshot CMS content, so that cannot be undone.

One asymmetry worth knowing: the platform reads a zero `sort` on **create** as "auto-assign, append last", so a literal `0` cannot be created — `create --sort=0` appends. `update --sort=0` does store a real 0.

## Manage Forms

List, create, update, and delete forms:

```bash
creght form list --site_id=<project_id>/<site_id>
creght form create --site_id=<project_id>/<site_id> --key=contact-form --name="Contact form" --schema=./contact.schema.json
creght form get --site_id=<project_id>/<site_id> --key=contact-form
creght form update --site_id=<project_id>/<site_id> --key=contact-form --schema=./contact.schema.json
creght form delete --site_id=<project_id>/<site_id> --key=contact-form
```

Inspect and delete form submissions:

```bash
creght form logs --site_id=<project_id>/<site_id> --key=contact-form
creght form log get --site_id=<project_id>/<site_id> --key=contact-form --log_id=<log_id>
creght form log delete --site_id=<project_id>/<site_id> --key=contact-form --log_id=<log_id>
```

Submit a form payload through the platform API:

```bash
creght form submit --site_id=<project_id>/<site_id> --key=contact-form --data=./payload.json
```

After creating or changing CMS collections or forms, run `creght pull` again to refresh generated files such as `/types/cms.d.ts` and `/types/form.d.ts` before writing code that imports those types.

## Manage Backend Tables

Project JSON tables provide persistent data for Creght/Talizen Func code through
`ctx.db.*`.

```bash
creght table list --site_id=<project_id>/<site_id>
creght table create --site_id=<project_id>/<site_id> --key=appointments --name="Appointments" --schema=./appointments.schema.json
creght table get --site_id=<project_id>/<site_id> --key=appointments
creght table update --site_id=<project_id>/<site_id> --key=appointments --schema=./appointments.schema.json
creght table delete --site_id=<project_id>/<site_id> --key=appointments
```

Manage seed or operational records:

```bash
creght table record list --site_id=<project_id>/<site_id> --table=appointments
creght table record list --site_id=<project_id>/<site_id> --table=appointments --where=./where.json
creght table record get --site_id=<project_id>/<site_id> --table=appointments --id=<record_id> --out=./record.json
creght table record create --site_id=<project_id>/<site_id> --table=appointments --data=./record.json
creght table record update --site_id=<project_id>/<site_id> --table=appointments --id=<record_id> --data=./patch.json
creght table record delete --site_id=<project_id>/<site_id> --table=appointments --id=<record_id>
```

`record update` sends a patch body to the backend. Existing fields are merged,
and a `null` field value removes that field. Both `create` and `update` accept
`--sort=<n>`; omitting the flag leaves `sort` out of the request instead of
zeroing it. Unlike content, a record's `sort` cannot be set to `0` — the platform
ignores a zero sort on record update, so `--sort=0` is rejected there rather than
silently doing nothing, and on `create` it means "append last".

## Func Backend Code As Files

Func is for small project-level backend workflows such as bookings, RSVP,
availability checks, protected status updates, and JSON-table reads/writes.
Func code is stored as ordinary site source files under `backend/func/`; there
is no separate Func resource. A Func's key is its extensionless path under
`backend/func/`, for example `booking` or `profile/settings`.

The workflow is the same as for any site file:

1. Run `creght pull --site_id=<project_id>/<site_id> --dir=./mysite`.
2. Edit or create files under `./mysite/backend/func/`.
3. Run `creght push --site_id=<project_id>/<site_id> --dir=./mysite`.

Examples:

- `backend/func/booking.ts` <-> remote site file `/backend/func/booking.ts`, Func key `booking`
- `backend/func/profile/settings.ts` <-> remote `/backend/func/profile/settings.ts`, Func key `profile/settings`

Because Func code is just a site file, it is versioned and published together
with the site, and it participates in the same 3-way merge and conflict
detection as every other file. Deleting a local Func file deletes the remote
site file on the next push (with `--delete`); renaming is treated as delete + create.

Func files should use ESM exports and the `(input, ctx)` signature:

```ts
export function create(input, ctx) {
  return ctx.db.insert("appointments", input)
}
```

Page and component code should call Func through the `talizen/func` SDK:

```ts
import { invoke } from "talizen/func"

await invoke("booking.create", input)
```

Use `talizen/auth` for login, registration, logout, current-user state, and
OAuth. Do not implement passwords, sessions, or OAuth callbacks in Func.

`func run` is the only Func command; it posts to a dedicated invocation endpoint
to self-test a Func method with sample input:

```bash
creght func run --site_id=<project_id>/<site_id> --key=booking.create --input=./input.json
```

The output matches the Func HTTP response protocol: successful runs print
`{"result": ...}` and thrown Func errors print `{"error": "..."}`. There is no
top-level `ok` execution wrapper.

There are no `creght func list/get/create/update/delete` commands. Manage Func
code by editing `backend/func` files and syncing them with pull/push
like any other site file; `func run` only runs sample input.

## Upload Assets

Upload a local file through the Creght site asset flow:

```bash
creght upload --site_id=<project_id>/<site_id> --file=./image.png
```

The command prints the public file URL by default; use that URL directly from
site code. Use `--json` to also get `file_path` (the storage path behind the URL)
and `hash_exist` (true when the same content was already uploaded, so only the
record was created):

```bash
creght upload --site_id=<project_id>/<site_id> --file=./image.png --json
```

Optional flags:

```bash
creght upload --site_id=<project_id>/<site_id> --file=./image.png --name=hero.png --mimetype=image/png
```

## Push And Pull Boundary

`push` is one-way:

```text
local directory -> Creght remote site
```

`push` fetches the remote file list, compares base/local/remote, uploads local
changes, and then exits. It never merges remote edits into local files — that
is `pull`'s job. If you edit the same site in the Web editor, run `creght pull`
to merge those edits into the workspace before pushing (see Resolve
Conflicts above).

Use a test project/site while validating the CLI. Do not run `push --force`
against production content unless the local directory is intended to be the
source of truth.

## Commands

Creght CLI is a local bridge for Creght site code. It can authenticate with
Creght, list projects and sites, pull remote site files into a local directory
with three-way merge, push local files back to Creght, resolve conflicts, open
the remote preview, and publish a site.

It can also manage site versions: immutable snapshots of a site's source files,
created and published like git commits.

The CLI commands still use the Creght backend and web app for the canonical
preview. The CLI does not render sites locally.

```bash
creght login [--web=https://creght.cn]
creght logout
creght config get
creght config set api_host=https://creght.cn
creght project list
creght pull --site_id=<project_id>/<site_id> --dir=./mysite
creght push --site_id=<project_id>/<site_id> --dir=./mysite
creght resolve --list
creght preview --site_id=<project_id>/<site_id>
creght publish --site_id=<project_id>/<site_id> [--note=<note>]
creght cms collections --site_id=<project_id>/<site_id>
creght cms collection create --site_id=<project_id>/<site_id> --key=<key> --name=<name> --schema=./schema.json
creght content list --site_id=<project_id>/<site_id> --collection=<key>
creght content create --site_id=<project_id>/<site_id> --collection=<key> --data=./content.json
creght form list --site_id=<project_id>/<site_id>
creght form create --site_id=<project_id>/<site_id> --key=<key> --name=<name> --schema=./schema.json
creght table list --site_id=<project_id>/<site_id>
creght table record create --site_id=<project_id>/<site_id> --table=<key> --data=./record.json
creght func run --site_id=<project_id>/<site_id> --key=<key.method> --input=./input.json
creght upload --site_id=<project_id>/<site_id> --file=./image.png
creght version
creght version create [--note=<note>] [--allow-dirty]
creght version list [--limit=<n>] [--json]
creght version publish <version_no> [--note=<note>]
```

Command meanings:

- `login`: Authenticate this machine with Creght and save a CLI token for the current API host.
- `logout`: Remove the saved CLI login for the current API host.
- `config`: Show (`config get`) or change (`config set api_host=<url>`) the default API host used when `CREGHT_API_HOST` is not set.
- `project`: List available projects and sites. Use `project_id/site_id` with site commands. Also supports `project create`.
- `pull`: Download site files (including Func code under `backend/func/`) into a local workspace, three-way merging remote and local edits.
- `push`: Push local workspace changes to the remote site/project after a three-way conflict check.
- `resolve`: List files with conflict markers, or resolve one by keeping the local (`--ours`) or remote (`--theirs`) side.
- `preview`: Open the remote preview URL for a site in the browser.
- `publish`: Snapshot the current remote site source into a new version and make it live in one step.
- `cms`: Manage CMS collections.
- `content`: Manage CMS content entries.
- `form`: Manage forms and form submissions.
- `table`: Manage project JSON tables and records used by Func.
- `func`: Run project Func backend code with sample input. Func code itself is edited as `backend/func` site files and synced with pull/push.
- `upload`: Upload a local file as a Creght site asset and print its URL.
- `version`: Print the installed CLI version. Subcommands manage site versions — immutable source snapshots: `version create` records one, `version list` shows them and which is live, `version publish` makes one live.

## Release

GitHub Releases are created by GitHub Actions when a tag matching `v*` is pushed.
The same workflow publishes the npm package `creght-cli`.

The release workflow builds binaries for:

- macOS: `darwin/amd64`, `darwin/arm64`
- Linux: `linux/amd64`, `linux/arm64`
- Windows: `windows/amd64`, `windows/arm64`

Create and push a release tag:

```bash
git tag v0.1.0
git push origin v0.1.0
```

Before pushing a release tag, make sure `package.json` has the same version as the
tag without the leading `v`, and configure npm Trusted Publishing for `creght-cli`
with GitHub repository `creght/creght-cli` and workflow filename `release.yml`.

If this repository is mirrored to GitHub with a different remote name, push the tag to that remote:

```bash
git remote add github git@github.com:creght-dev/creght-cli.git
git push github main
git push github v0.1.0
```
