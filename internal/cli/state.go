package cli

import (
	"bysir/creght-cli/internal/creght"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const stateDirName = ".creght"
const stateFileName = "state.json"

type workspaceState struct {
	SiteID    string                `json:"site_id"`
	UpdatedAt string                `json:"updated_at"`
	Files     map[string]stateEntry `json:"files"`
}

type stateEntry struct {
	Hash     string `json:"hash"`
	Readonly bool   `json:"readonly,omitempty"`
}

type snapshotEntry struct {
	ID       string
	Path     string
	Hash     string
	Body     string
	Readonly bool
}

type syncPlan struct {
	FileActions       []localFileAction
	Conflicts         []planConflict
	SkippedDeletes    []string
	RemoteOnlyUpdates []string
	NoBaseRemoteDiffs []string
}

type planConflict struct {
	Kind   string
	Path   string
	Reason string
}

type pullEntryPlan struct {
	Writes    []snapshotEntry
	Deletes   []string
	Conflicts []planConflict
}

func statePath(root string) string {
	return filepath.Join(root, stateDirName, stateFileName)
}

func loadWorkspaceState(root string) (workspaceState, bool, error) {
	body, err := os.ReadFile(statePath(root))
	if os.IsNotExist(err) {
		return workspaceState{}, false, nil
	}
	if err != nil {
		return workspaceState{}, false, fmt.Errorf("read state: %w", err)
	}
	var state workspaceState
	if err := json.Unmarshal(body, &state); err != nil {
		return workspaceState{}, false, fmt.Errorf("parse state: %w", err)
	}
	if state.Files == nil {
		state.Files = map[string]stateEntry{}
	}
	return state, true, nil
}

func saveWorkspaceState(root string, siteID string, files map[string]snapshotEntry) error {
	state := workspaceState{
		SiteID:    siteID,
		UpdatedAt: time.Now().Format(time.RFC3339Nano),
		Files:     map[string]stateEntry{},
	}
	for path, file := range files {
		state.Files[path] = stateEntry{Hash: file.Hash, Readonly: file.Readonly}
	}

	if err := os.MkdirAll(filepath.Dir(statePath(root)), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := os.WriteFile(statePath(root), append(body, '\n'), 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

func remoteFileSnapshot(files []creght.File) map[string]snapshotEntry {
	out := map[string]snapshotEntry{}
	for _, file := range files {
		if file.IsDir {
			continue
		}
		hash := strings.TrimSpace(file.Hash)
		if hash == "" {
			hash, _ = qetagHash([]byte(file.Body))
		}
		out[file.Path] = snapshotEntry{
			ID:       file.ID,
			Path:     file.Path,
			Hash:     hash,
			Body:     file.Body,
			Readonly: file.Readonly,
		}
	}
	return out
}

func localFileSnapshot(root string) (map[string]snapshotEntry, error) {
	out := map[string]snapshotEntry{}
	for _, syncRoot := range workspaceSyncRoots(root) {
		if _, err := os.Stat(syncRoot); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(syncRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if shouldSkipLocalPath(syncRoot, path) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !isUTF8FileBody(body) {
				return nil
			}
			remotePath, err := localWorkspacePathToRemote(root, path)
			if err != nil {
				return err
			}
			hash, err := qetagHash(body)
			if err != nil {
				return err
			}
			out[remotePath] = snapshotEntry{Path: remotePath, Hash: hash, Body: string(body)}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func buildSyncPlan(state workspaceState, hasState bool, localFiles map[string]snapshotEntry, remoteFiles map[string]snapshotEntry, allowDelete bool) syncPlan {
	var plan syncPlan
	plan.FileActions, plan.Conflicts, plan.SkippedDeletes, plan.RemoteOnlyUpdates, plan.NoBaseRemoteDiffs = buildFilePlan(state.Files, hasState, localFiles, remoteFiles, allowDelete)
	sortPlan(&plan)
	return plan
}

func buildFilePlan(base map[string]stateEntry, hasState bool, local map[string]snapshotEntry, remote map[string]snapshotEntry, allowDelete bool) ([]localFileAction, []planConflict, []string, []string, []string) {
	var actions []localFileAction
	var conflicts []planConflict
	var skippedDeletes []string
	var remoteOnlyUpdates []string
	var noBaseRemoteDiffs []string
	for _, path := range unionKeys(base, local, remote) {
		baseEntry, baseOK := base[path]
		localEntry, localOK := local[path]
		remoteEntry, remoteOK := remote[path]
		if !localOK && !remoteOK {
			continue
		}
		if remoteOK && remoteEntry.Readonly {
			continue
		}
		if !hasState || !baseOK {
			switch {
			case localOK && !remoteOK:
				actions = append(actions, createFileAction(path, localEntry.Body))
			case localOK && remoteOK && localEntry.Hash != remoteEntry.Hash:
				noBaseRemoteDiffs = append(noBaseRemoteDiffs, path)
				conflicts = append(conflicts, planConflict{Kind: "file", Path: path, Reason: "no base state for remote file; pull first or use --force"})
			}
			continue
		}
		localHash := ""
		if localOK {
			localHash = localEntry.Hash
		}
		remoteHash := ""
		if remoteOK {
			remoteHash = remoteEntry.Hash
		}
		localChanged := localHash != baseEntry.Hash
		remoteChanged := remoteHash != baseEntry.Hash
		switch {
		case localOK && remoteOK && localHash == remoteHash:
			continue
		case !localChanged && remoteChanged:
			remoteOnlyUpdates = append(remoteOnlyUpdates, path)
		case localChanged && !remoteChanged:
			if !localOK {
				if allowDelete {
					actions = append(actions, deleteFileAction(path))
				} else {
					skippedDeletes = append(skippedDeletes, path)
				}
			} else if remoteOK {
				actions = append(actions, updateFileAction(remoteEntry, localEntry.Body))
			} else {
				actions = append(actions, createFileAction(path, localEntry.Body))
			}
		case localChanged && remoteChanged:
			conflicts = append(conflicts, planConflict{Kind: "file", Path: path, Reason: "changed both locally and remotely"})
		}
	}
	return actions, conflicts, skippedDeletes, remoteOnlyUpdates, noBaseRemoteDiffs
}

func buildPullEntryPlan(kind string, base map[string]stateEntry, hasState bool, local map[string]snapshotEntry, remote map[string]snapshotEntry) pullEntryPlan {
	var plan pullEntryPlan
	for _, path := range unionKeys(base, local, remote) {
		baseEntry, baseOK := base[path]
		localEntry, localOK := local[path]
		remoteEntry, remoteOK := remote[path]
		if !localOK && !remoteOK {
			continue
		}
		if !hasState || !baseOK {
			switch {
			case remoteOK && !localOK:
				plan.Writes = append(plan.Writes, remoteEntry)
			case remoteOK && localOK && localEntry.Hash != remoteEntry.Hash:
				plan.Conflicts = append(plan.Conflicts, planConflict{Kind: kind, Path: path, Reason: "no base state for local file; move it aside or use pull --force"})
			}
			continue
		}

		localHash := ""
		if localOK {
			localHash = localEntry.Hash
		}
		remoteHash := ""
		if remoteOK {
			remoteHash = remoteEntry.Hash
		}
		localChanged := localHash != baseEntry.Hash
		remoteChanged := remoteHash != baseEntry.Hash
		switch {
		case localOK && remoteOK && localHash == remoteHash:
			continue
		case !localChanged && remoteChanged:
			if remoteOK {
				plan.Writes = append(plan.Writes, remoteEntry)
			} else {
				plan.Deletes = append(plan.Deletes, path)
			}
		case localChanged && !remoteChanged:
			continue
		case localChanged && remoteChanged:
			plan.Conflicts = append(plan.Conflicts, planConflict{Kind: kind, Path: path, Reason: "changed both locally and remotely"})
		}
	}
	sort.Slice(plan.Writes, func(i, j int) bool { return plan.Writes[i].Path < plan.Writes[j].Path })
	sort.Strings(plan.Deletes)
	sort.Slice(plan.Conflicts, func(i, j int) bool { return plan.Conflicts[i].Path < plan.Conflicts[j].Path })
	return plan
}

func unionKeys(base map[string]stateEntry, local map[string]snapshotEntry, remote map[string]snapshotEntry) []string {
	seen := map[string]struct{}{}
	for key := range base {
		seen[key] = struct{}{}
	}
	for key := range local {
		seen[key] = struct{}{}
	}
	for key := range remote {
		seen[key] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func createFileAction(path string, body string) localFileAction {
	return localFileAction{
		remotePath: path,
		action: creght.SiteActionChange{
			Action: "file_create",
			File: creght.SiteActionFileSpec{
				Path: creght.StringPtr(path),
				Body: creght.StringPtr(body),
			},
		},
	}
}

func updateFileAction(remote snapshotEntry, body string) localFileAction {
	return localFileAction{
		remotePath: remote.Path,
		action: creght.SiteActionChange{
			Action: "file_update",
			File: creght.SiteActionFileSpec{
				ID:   remote.ID,
				Body: creght.StringPtr(body),
			},
		},
	}
}

func sortPlan(plan *syncPlan) {
	sort.Slice(plan.FileActions, func(i, j int) bool { return plan.FileActions[i].remotePath < plan.FileActions[j].remotePath })
	sort.Slice(plan.Conflicts, func(i, j int) bool { return plan.Conflicts[i].Path < plan.Conflicts[j].Path })
	sort.Strings(plan.SkippedDeletes)
	sort.Strings(plan.RemoteOnlyUpdates)
	sort.Strings(plan.NoBaseRemoteDiffs)
}

func (p syncPlan) hasChanges() bool {
	return len(p.FileActions) > 0
}

func (p syncPlan) hasConflicts() bool {
	return len(p.Conflicts) > 0
}

func mergeStateSnapshot(base map[string]stateEntry, hasState bool, local map[string]snapshotEntry, remote map[string]snapshotEntry) map[string]snapshotEntry {
	next := map[string]snapshotEntry{}
	if !hasState {
		for path, localEntry := range local {
			if remoteEntry, ok := remote[path]; ok {
				next[path] = remoteEntry
			} else {
				next[path] = localEntry
			}
		}
		return next
	}

	for _, path := range unionKeys(base, local, remote) {
		baseEntry, baseOK := base[path]
		localEntry, localOK := local[path]
		remoteEntry, remoteOK := remote[path]
		switch {
		case localOK && baseOK && localEntry.Hash == baseEntry.Hash && (!remoteOK || remoteEntry.Hash != baseEntry.Hash):
			next[path] = snapshotEntry{Path: path, Hash: baseEntry.Hash, Readonly: baseEntry.Readonly}
		case localOK && remoteOK:
			next[path] = remoteEntry
		case localOK:
			next[path] = localEntry
		case baseOK && remoteOK && remoteEntry.Hash == baseEntry.Hash:
			next[path] = snapshotEntry{Path: path, Hash: baseEntry.Hash, Readonly: baseEntry.Readonly}
		}
	}
	return next
}
