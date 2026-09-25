package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// versionServer serves publish/state and file_list?version=<n> for one site,
// the two calls a snapshot pull makes. Asking for the workspace (no version)
// is a test failure: a snapshot must never read it.
func versionServer(t *testing.T, versions map[string][]creght.File) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/publish/state"):
			state := creght.SitePublishState{ReadOnly: true, CurrentVersionID: 102, CurrentVersionNo: 2}
			state.Versions = []creght.SiteVersion{
				{ID: 102, VersionNo: 2, Note: "second", CreatedAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)},
				{ID: 101, VersionNo: 1, Note: "first", CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
			}
			_ = json.NewEncoder(w).Encode(state)
		case strings.HasSuffix(r.URL.Path, "/file_list"):
			version := r.URL.Query().Get("version")
			if version == "" {
				t.Errorf("snapshot pull read the workspace file list")
			}
			files := versions[version]
			if files == nil {
				files = []creght.File{}
			}
			_ = json.NewEncoder(w).Encode(creght.FileListResponse{List: files})
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("CREGHT_API_HOST", server.URL)
	return server
}

func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel == "." || rel == ".creght" || strings.HasPrefix(rel, ".creght/") {
			if d.IsDir() && rel == ".creght" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			rel += "/"
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestPullVersionSnapshotMatchesEachVersionExactly(t *testing.T) {
	versionServer(t, map[string][]creght.File{
		"1": {
			{Path: "/page/Index.tsx", Body: "v1 index"},
			{Path: "/page/old/Gone.tsx", Body: "only in v1"},
			{Path: "/component/Nav", Body: "nav was a file in v1"},
			{Path: "/types/cms.d.ts", Body: "generated", Readonly: true},
			{Path: "/page", IsDir: true},
		},
		"2": {
			{Path: "/page/Index.tsx", Body: "v2 index"},
			{Path: "/component/Nav/index.tsx", Body: "nav is a dir in v2"},
			{Path: "/public/.well-known/x.txt", Body: "dotfiles are site files too"},
		},
	})
	dir := filepath.Join(t.TempDir(), "tpl")

	captureStdout(t, func() {
		if err := pullVersionSnapshot(context.Background(), "p1", "s1", dir, 1); err != nil {
			t.Fatalf("pull v1: %v", err)
		}
	})
	if got, want := listTree(t, dir), []string{"component/", "component/Nav", "page/", "page/Index.tsx", "page/old/", "page/old/Gone.tsx"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("v1 tree = %v, want %v", got, want)
	}

	// The directory is a git work tree: .git must survive switching versions.
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stray file the user dropped in is not part of v2 either.
	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := pullVersionSnapshot(context.Background(), "p1", "s1", dir, 2); err != nil {
			t.Fatalf("pull v2: %v", err)
		}
	})
	want := []string{".git/", ".git/HEAD", "component/", "component/Nav/", "component/Nav/index.tsx", "page/", "page/Index.tsx", "public/", "public/.well-known/", "public/.well-known/x.txt"}
	if got := listTree(t, dir); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("v2 tree = %v, want %v", got, want)
	}
	if got := readFile(t, filepath.Join(dir, "page", "Index.tsx")); got != "v2 index" {
		t.Fatalf("Index.tsx = %q, want the v2 body", got)
	}
	if !strings.Contains(out, "version 2 (id 102)") || !strings.Contains(out, "removed 3 file(s)") || !strings.Contains(out, "note: second") {
		t.Fatalf("output = %q", out)
	}

	state, hasState, err := loadWorkspaceState(dir)
	if err != nil || !hasState {
		t.Fatalf("state: %v %v", hasState, err)
	}
	if state.Snapshot == nil || state.Snapshot.VersionNo != 2 || state.Snapshot.VersionID != 102 {
		t.Fatalf("snapshot = %+v, want version 2 (id 102)", state.Snapshot)
	}
	if state.SiteID != "p1/s1" || len(state.Files) != 0 {
		t.Fatalf("state = %+v, want site p1/s1 and no base", state)
	}
	if _, err := os.Stat(filepath.Join(dir, stateDirName, baseDirName)); !os.IsNotExist(err) {
		t.Fatalf("snapshot wrote base objects (%v); nothing may merge against a snapshot", err)
	}
}

func TestPullVersionSnapshotRejectsUnknownVersion(t *testing.T) {
	versionServer(t, map[string][]creght.File{})
	dir := filepath.Join(t.TempDir(), "tpl")

	err := pullVersionSnapshot(context.Background(), "p1", "s1", dir, 9)
	if err == nil || !strings.Contains(err.Error(), "has no files") {
		t.Fatalf("err = %v, want an unknown-version error", err)
	}
	if _, statErr := os.Stat(statePath(dir)); !os.IsNotExist(statErr) {
		t.Fatalf("a failed pull left a state file behind")
	}
}

func TestPullVersionSnapshotRefusesWorkspacesAndUnrelatedDirs(t *testing.T) {
	versionServer(t, map[string][]creght.File{"1": {{Path: "/page/Index.tsx", Body: "v1"}}})

	workspace := writeTestWorkspace(t, "p1/s1", []creght.File{testRemoteFile(t, "id", "/page/Index.tsx", "live")}, nil)
	err := pullVersionSnapshot(context.Background(), "p1", "s1", workspace, 1)
	if err == nil || !strings.Contains(err.Error(), "editable workspace") {
		t.Fatalf("err = %v, want refusal to overwrite a workspace", err)
	}
	if got := readFile(t, filepath.Join(workspace, "page", "Index.tsx")); got != "live" {
		t.Fatalf("workspace file changed to %q", got)
	}

	unrelated := t.TempDir()
	if err := os.WriteFile(filepath.Join(unrelated, "notes.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = pullVersionSnapshot(context.Background(), "p1", "s1", unrelated, 1)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("err = %v, want refusal for a non-empty directory", err)
	}

	// A directory holding only .git is empty for this purpose.
	gitOnly := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gitOnly, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() {
		if err := pullVersionSnapshot(context.Background(), "p1", "s1", gitOnly, 1); err != nil {
			t.Fatalf("pull into a fresh git dir: %v", err)
		}
	})
}

func TestSnapshotDirectoryRefusesPushDiffAndPlainPull(t *testing.T) {
	versionServer(t, map[string][]creght.File{"1": {{Path: "/page/Index.tsx", Body: "v1"}}})
	dir := filepath.Join(t.TempDir(), "tpl")
	captureStdout(t, func() {
		if err := pullVersionSnapshot(context.Background(), "p1", "s1", dir, 1); err != nil {
			t.Fatalf("pull: %v", err)
		}
	})

	for name, run := range map[string]func(context.Context, []string) error{
		"push": runPush,
		"diff": runDiff,
		"pull": runPull,
		"rm":   runRemove,
	} {
		args := []string{"--dir=" + dir}
		if name == "rm" {
			args = append(args, "page/Index.tsx")
		}
		var err error
		captureStdout(t, func() { err = run(context.Background(), args) })
		if err == nil || !strings.Contains(err.Error(), "read-only snapshot") {
			t.Fatalf("%s in a snapshot: err = %v, want a read-only refusal", name, err)
		}
	}

	// The syncer refuses too, which covers version create's dirty check.
	syncer, err := NewSyncer(creght.NewClient("http://unused", ""), "p1", "s1", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer.requireWorkspace(); err == nil || !strings.Contains(err.Error(), "read-only snapshot") {
		t.Fatalf("requireWorkspace = %v, want a read-only refusal", err)
	}
}

func TestRunPullVersionNoFromInsideSnapshotSwitchesVersion(t *testing.T) {
	versionServer(t, map[string][]creght.File{
		"1": {{Path: "/page/Index.tsx", Body: "v1"}},
		"2": {{Path: "/page/Index.tsx", Body: "v2"}},
	})
	dir := filepath.Join(t.TempDir(), "tpl")
	captureStdout(t, func() {
		if err := runPull(context.Background(), []string{"--site_id=p1/s1", "--dir=" + dir, "--version_no=1"}); err != nil {
			t.Fatalf("pull v1: %v", err)
		}
		// Second pull names only the version: the site comes from the snapshot state.
		if err := runPull(context.Background(), []string{"--dir=" + dir, "--version_no=2"}); err != nil {
			t.Fatalf("pull v2: %v", err)
		}
	})
	if got := readFile(t, filepath.Join(dir, "page", "Index.tsx")); got != "v2" {
		t.Fatalf("Index.tsx = %q, want v2", got)
	}

	for _, bad := range []string{"0", "-1", "abc", "id:5"} {
		if err := runPull(context.Background(), []string{"--dir=" + dir, "--version_no=" + bad}); err == nil || !strings.Contains(err.Error(), "invalid --version_no") {
			t.Fatalf("--version_no=%s: err = %v, want invalid", bad, err)
		}
	}
	if err := runPull(context.Background(), []string{"--dir=" + dir, "--version_no=2", "page/Index.tsx"}); err == nil || !strings.Contains(err.Error(), "does not take a <path>") {
		t.Fatalf("--version_no with a path: err = %v", err)
	}
}

func TestPrintVersionListReadOnlyView(t *testing.T) {
	var out strings.Builder
	printVersionList(&out, creght.SitePublishState{
		ReadOnly:         true,
		CurrentVersionID: 102,
		CurrentVersionNo: 2,
		Versions:         []creght.SiteVersion{{ID: 102, VersionNo: 2, Note: "second"}},
	}, 0)
	got := out.String()
	if !strings.Contains(got, "* live: version 2 (id 102)") || !strings.Contains(got, "Read-only") || !strings.Contains(got, "--version_no") {
		t.Fatalf("output = %q", got)
	}

	out.Reset()
	printVersionList(&out, creght.SitePublishState{ReadOnly: true}, 0)
	if strings.Contains(out.String(), "version create") {
		t.Fatalf("read-only view suggests version create, which a non-member cannot run: %q", out.String())
	}
}
