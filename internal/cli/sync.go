package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Syncer struct {
	client    *creght.Client
	projectID string
	siteID    string
	dir       string
	clientID  string

	mu              sync.Mutex
	remoteByPath    map[string]creght.File
	funcMu          sync.Mutex
	remoteFuncByKey map[string]creght.ProjectFunc
}

type localFileAction struct {
	remotePath string
	action     creght.SiteActionChange
}

type localFuncAction struct {
	key    string
	action string
	fn     creght.ProjectFunc
}

func NewSyncer(client *creght.Client, projectID string, siteID string, dir string) (*Syncer, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve sync dir: %w", err)
	}

	return &Syncer{
		client:          client,
		projectID:       projectID,
		siteID:          siteID,
		dir:             absDir,
		clientID:        newClientID(),
		remoteByPath:    map[string]creght.File{},
		remoteFuncByKey: map[string]creght.ProjectFunc{},
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

func (s *Syncer) Run(ctx context.Context) error {
	if err := s.Push(ctx); err != nil {
		return err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}
	defer watcher.Close()

	err = s.watchDirs(watcher)
	if err != nil {
		return err
	}

	fmt.Printf("Syncing %s -> %s/%s\n", s.dir, s.projectID, s.siteID)

	debounce := map[string]*time.Timer{}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-watcher.Errors:
			if err != nil {
				fmt.Printf("watch error: %v\n", err)
			}
		case event := <-watcher.Events:
			if shouldSkipLocalPath(s.dir, event.Name) {
				continue
			}
			if event.Op&fsnotify.Create != 0 {
				info, statErr := os.Stat(event.Name)
				if statErr == nil && info.IsDir() {
					_ = filepath.WalkDir(event.Name, func(path string, d os.DirEntry, err error) error {
						if err != nil || !d.IsDir() {
							return nil
						}
						if shouldSkipLocalPath(s.dir, path) {
							return filepath.SkipDir
						}
						return watcher.Add(path)
					})
				}
			}

			key := event.Name
			if timer, ok := debounce[key]; ok {
				timer.Stop()
			}
			debounce[key] = time.AfterFunc(400*time.Millisecond, func() {
				delete(debounce, key)
				if err := s.handleEvent(context.Background(), event); err != nil {
					fmt.Printf("sync %s: %v\n", event.Name, err)
				}
			})
		}
	}
}

func (s *Syncer) Push(ctx context.Context) error {
	err := os.MkdirAll(s.dir, 0o755)
	if err != nil {
		return fmt.Errorf("create local dir: %w", err)
	}

	err = s.refreshRemote(ctx)
	if err != nil {
		return err
	}
	if s.shouldSyncFuncs() {
		if err := s.refreshRemoteFuncs(ctx); err != nil {
			return err
		}
	}

	return s.syncLocalSnapshot(ctx)
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

func (s *Syncer) refreshRemoteFuncs(ctx context.Context) error {
	funcs, err := s.client.GetProjectFuncList(ctx, s.projectID, url.Values{"limit": []string{"-1"}})
	if err != nil {
		return err
	}

	s.funcMu.Lock()
	defer s.funcMu.Unlock()

	s.remoteFuncByKey = make(map[string]creght.ProjectFunc, len(funcs.List))
	for _, fn := range funcs.List {
		if strings.TrimSpace(fn.Body) == "" && strings.TrimSpace(fn.ID) != "" {
			detail, err := s.client.GetProjectFunc(ctx, s.projectID, fn.ID)
			if err != nil {
				return err
			}
			fn = detail
		}
		key := normalizeLocalFuncKey(fn.Key)
		if key == "" {
			continue
		}
		s.remoteFuncByKey[key] = fn
	}
	return nil
}

func (s *Syncer) syncLocalSnapshot(ctx context.Context) error {
	actions, err := s.collectLocalSnapshotActions()
	if err != nil {
		return err
	}
	funcActions, err := s.collectLocalFuncActions()
	if err != nil {
		return err
	}

	if len(actions) > 0 {
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
	}

	if len(funcActions) > 0 {
		if err := s.applyFuncActions(ctx, funcActions); err != nil {
			return err
		}
		if err := s.refreshRemoteFuncs(ctx); err != nil {
			return err
		}
	}

	if len(actions) == 0 && len(funcActions) == 0 {
		fmt.Println("No local changes to push")
		return nil
	}

	fmt.Printf("synced %d frontend files and %d funcs\n", len(actions), len(funcActions))
	return nil
}

func (s *Syncer) collectLocalSnapshotActions() ([]localFileAction, error) {
	var actions []localFileAction
	localPaths := map[string]struct{}{}
	siteRoot := siteSyncRoot(s.dir)
	err := filepath.WalkDir(siteRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if shouldSkipLocalPath(siteRoot, path) || (!hasFrontendLayout(s.dir) && isWorkspaceBackendPath(s.dir, path)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

		remotePath, err := localPathToRemote(siteRoot, path)
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

func (s *Syncer) shouldSyncFuncs() bool {
	info, err := os.Stat(funcRoot(s.dir))
	return err == nil && info.IsDir()
}

func (s *Syncer) collectLocalFuncActions() ([]localFuncAction, error) {
	if !s.shouldSyncFuncs() {
		return nil, nil
	}

	var actions []localFuncAction
	localKeys := map[string]struct{}{}
	err := filepath.WalkDir(funcRoot(s.dir), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if shouldSkipLocalPath(funcRoot(s.dir), path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

		key, err := localPathToFuncKey(s.dir, path)
		if err != nil {
			return err
		}
		localKeys[key] = struct{}{}

		action, changed, err := s.localFuncAction(path)
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

	s.funcMu.Lock()
	for key, fn := range s.remoteFuncByKey {
		if _, existsLocally := localKeys[key]; existsLocally {
			continue
		}
		actions = append(actions, localFuncAction{key: key, action: "delete", fn: fn})
	}
	s.funcMu.Unlock()

	return actions, nil
}

func (s *Syncer) watchDirs(watcher *fsnotify.Watcher) error {
	return filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if shouldSkipLocalPath(s.dir, path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}

		return watcher.Add(path)
	})
}

func (s *Syncer) handleEvent(ctx context.Context, event fsnotify.Event) error {
	if s.shouldSyncFuncs() && isWorkspaceFuncPath(s.dir, event.Name) {
		return s.handleFuncEvent(ctx, event)
	}
	if hasFrontendLayout(s.dir) && !isPathInside(siteSyncRoot(s.dir), event.Name) {
		return nil
	}
	if !hasFrontendLayout(s.dir) && isWorkspaceBackendPath(s.dir, event.Name) {
		return nil
	}

	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		remotePath, err := localWorkspacePathToRemote(s.dir, event.Name)
		if err != nil {
			return err
		}

		return s.deleteRemotePath(ctx, remotePath)
	}
	if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
		info, err := os.Stat(event.Name)
		if err != nil || info.IsDir() {
			return nil
		}

		return s.upsertLocalFile(ctx, event.Name)
	}

	return nil
}

func (s *Syncer) upsertLocalFile(ctx context.Context, localPath string) error {
	action, changed, err := s.localFileAction(localPath)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}

	_, err = s.client.DoSiteAction(ctx, s.projectID, s.siteID, s.clientID, []creght.SiteActionChange{action.action})
	if err != nil {
		return err
	}

	err = s.refreshRemote(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("synced %s\n", action.remotePath)
	return nil
}

func (s *Syncer) localFileAction(localPath string) (localFileAction, bool, error) {
	remotePath, err := localWorkspacePathToRemote(s.dir, localPath)
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

func (s *Syncer) handleFuncEvent(ctx context.Context, event fsnotify.Event) error {
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		key, err := localPathToFuncKey(s.dir, event.Name)
		if err != nil {
			return err
		}
		return s.deleteFuncKey(ctx, key)
	}
	if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
		info, err := os.Stat(event.Name)
		if err != nil || info.IsDir() {
			return nil
		}
		return s.upsertLocalFunc(ctx, event.Name)
	}
	return nil
}

func (s *Syncer) upsertLocalFunc(ctx context.Context, localPath string) error {
	action, changed, err := s.localFuncAction(localPath)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if err := s.applyFuncActions(ctx, []localFuncAction{action}); err != nil {
		return err
	}
	if err := s.refreshRemoteFuncs(ctx); err != nil {
		return err
	}
	fmt.Printf("synced func %s\n", action.key)
	return nil
}

func (s *Syncer) localFuncAction(localPath string) (localFuncAction, bool, error) {
	key, err := localPathToFuncKey(s.dir, localPath)
	if err != nil {
		return localFuncAction{}, false, err
	}
	bodyBytes, err := os.ReadFile(localPath)
	if err != nil {
		return localFuncAction{}, false, fmt.Errorf("read func %s: %w", key, err)
	}
	if !isUTF8FileBody(bodyBytes) {
		return localFuncAction{}, false, nil
	}
	body := string(bodyBytes)

	s.funcMu.Lock()
	remote, exist := s.remoteFuncByKey[key]
	s.funcMu.Unlock()

	if exist && remote.Body == body {
		return localFuncAction{}, false, nil
	}

	fn := creght.ProjectFunc{
		ID:       remote.ID,
		Key:      key,
		Name:     remote.Name,
		Desc:     remote.Desc,
		Body:     body,
		Mimetype: remote.Mimetype,
	}
	if strings.TrimSpace(fn.Name) == "" {
		fn.Name = strings.Trim(strings.TrimPrefix(key, "/"), "/")
	}
	if strings.TrimSpace(fn.Mimetype) == "" {
		fn.Mimetype = "application/javascript"
	}
	action := "create"
	if exist {
		action = "update"
	}

	return localFuncAction{key: key, action: action, fn: fn}, true, nil
}

func (s *Syncer) applyFuncActions(ctx context.Context, actions []localFuncAction) error {
	for _, action := range actions {
		switch action.action {
		case "create":
			if _, err := s.client.CreateProjectFunc(ctx, s.projectID, action.fn); err != nil {
				return err
			}
		case "update":
			if err := s.client.UpdateProjectFunc(ctx, s.projectID, action.fn.ID, action.fn); err != nil {
				return err
			}
		case "delete":
			if err := s.client.DeleteProjectFunc(ctx, s.projectID, action.fn.ID); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown func action: %s", action.action)
		}
	}
	return nil
}

func (s *Syncer) deleteFuncKey(ctx context.Context, key string) error {
	s.funcMu.Lock()
	remote, exist := s.remoteFuncByKey[key]
	s.funcMu.Unlock()
	if !exist {
		return nil
	}
	if err := s.client.DeleteProjectFunc(ctx, s.projectID, remote.ID); err != nil {
		return err
	}
	if err := s.refreshRemoteFuncs(ctx); err != nil {
		return err
	}
	fmt.Printf("deleted func %s\n", key)
	return nil
}

func (s *Syncer) deleteRemotePath(ctx context.Context, remotePath string) error {
	s.mu.Lock()
	remote, exist := s.remoteByPath[remotePath]
	s.mu.Unlock()
	if !exist || remote.Readonly {
		return nil
	}

	_, err := s.client.DoSiteAction(ctx, s.projectID, s.siteID, s.clientID, []creght.SiteActionChange{
		deleteFileAction(remotePath).action,
	})
	if err != nil {
		return err
	}

	err = s.refreshRemote(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("deleted %s\n", remotePath)
	return nil
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
