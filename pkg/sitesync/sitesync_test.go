package sitesync_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/br41n10/qetag"

	"github.com/creght-dev/creght-cli/internal/cli"
	"github.com/creght-dev/creght-cli/internal/creght"
	"github.com/creght-dev/creght-cli/pkg/sitesync"
)

// fakeSite is one site's live files plus its versions, served the way the
// platform serves them to the CLI.
type fakeSite struct {
	t        *testing.T
	mu       sync.Mutex
	files    map[string]string            // path -> body
	versions map[string]map[string]string // version_no -> path -> body
	nextID   int
	ids      map[string]string // path -> id
	tokens   []string          // Authorization seen, in order
	accept   func(token string) bool
}

func newFakeSite(t *testing.T, files map[string]string) (*fakeSite, *httptest.Server) {
	t.Helper()
	f := &fakeSite{t: t, files: map[string]string{}, versions: map[string]map[string]string{}, ids: map[string]string{}}
	for p, b := range files {
		f.put(p, b)
	}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	return f, server
}

func (f *fakeSite) put(path, body string) {
	if _, ok := f.ids[path]; !ok {
		f.nextID++
		f.ids[path] = fmt.Sprintf("f%d", f.nextID)
	}
	f.files[path] = body
}

// edit changes a live file as the web editor would.
func (f *fakeSite) edit(path, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put(path, body)
}

func (f *fakeSite) body(path string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[path]
	return b, ok
}

func hash(body string) string {
	qe := qetag.New()
	_, _ = qe.Write([]byte(body))
	return qe.Etag()
}

func (f *fakeSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.tokens = append(f.tokens, auth)
	if f.accept != nil && !f.accept(auth) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"message":"请登录后操作"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/file_list"):
		src := f.files
		if v := r.URL.Query().Get("version"); v != "" {
			src = f.versions[v]
		}
		list := []creght.File{}
		for p, b := range src {
			list = append(list, creght.File{ID: f.ids[p], Path: p, Body: b, Hash: hash(b)})
		}
		_ = json.NewEncoder(w).Encode(creght.FileListResponse{List: list})
	case strings.HasSuffix(r.URL.Path, "/site_action"):
		var req struct {
			Changes []creght.SiteActionChange `json:"changes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.t.Errorf("decode site_action: %v", err)
		}
		for _, c := range req.Changes {
			switch c.Action {
			case "file_create":
				f.put(*c.File.Path, *c.File.Body)
			case "file_update":
				for p, id := range f.ids {
					if id == c.File.ID {
						f.files[p] = *c.File.Body
					}
				}
			case "file_delete":
				delete(f.files, *c.File.Path)
			}
		}
		n := len(req.Changes)
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"total":%d,"success":%d,"failed":0}}`, n, n)
	case strings.HasSuffix(r.URL.Path, "/publish/state"):
		_ = json.NewEncoder(w).Encode(creght.SitePublishState{
			Versions:         []creght.SiteVersion{{ID: 102, VersionNo: 2, Note: "second"}, {ID: 101, VersionNo: 1, Note: "first"}},
			CurrentVersionID: 102,
			CurrentVersionNo: 2,
		})
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":404,"message":"not found"}`))
	}
}

var site = sitesync.Site{ProjectID: "p1", SiteID: "s1"}

func newClient(t *testing.T, server *httptest.Server, log io.Writer) *sitesync.Client {
	t.Helper()
	c, err := sitesync.New(sitesync.Options{
		Host:  server.URL,
		Token: func(context.Context) (string, error) { return "tok", nil },
		Log:   log,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func statuses(res sitesync.DiffResult) map[string]string {
	out := map[string]string{}
	for _, e := range res.Files {
		out[e.Path] = e.Status + ":" + e.Action
	}
	return out
}

func TestPullDiffPushRoundTrip(t *testing.T) {
	fake, server := newFakeSite(t, map[string]string{
		"/page/Index.tsx": "export default () => 'home'\n",
		"/page/Old.tsx":   "old\n",
	})
	var log bytes.Buffer
	c := newClient(t, server, &log)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "site")

	res, err := c.Pull(ctx, dir, site, sitesync.PullOptions{SkipAgentsFile: true})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if res.Changed != 2 || len(res.Conflicted) != 0 {
		t.Fatalf("Pull = %+v, want 2 clean changes", res)
	}
	if got := readFile(t, filepath.Join(dir, "page", "Index.tsx")); got != "export default () => 'home'\n" {
		t.Fatalf("Index.tsx = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("SkipAgentsFile still wrote AGENTS.md (%v)", err)
	}
	ws, err := sitesync.OpenWorkspace(dir)
	if err != nil {
		t.Fatalf("OpenWorkspace: %v", err)
	}
	if ws.Site != site || ws.APIHost != server.URL {
		t.Fatalf("workspace = %+v, want site p1/s1 stamped with %s", ws, server.URL)
	}

	writeFile(t, filepath.Join(dir, "page", "Index.tsx"), "export default () => 'new home'\n")
	writeFile(t, filepath.Join(dir, "page", "About.tsx"), "about\n")
	if err := os.Remove(filepath.Join(dir, "page", "Old.tsx")); err != nil {
		t.Fatal(err)
	}

	diff, err := c.Diff(ctx, dir, sitesync.DiffOptions{Delete: true})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	want := map[string]string{
		"/page/Index.tsx": "local-change:file_update",
		"/page/About.tsx": "local-change:file_create",
		"/page/Old.tsx":   "local-change:file_delete",
	}
	if got := statuses(diff); fmt.Sprint(got) != fmt.Sprint(want) || diff.HasConflicts {
		t.Fatalf("Diff = %v (conflicts %v), want %v", got, diff.HasConflicts, want)
	}

	pushed, err := c.Push(ctx, dir, sitesync.PushOptions{Delete: true})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(pushed.Changes) != 3 {
		t.Fatalf("Push changes = %+v, want 3", pushed.Changes)
	}
	if b, _ := fake.body("/page/Index.tsx"); b != "export default () => 'new home'\n" {
		t.Fatalf("remote Index.tsx = %q", b)
	}
	if _, ok := fake.body("/page/Old.tsx"); ok {
		t.Fatalf("remote Old.tsx still there")
	}
	if !strings.Contains(log.String(), "synced 3 files") {
		t.Fatalf("log = %q, want the push summary", log.String())
	}

	diff, err = c.Diff(ctx, dir, sitesync.DiffOptions{Delete: true})
	if err != nil || len(diff.Files) != 0 {
		t.Fatalf("Diff after push = %+v, %v; want nothing left", diff, err)
	}
}

func TestPullMergesAndResolveSettlesConflicts(t *testing.T) {
	fake, server := newFakeSite(t, map[string]string{
		"/page/a.tsx": "one\ntwo\nthree\nfour\nfive\n",
		"/page/b.tsx": "title\nbody\n",
	})
	c := newClient(t, server, nil)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "site")
	if _, err := c.Pull(ctx, dir, site, sitesync.PullOptions{}); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	// a.tsx: edits on different lines merge. b.tsx: the same line conflicts.
	writeFile(t, filepath.Join(dir, "page", "a.tsx"), "ONE\ntwo\nthree\nfour\nfive\n")
	fake.edit("/page/a.tsx", "one\ntwo\nthree\nfour\nFIVE\n")
	writeFile(t, filepath.Join(dir, "page", "b.tsx"), "local title\nbody\n")
	fake.edit("/page/b.tsx", "remote title\nbody\n")

	diff, err := c.Diff(ctx, dir, sitesync.DiffOptions{})
	if err != nil || !diff.HasConflicts {
		t.Fatalf("Diff = %+v, %v; want conflicts before pulling", diff, err)
	}
	if _, err := c.Push(ctx, dir, sitesync.PushOptions{}); err == nil {
		t.Fatalf("Push with conflicts succeeded")
	}

	res, err := c.Pull(ctx, dir, site, sitesync.PullOptions{})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if fmt.Sprint(res.Merged) != "[/page/a.tsx]" || fmt.Sprint(res.Conflicted) != "[/page/b.tsx]" {
		t.Fatalf("Pull = %+v, want a.tsx merged and b.tsx conflicted", res)
	}
	if got := readFile(t, filepath.Join(dir, "page", "a.tsx")); got != "ONE\ntwo\nthree\nfour\nFIVE\n" {
		t.Fatalf("merged a.tsx = %q", got)
	}
	b := readFile(t, filepath.Join(dir, "page", "b.tsx"))
	if !strings.Contains(b, "<<<<<<< local") || !strings.Contains(b, ">>>>>>> remote") {
		t.Fatalf("b.tsx = %q, want conflict markers", b)
	}

	conflicts, err := sitesync.Conflicts(dir)
	if err != nil || fmt.Sprint(conflicts) != "[/page/b.tsx]" {
		t.Fatalf("Conflicts = %v, %v", conflicts, err)
	}
	if _, err := c.Push(ctx, dir, sitesync.PushOptions{}); err == nil {
		t.Fatalf("Push with conflict markers succeeded")
	}

	n, err := sitesync.Resolve(dir, "page/b.tsx", sitesync.Theirs)
	if err != nil || n != 1 {
		t.Fatalf("Resolve = %d, %v", n, err)
	}
	if got := readFile(t, filepath.Join(dir, "page", "b.tsx")); got != "remote title\nbody\n" {
		t.Fatalf("resolved b.tsx = %q", got)
	}
	if _, err := c.Push(ctx, dir, sitesync.PushOptions{}); err != nil {
		t.Fatalf("Push after resolve: %v", err)
	}
	if got, _ := fake.body("/page/a.tsx"); got != "ONE\ntwo\nthree\nfour\nFIVE\n" {
		t.Fatalf("remote a.tsx = %q, want the merge pushed", got)
	}
}

func TestTokenIsAskedForOnEveryRequest(t *testing.T) {
	fake, server := newFakeSite(t, map[string]string{"/page/Index.tsx": "x\n"})
	calls := 0
	c, err := sitesync.New(sitesync.Options{
		Host: server.URL,
		Token: func(context.Context) (string, error) {
			calls++
			return fmt.Sprintf("t%d", calls), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "site")
	if _, err := c.Pull(context.Background(), dir, site, sitesync.PullOptions{}); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if _, err := c.Diff(context.Background(), dir, sitesync.DiffOptions{}); err != nil {
		t.Fatalf("Diff: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.tokens) < 2 || fake.tokens[0] != "t1" || fake.tokens[len(fake.tokens)-1] != fmt.Sprintf("t%d", calls) {
		t.Fatalf("tokens = %v, want a fresh token per request", fake.tokens)
	}
}

func TestRejectedTokenIsUnauthorized(t *testing.T) {
	fake, server := newFakeSite(t, map[string]string{"/page/Index.tsx": "x\n"})
	fake.accept = func(token string) bool { return token == "good" }
	c := newClient(t, server, nil) // sends "tok"
	_, err := c.Pull(context.Background(), filepath.Join(t.TempDir(), "site"), site, sitesync.PullOptions{})
	if !sitesync.IsUnauthorized(err) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestVersionsAndSnapshotPull(t *testing.T) {
	fake, server := newFakeSite(t, map[string]string{"/page/Index.tsx": "live\n"})
	fake.versions["1"] = map[string]string{"/page/Index.tsx": "v1\n", "/page/Gone.tsx": "gone\n"}
	fake.versions["2"] = map[string]string{"/page/Index.tsx": "v2\n"}
	c := newClient(t, server, nil)
	ctx := context.Background()

	state, err := c.Versions(ctx, site)
	if err != nil || len(state.Versions) != 2 || state.CurrentVersionNo != 2 {
		t.Fatalf("Versions = %+v, %v", state, err)
	}

	dir := filepath.Join(t.TempDir(), "tpl")
	if _, err := c.Pull(ctx, dir, site, sitesync.PullOptions{VersionNo: 1}); err != nil {
		t.Fatalf("Pull v1: %v", err)
	}
	res, err := c.Pull(ctx, dir, site, sitesync.PullOptions{VersionNo: 2})
	if err != nil {
		t.Fatalf("Pull v2: %v", err)
	}
	if res.Snapshot == nil || res.Snapshot.VersionID != 102 || res.Snapshot.Note != "second" || res.Snapshot.Removed != 1 {
		t.Fatalf("Snapshot = %+v", res.Snapshot)
	}
	if got := readFile(t, filepath.Join(dir, "page", "Index.tsx")); got != "v2\n" {
		t.Fatalf("Index.tsx = %q", got)
	}
	ws, err := sitesync.OpenWorkspace(dir)
	if err != nil || ws.SnapshotVersionNo != 2 {
		t.Fatalf("OpenWorkspace = %+v, %v", ws, err)
	}
	if _, err := c.Push(ctx, dir, sitesync.PushOptions{}); err == nil || !strings.Contains(err.Error(), "read-only snapshot") {
		t.Fatalf("Push in a snapshot = %v, want a refusal", err)
	}
}

// TestCLIAndPackageTakeTurns: the same directory, synced alternately by the CLI
// and the package, behaves as one workspace.
func TestCLIAndPackageTakeTurns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CREGHT_NO_AUTO_UPDATE", "1")

	fake, server := newFakeSite(t, map[string]string{"/page/Index.tsx": "one\n"})
	t.Setenv("CREGHT_API_HOST", server.URL)
	t.Setenv("CREGHT_TOKEN", "tok")
	c := newClient(t, server, nil)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "site")

	// Pulled by the CLI, edited, diffed by both: same JSON.
	quiet(t, func() error { return cli.Run(ctx, []string{"pull", "--site_id=p1/s1", "--dir=" + dir}) })
	writeFile(t, filepath.Join(dir, "page", "Index.tsx"), "two\n")
	pkgDiff, err := c.Diff(ctx, dir, sitesync.DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	cliOut := quiet(t, func() error { return cli.Run(ctx, []string{"diff", "--json", "--dir=" + dir}) })
	want, _ := json.MarshalIndent(pkgDiff, "", "  ")
	if strings.TrimSpace(cliOut) != string(want) {
		t.Fatalf("creght diff --json = %s\npackage Diff = %s", cliOut, want)
	}

	// Pushed by the package, then a remote edit pulled by the CLI.
	if _, err := c.Push(ctx, dir, sitesync.PushOptions{}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	fake.edit("/page/Index.tsx", "three\n")
	quiet(t, func() error { return cli.Run(ctx, []string{"pull", "--dir=" + dir}) })
	if got := readFile(t, filepath.Join(dir, "page", "Index.tsx")); got != "three\n" {
		t.Fatalf("Index.tsx = %q", got)
	}
	diff, err := c.Diff(ctx, dir, sitesync.DiffOptions{})
	if err != nil || len(diff.Files) != 0 {
		t.Fatalf("Diff after CLI pull = %+v, %v; want clean", diff, err)
	}
}

// quiet runs fn with stdout captured and returns what it printed.
func quiet(t *testing.T, fn func() error) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := fn()
	_ = w.Close()
	os.Stdout = orig
	out := <-done
	if runErr != nil {
		t.Fatalf("command failed: %v\noutput: %s", runErr, out)
	}
	return out
}
