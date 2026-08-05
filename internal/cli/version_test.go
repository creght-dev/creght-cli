package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bysir/creght-cli/internal/creght"
)

func testPublishState() creght.SitePublishState {
	return creght.SitePublishState{
		Versions: []creght.SiteVersion{
			{ID: 456, VersionNo: 12, Note: "Add pricing"},
			{ID: 455, VersionNo: 11, Note: "Fix nav"},
		},
		CurrentVersionID: 455,
		CurrentVersionNo: 11,
	}
}

func TestResolveVersionSelector(t *testing.T) {
	state := testPublishState()

	tests := []struct {
		name     string
		selector string
		wantID   int64
		wantErr  string
	}{
		{name: "version number resolves to its id", selector: "12", wantID: 456},
		{name: "older version number", selector: "11", wantID: 455},
		{name: "surrounding spaces are ignored", selector: "  12  ", wantID: 456},
		{name: "id prefix selects by id", selector: "id:455", wantID: 455},
		{name: "uppercase id prefix", selector: "ID:455", wantID: 455},
		{name: "hash prefix selects by id", selector: "#456", wantID: 456},
		// An id outside the listed window is still publishable: the server
		// validates that it belongs to this site.
		{name: "unlisted id passes through", selector: "id:99", wantID: 99},
		{name: "unknown version number is refused", selector: "99", wantErr: "not found"},
		{name: "non numeric is refused", selector: "latest", wantErr: "expected a positive <version_no>"},
		{name: "zero is refused", selector: "0", wantErr: "expected a positive <version_no>"},
		{name: "negative is refused", selector: "-3", wantErr: "expected a positive <version_no>"},
		{name: "non numeric id is refused", selector: "id:abc", wantErr: "expected id:<version_id>"},
		{name: "zero id is refused", selector: "id:0", wantErr: "expected id:<version_id>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, err := resolveVersionSelector(state, tt.selector)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("resolveVersionSelector(%q) = %+v, want error", tt.selector, version)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveVersionSelector(%q): %v", tt.selector, err)
			}
			if version.ID != tt.wantID {
				t.Fatalf("resolveVersionSelector(%q) = id %d, want id %d", tt.selector, version.ID, tt.wantID)
			}
		})
	}
}

// A bare number must never be read as a version id: version 11 and id 11 are
// different versions, and publishing the wrong one changes the live site.
func TestResolveVersionSelectorPrefersVersionNumberOverID(t *testing.T) {
	state := creght.SitePublishState{
		Versions: []creght.SiteVersion{
			{ID: 11, VersionNo: 3},
			{ID: 9, VersionNo: 11},
		},
	}

	version, err := resolveVersionSelector(state, "11")
	if err != nil {
		t.Fatalf("resolveVersionSelector: %v", err)
	}
	if version.ID != 9 {
		t.Fatalf("selector %q resolved to id %d, want id 9 (the version numbered 11)", "11", version.ID)
	}
}

// fileListServer serves the site file_list endpoint the dirty check reads.
func fileListServer(t *testing.T, files []creght.File) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/file_list") {
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(creght.FileListResponse{List: files})
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

// writeTestWorkspace creates a pulled workspace whose base state matches the
// given remote files, then applies local edits on top.
func writeTestWorkspace(t *testing.T, siteRef string, remote []creght.File, localEdits map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	for _, file := range remote {
		path := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(file.Path, "/")))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(file.Body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveWorkspaceState(dir, siteRef, remoteFileSnapshot(remote)); err != nil {
		t.Fatalf("saveWorkspaceState: %v", err)
	}

	for path, body := range localEdits {
		full := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(path, "/")))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if body == "" {
			if err := os.Remove(full); err != nil {
				t.Fatalf("remove %s: %v", path, err)
			}
			continue
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func testRemoteFile(t *testing.T, id string, path string, body string) creght.File {
	t.Helper()
	hash, err := qetagHash([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return creght.File{ID: id, Path: path, Body: body, Hash: hash}
}

func TestRequirePushedWorkspaceBlocksUnpushedEdit(t *testing.T) {
	remote := []creght.File{testRemoteFile(t, "index-id", "/page/Index.tsx", "old\n")}
	server, _ := fileListServer(t, remote)
	dir := writeTestWorkspace(t, "project-1/site-1", remote, map[string]string{
		"/page/Index.tsx": "edited locally\n",
	})

	client := creght.NewClient(server.URL, "")
	err := requirePushedWorkspace(context.Background(), client, "project-1", "site-1", dir)
	if err == nil {
		t.Fatalf("requirePushedWorkspace = nil, want refusal for an unpushed edit")
	}
	if !strings.Contains(err.Error(), "creght push") {
		t.Fatalf("error = %q, want it to point at creght push", err)
	}
	if !strings.Contains(err.Error(), "--allow-dirty") {
		t.Fatalf("error = %q, want it to mention the --allow-dirty escape hatch", err)
	}
}

func TestRequirePushedWorkspaceBlocksUnpushedCreateAndDelete(t *testing.T) {
	remote := []creght.File{
		testRemoteFile(t, "index-id", "/page/Index.tsx", "index\n"),
		testRemoteFile(t, "stale-id", "/page/Stale.tsx", "stale\n"),
	}
	server, _ := fileListServer(t, remote)
	dir := writeTestWorkspace(t, "project-1/site-1", remote, map[string]string{
		"/page/Pricing.tsx": "new page\n",
		"/page/Stale.tsx":   "", // deleted locally
	})

	client := creght.NewClient(server.URL, "")
	err := requirePushedWorkspace(context.Background(), client, "project-1", "site-1", dir)
	if err == nil {
		t.Fatalf("requirePushedWorkspace = nil, want refusal for an unpushed create and delete")
	}
	// Both the new file and the local deletion have to count, or a version would
	// silently miss them.
	if !strings.Contains(err.Error(), "2 changes are") {
		t.Fatalf("error = %q, want it to report both pending changes", err)
	}
}

func TestRequirePushedWorkspaceAllowsCleanWorkspace(t *testing.T) {
	remote := []creght.File{testRemoteFile(t, "index-id", "/page/Index.tsx", "index\n")}
	server, _ := fileListServer(t, remote)
	dir := writeTestWorkspace(t, "project-1/site-1", remote, nil)

	client := creght.NewClient(server.URL, "")
	if err := requirePushedWorkspace(context.Background(), client, "project-1", "site-1", dir); err != nil {
		t.Fatalf("requirePushedWorkspace on a clean workspace: %v", err)
	}
}

// Outside a pulled workspace there is no local copy to compare, so the check
// must pass without even reaching the API.
func TestRequirePushedWorkspaceSkipsWithoutWorkspaceState(t *testing.T) {
	server, calls := fileListServer(t, nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	client := creght.NewClient(server.URL, "")
	if err := requirePushedWorkspace(context.Background(), client, "project-1", "site-1", dir); err != nil {
		t.Fatalf("requirePushedWorkspace outside a workspace: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("file_list called %d time(s), want none outside a workspace", *calls)
	}
}

func TestVersionCommandKeepsCLIVersionAndAddsSubcommands(t *testing.T) {
	root := newRootCommand(context.Background(), nil)

	cmd, _, err := root.Find([]string{"version"})
	if err != nil {
		t.Fatalf("find version: %v", err)
	}
	if cmd.Name() != "version" {
		t.Fatalf("resolved %q, want the version command", cmd.Name())
	}
	// Bare `creght version` must keep printing the CLI version.
	if cmd.Run == nil {
		t.Fatalf("version command has no bare-invocation handler")
	}

	for _, name := range []string{"create", "list", "publish"} {
		sub, _, err := root.Find([]string{"version", name})
		if err != nil {
			t.Fatalf("find version %s: %v", name, err)
		}
		if sub.Name() != name {
			t.Fatalf("version %s resolved to %q", name, sub.Name())
		}
		siteFlag := sub.Flags().Lookup("site_id")
		if siteFlag == nil || !strings.Contains(siteFlag.Usage, "Optional inside a pulled workspace") {
			t.Fatalf("version %s --site_id help does not explain optional usage", name)
		}
		if sub.Flags().Lookup("dir") == nil {
			t.Fatalf("version %s is missing --dir", name)
		}
		if sub.Flags().Lookup("json") == nil {
			t.Fatalf("version %s is missing --json", name)
		}
	}

	create, _, _ := root.Find([]string{"version", "create"})
	if create.Flags().Lookup("allow-dirty") == nil {
		t.Fatalf("version create is missing --allow-dirty")
	}
	if !strings.Contains(create.Long, "unpushed") {
		t.Fatalf("version create long help does not explain the unpushed-changes check: %q", create.Long)
	}
	publish, _, _ := root.Find([]string{"version", "publish"})
	if !strings.Contains(publish.Long, "id:<version_id>") {
		t.Fatalf("version publish long help does not document the id selector: %q", publish.Long)
	}
	// A --version flag here would be swallowed by the root's "print the CLI
	// version" scan, silently printing a version string instead of publishing.
	if publish.Flags().Lookup("version") != nil {
		t.Fatalf("version publish must not define a --version flag; the root scan for --version would swallow it")
	}
}

// hasVersionArg runs before cobra, so it must not hijack a subcommand that
// merely carries a version selector.
func TestHasVersionArgDoesNotHijackVersionPublish(t *testing.T) {
	hijacked := []struct {
		name string
		args []string
	}{
		{name: "publish by number", args: []string{"version", "publish", "12"}},
		{name: "publish by id", args: []string{"version", "publish", "id:456"}},
		{name: "publish with note", args: []string{"version", "publish", "12", "--note=x"}},
		{name: "create", args: []string{"version", "create", "--note=x"}},
		{name: "list", args: []string{"version", "list", "--json"}},
	}
	for _, tt := range hijacked {
		t.Run(tt.name, func(t *testing.T) {
			if hasVersionArg(tt.args) {
				t.Fatalf("hasVersionArg(%v) = true, want false so the subcommand runs", tt.args)
			}
		})
	}

	for _, args := range [][]string{{"-v"}, {"--version"}} {
		if !hasVersionArg(args) {
			t.Fatalf("hasVersionArg(%v) = false, want true for the CLI version flags", args)
		}
	}
}

func TestRunVersionRejectsUnknownSubcommand(t *testing.T) {
	err := runVersion(context.Background(), []string{"rollback"})
	if err == nil || !strings.Contains(err.Error(), "unknown version command") {
		t.Fatalf("runVersion(rollback) = %v, want an unknown-command error", err)
	}
}

func TestPrintVersionListMarksLiveVersionAndPendingChanges(t *testing.T) {
	created := time.Date(2026, 8, 5, 14, 31, 0, 0, time.Local)
	state := creght.SitePublishState{
		Versions: []creght.SiteVersion{
			{ID: 456, VersionNo: 12, Note: "Add pricing page", From: "api_generate_version", CreatedAt: created},
			{ID: 455, VersionNo: 11, Note: "Fix nav", From: "publish", CreatedAt: created.Add(-3 * time.Hour)},
		},
		CurrentVersionID: 455,
		CurrentVersionNo: 11,
		PublishTargets:   []string{"demo.creght.cn"},
		Domains: []creght.SitePublishDomain{
			{ID: 0, Domain: "demo.creght.cn", System: true, Follow: true},
			{ID: 7, Domain: "www.example.com", PublishVersionID: 456, PublishVersionNo: 12},
		},
		HasChanges:       true,
		WorkspaceChanges: []creght.SiteFileChange{{Path: "/page/Index.tsx", ChangeAction: "update"}},
	}

	var out strings.Builder
	printVersionList(&out, state, 0)
	got := out.String()

	for _, want := range []string{
		"VERSION", "12", "456", "2026-08-05 14:31", "api_generate_version", "Add pricing page",
		"* live: version 11 (id 455), served by demo.creght.cn",
		"pinned: www.example.com -> version 12 (id 456)",
		"1 change is pending on the site since the newest version",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}

	// The live marker belongs on version 11, not on the newest version.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "455") && !strings.HasPrefix(line, "*") {
			t.Fatalf("live version row is not marked:\n%s", got)
		}
		if strings.Contains(line, "456") && strings.HasPrefix(line, "*") {
			t.Fatalf("unpublished version row is marked live:\n%s", got)
		}
	}
}

func TestPrintVersionListLimitAndEmptyStates(t *testing.T) {
	state := testPublishState()

	var limited strings.Builder
	printVersionList(&limited, state, 1)
	if !strings.Contains(limited.String(), "... 1 more version(s)") {
		t.Fatalf("--limit did not report the truncated versions:\n%s", limited.String())
	}
	if strings.Contains(limited.String(), "Fix nav") {
		t.Fatalf("--limit=1 printed a second version:\n%s", limited.String())
	}

	var empty strings.Builder
	printVersionList(&empty, creght.SitePublishState{}, 0)
	if !strings.Contains(empty.String(), "No versions yet") {
		t.Fatalf("empty site output = %q", empty.String())
	}
	if !strings.Contains(empty.String(), "Nothing published yet") {
		t.Fatalf("empty site output does not say nothing is live: %q", empty.String())
	}
}

func TestVersionLabelAndSelector(t *testing.T) {
	if got := versionLabel(12, 456); got != "version 12 (id 456)" {
		t.Fatalf("versionLabel(12, 456) = %q", got)
	}
	// Legacy visual-editor sites have no per-site number.
	if got := versionLabel(0, 456); got != "version id 456" {
		t.Fatalf("versionLabel(0, 456) = %q", got)
	}
	if got := publishSelector(12, 456); got != "12" {
		t.Fatalf("publishSelector(12, 456) = %q, want the version number", got)
	}
	if got := publishSelector(0, 456); got != "id:456" {
		t.Fatalf("publishSelector(0, 456) = %q, want an id selector", got)
	}
}
