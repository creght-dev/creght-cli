package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/creght-dev/creght-cli/internal/workspace"
)

// apiHostSource records where the API host a command talks to came from.
//
// Without it the CLI can only print a bare URL, which is exactly the case that
// needs explaining: a workspace pulled from one Creght deployment while the
// saved default names another silently talks to the workspace's host, and a
// user staring at "Current API host" has no way to account for it.
type apiHostSource int

const (
	apiHostSourceBuiltin apiHostSource = iota
	apiHostSourceConfig
	apiHostSourceEnv
	apiHostSourceWorkspace
)

// implicit reports whether the host was discovered rather than chosen.
//
// An implicit host must never be written back as the saved default. Both
// CREGHT_API_HOST and a workspace's recorded host are scoped — to one command
// and to one directory tree respectively — so a login made under either has to
// leave the default where the user put it.
func (s apiHostSource) implicit() bool {
	return s == apiHostSourceEnv || s == apiHostSourceWorkspace
}

type resolvedAPIHost struct {
	Host   string
	Source apiHostSource
	// Workspace is the discovered workspace root, set only when Source is
	// apiHostSourceWorkspace.
	Workspace string
}

// describe names the host's origin for `creght -h` and `creght config get`.
func (r resolvedAPIHost) describe() string {
	switch r.Source {
	case apiHostSourceEnv:
		return "CREGHT_API_HOST environment variable (this command only)"
	case apiHostSourceWorkspace:
		return fmt.Sprintf("auto-discovered from workspace %s (.creght/state.json)", r.Workspace)
	case apiHostSourceConfig:
		return "saved default (creght config set api_host)"
	default:
		return "built-in default"
	}
}

// resolveAPIHost picks the host for this command, most specific first:
// the workspace the command works in, then the environment variable, then the
// saved default, then the built-in.
//
// The workspace outranks CREGHT_API_HOST because a site lives on exactly one
// deployment: a host program sets the variable once for a whole project, whose
// sites may sit on different deployments, and a site's files sent anywhere but
// where they were pulled from would reach the wrong account or no site at all.
// The variable picks the host only for commands outside any workspace.
//
// savedHost is the api_host as stored in the config file, unresolved.
func resolveAPIHost(savedHost string) resolvedAPIHost {
	if host, root, ok := discoverWorkspaceAPIHost(); ok {
		return resolvedAPIHost{Host: host, Source: apiHostSourceWorkspace, Workspace: root}
	}
	if host, ok := envAPIHost(); ok {
		return resolvedAPIHost{Host: canonicalAPIHost(host), Source: apiHostSourceEnv}
	}
	if host := canonicalAPIHost(savedHost); host != "" {
		return resolvedAPIHost{Host: host, Source: apiHostSourceConfig}
	}

	return resolvedAPIHost{Host: canonicalAPIHost(defaultAPIHostValue), Source: apiHostSourceBuiltin}
}

// currentAPIHost resolves the host against the saved config, for callers that
// only want to report or record it.
func currentAPIHost() resolvedAPIHost {
	saved, err := loadRawConfig()
	if err != nil {
		return resolveAPIHost("")
	}

	return resolveAPIHost(saved.APIHost)
}

// targetWorkspaceDir is the site directory the running command works on, set
// by resolveSiteWorkspace once a command has settled its --dir. Empty until
// then, and for commands that work on no site directory.
var targetWorkspaceDir string

// resolveSiteWorkspace is workspace.ResolveSiteWorkspace that also makes the
// resolved directory the one the API host is discovered from, so
// `creght push --dir=sites/a` talks to the deployment sites/a was pulled from
// wherever it is run.
func resolveSiteWorkspace(dir string, siteID string, discover bool, requireState bool) (string, string, error) {
	root, resolvedSiteID, err := workspace.ResolveSiteWorkspace(dir, siteID, discover, requireState)
	if err == nil {
		targetWorkspaceDir = root
	}

	return root, resolvedSiteID, err
}

// discoverWorkspaceAPIHost reports the host the command's workspace was
// pulled from. A command with a site directory asks that directory alone —
// resolveSiteWorkspace already searched its parents where the command allows
// it, and a fresh pull into a subdirectory of another workspace must not
// inherit that workspace's host. Any other command walks up from the working
// directory. A workspace pulled by an older CLI has no recorded host and is
// skipped, falling back to CREGHT_API_HOST or the saved default as before.
func discoverWorkspaceAPIHost() (string, string, bool) {
	start, searchParents := targetWorkspaceDir, false
	if start == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", "", false
		}
		start, searchParents = cwd, true
	}

	root, state, hasState, err := findWorkspaceState(start, searchParents)
	if err != nil || !hasState {
		return "", "", false
	}
	host := canonicalAPIHost(strings.TrimSpace(state.APIHost))
	if host == "" {
		return "", "", false
	}

	return host, root, true
}
