package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
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

	mu           sync.Mutex
	remoteByPath map[string]creght.File
}

type localFileAction struct {
	remotePath string
	action     creght.SiteActionChange
}

type syncPlanContext struct {
	plan       syncPlan
	state      workspaceState
	hasState   bool
	localFiles map[string]snapshotEntry
}

func NewSyncer(client *creght.Client, projectID string, siteID string, dir string) (*Syncer, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve sync dir: %w", err)
	}

	return &Syncer{
		client:       client,
		projectID:    projectID,
		siteID:       siteID,
		dir:          absDir,
		clientID:     newClientID(),
		remoteByPath: map[string]creght.File{},
	}, nil
}

func newClientID() string {
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
	state, hasState, err := loadWorkspaceState(s.dir)
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
	return nil
}

func (s *Syncer) Push(ctx context.Context) error {
	if err := s.requireWorkspace(); err != nil {
		return err
	}

	err := os.MkdirAll(s.dir, 0o755)
	if err != nil {
		return fmt.Errorf("create local dir: %w", err)
	}

	err = s.refreshRemote(ctx)
	if err != nil {
		return err
	}

	if err := s.syncLocalSnapshot(ctx); err != nil {
		return err
	}

	return s.saveCurrentState()
}

// PushSafe uploads local changes after a three-way comparison. Conflicted
// files abort the push unless skipConflicts is set, in which case everything
// else is pushed and the conflicted files keep their base state so a later
// pull can still merge them.
func (s *Syncer) PushSafe(ctx context.Context, allowDelete bool, skipConflicts bool) error {
	planCtx, err := s.buildPlanContext(ctx, allowDelete)
	if err != nil {
		return err
	}
	plan := planCtx.plan
	if plan.hasConflicts() && !skipConflicts {
		printSyncPlan(plan, false)
		return fmt.Errorf("push has conflicts; run creght pull to merge remote changes (then resolve if needed), use --skip-conflicts to push the rest, or --force to overwrite remote changes")
	}
	if !plan.hasChanges() {
		printSyncPlan(plan, false)
		if err := s.saveMergedState(planCtx); err != nil {
			return err
		}
		return nil
	}
	if err := s.applyPlan(ctx, plan); err != nil {
		return err
	}
	if err := s.refreshRemote(ctx); err != nil {
		return err
	}
	if err := s.saveMergedState(planCtx); err != nil {
		return err
	}
	printSyncPlan(plan, false)
	if plan.hasConflicts() {
		fmt.Printf("synced %d files, skipped %d conflicted file(s)\n", len(plan.FileActions), len(plan.Conflicts))
	} else {
		fmt.Printf("synced %d files\n", len(plan.FileActions))
	}
	return nil
}

func (s *Syncer) buildPlanContext(ctx context.Context, allowDelete bool) (syncPlanContext, error) {
	err := os.MkdirAll(s.dir, 0o755)
	if err != nil {
		return syncPlanContext{}, fmt.Errorf("create local dir: %w", err)
	}

	state, hasState, err := loadWorkspaceState(s.dir)
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

	if err := s.refreshRemote(ctx); err != nil {
		return syncPlanContext{}, err
	}

	localFiles, err := localFileSnapshot(s.dir)
	if err != nil {
		return syncPlanContext{}, err
	}

	plan := buildSyncPlan(state, hasState, localFiles, s.currentRemoteFileSnapshot(), allowDelete)
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
			changes = append(changes, action.action)
		}
		if _, err := s.client.DoSiteAction(ctx, s.projectID, s.siteID, s.clientID, changes); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) refreshRemote(ctx context.Context) error {
	files, err := s.client.GetFileList(ctx, s.projectID, s.siteID)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.remoteByPath = make(map[string]creght.File, len(files.List))
	for _, file := range files.List {
		if file.IsDir {
			continue
		}
		s.remoteByPath[file.Path] = file
	}

	return nil
}

func (s *Syncer) syncLocalSnapshot(ctx context.Context) error {
	actions, err := s.collectLocalSnapshotActions()
	if err != nil {
		return err
	}

	if len(actions) == 0 {
		fmt.Println("No local changes to push")
		return nil
	}

	backupDir, err := s.backupDivergedRemoteFiles(actions)
	if err != nil {
		return err
	}
	if backupDir != "" {
		fmt.Printf("Backed up overwritten remote files to %s\n", backupDir)
	}

	changes := make([]creght.SiteActionChange, 0, len(actions))
	for _, action := range actions {
		changes = append(changes, action.action)
	}

	_, err = s.client.DoSiteAction(ctx, s.projectID, s.siteID, s.clientID, changes)
	if err != nil {
		return err
	}

	err = s.refreshRemote(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("synced %d files\n", len(actions))
	return nil
}

// backupDivergedRemoteFiles saves remote copies that the given actions will
// overwrite or delete when the remote content diverged from the recorded base
// — i.e. remote edits that exist nowhere locally and would otherwise be lost.
func (s *Syncer) backupDivergedRemoteFiles(actions []localFileAction) (string, error) {
	state, hasState, err := loadWorkspaceState(s.dir)
	if err != nil {
		return "", err
	}

	toBackup := map[string]string{}
	s.mu.Lock()
	for _, action := range actions {
		remote, ok := s.remoteByPath[action.remotePath]
		if !ok {
			continue
		}
		hash := strings.TrimSpace(remote.Hash)
		if hash == "" {
			hash, _ = qetagHash([]byte(remote.Body))
		}
		if hasState {
			if base, ok := state.Files[action.remotePath]; ok && base.Hash == hash {
				continue
			}
		}
		toBackup[action.remotePath] = remote.Body
	}
	s.mu.Unlock()

	if len(toBackup) == 0 {
		return "", nil
	}
	return writeBackupFiles(s.dir, "remote", toBackup)
}

func (s *Syncer) siteRef() string {
	return s.projectID + "/" + s.siteID
}

func (s *Syncer) currentRemoteFileSnapshot() map[string]snapshotEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	files := make([]creght.File, 0, len(s.remoteByPath))
	for _, file := range s.remoteByPath {
		files = append(files, file)
	}
	return remoteFileSnapshot(files)
}

func (s *Syncer) saveCurrentState() error {
	return saveWorkspaceState(s.dir, s.siteRef(), s.currentRemoteFileSnapshot())
}

func (s *Syncer) saveMergedState(planCtx syncPlanContext) error {
	return saveWorkspaceState(
		s.dir,
		s.siteRef(),
		mergeStateSnapshot(planCtx.state.Files, planCtx.hasState, planCtx.localFiles, s.currentRemoteFileSnapshot()),
	)
}

func (s *Syncer) saveLocalBaseState() error {
	state, hasState, err := loadWorkspaceState(s.dir)
	if err != nil {
		return err
	}
	localFiles, err := localFileSnapshot(s.dir)
	if err != nil {
		return err
	}
	return saveWorkspaceState(
		s.dir,
		s.siteRef(),
		mergeStateSnapshot(state.Files, hasState, localFiles, s.currentRemoteFileSnapshot()),
	)
}

func (s *Syncer) collectLocalSnapshotActions() ([]localFileAction, error) {
	var actions []localFileAction
	localPaths := map[string]struct{}{}
	err := walkWorkspaceFiles(s.dir, func(path string) error {
		remotePath, err := localPathToRemote(s.dir, path)
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
		if remote.Readonly {
			continue
		}
		if _, existsLocally := localPaths[remotePath]; existsLocally {
			continue
		}
		actions = append(actions, deleteFileAction(remotePath))
	}
	s.mu.Unlock()

	return actions, nil
}

func (s *Syncer) localFileAction(localPath string) (localFileAction, bool, error) {
	remotePath, err := localPathToRemote(s.dir, localPath)
	if err != nil {
		return localFileAction{}, false, err
	}

	bodyBytes, err := os.ReadFile(localPath)
	if err != nil {
		return localFileAction{}, false, fmt.Errorf("read %s: %w", remotePath, err)
	}
	if !isUTF8FileBody(bodyBytes) {
		return localFileAction{}, false, nil
	}
	hash, err := qetagHash(bodyBytes)
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

	return localFileAction{remotePath: remotePath, action: action}, true, nil
}

func printSyncPlan(plan syncPlan, dryRun bool) {
	prefix := ""
	if dryRun {
		prefix = "would "
	}
	for _, action := range plan.FileActions {
		fmt.Printf("%s%s %s\n", prefix, siteActionLabel(action.action.Action), action.remotePath)
	}
	for _, path := range plan.SkippedDeletes {
		fmt.Printf("skip delete %s (use --delete to delete remote files removed locally)\n", path)
	}
	for _, path := range plan.RemoteOnlyUpdates {
		fmt.Printf("keep remote update %s (local copy is unchanged from last pull)\n", path)
	}
	for _, conflict := range plan.Conflicts {
		fmt.Printf("conflict %s %s: %s\n", conflict.Kind, conflict.Path, conflict.Reason)
	}
	if !plan.hasChanges() && len(plan.SkippedDeletes) == 0 && len(plan.RemoteOnlyUpdates) == 0 && len(plan.Conflicts) == 0 {
		if dryRun {
			fmt.Println("No local changes")
		} else {
			fmt.Println("No local changes to push")
		}
	}
}

func siteActionLabel(action string) string {
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

func deleteFileAction(remotePath string) localFileAction {
	return localFileAction{
		remotePath: remotePath,
		action: creght.SiteActionChange{
			Action: "file_delete",
			File: creght.SiteActionFileSpec{
				Path: creght.StringPtr(remotePath),
			},
		},
	}
}
