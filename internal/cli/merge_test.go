package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMerge3NonOverlappingEditsMergeCleanly(t *testing.T) {
	base := "a\nb\nc\nd\ne\n"
	local := "A\nb\nc\nd\ne\n"
	remote := "a\nb\nc\nd\nE\n"

	merged, clean := merge3(base, local, remote)
	if !clean {
		t.Fatalf("expected clean merge, got conflict:\n%s", merged)
	}
	if merged != "A\nb\nc\nd\nE\n" {
		t.Fatalf("merged = %q, want both edits applied", merged)
	}
}

func TestMerge3IdenticalEditsMergeCleanly(t *testing.T) {
	base := "a\nb\n"
	changed := "X\nb\n"

	merged, clean := merge3(base, changed, changed)
	if !clean || merged != changed {
		t.Fatalf("merged = %q clean=%v, want identical edit taken once", merged, clean)
	}
}

func TestMerge3LocalOnlyChangeKeepsLocal(t *testing.T) {
	base := "a\nb\n"
	local := "a\nB\n"

	merged, clean := merge3(base, local, base)
	if !clean || merged != local {
		t.Fatalf("merged = %q clean=%v, want local kept", merged, clean)
	}
}

func TestMerge3OverlappingEditsWriteMarkers(t *testing.T) {
	base := "a\nb\nc\n"
	local := "a\nLOCAL\nc\n"
	remote := "a\nREMOTE\nc\n"

	merged, clean := merge3(base, local, remote)
	if clean {
		t.Fatalf("expected conflict, got clean merge: %q", merged)
	}
	want := "a\n" + conflictMarkerLocal + "\nLOCAL\n" + conflictMarkerSep + "\nREMOTE\n" + conflictMarkerRemote + "\nc\n"
	if merged != want {
		t.Fatalf("merged = %q, want %q", merged, want)
	}
	if !hasConflictMarkers(merged) {
		t.Fatal("merged output not detected by hasConflictMarkers")
	}
}

func TestMerge3InsertionsAtSamePointConflict(t *testing.T) {
	base := "a\nb\n"
	local := "a\nx\nb\n"
	remote := "a\ny\nb\n"

	merged, clean := merge3(base, local, remote)
	if clean {
		t.Fatalf("expected conflict for competing insertions, got %q", merged)
	}
}

func TestResolveConflictBodyKeepsChosenSide(t *testing.T) {
	base := "a\nb\nc\n"
	local := "a\nLOCAL\nc\n"
	remote := "a\nREMOTE\nc\n"
	merged, _ := merge3(base, local, remote)

	ours, count, err := resolveConflictBody(merged, true)
	if err != nil || count != 1 || ours != local {
		t.Fatalf("ours = %q count=%d err=%v, want local body", ours, count, err)
	}
	theirs, count, err := resolveConflictBody(merged, false)
	if err != nil || count != 1 || theirs != remote {
		t.Fatalf("theirs = %q count=%d err=%v, want remote body", theirs, count, err)
	}
}

func TestResolveConflictBodyWithoutMarkersErrors(t *testing.T) {
	if _, _, err := resolveConflictBody("plain\ntext\n", true); err == nil {
		t.Fatal("expected error for body without markers")
	}
}

func TestHasConflictMarkersIgnoresPartialMarkers(t *testing.T) {
	for _, body := range []string{
		"a\n=======\nb\n",
		"<<<<<<< local\nno separator or end\n",
		"doc says use >>>>>>> remote to end a block\n",
	} {
		if hasConflictMarkers(body) {
			t.Fatalf("hasConflictMarkers(%q) = true, want false", body)
		}
	}
}

func TestRunResolveFromSubdirectoryUsesCwdRelativePath(t *testing.T) {
	dir := t.TempDir()
	if err := saveWorkspaceState(dir, "project/site", map[string]snapshotEntry{}); err != nil {
		t.Fatal(err)
	}
	merged, _ := merge3("a\nb\nc\n", "a\nLOCAL\nc\n", "a\nREMOTE\nc\n")
	pageDir := filepath.Join(dir, "page")
	if err := os.MkdirAll(pageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pagePath := filepath.Join(pageDir, "Index.tsx")
	if err := os.WriteFile(pagePath, []byte(merged), 0o644); err != nil {
		t.Fatal(err)
	}

	chdir(t, pageDir)
	// No --dir: the workspace root is discovered by walking up, and the
	// relative path resolves against the current directory.
	if err := runResolve(context.Background(), []string{"Index.tsx", "--theirs"}); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "a\nREMOTE\nc\n" {
		t.Fatalf("resolved body = %q, want remote side", string(body))
	}
}

func TestRunResolveOursRewritesFile(t *testing.T) {
	dir := t.TempDir()
	if err := saveWorkspaceState(dir, "project/site", map[string]snapshotEntry{}); err != nil {
		t.Fatal(err)
	}
	merged, _ := merge3("a\nb\nc\n", "a\nLOCAL\nc\n", "a\nREMOTE\nc\n")
	pagePath := filepath.Join(dir, "page", "Index.tsx")
	if err := os.MkdirAll(filepath.Dir(pagePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pagePath, []byte(merged), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runResolve(context.Background(), []string{"page/Index.tsx", "--ours", "--dir=" + dir}); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "a\nLOCAL\nc\n" {
		t.Fatalf("resolved body = %q, want local side", string(body))
	}
	if strings.Contains(string(body), conflictMarkerSep) {
		t.Fatal("markers left after resolve")
	}
}
