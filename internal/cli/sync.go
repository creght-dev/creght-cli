package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
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

	mu           sync.Mutex
	remoteByPath map[string]creght.File
}

type localFileAction struct {
	remotePath string
	action     creght.SiteActionChange
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

func (s *Syncer) syncLocalSnapshot(ctx context.Context) error {
	actions, err := s.collectLocalSnapshotActions()
	if err != nil {
		return err
	}
	if len(actions) == 0 {
		fmt.Println("No local changes to push")
		return nil
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

	fmt.Printf("synced %d changed files\n", len(actions))
	return nil
}

func (s *Syncer) collectLocalSnapshotActions() ([]localFileAction, error) {
	var actions []localFileAction
	localPaths := map[string]struct{}{}
	err := filepath.WalkDir(s.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if shouldSkipLocalPath(s.dir, path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

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
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		remotePath, err := localPathToRemote(s.dir, event.Name)
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
