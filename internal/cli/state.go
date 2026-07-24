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
const baseDirName = "base"

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
	Writes []snapshotEntry
	// CleanMerges are files changed on both sides whose edits do not overlap;
	// Body holds the auto-merged content.
	CleanMerges []snapshotEntry
	// ConflictWrites are files changed on both sides with overlapping edits;
	// Body holds the content with conflict markers.
	ConflictWrites []snapshotEntry
	Deletes        []string
	Conflicts      []planConflict
}

func statePath(root string) string {
	return filepath.Join(root, stateDirName, stateFileName)
}

func baseObjectPath(root string, hash string) string {
	return filepath.Join(root, stateDirName, baseDirName, hash)
}

// writeBaseObjects stores file bodies under .creght/base/<hash> so later
// pulls/pushes can three-way merge against the recorded base content. Entries
// whose Body does not match their Hash (e.g. hash-only entries carried over
// from a previous state) are skipped; their blob is either already stored or
// simply unavailable.
func writeBaseObjects(root string, files map[string]snapshotEntry) error {
	for _, file := range files {
		if file.Hash == "" {
			continue
		}
		path := baseObjectPath(root, file.Hash)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		hash, err := qetagHash([]byte(file.Body))
		if err != nil || hash != file.Hash {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create base dir: %w", err)
		}
		if err := os.WriteFile(path, []byte(file.Body), 0o644); err != nil {
			return fmt.Errorf("write base object %s: %w", file.Hash, err)
		}
	}
	return nil
}

// readBaseObject returns the stored base content for a hash, if present.
// Workspaces pulled by older CLI versions have no base objects; callers fall
// back to hash-only conflict detection.
func readBaseObject(root string, hash string) (string, bool) {
	if hash == "" {
		return "", false
	}
	body, err := os.ReadFile(baseObjectPath(root, hash))
	if err != nil {
		return "", false
	}
	return string(body), true
}

// gcBaseObjects removes base objects no longer referenced by the state.
func gcBaseObjects(root string, files map[string]stateEntry) {
	referenced := map[string]struct{}{}
	for _, entry := range files {
		referenced[entry.Hash] = struct{}{}
	}
	entries, err := os.ReadDir(filepath.Join(root, stateDirName, baseDirName))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := referenced[entry.Name()]; !ok {
			_ = os.Remove(baseObjectPath(root, entry.Name()))
		}
	}
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

// resolveSiteWorkspace resolves a command's workspace directory and site ref.
// When searchParents is true (the caller did not explicitly pass --dir), it
// walks upward so commands also work from a subdirectory of a pulled workspace.
func resolveSiteWorkspace(dir string, siteID string, searchParents bool, requireWorkspace bool) (string, string, error) {
	root, state, hasState, err := findWorkspaceState(dir, searchParents)
	if err != nil {
		return "", "", err
	}
	if !hasState {
		if requireWorkspace {
			absDir, absErr := filepath.Abs(dir)
			if absErr != nil {
				return "", "", fmt.Errorf("resolve workspace dir: %w", absErr)
			}
			return "", "", fmt.Errorf("%s is not inside a creght workspace (missing .creght/state.json); run creght pull --site_id=<project_id>/<site_id> first, or pass --dir pointing at a pulled workspace", absDir)
		}
		return dir, strings.TrimSpace(siteID), nil
	}

	stateSiteID := strings.TrimSpace(state.SiteID)
	requestedSiteID := strings.TrimSpace(siteID)
	if requestedSiteID == "" {
		if stateSiteID == "" {
			return "", "", fmt.Errorf("workspace %s has no site_id in .creght/state.json; pass --site_id=<project_id>/<site_id>", root)
		}
		requestedSiteID = stateSiteID
	} else if stateSiteID != "" && requestedSiteID != stateSiteID {
		return "", "", fmt.Errorf("workspace state belongs to %s, not %s", stateSiteID, requestedSiteID)
	}

	return root, requestedSiteID, nil
}

func findWorkspaceState(start string, searchParents bool) (string, workspaceState, bool, error) {
	root, err := filepath.Abs(start)
	if err != nil {
		return "", workspaceState{}, false, fmt.Errorf("resolve workspace dir: %w", err)
	}

	for {
		state, hasState, err := loadWorkspaceState(root)
		if err != nil {
			return "", workspaceState{}, false, err
		}
		if hasState {
			return root, state, true, nil
		}
		if !searchParents {
			return root, workspaceState{}, false, nil
		}
		parent := filepath.Dir(root)
		if parent == root {
			return root, workspaceState{}, false, nil
		}
		root = parent
	}
}

func saveWorkspaceState(root string, siteID string, files map[string]snapshotEntry) error {
	ignore, err := loadCreghtIgnore(root)
	if err != nil {
		return err
	}
	files = filterIgnoredSnapshot(ignore, files)
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
	if err := writeBaseObjects(root, files); err != nil {
		return err
	}
	gcBaseObjects(root, state.Files)
	return nil
}

// putStateFileEntry updates the base state for a single file (used by
// single-file pull/push) without rewriting the whole snapshot.
func putStateFileEntry(root string, siteID string, entry snapshotEntry) error {
	state, hasState, err := loadWorkspaceState(root)
	if err != nil {
		return err
	}
	if !hasState || state.Files == nil {
		state.Files = map[string]stateEntry{}
	}
	ignore, err := loadCreghtIgnore(root)
	if err != nil {
		return err
	}
	state.Files = filterIgnoredState(ignore, state.Files)
	if strings.TrimSpace(state.SiteID) == "" {
		state.SiteID = siteID
	}
	state.Files[entry.Path] = stateEntry{Hash: entry.Hash, Readonly: entry.Readonly}
	state.UpdatedAt = time.Now().Format(time.RFC3339Nano)

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
	return writeBaseObjects(root, map[string]snapshotEntry{entry.Path: entry})
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
	err := walkWorkspaceFiles(root, func(path string) error {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !isUTF8FileBody(body) {
			return nil
		}
		remotePath, err := localPathToRemote(root, path)
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
				if hasConflictMarkers(localEntry.Body) {
					conflicts = append(conflicts, markerConflict(path))
					continue
				}
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
			} else if hasConflictMarkers(localEntry.Body) {
				conflicts = append(conflicts, markerConflict(path))
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

// markerConflict blocks a file whose local copy still contains conflict
// markers from a previous pull; pushing those would publish broken code.
func markerConflict(path string) planConflict {
	return planConflict{Kind: "file", Path: path, Reason: "contains unresolved conflict markers; edit the file or run creght resolve"}
}

// buildPullEntryPlan plans a pull. baseBody looks up recorded base content by
// hash (nil disables merging); files changed on both sides are three-way
// merged when the base content is available, otherwise reported as conflicts.
func buildPullEntryPlan(kind string, base map[string]stateEntry, hasState bool, local map[string]snapshotEntry, remote map[string]snapshotEntry, baseBody func(hash string) (string, bool)) pullEntryPlan {
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
			if localOK && remoteOK && hasConflictMarkers(localEntry.Body) {
				plan.Conflicts = append(plan.Conflicts, markerConflict(path))
				continue
			}
			if localOK && remoteOK && baseBody != nil {
				if baseText, ok := baseBody(baseEntry.Hash); ok {
					merged, clean := merge3(baseText, localEntry.Body, remoteEntry.Body)
					entry := snapshotEntry{ID: remoteEntry.ID, Path: path, Hash: remoteEntry.Hash, Body: merged, Readonly: remoteEntry.Readonly}
					if clean {
						plan.CleanMerges = append(plan.CleanMerges, entry)
					} else {
						plan.ConflictWrites = append(plan.ConflictWrites, entry)
					}
					continue
				}
			}
			plan.Conflicts = append(plan.Conflicts, planConflict{Kind: kind, Path: path, Reason: "changed both locally and remotely"})
		}
	}
	sort.Slice(plan.Writes, func(i, j int) bool { return plan.Writes[i].Path < plan.Writes[j].Path })
	sort.Slice(plan.CleanMerges, func(i, j int) bool { return plan.CleanMerges[i].Path < plan.CleanMerges[j].Path })
	sort.Slice(plan.ConflictWrites, func(i, j int) bool { return plan.ConflictWrites[i].Path < plan.ConflictWrites[j].Path })
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
		case localOK && remoteOK && baseOK && localEntry.Hash != baseEntry.Hash && remoteEntry.Hash != baseEntry.Hash && localEntry.Hash != remoteEntry.Hash:
			// Unresolved both-sides change (e.g. push --skip-conflicts): keep
			// the old base so a later pull can still three-way merge.
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
