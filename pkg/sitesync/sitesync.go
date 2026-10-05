// Package sitesync syncs Creght site code between a local directory and the
// platform: the pull, diff, push, resolve and version list of the creght CLI,
// as a library. The CLI's own commands run through this package, so the two
// never drift apart.
//
// A directory synced with this package is an ordinary creght workspace —
// .creght/state.json, the recorded base under .creght/base/, conflict markers
// (<<<<<<< local / ======= / >>>>>>> remote) and .creghtignore are exactly
// what the CLI reads and writes — so the CLI and this package can take turns
// on the same directory.
//
// The package never starts a process and never touches the CLI's config.json:
// the caller names the API host and supplies the token.
//
//	c, err := sitesync.New(sitesync.Options{
//		Host:  "https://creght.cn",
//		Token: func(ctx context.Context) (string, error) { return oauth.AccessToken(ctx) },
//		Log:   os.Stderr,
//	})
//	site, _ := sitesync.ParseSite("p9ok3myl0ne6/p9ok3mzxgpzm")
//	res, err := c.Pull(ctx, "./site", site, sitesync.PullOptions{})
//	diff, err := c.Diff(ctx, "./site", sitesync.DiffOptions{Delete: true})
//	pushed, err := c.Push(ctx, "./site", sitesync.PushOptions{Delete: true})
package sitesync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/creght-dev/creght-cli/internal/creght"
	"github.com/creght-dev/creght-cli/internal/workspace"
)

// ErrUnauthorized matches (with errors.Is) an error from a request the
// platform answered with 401: the token is missing, expired or revoked.
var ErrUnauthorized = creght.ErrUnauthorized

// Options configures a Client.
type Options struct {
	// Host is the API host of the Creght deployment, e.g. https://creght.cn.
	Host string
	// Token returns the access token to send. It is called before every
	// request, so a caller that refreshes its token can return the fresh one.
	// A nil Token sends no token, which only public reads accept.
	Token func(ctx context.Context) (string, error)
	// Log receives the progress and plan lines the CLI prints (merged files,
	// conflicts, "synced N files"). Nil discards them.
	Log io.Writer
}

// Client syncs workspaces against one Creght deployment.
type Client struct {
	host string
	api  *creght.Client
	log  io.Writer
}

// New returns a Client for opts.Host.
func New(opts Options) (*Client, error) {
	host := workspace.CanonicalAPIHost(opts.Host)
	u, err := url.Parse(host)
	if host == "" || err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("sitesync: invalid Host %q: want an absolute URL such as https://creght.cn", opts.Host)
	}
	token := opts.Token
	if token == nil {
		token = func(context.Context) (string, error) { return "", nil }
	}
	log := opts.Log
	if log == nil {
		log = io.Discard
	}

	return &Client{host: host, api: creght.NewClientWithTokenFunc(host, token), log: log}, nil
}

// Host is the canonical API host the client talks to.
func (c *Client) Host() string { return c.host }

// Site names one site of a project.
type Site struct {
	ProjectID string
	SiteID    string
}

// ParseSite parses the CLI's "<project_id>/<site_id>" form.
func ParseSite(ref string) (Site, error) {
	projectID, siteID, ok := strings.Cut(strings.TrimSpace(ref), "/")
	projectID, siteID = strings.TrimSpace(projectID), strings.TrimSpace(siteID)
	if !ok || projectID == "" || siteID == "" || strings.Contains(siteID, "/") {
		return Site{}, fmt.Errorf("invalid site %q; expected <project_id>/<site_id>", ref)
	}
	return Site{ProjectID: projectID, SiteID: siteID}, nil
}

// String is the "<project_id>/<site_id>" form.
func (s Site) String() string { return s.ProjectID + "/" + s.SiteID }

// Workspace is what .creght/state.json records about a directory.
type Workspace struct {
	Dir  string
	Site Site
	// APIHost is the deployment the workspace was pulled from; empty for
	// workspaces pulled by old CLI versions.
	APIHost string
	// SnapshotVersionNo is set when the directory holds a read-only version
	// snapshot (pulled with PullOptions.VersionNo) rather than a workspace.
	SnapshotVersionNo int64
}

// OpenWorkspace reads dir's .creght/state.json. dir must be the workspace root.
func OpenWorkspace(dir string) (Workspace, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return Workspace{}, fmt.Errorf("resolve dir: %w", err)
	}
	state, hasState, err := workspace.LoadWorkspaceState(root)
	if err != nil {
		return Workspace{}, err
	}
	if !hasState {
		return Workspace{}, fmt.Errorf("%s is not a creght workspace (missing .creght/state.json); pull the site into it first", root)
	}
	site, err := ParseSite(state.SiteID)
	if err != nil {
		return Workspace{}, fmt.Errorf("%s: .creght/state.json: %w", root, err)
	}
	ws := Workspace{Dir: root, Site: site, APIHost: state.APIHost}
	if state.Snapshot != nil {
		ws.SnapshotVersionNo = state.Snapshot.VersionNo
	}
	return ws, nil
}

// PullOptions are pull's flags.
type PullOptions struct {
	// VersionNo, when set, pulls that site version as a read-only snapshot
	// (pull --version_no): the directory is made to match the version
	// exactly, deleting everything else except .git and .creght at its root,
	// and push, diff and resolve refuse to run there. The directory must be
	// new, empty, or already a snapshot.
	VersionNo int64
	// Force overwrites local files with the remote workspace instead of
	// merging (pull --force); overwritten local work is backed up under
	// .creght/backup/. Ignored with VersionNo.
	Force bool
	// SkipAgentsFile leaves out the AGENTS.md a pull writes into a workspace
	// that has none. Ignored with VersionNo.
	SkipAgentsFile bool
}

// PullResult reports what Pull did.
type PullResult struct {
	Dir     string
	Changed int
	// Merged are files changed on both sides whose edits merged cleanly.
	Merged []string
	// Conflicted are files changed on both sides with overlapping edits; they
	// now hold conflict markers. Pull still succeeds — the CLI turns a
	// non-empty Conflicted into a failing exit — and push refuses until each
	// is edited or settled with Resolve.
	Conflicted []string
	// BackupDir is where overwritten local work was saved, if anything was.
	BackupDir         string
	AgentsFileCreated bool
	// Snapshot is set for a VersionNo pull.
	Snapshot *Snapshot
}

// Snapshot describes a version pulled with PullOptions.VersionNo.
type Snapshot struct {
	VersionNo int64
	// VersionID and Note are labels from the publish state; zero or empty
	// when it could not be read.
	VersionID int64
	Note      string
	// Files is how many files the version has; Removed is how many files the
	// directory held that the version does not, and were deleted.
	Files   int
	Removed int
}

// Pull brings the site into dir: a new directory, or a workspace of the same
// site, whose local edits are three-way merged with the remote ones against
// the recorded base (creght pull). With opts.VersionNo it pulls a read-only
// version snapshot instead.
func (c *Client) Pull(ctx context.Context, dir string, site Site, opts PullOptions) (PullResult, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return PullResult{}, fmt.Errorf("resolve dir: %w", err)
	}

	if opts.VersionNo != 0 {
		if opts.VersionNo < 0 {
			return PullResult{}, fmt.Errorf("invalid VersionNo %d; expected a positive version number", opts.VersionNo)
		}
		snap, err := workspace.PullVersionSnapshot(ctx, c.api, site.ProjectID, site.SiteID, root, c.host, opts.VersionNo)
		if err != nil {
			return PullResult{}, err
		}
		return PullResult{
			Dir:     snap.Dir,
			Changed: snap.Files,
			Snapshot: &Snapshot{
				VersionNo: snap.VersionNo,
				VersionID: snap.VersionID,
				Note:      snap.Note,
				Files:     snap.Files,
				Removed:   snap.Removed,
			},
		}, nil
	}

	res, err := workspace.Pull(ctx, c.api, site.ProjectID, site.SiteID, root, c.host, workspace.PullOptions{
		Force:          opts.Force,
		SkipAgentsFile: opts.SkipAgentsFile,
	}, c.log)
	if err != nil {
		return PullResult{}, err
	}
	return PullResult{
		Dir:               res.Dir,
		Changed:           res.Changed,
		Merged:            res.Merged,
		Conflicted:        res.Conflicted,
		BackupDir:         res.BackupDir,
		AgentsFileCreated: res.AgentsFileCreated,
	}, nil
}

// DiffOptions are diff's flags.
type DiffOptions struct {
	// Delete plans remote deletions for files removed locally (diff --delete).
	Delete bool
}

// DiffResult is the change plan, exactly as creght diff --json prints it.
type DiffResult = workspace.DiffResult

// DiffEntry is one file of a DiffResult. Status is one of
//
//	local-change    push would upload it; Action is file_create, file_update or file_delete
//	conflict        changed on both sides (or still holds conflict markers); push refuses
//	remote-only     changed only remotely; pull brings it in
//	no-base         differs from the remote with no recorded base
//	ignored-remote  hidden by .creghtignore but still on the site
type DiffEntry = workspace.DiffEntry

// Diff compares the workspace in dir with the remote site against the recorded
// base, without changing anything (creght diff --json). A conflict does not
// make it fail; check DiffResult.HasConflicts.
func (c *Client) Diff(ctx context.Context, dir string, opts DiffOptions) (DiffResult, error) {
	syncer, err := c.syncer(dir)
	if err != nil {
		return DiffResult{}, err
	}
	plan, err := syncer.Plan(ctx, opts.Delete)
	if err != nil {
		return DiffResult{}, err
	}
	return plan.Result(), nil
}

// PushOptions are push's flags.
type PushOptions struct {
	// Delete deletes remote files removed locally (push --delete).
	Delete bool
	// Force overwrites the remote site with the local workspace without the
	// three-way check (push --force). It uploads every differing file and
	// deletes every remote file missing locally whether or not Delete is set;
	// remote edits it overwrites are backed up under .creght/backup/.
	Force bool
	// SkipConflicts pushes everything else when some files conflict, leaving
	// those for a later pull (push --skip-conflicts).
	SkipConflicts bool
}

// FileChange is one change made to the remote site. Action is file_create,
// file_update or file_delete.
type FileChange = workspace.FileChange

// PushResult reports what Push changed.
type PushResult struct {
	Changes []FileChange
	// SkippedConflicts are the files SkipConflicts left out.
	SkippedConflicts []string
}

// Push uploads the workspace in dir (creght push). Without Force it refuses
// when a file changed both locally and remotely since the last pull — pull
// first to merge — unless SkipConflicts is set.
func (c *Client) Push(ctx context.Context, dir string, opts PushOptions) (PushResult, error) {
	syncer, err := c.syncer(dir)
	if err != nil {
		return PushResult{}, err
	}
	var report workspace.PushReport
	if opts.Force {
		report, err = syncer.Push(ctx)
	} else {
		report, err = syncer.PushSafe(ctx, opts.Delete, opts.SkipConflicts)
	}
	return PushResult{Changes: report.Changes, SkippedConflicts: report.SkippedConflicts}, err
}

func (c *Client) syncer(dir string) (*workspace.Syncer, error) {
	ws, err := OpenWorkspace(dir)
	if err != nil {
		return nil, err
	}
	return workspace.NewSyncer(c.api, ws.Site.ProjectID, ws.Site.SiteID, ws.Dir, c.host, c.log)
}

// Side picks which half of a conflict Resolve keeps.
type Side string

const (
	// Ours keeps the local side (creght resolve --ours).
	Ours Side = "ours"
	// Theirs keeps the remote side (creght resolve --theirs).
	Theirs Side = "theirs"
)

// Conflicts lists the files of the workspace in dir that still hold conflict
// markers, as site paths such as /page/Index.tsx (creght resolve --list).
func Conflicts(dir string) ([]string, error) {
	ws, err := OpenWorkspace(dir)
	if err != nil {
		return nil, err
	}
	return workspace.ConflictedFiles(ws.Dir)
}

// Resolve settles every conflict block in one file of the workspace in dir by
// keeping one side, and returns how many blocks it resolved (creght resolve).
// path is the site path, with or without the leading slash
// ("page/Index.tsx" or "/page/Index.tsx"). Purely local; push uploads it.
func Resolve(dir string, path string, side Side) (int, error) {
	if side != Ours && side != Theirs {
		return 0, fmt.Errorf("invalid side %q; want sitesync.Ours or sitesync.Theirs", side)
	}
	ws, err := OpenWorkspace(dir)
	if err != nil {
		return 0, err
	}
	remotePath := workspace.NormalizeSitePath(strings.TrimSpace(path))
	if remotePath == "/" || strings.HasPrefix(remotePath, "/..") {
		return 0, fmt.Errorf("invalid path %q", path)
	}
	return workspace.ResolveFile(ws.Dir, remotePath, side == Ours)
}

// PublishState is a site's versions and publish targets, exactly as
// creght version list --json prints it. For a reader of a public-copy project
// that is not a member, ReadOnly is set and only the versions and the live
// version are filled.
type PublishState = creght.SitePublishState

// Version is one entry of PublishState.Versions.
type Version = creght.SiteVersion

// Versions returns the site's versions (creght version list).
func (c *Client) Versions(ctx context.Context, site Site) (PublishState, error) {
	return c.api.GetSitePublishState(ctx, site.ProjectID, site.SiteID)
}

// IsUnauthorized reports whether err came from a 401.
func IsUnauthorized(err error) bool { return errors.Is(err, ErrUnauthorized) }
