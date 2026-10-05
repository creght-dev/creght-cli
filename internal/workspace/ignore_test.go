package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/creght-dev/creght-cli/internal/creght"
)

func writeIgnoreTestFile(t *testing.T, root string, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, CreghtIgnoreFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCreghtIgnorePatterns(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreTestFile(t, dir, `
# generated output
cache/*
!cache/keep.txt
*.tmp
/root-only.txt
docs/
`)

	ignore, err := LoadCreghtIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]bool{
		"/.creghtignore":        true,
		"/cache/item.json":      true,
		"/cache/nested/a.json":  true,
		"/cache/keep.txt":       false,
		"/src/result.tmp":       true,
		"/root-only.txt":        true,
		"/nested/root-only.txt": false,
		"/docs/guide.md":        true,
		"/src/index.ts":         false,
	}
	for path, want := range tests {
		if got := ignore.Matches(path); got != want {
			t.Errorf("matches(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestWalkWorkspaceFilesUsesCreghtIgnore(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreTestFile(t, dir, "generated/*\n!generated/keep.ts\n")
	for path, body := range map[string]string{
		"generated/drop.ts":        "drop\n",
		"generated/nested/drop.ts": "drop nested\n",
		"generated/keep.ts":        "keep\n",
		"page/Index.tsx":           "page\n",
	} {
		localPath := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(localPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var got []string
	if err := WalkWorkspaceFiles(dir, func(path string) error {
		remotePath, err := LocalPathToRemote(dir, path)
		if err == nil {
			got = append(got, remotePath)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{"/generated/keep.ts": true, "/page/Index.tsx": true}
	if len(got) != len(want) {
		t.Fatalf("walked %v, want exactly %v", got, want)
	}
	for _, path := range got {
		if !want[path] {
			t.Errorf("unexpected walked path %s", path)
		}
	}
}

func TestIgnoredLegacyFrontendDirectoryDoesNotBlockWalk(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreTestFile(t, dir, "frontend/*\n")
	path := filepath.Join(dir, "frontend", "page", "Index.tsx")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WalkWorkspaceFiles(dir, func(string) error {
		t.Fatal("ignored legacy frontend file was walked")
		return nil
	}); err != nil {
		t.Fatalf("ignored legacy frontend directory blocked walk: %v", err)
	}
}

func TestEnsurePulledAgentsFileHonorsCreghtIgnore(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreTestFile(t, dir, "AGENTS.md\n")

	created, err := EnsurePulledAgentsFile(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("ignored AGENTS.md was generated")
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("ignored AGENTS.md stat error = %v, want not exist", err)
	}
}

func TestSafePullWorkspacePreservesIgnoredFiles(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreTestFile(t, dir, "generated/*\n")
	ignoredPath := filepath.Join(dir, "generated", "local.txt")
	if err := os.MkdirAll(filepath.Dir(ignoredPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ignoredPath, []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	remote := RemoteFileSnapshot([]creght.File{
		{Path: "/generated/local.txt", Body: "remote\n"},
		{Path: "/page/Index.tsx", Body: "page\n"},
		{Path: "/.creghtignore", Body: "remote config\n"},
	})
	outcome, err := SafePullWorkspace(dir, "project/site", "", remote, os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.changed != 1 {
		t.Fatalf("changed = %d, want 1", outcome.changed)
	}
	body, err := os.ReadFile(ignoredPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "local\n" {
		t.Fatalf("ignored file overwritten with %q", body)
	}
	state, ok, err := LoadWorkspaceState(dir)
	if err != nil || !ok {
		t.Fatalf("load state: ok=%v err=%v", ok, err)
	}
	if _, exists := state.Files["/generated/local.txt"]; exists {
		t.Fatal("ignored remote file was recorded in state")
	}
	if _, exists := state.Files["/.creghtignore"]; exists {
		t.Fatal(".creghtignore was recorded in state")
	}
	if _, exists := state.Files["/page/Index.tsx"]; !exists {
		t.Fatal("regular remote file missing from state")
	}
}

func TestForcePullWriterPreservesIgnoredFiles(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreTestFile(t, dir, "generated/*\n")
	ignoredPath := filepath.Join(dir, "generated", "local.txt")
	if err := os.MkdirAll(filepath.Dir(ignoredPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ignoredPath, []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteRemoteFilesToWorkspace(dir, []creght.File{
		{Path: "/generated/local.txt", Body: "remote\n"},
		{Path: "/page/Index.tsx", Body: "page\n"},
	}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(ignoredPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "local\n" {
		t.Fatalf("force pull overwrote ignored file with %q", body)
	}
	if _, err := os.Stat(filepath.Join(dir, "page", "Index.tsx")); err != nil {
		t.Fatalf("regular remote file was not written: %v", err)
	}
}

func TestCollectLocalSnapshotActionsPreservesIgnoredRemoteFiles(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreTestFile(t, dir, "generated/*\n")
	if err := os.WriteFile(filepath.Join(dir, "page.tsx"), []byte("page\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Syncer{
		dir: dir,
		remoteByPath: map[string]creght.File{
			"/generated/remote.txt": {Path: "/generated/remote.txt", Hash: "remote-hash"},
		},
	}
	actions, err := s.collectLocalSnapshotActions()
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range actions {
		if action.RemotePath == "/generated/remote.txt" {
			t.Fatal("push attempted to delete ignored remote file")
		}
		if action.RemotePath == "/.creghtignore" {
			t.Fatal("push attempted to upload .creghtignore")
		}
	}
}

func TestIgnoredRemotePathsNamesHiddenRemoteFiles(t *testing.T) {
	dir := t.TempDir()
	writeIgnoreTestFile(t, dir, "docs/\noutput/*\n")
	ignore, err := LoadCreghtIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}

	remote := []creght.File{
		{Path: "/page/Index.tsx"},
		{Path: "/docs/a.md"},
		{Path: "/docs/sub/b.md"},
		{Path: "/output/shot.png"},
		{Path: "/.creghtignore"},
		{Path: "/docs", IsDir: true},
	}
	got := IgnoredRemotePaths(ignore, remote)
	want := []string{"/docs/a.md", "/docs/sub/b.md", "/output/shot.png"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestIgnoredRemotePathsEmptyWithoutRules(t *testing.T) {
	dir := t.TempDir()
	ignore, err := LoadCreghtIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	remote := []creght.File{{Path: "/page/Index.tsx"}}
	if got := IgnoredRemotePaths(ignore, remote); len(got) != 0 {
		t.Fatalf("expected no ignored paths, got %v", got)
	}
}

func TestDropStateFileEntryRemovesBaseEntry(t *testing.T) {
	dir := t.TempDir()
	files := map[string]SnapshotEntry{
		"/page/Index.tsx": {Path: "/page/Index.tsx", Hash: "h1", Body: "a"},
		"/docs/a.md":      {Path: "/docs/a.md", Hash: "h2", Body: "b"},
	}
	if err := SaveWorkspaceState(dir, "pid/sid", "", files); err != nil {
		t.Fatal(err)
	}
	if err := DropStateFileEntry(dir, "/docs/a.md"); err != nil {
		t.Fatal(err)
	}
	state, hasState, err := LoadWorkspaceState(dir)
	if err != nil || !hasState {
		t.Fatalf("state not readable: %v", err)
	}
	if _, ok := state.Files["/docs/a.md"]; ok {
		t.Fatal("base entry survived dropStateFileEntry")
	}
	if _, ok := state.Files["/page/Index.tsx"]; !ok {
		t.Fatal("dropStateFileEntry removed an unrelated entry")
	}
}
