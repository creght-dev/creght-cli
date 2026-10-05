package workspace

import (
	"context"
	"io"

	udiff "github.com/aymanbagabas/go-udiff"
)

// Plan is what push would do to the remote site, against the recorded base.
type Plan struct {
	root   string
	ctx    syncPlanContext
	remote map[string]SnapshotEntry
}

// Plan compares local files, the recorded base and the remote site.
// allowDelete plans remote deletions for files removed locally.
func (s *Syncer) Plan(ctx context.Context, allowDelete bool) (Plan, error) {
	planCtx, err := s.buildPlanContext(ctx, allowDelete)
	if err != nil {
		return Plan{}, err
	}
	return Plan{root: s.dir, ctx: planCtx, remote: s.currentRemoteFileSnapshot()}, nil
}

// HasConflicts reports whether push would refuse to run.
func (p Plan) HasConflicts() bool { return p.ctx.plan.hasConflicts() }

// Result is the plan in diff --json form.
func (p Plan) Result() DiffResult {
	return diffResult(p.ctx.plan, ConflictJSONDetails(p.root, p.ctx, p.remote))
}

// LocalChanges are the changes push would make.
func (p Plan) LocalChanges() []FileChange { return fileChanges(p.ctx.plan.FileActions) }

// Conflicts are the files push would refuse over.
func (p Plan) Conflicts() []PlanConflict { return p.ctx.plan.Conflicts }

// Print writes the plan as diff prints it.
func (p Plan) Print(out io.Writer) { PrintSyncPlan(out, p.ctx.plan, true) }

// DiffEntry / DiffResult are the machine-readable form of a sync plan,
// so an agent can decide how to resolve without parsing human text. Conflict
// entries carry the base->local and base->remote diffs plus whether a pull
// would auto-merge them, when the base content is recorded.
type DiffEntry struct {
	Path             string `json:"path"`
	Status           string `json:"status"`
	Action           string `json:"action,omitempty"`
	Reason           string `json:"reason,omitempty"`
	AutoMergeable    *bool  `json:"auto_mergeable,omitempty"`
	BaseToLocalDiff  string `json:"base_to_local_diff,omitempty"`
	BaseToRemoteDiff string `json:"base_to_remote_diff,omitempty"`
}

type DiffResult struct {
	HasConflicts bool        `json:"has_conflicts"`
	Files        []DiffEntry `json:"files"`
}

type conflictJSONDetail struct {
	reason           string
	autoMergeable    *bool
	baseToLocalDiff  string
	baseToRemoteDiff string
}

// conflictJSONDetails computes per-conflict detail for diff --json from the
// plan context and the current remote snapshot.
func ConflictJSONDetails(root string, planCtx syncPlanContext, remote map[string]SnapshotEntry) map[string]conflictJSONDetail {
	details := map[string]conflictJSONDetail{}
	for _, c := range planCtx.plan.Conflicts {
		detail := conflictJSONDetail{reason: c.Reason}
		base, baseOK := planCtx.state.Files[c.Path]
		local, localOK := planCtx.localFiles[c.Path]
		remoteEntry, remoteOK := remote[c.Path]
		if baseOK && localOK && remoteOK && !HasConflictMarkers(local.Body) {
			if baseText, ok := ReadBaseObject(root, base.Hash); ok {
				detail.baseToLocalDiff = UnifiedLineDiff("base:"+c.Path, "local:"+c.Path, baseText, local.Body)
				detail.baseToRemoteDiff = UnifiedLineDiff("base:"+c.Path, "remote:"+c.Path, baseText, remoteEntry.Body)
				_, clean := Merge3(baseText, local.Body, remoteEntry.Body)
				detail.autoMergeable = &clean
			}
		}
		details[c.Path] = detail
	}
	return details
}

func diffResult(plan syncPlan, conflictDetails map[string]conflictJSONDetail) DiffResult {
	out := DiffResult{HasConflicts: plan.hasConflicts()}
	for _, a := range plan.FileActions {
		out.Files = append(out.Files, DiffEntry{Path: a.RemotePath, Status: "local-change", Action: a.Action.Action})
	}
	for _, c := range plan.Conflicts {
		entry := DiffEntry{Path: c.Path, Status: "conflict", Reason: c.Reason}
		if detail, ok := conflictDetails[c.Path]; ok {
			entry.Reason = detail.reason
			entry.AutoMergeable = detail.autoMergeable
			entry.BaseToLocalDiff = detail.baseToLocalDiff
			entry.BaseToRemoteDiff = detail.baseToRemoteDiff
		}
		out.Files = append(out.Files, entry)
	}
	for _, p := range plan.RemoteOnlyUpdates {
		out.Files = append(out.Files, DiffEntry{Path: p, Status: "remote-only"})
	}
	for _, p := range plan.NoBaseRemoteDiffs {
		out.Files = append(out.Files, DiffEntry{Path: p, Status: "no-base"})
	}
	for _, p := range plan.IgnoredRemote {
		out.Files = append(out.Files, DiffEntry{Path: p, Status: "ignored-remote", Reason: "hidden by .creghtignore; still on the site, push cannot delete it, use creght rm"})
	}
	return out
}

// UnifiedLineDiff produces a git-style unified diff: only hunks around changed
// lines, with 3 lines of context and "@@ -a,b +c,d @@" headers, so a one-line
// change in a long file prints a few lines instead of the whole file. The diff
// itself comes from go-udiff, the diff implementation extracted from
// x/tools (gopls).
func UnifiedLineDiff(aName string, bName string, a string, b string) string {
	return udiff.Unified(aName, bName, a, b)
}
