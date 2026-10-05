package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/creght-dev/creght-cli/internal/creght"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Syncer struct {
	client    *creght.Client
	projectID string
	siteID    string
	dir       string
	clientID  string
	// apiHost is stamped on the workspace state the first time it is written.
	apiHost string
	// out receives the progress and plan lines a command would print.
	out io.Writer
	// ignoredRemote records the remote paths refreshRemote dropped because
	// .creghtignore matched them, so push and diff can name what is still on
	// the site instead of hiding it.
	ignoredRemote []string
	ignore        *creghtIgnore

	mu           sync.Mutex
	remoteByPath map[string]creght.File
}

type localFileAction struct {
	RemotePath string
	Action     creght.SiteActionChange
}

type syncPlanContext struct {
	plan       syncPlan
	state      WorkspaceState
	hasState   bool
	localFiles map[string]SnapshotEntry
}

func NewSyncer(client *creght.Client, projectID string, siteID string, dir string, apiHost string, out io.Writer) (*Syncer, error) {
	if out == nil {
		out = io.Discard
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve sync dir: %w", err)
	}
	ignore, err := LoadCreghtIgnore(absDir)
	if err != nil {
		return nil, err
	}

	return &Syncer{
		client:       client,
		projectID:    projectID,
		siteID:       siteID,
		dir:          absDir,
		clientID:     NewClientID(),
		apiHost:      apiHost,
		out:          out,
		ignore:       ignore,
		remoteByPath: map[string]creght.File{},
	}, nil
}

func (s *Syncer) ensureIgnore() error {
	if s.ignore != nil {
		return nil
	}
	ignore, err := LoadCreghtIgnore(s.dir)
	if err != nil {
		return err
	}
	s.ignore = ignore
	return nil
}

func NewClientID() string {
	var b [16]byte
	_, err := rand.Read(b[:])
	if err != nil {
		return fmt.Sprintf("creght-cli-%d", time.Now().UnixNano())
	}

	return "creght-cli-" + hex.EncodeToString(b[:])
}

// requireWorkspace 校验 s.dir 是一个已 pull 的、属于目标站点的工作区。
// push/sync/diff 不允许把任意目录隐式当作工作区（曾发生误从无关目录 push 导致整棵仓库被上传）。
func (s *Syncer) requireWorkspace() error {
	state, hasState, err := LoadWorkspaceState(s.dir)
	if err != nil {
		return err
	}
	if !hasState {
		return fmt.Errorf(
			"%s is not a creght workspace (missing .creght/state.json); run `creght pull --site_id=%s --dir=%s` first, or pass --dir pointing at a pulled workspace",
			s.dir, s.siteRef(), s.dir,
		)
	}
	if strings.TrimSpace(state.SiteID) != "" && state.SiteID != s.siteRef() {
		return fmt.Errorf("workspace state belongs to %s, not %s", state.SiteID, s.siteRef())
	}
	return RefuseSnapshotWorkspace(s.dir, "syncing")
}

// FileChange is one change push made to the remote site.
type FileChange struct {
	Path string `json:"path"`
	// Action is file_create, file_update or file_delete.
	Action string `json:"action"`
}

// PushReport is what a push did.
type PushReport struct {
	Changes []FileChange
	// SkippedConflicts are files PushSafe's skipConflicts left out; they keep
	// their base so a later pull can still merge them.
	SkippedConflicts []string
}

func fileChanges(actions []localFileAction) []FileChange {
	changes := make([]FileChange, 0, len(actions))
	for _, a := range actions {
		changes = append(changes, FileChange{Path: a.RemotePath, Action: a.Action.Action})
	}
	return changes
}

// Push overwrites the remote site with the local workspace (push --force):
// every local file that differs is uploaded and every remote file missing
// locally is deleted, without a three-way check.
func (s *Syncer) Push(ctx context.Context) (PushReport, error) {
	if err := s.requireWorkspace(); err != nil {
		return PushReport{}, err
	}

	err := os.MkdirAll(s.dir, 0o755)
	if err != nil {
		return PushReport{}, fmt.Errorf("create local dir: %w", err)
	}

	err = s.refreshRemote(ctx)
	if err != nil {
		return PushReport{}, err
	}

	actions, err := s.syncLocalSnapshot(ctx)
	if err != nil {
		return PushReport{}, err
	}

	return PushReport{Changes: fileChanges(actions)}, s.saveCurrentState()
}

// PushSafe uploads local changes after a three-way comparison. Conflicted
// files abort the push unless skipConflicts is set, in which case everything
// else is pushed and the conflicted files keep their base state so a later
// pull can still merge them.
func (s *Syncer) PushSafe(ctx context.Context, allowDelete bool, skipConflicts bool) (PushReport, error) {
	planCtx, err := s.buildPlanContext(ctx, allowDelete)
	if err != nil {
		return PushReport{}, err
	}
	plan := planCtx.plan
	if plan.hasConflicts() && !skipConflicts {
		PrintSyncPlan(s.out, plan, false)
		return PushReport{}, fmt.Errorf("push has conflicts; run creght pull to merge remote changes (then resolve if needed), use --skip-conflicts to push the rest, or --force to overwrite remote changes")
	}
	report := PushReport{}
	for _, c := range plan.Conflicts {
		report.SkippedConflicts = append(report.SkippedConflicts, c.Path)
	}
	if !plan.hasChanges() {
		PrintSyncPlan(s.out, plan, false)
		if err := s.saveMergedState(planCtx); err != nil {
			return PushReport{}, err
		}
		return report, nil
	}
	if err := s.applyPlan(ctx, plan); err != nil {
		return PushReport{}, err
	}
	report.Changes = fileChanges(plan.FileActions)
	if err := s.refreshRemote(ctx); err != nil {
		return report, err
	}
	if err := s.saveMergedState(planCtx); err != nil {
		return report, err
	}
	PrintSyncPlan(s.out, plan, false)
	if plan.hasConflicts() {
		fmt.Fprintf(s.out, "synced %d files, skipped %d conflicted file(s)\n", len(plan.FileActions), len(plan.Conflicts))
	} else {
		fmt.Fprintf(s.out, "synced %d files\n", len(plan.FileActions))
	}
	return report, nil
}

func (s *Syncer) buildPlanContext(ctx context.Context, allowDelete bool) (syncPlanContext, error) {
	err := os.MkdirAll(s.dir, 0o755)
	if err != nil {
		return syncPlanContext{}, fmt.Errorf("create local dir: %w", err)
	}

	state, hasState, err := LoadWorkspaceState(s.dir)
	if err != nil {
		return syncPlanContext{}, err
	}
	if !hasState {
		// 不允许把任意目录隐式当作工作区：曾发生误从无关目录 push 导致整棵仓库被上传。
		return syncPlanContext{}, fmt.Errorf(
			"%s is not a creght workspace (missing .creght/state.json); run `creght pull --site_id=%s --dir=%s` first, or pass --dir pointing at a pulled workspace",
			s.dir, s.siteRef(), s.dir,
		)
	}
	if strings.TrimSpace(state.SiteID) != "" && state.SiteID != s.siteRef() {
		return syncPlanContext{}, fmt.Errorf("workspace state belongs to %s, not %s", state.SiteID, s.siteRef())
	}
	if err := RefuseSnapshotWorkspace(s.dir, "syncing"); err != nil {
		return syncPlanContext{}, err
	}
	if err := s.ensureIgnore(); err != nil {
		return syncPlanContext{}, err
	}
	state.Files = FilterIgnoredState(s.ignore, state.Files)

	if err := s.refreshRemote(ctx); err != nil {
		return syncPlanContext{}, err
	}

	localFiles, err := LocalFileSnapshot(s.dir)
	if err != nil {
		return syncPlanContext{}, err
	}

	remoteFiles := s.currentRemoteFileSnapshot()
	plan := BuildSyncPlan(state, hasState, localFiles, remoteFiles, allowDelete)
	plan.IgnoredRemote = s.currentIgnoredRemote()
	return syncPlanContext{
		plan:       plan,
		state:      state,
		hasState:   hasState,
		localFiles: localFiles,
	}, nil
}

func (s *Syncer) applyPlan(ctx context.Context, plan syncPlan) error {
	if len(plan.FileActions) > 0 {
		changes := make([]creght.SiteActionChange, 0, len(plan.FileActions))
		for _, action := range plan.FileActions {
			changes = append(changes, action.Action)
		}
		if _, err := s.client.DoSiteAction(ctx, s.projectID, s.siteID, s.clientID, changes); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) refreshRemote(ctx context.Context) error {
	if err := s.ensureIgnore(); err != nil {
		return err
	}
	files, err := s.client.GetFileList(ctx, s.projectID, s.siteID)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.remoteByPath = make(map[string]creght.File, len(files.List))
	for _, file := range files.List {
		if file.IsDir || s.ignore.Matches(file.Path) {
			continue
		}
		s.remoteByPath[file.Path] = file
	}
	s.ignoredRemote = IgnoredRemotePaths(s.ignore, files.List)

	return nil
}

func (s *Syncer) syncLocalSnapshot(ctx context.Context) ([]localFileAction, error) {
	actions, err := s.collectLocalSnapshotActions()
	if err != nil {
		return nil, err
	}

	if len(actions) == 0 {
		fmt.Fprintln(s.out, "No local changes to push")
		return nil, nil
	}

	backupDir, err := s.backupDivergedRemoteFiles(actions)
	if err != nil {
		return nil, err
	}
	if backupDir != "" {
		fmt.Fprintf(s.out, "Backed up overwritten remote files to %s\n", backupDir)
	}

	changes := make([]creght.SiteActionChange, 0, len(actions))
	for _, action := range actions {
		changes = append(changes, action.Action)
	}

	_, err = s.client.DoSiteAction(ctx, s.projectID, s.siteID, s.clientID, changes)
	if err != nil {
		return nil, err
	}

	err = s.refreshRemote(ctx)
	if err != nil {
		return actions, err
	}

	fmt.Fprintf(s.out, "synced %d files\n", len(actions))
	return actions, nil
}

// backupDivergedRemoteFiles saves remote copies that the given actions will
// overwrite or delete when the remote content diverged from the recorded base
// — i.e. remote edits that exist nowhere locally and would otherwise be lost.
func (s *Syncer) backupDivergedRemoteFiles(actions []localFileAction) (string, error) {
	state, hasState, err := LoadWorkspaceState(s.dir)
	if err != nil {
		return "", err
	}

	toBackup := map[string]string{}
	s.mu.Lock()
	for _, action := range actions {
		remote, ok := s.remoteByPath[action.RemotePath]
		if !ok {
			continue
		}
		hash := strings.TrimSpace(remote.Hash)
		if hash == "" {
			hash, _ = QetagHash([]byte(remote.Body))
		}
		if hasState {
			if base, ok := state.Files[action.RemotePath]; ok && base.Hash == hash {
				continue
			}
		}
		toBackup[action.RemotePath] = remote.Body
	}
	s.mu.Unlock()

	if len(toBackup) == 0 {
		return "", nil
	}
	return WriteBackupFiles(s.dir, "remote", toBackup)
}

// Dir is the workspace root, made absolute.
func (s *Syncer) Dir() string { return s.dir }

func (s *Syncer) siteRef() string {
	return s.projectID + "/" + s.siteID
}

func (s *Syncer) currentRemoteFileSnapshot() map[string]SnapshotEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	files := make([]creght.File, 0, len(s.remoteByPath))
	for _, file := range s.remoteByPath {
		files = append(files, file)
	}
	return RemoteFileSnapshot(files)
}

func (s *Syncer) currentIgnoredRemote() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.ignoredRemote...)
}

func (s *Syncer) saveCurrentState() error {
	return SaveWorkspaceState(s.dir, s.siteRef(), s.apiHost, s.currentRemoteFileSnapshot())
}

func (s *Syncer) saveMergedState(planCtx syncPlanContext) error {
	return SaveWorkspaceState(
		s.dir,
		s.siteRef(),
		s.apiHost,
		MergeStateSnapshot(planCtx.state.Files, planCtx.hasState, planCtx.localFiles, s.currentRemoteFileSnapshot()),
	)
}

func (s *Syncer) saveLocalBaseState() error {
	state, hasState, err := LoadWorkspaceState(s.dir)
	if err != nil {
		return err
	}
	localFiles, err := LocalFileSnapshot(s.dir)
	if err != nil {
		return err
	}
	return SaveWorkspaceState(
		s.dir,
		s.siteRef(),
		s.apiHost,
		MergeStateSnapshot(state.Files, hasState, localFiles, s.currentRemoteFileSnapshot()),
	)
}

func (s *Syncer) collectLocalSnapshotActions() ([]localFileAction, error) {
	if err := s.ensureIgnore(); err != nil {
		return nil, err
	}
	var actions []localFileAction
	localPaths := map[string]struct{}{}
	err := WalkWorkspaceFiles(s.dir, func(path string) error {
		remotePath, err := LocalPathToRemote(s.dir, path)
		if err != nil {
			return err
		}
		localPaths[remotePath] = struct{}{}

		action, changed, err := s.localFileAction(path)
		if err != nil {
			return err
		}
		if changed {
			actions = append(actions, action)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	for remotePath, remote := range s.remoteByPath {
		if remote.Readonly || s.ignore.Matches(remotePath) {
			continue
		}
		if _, existsLocally := localPaths[remotePath]; existsLocally {
			continue
		}
		actions = append(actions, DeleteFileAction(remotePath))
	}
	s.mu.Unlock()

	return actions, nil
}

func (s *Syncer) localFileAction(localPath string) (localFileAction, bool, error) {
	remotePath, err := LocalPathToRemote(s.dir, localPath)
	if err != nil {
		return localFileAction{}, false, err
	}

	bodyBytes, err := os.ReadFile(localPath)
	if err != nil {
		return localFileAction{}, false, fmt.Errorf("read %s: %w", remotePath, err)
	}
	if !IsUTF8FileBody(bodyBytes) {
		return localFileAction{}, false, nil
	}
	hash, err := QetagHash(bodyBytes)
	if err != nil {
		return localFileAction{}, false, err
	}
	body := string(bodyBytes)

	s.mu.Lock()
	remote, exist := s.remoteByPath[remotePath]
	s.mu.Unlock()

	if exist && remote.Readonly {
		return localFileAction{}, false, nil
	}
	if exist && remote.Hash != "" && remote.Hash == hash {
		return localFileAction{}, false, nil
	}

	action := creght.SiteActionChange{
		Action: "file_create",
		File: creght.SiteActionFileSpec{
			Path: creght.StringPtr(remotePath),
			Body: creght.StringPtr(body),
		},
	}
	if exist {
		action.Action = "file_update"
		action.File = creght.SiteActionFileSpec{
			ID:   remote.ID,
			Body: creght.StringPtr(body),
		}
	}

	return localFileAction{RemotePath: remotePath, Action: action}, true, nil
}

func PrintSyncPlan(out io.Writer, plan syncPlan, dryRun bool) {
	prefix := ""
	if dryRun {
		prefix = "would "
	}
	for _, action := range plan.FileActions {
		fmt.Fprintf(out, "%s%s %s\n", prefix, SiteActionLabel(action.Action.Action), action.RemotePath)
	}
	for _, path := range plan.SkippedDeletes {
		fmt.Fprintf(out, "skip delete %s (use --delete to delete remote files removed locally)\n", path)
	}
	for _, path := range plan.RemoteOnlyUpdates {
		fmt.Fprintf(out, "keep remote update %s (local copy is unchanged from last pull)\n", path)
	}
	for _, conflict := range plan.Conflicts {
		fmt.Fprintf(out, "conflict %s %s: %s\n", conflict.Kind, conflict.Path, conflict.Reason)
	}
	printIgnoredRemote(out, plan.IgnoredRemote, dryRun)
	if !plan.hasChanges() && len(plan.SkippedDeletes) == 0 && len(plan.RemoteOnlyUpdates) == 0 && len(plan.Conflicts) == 0 {
		if dryRun {
			fmt.Fprintln(out, "No local changes")
		} else {
			fmt.Fprintln(out, "No local changes to push")
		}
	}
}

func SiteActionLabel(action string) string {
	switch action {
	case "file_create":
		return "create"
	case "file_update":
		return "update"
	case "file_delete":
		return "delete"
	default:
		return action
	}
}

// printIgnoredRemote names the remote files .creghtignore is hiding. Without
// it the user reads "not synced" as "not on the site", and only finds out
// otherwise by loading the page.
//
// detailed is set by diff, the command you run to inspect the situation: it
// lists the paths and how to delete one. push gets a single factual line —
// ignoring a remote path can be deliberate, and a scolding paragraph on every
// push would train the user to skip the whole summary.
func printIgnoredRemote(out io.Writer, paths []string, detailed bool) {
	if len(paths) == 0 {
		return
	}
	if !detailed {
		fmt.Fprintf(out, "ignored %d remote file(s) matched by .creghtignore, still on the site (creght diff lists them)\n", len(paths))
		return
	}
	const show = 5
	listed := paths
	suffix := ""
	if len(listed) > show {
		listed = listed[:show]
		suffix = fmt.Sprintf(", and %d more", len(paths)-show)
	}
	fmt.Fprintf(out,
		"ignored %d remote file(s) matched by .creghtignore, still live on the site and out of push's reach: %s%s\n",
		len(paths), strings.Join(listed, ", "), suffix,
	)
	fmt.Fprintf(out, "  delete one with: creght rm <path>\n")
}

func DeleteFileAction(remotePath string) localFileAction {
	return localFileAction{
		RemotePath: remotePath,
		Action: creght.SiteActionChange{
			Action: "file_delete",
			File: creght.SiteActionFileSpec{
				Path: creght.StringPtr(remotePath),
			},
		},
	}
}
