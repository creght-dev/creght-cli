# Creght CLI Agent Notes

## Project Purpose

This repository contains the Cregh CLI, a Go command-line tool that acts as a local bridge for Cregh site code.

It supports:

- logging in to Cregh from the terminal
- listing projects and sites
- pulling remote site files into a local directory
- pushing local site files back to Cregh
- deleting a single remote site file by path (`creght rm`), which also reaches
  files hidden by `.creghtignore` that `push --delete` cannot plan a deletion
  for
- printing a site's preview, live and editor addresses (`creght url`, aliased
  as `creght preview`); it prints and stays out of the way, and only opens a
  browser when `--open` asks it to
- creating, listing, and publishing site versions (immutable source snapshots)
- pulling one site version as a read-only snapshot directory (`creght pull
  --version_no=<n> --dir=<dir>`); see "Public-copy readers" below
- browsing project templates and creating a project from one (`creght tpl
  list/get/categories/use`, backed by `/api/u/tpl/*`; `tpl use` wraps
  `POST /api/u/project` with `tpl_id` and prints the new sites ready to pull)
- searching the UI reference library (`creght refs search/vocab`, backed by the
  public Func `/func/refs.*` on the library's own Creght site, not the API host;
  no login; `--save` downloads CDN-resized images for agents that read local files)

The CLI does not render sites locally. Rendering, CMS, assets, and realtime preview are handled by the Cregh backend and web app.

## Backend

The CLI talks to the Creght backend over HTTP; it has no code dependency on it.
Machine-specific notes — where the backend checkout lives, the local dev
addresses — go in `AGENTS.local.md` (not committed). Read it too when it exists.

A local backend conventionally answers on `http://localhost:8433` and its web app
on `http://localhost:5173`; `creght login` picks the latter automatically for a
localhost API host.

Production defaults:

```text
API: https://creght.cn
Web: https://creght.cn
```

Useful local commands:

```bash
CREGHT_API_HOST=http://localhost:8433 creght login --web=http://localhost:5173
CREGHT_API_HOST=http://localhost:8433 creght project list
CREGHT_API_HOST=http://localhost:8433 creght pull --site_id=<project_id>/<site_id> --dir=./mysite
CREGHT_API_HOST=http://localhost:8433 creght push --site_id=<project_id>/<site_id> --dir=./mysite
CREGHT_API_HOST=http://localhost:8433 creght url --site_id=<project_id>/<site_id>
creght publish
creght publish --site_id=<project_id>/<site_id> --note="Release note"
creght version create --note="Add pricing page"
creght version list
creght version publish <version_no>
```

The API host is resolved most-specific-first: `CREGHT_API_HOST`, then the
`api_host` recorded in `.creght/state.json` by the workspace the working
directory sits in, then the saved default, then the built-in `https://creght.cn`.
`creght -h` prints the host in effect and names which of the four it came from.

`CREGHT_API_HOST` applies to the one command it prefixes and never changes the
saved default or a workspace's recorded host — including on `login`, which saves
that host's token and leaves both alone. `creght pull` stamps the workspace with
the host it pulled from, so later commands in that directory need no prefix; the
stamp is written once and never rewritten. To point every command at a backend
instead of repeating the prefix, run
`creght config set api_host=http://localhost:8433`; `creght config get` shows the
current default plus any override or auto-discovery in effect.

## Token from the environment

`CREGHT_TOKEN` hands the CLI a token for every command it is set on, ahead of
the one saved in `config.json` (`applyEnvToken` in `internal/cli/token.go`). It
exists for a host program that holds its own Creght OAuth access token and runs
`creght` for the user, so the user never logs in to the CLI. The token is
borrowed: the CLI never saves, refreshes or revokes it. `login` and `logout`
refuse to run while it is set; a 401 carries a hint naming the token's source
(`authHint`, set on the client by `clientFromConfig`); `creght whoami` and
`creght -h` say which token is in use.

## Site versions

Version endpoints live in the backend under
`/api/u/project/:project_id/site/:site_id/`:

- `GET publish/state` backs `version list` (versions, live version, pinned
  domains, and the files changed since the newest version)
- `POST publish/version` backs all three writes: `create_only:true` snapshots
  without publishing, `version_id:0` snapshots and publishes (`creght publish`),
  and `version_id:<n>` publishes an existing version

Only Talizen (file-based) sites are supported. The endpoints branch on project
type internally; legacy visual-editor sites have no per-site version number, so
`version publish` falls back to `id:<version_id>` selection there.

### Public-copy readers

A project with public copy (`can_public_copy`) on shares its versions with
non-members: the backend lets them through `publish/state` (answered with
`read_only:true` — versions and the live version only, no domains, publish
targets or unversioned changes) and `file_list` with an explicit
`version=<version_no>` (the workspace is refused). That backs `version list`
and `pull --version_no` for someone who copied a template and merges its next
versions into their copy.

`pull --version_no` writes a snapshot, not a workspace: `.creght/state.json`
carries `snapshot` and no base, push/diff/rm/plain pull refuse to run there
(`refuseSnapshotWorkspace` in `internal/cli/snapshot.go`), and each pull makes
the directory match the version exactly, deleting everything else except `.git`
and `.creght` at its root. The flag is `--version_no`, not `--version`: the root
command treats a bare `--version` anywhere as "print the CLI version".

## Self-update

`creght update` updates the CLI in place. Every install path ends in the same
swap: download the GitHub release archive, check it against `checksums.txt`,
write the binary to a temp file beside the target and rename it over — then run
the installed binary and require it to print the version it claims to be. A
binary that does not run, or reports the wrong version, is rolled back to the
one it replaced. A `dev` build is refused.

The one exception is a *manual* `creght update` on an npm-vendored binary
(`<package>/vendor/<platform>-<arch>/creght`, confirmed by the package.json
name): that shells out to `npm install -g creght-cli@<version>`, so the whole
package — `bin/creght.js` included — moves together. The background worker
deliberately does **not**: `npm install -g` retires the package directory and
rebuilds the bin symlink, so `creght` is missing for the tens of seconds the
reinstall takes, and a detached worker killed in that window leaves the command
missing for good with nothing left to run that could repair it. The worker
swaps the vendored binary and rewrites the `version` field of the installed
package.json instead, so a later `npm update` does not reinstall over it. The
trade-off: a release that changes `bin/creght.js` needs a manual update or an
`npm install -g` to pick the wrapper up.

`releaseAPIBaseURL`, `releaseDownloadURL` and `runningExecutable` in
`internal/cli/update.go` are variables so tests can point them at an httptest
server and a temp binary.

The CLI also updates itself. The process is not resident, so a regular command
start spawns `creght update --auto` (a hidden flag) as a detached background
process — `Setsid` on Unix, `DETACHED_PROCESS` on Windows — which outlives the
command, installs the release, and records what it did; the next start then
runs the new binary and prints a one-line notice on stderr — only when stderr
is a terminal; for a program caller the notice stays pending, since callers
often merge stderr into the stdout they parse as JSON. Checks are
throttled to one per hour via `last_check_at` in `update-state.json` next to
`config.json` (the stamp is written before spawning, so failures also wait out
the hour), `update.lock` in the same directory admits one worker at a time and
is taken over after 30 minutes so a killed worker cannot wedge updates, the
worker logs to `update.log`, dev builds never take part, and
`CREGHT_NO_AUTO_UPDATE=1` disables the whole thing — the notice included.
`updateStateDir` in `internal/cli/update_auto.go` is a variable so tests can
point the state at a temp dir.

The invariant behind all of it: there is no moment at which there is no working
`creght`. Anything that would end with a missing or broken binary must restore
the old one instead.

## Release

Do not run local GoReleaser validation before release. Releases are validated
and built by the GitHub Actions release workflow when the release tag is pushed.
