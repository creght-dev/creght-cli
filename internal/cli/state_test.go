package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSiteWorkspaceDiscoversParentAndSiteID(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "pages", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveWorkspaceState(root, "project/site", map[string]snapshotEntry{}); err != nil {
		t.Fatal(err)
	}

	gotRoot, gotSiteID, err := resolveSiteWorkspace(nested, "", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != root {
		t.Fatalf("root = %q, want %q", gotRoot, root)
	}
	if gotSiteID != "project/site" {
		t.Fatalf("site id = %q, want project/site", gotSiteID)
	}
}

func TestResolveSiteWorkspaceExplicitDirDoesNotSearchParents(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "new-site")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveWorkspaceState(root, "outer/site", map[string]snapshotEntry{}); err != nil {
		t.Fatal(err)
	}

	gotRoot, gotSiteID, err := resolveSiteWorkspace(nested, "new/site", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != nested {
		t.Fatalf("root = %q, want %q", gotRoot, nested)
	}
	if gotSiteID != "new/site" {
		t.Fatalf("site id = %q, want new/site", gotSiteID)
	}
}

func TestResolveSiteWorkspaceRejectsMismatchedSiteID(t *testing.T) {
	root := t.TempDir()
	if err := saveWorkspaceState(root, "project/site", map[string]snapshotEntry{}); err != nil {
		t.Fatal(err)
	}

	_, _, err := resolveSiteWorkspace(root, "other/site", true, true)
	if err == nil {
		t.Fatal("expected mismatched site id error")
	}
}

func TestBuildSyncPlanKeepsRemoteOnlyFilesWithoutBase(t *testing.T) {
	plan := buildSyncPlan(workspaceState{}, false,
		map[string]snapshotEntry{},
		map[string]snapshotEntry{
			"/messages/zh-CN.json": {ID: "remote-id", Path: "/messages/zh-CN.json", Hash: "remote-hash"},
		},
		false,
	)

	if plan.hasChanges() || plan.hasConflicts() {
		t.Fatalf("got plan %+v, want no changes or conflicts", plan)
	}
}

func TestBuildSyncPlanSkipsDeletesByDefault(t *testing.T) {
	baseHash := testHash(t, "old\n")
	state := workspaceState{Files: map[string]stateEntry{
		"/messages/en.json": {Hash: baseHash},
	}}
	remote := map[string]snapshotEntry{
		"/messages/en.json": {ID: "remote-id", Path: "/messages/en.json", Hash: baseHash},
	}

	plan := buildSyncPlan(state, true,
		map[string]snapshotEntry{},
		remote,
		false,
	)

	if len(plan.FileActions) != 0 {
		t.Fatalf("got file actions %+v, want none", plan.FileActions)
	}
	if len(plan.SkippedDeletes) != 1 || plan.SkippedDeletes[0] != "/messages/en.json" {
		t.Fatalf("got skipped deletes %+v, want /messages/en.json", plan.SkippedDeletes)
	}

	deletePlan := buildSyncPlan(state, true,
		map[string]snapshotEntry{},
		remote,
		true,
	)
	if len(deletePlan.FileActions) != 1 || deletePlan.FileActions[0].action.Action != "file_delete" {
		t.Fatalf("got delete plan %+v, want one file_delete", deletePlan.FileActions)
	}
}

func TestBuildSyncPlanKeepsRemoteUpdateWhenLocalUnchanged(t *testing.T) {
	baseHash := testHash(t, "old\n")
	state := workspaceState{Files: map[string]stateEntry{
		"/page/index.tsx": {Hash: baseHash},
	}}

	plan := buildSyncPlan(state, true,
		map[string]snapshotEntry{
			"/page/index.tsx": {Path: "/page/index.tsx", Hash: baseHash, Body: "old\n"},
		},
		map[string]snapshotEntry{
			"/page/index.tsx": {ID: "remote-id", Path: "/page/index.tsx", Hash: testHash(t, "remote\n"), Body: "remote\n"},
		},
		false,
	)

	if len(plan.FileActions) != 0 || plan.hasConflicts() {
		t.Fatalf("got plan %+v, want no upload and no conflict", plan)
	}
	if len(plan.RemoteOnlyUpdates) != 1 || plan.RemoteOnlyUpdates[0] != "/page/index.tsx" {
		t.Fatalf("got remote-only updates %+v, want /page/index.tsx", plan.RemoteOnlyUpdates)
	}
}

func TestBuildPullEntryPlanWritesRemoteUpdateWhenLocalUnchanged(t *testing.T) {
	baseHash := testHash(t, "old\n")
	remoteHash := testHash(t, "remote\n")
	plan := buildPullEntryPlan("file",
		map[string]stateEntry{
			"/page/index.tsx": {Hash: baseHash},
		},
		true,
		map[string]snapshotEntry{
			"/page/index.tsx": {Path: "/page/index.tsx", Hash: baseHash, Body: "old\n"},
		},
		map[string]snapshotEntry{
			"/page/index.tsx": {ID: "remote-id", Path: "/page/index.tsx", Hash: remoteHash, Body: "remote\n"},
		},
		nil,
	)

	if len(plan.Writes) != 1 || plan.Writes[0].Path != "/page/index.tsx" {
		t.Fatalf("got writes %+v, want /page/index.tsx", plan.Writes)
	}
	if len(plan.Conflicts) != 0 {
		t.Fatalf("got conflicts %+v, want none", plan.Conflicts)
	}
}

func TestBuildPullEntryPlanConflictsWhenBothSidesChanged(t *testing.T) {
	baseHash := testHash(t, "old\n")
	plan := buildPullEntryPlan("file",
		map[string]stateEntry{
			"/page/index.tsx": {Hash: baseHash},
		},
		true,
		map[string]snapshotEntry{
			"/page/index.tsx": {Path: "/page/index.tsx", Hash: testHash(t, "local\n"), Body: "local\n"},
		},
		map[string]snapshotEntry{
			"/page/index.tsx": {ID: "remote-id", Path: "/page/index.tsx", Hash: testHash(t, "remote\n"), Body: "remote\n"},
		},
		nil,
	)

	if len(plan.Conflicts) != 1 || plan.Conflicts[0].Path != "/page/index.tsx" {
		t.Fatalf("got conflicts %+v, want /page/index.tsx conflict", plan.Conflicts)
	}
	if len(plan.Writes) != 0 {
		t.Fatalf("got writes %+v, want none", plan.Writes)
	}
}

func TestMergeStateSnapshotPreservesBaseForRemoteOnlyUpdate(t *testing.T) {
	baseHash := testHash(t, "old\n")
	remoteHash := testHash(t, "remote\n")
	next := mergeStateSnapshot(
		map[string]stateEntry{
			"/page/index.tsx": {Hash: baseHash},
		},
		true,
		map[string]snapshotEntry{
			"/page/index.tsx": {Path: "/page/index.tsx", Hash: baseHash, Body: "old\n"},
		},
		map[string]snapshotEntry{
			"/page/index.tsx": {ID: "remote-id", Path: "/page/index.tsx", Hash: remoteHash, Body: "remote\n"},
		},
	)

	if next["/page/index.tsx"].Hash != baseHash {
		t.Fatalf("got next base hash %q, want original base hash %q", next["/page/index.tsx"].Hash, baseHash)
	}
}

func TestBuildSyncPlanConflictsWhenBothSidesChanged(t *testing.T) {
	baseHash := testHash(t, "old\n")
	state := workspaceState{Files: map[string]stateEntry{
		"/page/index.tsx": {Hash: baseHash},
	}}

	plan := buildSyncPlan(state, true,
		map[string]snapshotEntry{
			"/page/index.tsx": {Path: "/page/index.tsx", Hash: testHash(t, "local\n"), Body: "local\n"},
		},
		map[string]snapshotEntry{
			"/page/index.tsx": {ID: "remote-id", Path: "/page/index.tsx", Hash: testHash(t, "remote\n"), Body: "remote\n"},
		},
		false,
	)

	if len(plan.Conflicts) != 1 {
		t.Fatalf("got conflicts %+v, want one conflict", plan.Conflicts)
	}
	if plan.Conflicts[0].Path != "/page/index.tsx" {
		t.Fatalf("got conflict path %q, want /page/index.tsx", plan.Conflicts[0].Path)
	}
}

func TestBuildSyncPlanUpdatesBackendFile(t *testing.T) {
	baseHash := testHash(t, "export function main() { return 'old' }\n")
	newBody := "export function main() { return 'new' }\n"
	state := workspaceState{Files: map[string]stateEntry{
		"/backend/func/booking.ts": {Hash: baseHash},
	}}

	plan := buildSyncPlan(state, true,
		map[string]snapshotEntry{
			"/backend/func/booking.ts": {Path: "/backend/func/booking.ts", Hash: testHash(t, newBody), Body: newBody},
		},
		map[string]snapshotEntry{
			"/backend/func/booking.ts": {ID: "file-id", Path: "/backend/func/booking.ts", Hash: baseHash, Body: "export function main() { return 'old' }\n"},
		},
		false,
	)

	if len(plan.FileActions) != 1 || plan.FileActions[0].action.Action != "file_update" {
		t.Fatalf("got file actions %+v, want one file_update", plan.FileActions)
	}
	if plan.FileActions[0].remotePath != "/backend/func/booking.ts" {
		t.Fatalf("got remote path %q, want /backend/func/booking.ts", plan.FileActions[0].remotePath)
	}
}

func TestBuildPullEntryPlanAutoMergesBothChanged(t *testing.T) {
	baseBody := "a\nb\nc\n"
	localBody := "A\nb\nc\n"
	remoteBody := "a\nb\nC\n"
	plan := buildPullEntryPlan("file",
		map[string]stateEntry{"/page/index.tsx": {Hash: testHash(t, baseBody)}},
		true,
		map[string]snapshotEntry{"/page/index.tsx": {Path: "/page/index.tsx", Hash: testHash(t, localBody), Body: localBody}},
		map[string]snapshotEntry{"/page/index.tsx": {ID: "remote-id", Path: "/page/index.tsx", Hash: testHash(t, remoteBody), Body: remoteBody}},
		func(hash string) (string, bool) { return baseBody, true },
	)

	if len(plan.Conflicts) != 0 || len(plan.ConflictWrites) != 0 {
		t.Fatalf("got plan %+v, want clean merge", plan)
	}
	if len(plan.CleanMerges) != 1 || plan.CleanMerges[0].Body != "A\nb\nC\n" {
		t.Fatalf("got clean merges %+v, want merged body with both edits", plan.CleanMerges)
	}
}

func TestBuildPullEntryPlanWritesMarkersOnOverlap(t *testing.T) {
	baseBody := "a\nb\nc\n"
	localBody := "a\nLOCAL\nc\n"
	remoteBody := "a\nREMOTE\nc\n"
	plan := buildPullEntryPlan("file",
		map[string]stateEntry{"/page/index.tsx": {Hash: testHash(t, baseBody)}},
		true,
		map[string]snapshotEntry{"/page/index.tsx": {Path: "/page/index.tsx", Hash: testHash(t, localBody), Body: localBody}},
		map[string]snapshotEntry{"/page/index.tsx": {ID: "remote-id", Path: "/page/index.tsx", Hash: testHash(t, remoteBody), Body: remoteBody}},
		func(hash string) (string, bool) { return baseBody, true },
	)

	if len(plan.Conflicts) != 0 || len(plan.CleanMerges) != 0 {
		t.Fatalf("got plan %+v, want one conflict write", plan)
	}
	if len(plan.ConflictWrites) != 1 || !hasConflictMarkers(plan.ConflictWrites[0].Body) {
		t.Fatalf("got conflict writes %+v, want marker body", plan.ConflictWrites)
	}
}

func TestBuildPullEntryPlanFallsBackWithoutBaseContent(t *testing.T) {
	plan := buildPullEntryPlan("file",
		map[string]stateEntry{"/page/index.tsx": {Hash: testHash(t, "old\n")}},
		true,
		map[string]snapshotEntry{"/page/index.tsx": {Path: "/page/index.tsx", Hash: testHash(t, "local\n"), Body: "local\n"}},
		map[string]snapshotEntry{"/page/index.tsx": {ID: "remote-id", Path: "/page/index.tsx", Hash: testHash(t, "remote\n"), Body: "remote\n"}},
		func(hash string) (string, bool) { return "", false },
	)

	if len(plan.Conflicts) != 1 || len(plan.CleanMerges) != 0 || len(plan.ConflictWrites) != 0 {
		t.Fatalf("got plan %+v, want legacy conflict when base content is missing", plan)
	}
}

func TestBuildSyncPlanBlocksConflictMarkers(t *testing.T) {
	baseBody := "old\n"
	markerBody := conflictMarkerLocal + "\nlocal\n" + conflictMarkerSep + "\nremote\n" + conflictMarkerRemote + "\n"
	state := workspaceState{Files: map[string]stateEntry{
		"/page/index.tsx": {Hash: testHash(t, baseBody)},
	}}

	plan := buildSyncPlan(state, true,
		map[string]snapshotEntry{
			"/page/index.tsx": {Path: "/page/index.tsx", Hash: testHash(t, markerBody), Body: markerBody},
		},
		map[string]snapshotEntry{
			"/page/index.tsx": {ID: "remote-id", Path: "/page/index.tsx", Hash: testHash(t, baseBody), Body: baseBody},
		},
		false,
	)

	if len(plan.FileActions) != 0 {
		t.Fatalf("got actions %+v, want none for marker file", plan.FileActions)
	}
	if len(plan.Conflicts) != 1 || !strings.Contains(plan.Conflicts[0].Reason, "conflict markers") {
		t.Fatalf("got conflicts %+v, want marker conflict", plan.Conflicts)
	}
}

func TestMergeStateSnapshotKeepsBaseForUnresolvedConflict(t *testing.T) {
	baseHash := testHash(t, "old\n")
	next := mergeStateSnapshot(
		map[string]stateEntry{"/page/index.tsx": {Hash: baseHash}},
		true,
		map[string]snapshotEntry{"/page/index.tsx": {Path: "/page/index.tsx", Hash: testHash(t, "local\n"), Body: "local\n"}},
		map[string]snapshotEntry{"/page/index.tsx": {ID: "remote-id", Path: "/page/index.tsx", Hash: testHash(t, "remote\n"), Body: "remote\n"}},
	)

	if next["/page/index.tsx"].Hash != baseHash {
		t.Fatalf("got next base hash %q, want unresolved conflict to keep base %q", next["/page/index.tsx"].Hash, baseHash)
	}
}

func TestSaveWorkspaceStateWritesAndGCsBaseObjects(t *testing.T) {
	root := t.TempDir()
	body := "hello\n"
	hash := testHash(t, body)
	files := map[string]snapshotEntry{
		"/page/index.tsx": {Path: "/page/index.tsx", Hash: hash, Body: body},
	}
	if err := saveWorkspaceState(root, "project/site", files); err != nil {
		t.Fatal(err)
	}

	got, ok := readBaseObject(root, hash)
	if !ok || got != body {
		t.Fatalf("readBaseObject = %q ok=%v, want stored body", got, ok)
	}

	if err := saveWorkspaceState(root, "project/site", map[string]snapshotEntry{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := readBaseObject(root, hash); ok {
		t.Fatal("base object survived GC after being dereferenced")
	}
}

func TestBackupOverwrittenLocalFilesSkipsUnmodified(t *testing.T) {
	root := t.TempDir()
	baseBody := "base\n"
	modifiedBody := "modified\n"
	state := workspaceState{Files: map[string]stateEntry{
		"/clean.txt": {Hash: testHash(t, baseBody)},
		"/dirty.txt": {Hash: testHash(t, baseBody)},
	}}
	localFiles := map[string]snapshotEntry{
		"/clean.txt": {Path: "/clean.txt", Hash: testHash(t, baseBody), Body: baseBody},
		"/dirty.txt": {Path: "/dirty.txt", Hash: testHash(t, modifiedBody), Body: modifiedBody},
	}
	incoming := map[string]string{
		"/clean.txt": "new remote\n",
		"/dirty.txt": "new remote\n",
	}

	backupDir, err := backupOverwrittenLocalFiles(root, state, true, localFiles, incoming)
	if err != nil {
		t.Fatal(err)
	}
	if backupDir == "" {
		t.Fatal("expected a backup dir for the modified file")
	}
	if _, err := os.Stat(filepath.Join(backupDir, "dirty.txt")); err != nil {
		t.Fatalf("modified file missing from backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backupDir, "clean.txt")); !os.IsNotExist(err) {
		t.Fatalf("unmodified file should not be backed up, stat err = %v", err)
	}
	body, err := os.ReadFile(filepath.Join(backupDir, "dirty.txt"))
	if err != nil || string(body) != modifiedBody {
		t.Fatalf("backup body = %q err=%v, want local modified content", body, err)
	}
}

func testHash(t *testing.T, body string) string {
	t.Helper()
	hash, err := qetagHash([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
