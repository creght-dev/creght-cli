package cli

import "testing"

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

func testHash(t *testing.T, body string) string {
	t.Helper()
	hash, err := qetagHash([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
