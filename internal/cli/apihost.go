package cli

import (
	"fmt"
	"os"
	"strings"
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
	apiHostSourceWorkspace
	apiHostSourceEnv
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
// the environment variable, then the workspace the working directory sits in,
// then the saved default, then the built-in.
//
// savedHost is the api_host as stored in the config file, unresolved.
func resolveAPIHost(savedHost string) resolvedAPIHost {
	if host, ok := envAPIHost(); ok {
		return resolvedAPIHost{Host: canonicalAPIHost(host), Source: apiHostSourceEnv}
	}
	if host, root, ok := discoverWorkspaceAPIHost(); ok {
		return resolvedAPIHost{Host: host, Source: apiHostSourceWorkspace, Workspace: root}
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

// discoverWorkspaceAPIHost walks up from the working directory looking for a
// pulled workspace, and reports the host it was pulled from.
//
// The walk starts at the working directory rather than a command's --dir so
// that every command follows one rule: the host comes from where you are. A
// workspace pulled by an older CLI has no recorded host and is skipped, falling
// back to the saved default exactly as before.
func discoverWorkspaceAPIHost() (string, string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", false
	}

	root, state, hasState, err := findWorkspaceState(cwd, true)
	if err != nil || !hasState {
		return "", "", false
	}
	host := canonicalAPIHost(strings.TrimSpace(state.APIHost))
	if host == "" {
		return "", "", false
	}

	return host, root, true
}

// keepOrRecordAPIHost stamps a workspace with the host it syncs against the
// first time it is written, then leaves it alone.
//
// Recording it is what lets later commands in that directory drop the
// CREGHT_API_HOST prefix. Never rewriting it is what stops a one-off override
// from silently repointing the workspace at another deployment.
func keepOrRecordAPIHost(recorded string) string {
	if host := canonicalAPIHost(recorded); host != "" {
		return host
	}

	return currentAPIHost().Host
}
