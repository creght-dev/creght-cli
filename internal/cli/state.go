package cli

import (
	"bysir/creght-cli/internal/creght"
	"crypto/sha256"
	"encoding/hex"
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
	Funcs     map[string]stateEntry `json:"funcs"`
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
	Name     string
	Desc     string
	Mimetype string
	Readonly bool
}

type syncPlan struct {
	FileActions       []localFileAction
	FuncActions       []localFuncAction
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
	if state.Funcs == nil {
		state.Funcs = map[string]stateEntry{}
	}
	return state, true, nil
}

func saveWorkspaceState(root string, siteID string, files map[string]snapshotEntry, funcs map[string]snapshotEntry) error {
	state := workspaceState{
		SiteID:    siteID,
		UpdatedAt: time.Now().Format(time.RFC3339Nano),
		Files:     map[string]stateEntry{},
		Funcs:     map[string]stateEntry{},
	}
	for path, file := range files {
		state.Files[path] = stateEntry{Hash: file.Hash, Readonly: file.Readonly}
	}
	for key, fn := range funcs {
		state.Funcs[key] = stateEntry{Hash: fn.Hash}
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

func remoteFuncSnapshot(funcs []creght.ProjectFunc) map[string]snapshotEntry {
	out := map[string]snapshotEntry{}
	for _, fn := range funcs {
		key := normalizeLocalFuncKey(fn.Key)
		if key == "" {
			continue
		}
		out[key] = snapshotEntry{
			ID:       fn.ID,
			Path:     key,
			Hash:     stableBodyHash(fn.Body),
			Body:     fn.Body,
			Name:     fn.Name,
			Desc:     fn.Desc,
			Mimetype: fn.Mimetype,
		}
	}
	return out
}

func localFileSnapshot(root string) (map[string]snapshotEntry, error) {
	out := map[string]snapshotEntry{}
	siteRoot := siteSyncRoot(root)
	if _, err := os.Stat(siteRoot); os.IsNotExist(err) {
		return out, nil
	}
	err := filepath.WalkDir(siteRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if shouldSkipLocalPath(siteRoot, path) || (!hasFrontendLayout(root) && isWorkspaceBackendPath(root, path)) {
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
		remotePath, err := localPathToRemote(siteRoot, path)
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
	return out, err
}

func localFuncSnapshot(root string) (map[string]snapshotEntry, error) {
	out := map[string]snapshotEntry{}
	if _, err := os.Stat(funcRoot(root)); os.IsNotExist(err) {
		return out, nil
	}
	err := filepath.WalkDir(funcRoot(root), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if shouldSkipLocalPath(funcRoot(root), path) {
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
		key, err := localPathToFuncKey(root, path)
		if err != nil {
			return err
		}
		out[key] = snapshotEntry{Path: key, Hash: stableBodyHash(string(body)), Body: string(body)}
		return nil
	})
	return out, err
}

func stableBodyHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func buildSyncPlan(state workspaceState, hasState bool, localFiles map[string]snapshotEntry, remoteFiles map[string]snapshotEntry, localFuncs map[string]snapshotEntry, remoteFuncs map[string]snapshotEntry, allowDelete bool) syncPlan {
	var plan syncPlan
	plan.FileActions, plan.Conflicts, plan.SkippedDeletes, plan.RemoteOnlyUpdates, plan.NoBaseRemoteDiffs = buildFilePlan(state.Files, hasState, localFiles, remoteFiles, allowDelete)
	funcActions, funcConflicts, funcSkippedDeletes, funcRemoteOnly, funcNoBase := buildFuncPlan(state.Funcs, hasState, localFuncs, remoteFuncs, allowDelete)
	plan.FuncActions = funcActions
	plan.Conflicts = append(plan.Conflicts, funcConflicts...)
	plan.SkippedDeletes = append(plan.SkippedDeletes, funcSkippedDeletes...)
	plan.RemoteOnlyUpdates = append(plan.RemoteOnlyUpdates, funcRemoteOnly...)
	plan.NoBaseRemoteDiffs = append(plan.NoBaseRemoteDiffs, funcNoBase...)
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

func buildFuncPlan(base map[string]stateEntry, hasState bool, local map[string]snapshotEntry, remote map[string]snapshotEntry, allowDelete bool) ([]localFuncAction, []planConflict, []string, []string, []string) {
	var actions []localFuncAction
	var conflicts []planConflict
	var skippedDeletes []string
	var remoteOnlyUpdates []string
	var noBaseRemoteDiffs []string
	for _, key := range unionKeys(base, local, remote) {
		baseEntry, baseOK := base[key]
		localEntry, localOK := local[key]
		remoteEntry, remoteOK := remote[key]
		if !localOK && !remoteOK {
			continue
		}
		if !hasState || !baseOK {
			switch {
			case localOK && !remoteOK:
				actions = append(actions, createFuncAction(key, localEntry.Body, snapshotEntry{}))
			case localOK && remoteOK && localEntry.Hash != remoteEntry.Hash:
				noBaseRemoteDiffs = append(noBaseRemoteDiffs, "func:"+key)
				conflicts = append(conflicts, planConflict{Kind: "func", Path: key, Reason: "no base state for remote func; pull first or use --force"})
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
			remoteOnlyUpdates = append(remoteOnlyUpdates, "func:"+key)
		case localChanged && !remoteChanged:
			if !localOK {
				if allowDelete {
					actions = append(actions, localFuncAction{key: key, action: "delete", fn: creght.ProjectFunc{ID: remoteEntry.ID, Key: key}})
				} else {
					skippedDeletes = append(skippedDeletes, "func:"+key)
				}
			} else if remoteOK {
				actions = append(actions, updateFuncAction(key, localEntry.Body, remoteEntry))
			} else {
				actions = append(actions, createFuncAction(key, localEntry.Body, snapshotEntry{}))
			}
		case localChanged && remoteChanged:
			conflicts = append(conflicts, planConflict{Kind: "func", Path: key, Reason: "changed both locally and remotely"})
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

func createFuncAction(key string, body string, remote snapshotEntry) localFuncAction {
	return localFuncAction{key: key, action: "create", fn: projectFuncFromSnapshot(key, body, remote)}
}

func updateFuncAction(key string, body string, remote snapshotEntry) localFuncAction {
	return localFuncAction{key: key, action: "update", fn: projectFuncFromSnapshot(key, body, remote)}
}

func projectFuncFromSnapshot(key string, body string, remote snapshotEntry) creght.ProjectFunc {
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
	return fn
}

func sortPlan(plan *syncPlan) {
	sort.Slice(plan.FileActions, func(i, j int) bool { return plan.FileActions[i].remotePath < plan.FileActions[j].remotePath })
	sort.Slice(plan.FuncActions, func(i, j int) bool { return plan.FuncActions[i].key < plan.FuncActions[j].key })
	sort.Slice(plan.Conflicts, func(i, j int) bool { return plan.Conflicts[i].Path < plan.Conflicts[j].Path })
	sort.Strings(plan.SkippedDeletes)
	sort.Strings(plan.RemoteOnlyUpdates)
	sort.Strings(plan.NoBaseRemoteDiffs)
}

func (p syncPlan) hasChanges() bool {
	return len(p.FileActions) > 0 || len(p.FuncActions) > 0
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
