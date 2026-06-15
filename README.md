# Creght CLI

Creght CLI is a thin local bridge for syncing site code between a local directory and Creght.

The CLI can also run a local Vite preview for pulled Creght projects. Creght remains responsible for cloud rendering, CMS, assets, and the realtime preview environment.

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

The config file contains the API host and CLI token.

When `--web` is omitted, the CLI uses `CREGHT_WEB_HOST` if set. For local API hosts such as `localhost` or `127.0.0.1`, it defaults to `http://localhost:5173`.
For production, the default API host and default web host are both `https://creght.cn`.

## Logout

Remove the saved CLI config:

```bash
creght logout
```

This clears the saved token and any saved API host. The next command will use the production default unless you set `CREGHT_API_HOST`.

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

Use the `project_id/site_id` value with `pull`, `push`, and `sync`.

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

## Pull Site Code

Download the current remote site files into a local directory:

```bash
creght pull --site_id=<project_id>/<site_id> --dir=./mysite
```

For local development:

```bash
CREGHT_API_HOST=http://localhost:8433 creght pull --site_id=<project_id>/<site_id> --dir=./mysite
```

The command writes remote files such as `/page/...`, `/component/...`, and `creght.config.ts` into the target directory.

## Local Vite Preview

Creght projects pulled by the CLI usually do not have their own `package.json`
or `node_modules`. The local preview plugin therefore uses Vite only for local
file serving and TSX transpilation; third-party packages continue to resolve
through the Creght import map, matching the Web editor preview model. In
`creght dev`, the CLI loads the platform import map from server system info and
passes it to the Vite plugin; the plugin's local map is only a fallback.

Install Vite in the local project folder:

```bash
cd ./mysite
npm init -y
npm install -D vite esbuild creght-cli
```

Create `vite.config.mjs`:

```js
import { defineConfig } from 'vite'
import creght from 'creght-cli/vite'

export default defineConfig({
  plugins: [
    creght({
      apiHost: 'https://creght.cn',
      projectId: '<project_id>',
      // token: process.env.CREGHT_TOKEN,
    }),
  ],
})
```

Run it:

```bash
npx vite --host 0.0.0.0
```

The plugin maps `/page/Index.tsx` to `/`, `/page/About.tsx` to `/about`, starts
from the platform import map, merges `creght.config.ts` import-map entries,
loads `/index.css` through the Tailwind browser runtime, proxies local `/api/*`
requests to `apiHost`, calls page `getServerSideProps()` in the browser for a
preview-only first render, and uses Vite HMR to re-import the current page
module after local file changes without a full page reload.

## Push Local Changes

Push the current local directory snapshot to Creght and exit:

```bash
creght push --site_id=<project_id>/<site_id> --dir=./mysite
```

For local development:

```bash
CREGHT_API_HOST=http://localhost:8433 creght push --site_id=<project_id>/<site_id> --dir=./mysite
```

The CLI scans the local directory and calls the existing Creght `site_action`
API to create or update remote files.

## Sync Local Changes

Run watch mode for a local directory:

```bash
creght sync --site_id=<project_id>/<site_id> --dir=./mysite
```

For local development:

```bash
CREGHT_API_HOST=http://localhost:8433 creght sync --site_id=<project_id>/<site_id> --dir=./mysite
```

`sync` first pushes the current local snapshot, then keeps running and
automatically listens for local file changes. When a file is changed locally,
the CLI calls the existing Creght `site_action` API and updates the remote site
in realtime. The command also prints the remote preview URL when available.

## Local Web Editor Bidirectional Sync

Run local files and the online Creght editor against the same cloud realtime
files:

```bash
creght dev --site_id=<project_id>/<site_id> --dir=./mysite
```

For local backend or web development:

```bash
CREGHT_API_HOST=http://localhost:8433 creght dev --web=http://localhost:5173 --site_id=<project_id>/<site_id> --dir=./mysite
```

The command prints the online Web editor URL, pushes local file changes to
Creght, and listens to the existing WebSocket collaboration channel so editor
changes are written back to the local directory. MVP conflict handling is last
write wins.

`dev` also starts a local Vite preview by default:

```text
  VITE v8.0.14  ready in 529 ms
  ➜  Local:   http://localhost:5173/
Local Vite:  started (preferred http://localhost:5173; use the Vite Local URL above)
```

Use `--preview-port` or `--preview-host` to change the preferred local preview
address. If that port is occupied, Vite uses its normal auto-port behavior and
prints the actual URL in the terminal:

```bash
creght dev --site_id=<project_id>/<site_id> --dir=./mysite --preview-port=5174
```

Disable the local preview when you only want file sync:

```bash
creght dev --site_id=<project_id>/<site_id> --dir=./mysite --no-preview
```

The preview uses the bundled `creght-cli/vite` plugin. If the site directory
has `node_modules/.bin/vite`, that local Vite is used; otherwise the CLI starts
a hidden temporary Vite runtime under `.creght/` and installs `vite` plus
`esbuild` there.

Local file changes are pushed through Vite HMR as a React root re-render. This
avoids a browser-level refresh, but it is not yet full React Fast Refresh and
does not guarantee component state preservation.

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
creght content create --site_id=<project_id>/<site_id> --collection=blogs --data=./content.json --slug=hello-world
creght content update --site_id=<project_id>/<site_id> --collection=blogs --id=<content_id> --data=./content.json
creght content delete --site_id=<project_id>/<site_id> --collection=blogs --id=<content_id>
```

`--data` can point to either a plain CMS content body or a full content object. A plain content body may include a business field named `body`. The CLI treats JSON as a full content object only when it includes wrapper fields such as `id`, `slug`, `content_app_id`, `json_schema`, `status`, `sort`, or `tags`.

If your business JSON has a top-level `slug`, do not pass it as plain body JSON because `slug` is a content wrapper field. Either pass the slug as a flag and omit it from `--data`:

```bash
creght content create --site_id=<project_id>/<site_id> --collection=prompts --data=./content-body.json --slug=typography-v02
```

Or use a full content object and put business fields under `body`:

```json
{
  "slug": "typography-v02",
  "body": {
    "title": "Typography V.02",
    "description": "100vh",
    "tags": ["skill"]
  }
}
```

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

## Upload Assets

Upload a local file through the Creght site asset flow:

```bash
creght upload --site_id=<project_id>/<site_id> --file=./image.png
```

The command prints the public file URL by default. Use `--json` to inspect the
full upload metadata, including `site_path`, a stable `/_assets/...` path that
can be used from Creght site code:

```bash
creght upload --site_id=<project_id>/<site_id> --file=./image.png --json
```

Optional flags:

```bash
creght upload --site_id=<project_id>/<site_id> --file=./image.png --name=hero.png --mimetype=image/png
```

## Push And Sync Boundary

The current MVP push/sync mode is one-way:

```text
local directory -> Creght remote site
```

`push` fetches the remote file list to build the local path to remote file id
mapping, scans the local directory, uploads the current local snapshot, and then
exits.

`sync` is watch mode. It performs the same initial local snapshot push, then
keeps running and automatically listens for later local changes.

Neither command pulls Web editor changes back to the local directory while
running. If you edit the same site in the Web editor, run `pull` manually or
restart from a clean local copy before continuing.

Use a test project/site while validating the CLI. Do not run `push` or `sync`
against production content unless the local directory is intended to be the
source of truth.

## Commands

Creght CLI is a local bridge for Creght site code. It can authenticate with
Creght, list projects and sites, pull remote site files into a local directory,
push local files back to Creght, watch local files for realtime sync, open the
remote preview, and publish a site.

The CLI commands still use the Creght backend and web app for the canonical
preview. The Vite plugin is a local development helper and intentionally does
not implement full production SSR.

```bash
creght login [--web=https://creght.cn]
creght logout
creght project list
creght pull --site_id=<project_id>/<site_id> --dir=./mysite
creght push --site_id=<project_id>/<site_id> --dir=./mysite
creght sync --site_id=<project_id>/<site_id> --dir=./mysite
creght dev --site_id=<project_id>/<site_id> --dir=./mysite [--web=https://creght.cn]
creght preview --site_id=<project_id>/<site_id>
creght publish --site_id=<project_id>/<site_id> [--note=<note>]
creght cms collections --site_id=<project_id>/<site_id>
creght cms collection create --site_id=<project_id>/<site_id> --key=<key> --name=<name> --schema=./schema.json
creght content list --site_id=<project_id>/<site_id> --collection=<key>
creght content create --site_id=<project_id>/<site_id> --collection=<key> --data=./content.json
creght form list --site_id=<project_id>/<site_id>
creght form create --site_id=<project_id>/<site_id> --key=<key> --name=<name> --schema=./schema.json
creght upload --site_id=<project_id>/<site_id> --file=./image.png
creght version
```

Command meanings:

- `login`: Authenticate this machine with Creght and save a CLI token.
- `logout`: Remove the saved CLI token and API host configuration.
- `project`: List available projects and sites. Use `project_id/site_id` with site commands. Also supports `project create`.
- `pull`: Download the current remote site files into a local directory.
- `push`: Push the current local directory snapshot to the remote site.
- `sync`: Watch mode; push the current snapshot, then keep listening for local changes.
- `dev`: Bidirectionally sync local files with cloud realtime files and the online Web editor.
- `preview`: Open the remote preview URL for a site in the browser.
- `publish`: Publish a site to make the current remote site version live.
- `cms`: Manage CMS collections.
- `content`: Manage CMS content entries.
- `form`: Manage forms and form submissions.
- `upload`: Upload a local file as a Creght site asset and print its URL.
- `version`: Print the installed CLI version.

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
