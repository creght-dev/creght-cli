package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAPIHostWorkspace creates a pulled workspace with the given recorded API host
// ("" leaves api_host out, as a workspace pulled by an older CLI would).
func writeAPIHostWorkspace(t *testing.T, siteID string, apiHost string) string {
	t.Helper()

	root := t.TempDir()
	state := workspaceState{SiteID: siteID, APIHost: apiHost, Files: map[string]stateEntry{}}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, stateDirName), 0o755); err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	if err := os.WriteFile(statePath(root), body, 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	return root
}

func readTestWorkspaceState(t *testing.T, root string) workspaceState {
	t.Helper()

	state, hasState, err := loadWorkspaceState(root)
	if err != nil {
		t.Fatalf("loadWorkspaceState: %v", err)
	}
	if !hasState {
		t.Fatalf("no state in %s", root)
	}

	return state
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = original }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}

	return string(out)
}

func TestLoadConfigPrefersWorkspaceHostOverSavedDefault(t *testing.T) {
	useTempConfigDir(t)
	writeTestConfig(t, `{"api_host":"https://creght.cn","tokens":{"https://creght.cn":"default-token","https://talizen.com":"workspace-token"}}`)
	t.Chdir(writeAPIHostWorkspace(t, "p1/s1", "https://talizen.com"))

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.APIHost != "https://talizen.com" {
		t.Fatalf("APIHost = %q, want the workspace host", cfg.APIHost)
	}
	if cfg.Token != "workspace-token" {
		t.Fatalf("Token = %q, want that host's token", cfg.Token)
	}
}

// The workspace is discovered from a subdirectory too, so commands do not have
// to run at the workspace root.
func TestLoadConfigDiscoversWorkspaceHostFromSubdirectory(t *testing.T) {
	useTempConfigDir(t)
	writeTestConfig(t, `{"api_host":"https://creght.cn"}`)
	root := writeAPIHostWorkspace(t, "p1/s1", "https://talizen.com")
	nested := filepath.Join(root, "page", "blog")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested dir: %v", err)
	}
	t.Chdir(nested)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.APIHost != "https://talizen.com" {
		t.Fatalf("APIHost = %q, want the workspace host", cfg.APIHost)
	}
}

func TestEnvAPIHostOutranksWorkspaceHost(t *testing.T) {
	useTempConfigDir(t)
	writeTestConfig(t, `{"api_host":"https://creght.cn"}`)
	t.Chdir(writeAPIHostWorkspace(t, "p1/s1", "https://talizen.com"))
	t.Setenv("CREGHT_API_HOST", "http://localhost:8433")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.APIHost != "http://localhost:8433" {
		t.Fatalf("APIHost = %q, want the environment override", cfg.APIHost)
	}
}

// A workspace pulled before the CLI recorded api_host must keep working exactly
// as it did: fall through to the saved default.
func TestWorkspaceWithoutRecordedHostFallsBackToSavedDefault(t *testing.T) {
	useTempConfigDir(t)
	writeTestConfig(t, `{"api_host":"https://creght.cn"}`)
	t.Chdir(writeAPIHostWorkspace(t, "p1/s1", ""))

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.APIHost != "https://creght.cn" {
		t.Fatalf("APIHost = %q, want the saved default", cfg.APIHost)
	}
}

func TestRootHelpNamesWorkspaceAsAPIHostSource(t *testing.T) {
	useTempConfigDir(t)
	writeTestConfig(t, `{"api_host":"https://creght.cn"}`)
	root := writeAPIHostWorkspace(t, "p1/s1", "https://talizen.com")
	t.Chdir(root)

	output := captureStdout(t, func() {
		if err := Run(context.Background(), []string{"-h"}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	})

	if !strings.Contains(output, "Current API host: https://talizen.com") {
		t.Fatalf("output = %q, want the discovered host", output)
	}
	// The directory matters: it is the only way to tell which workspace decided
	// the host when several are nested.
	wantSource := "source: auto-discovered from workspace " + root
	if !strings.Contains(output, wantSource) {
		t.Fatalf("output = %q, want %q", output, wantSource)
	}
}

func TestRootHelpNamesSavedDefaultAsAPIHostSource(t *testing.T) {
	useTempConfigDir(t)
	writeTestConfig(t, `{"api_host":"https://creght.cn"}`)
	t.Chdir(t.TempDir())

	output := captureStdout(t, func() {
		if err := Run(context.Background(), []string{"-h"}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	})

	if !strings.Contains(output, "Current API host: https://creght.cn") {
		t.Fatalf("output = %q", output)
	}
	if !strings.Contains(output, "source: saved default (creght config set api_host)") {
		t.Fatalf("output = %q, want the saved-default source", output)
	}
}

func TestRootHelpNamesEnvAsAPIHostSource(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	t.Setenv("CREGHT_API_HOST", "http://localhost:8433")

	output := captureStdout(t, func() {
		if err := Run(context.Background(), []string{"-h"}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	})

	if !strings.Contains(output, "Current API host: http://localhost:8433") {
		t.Fatalf("output = %q", output)
	}
	if !strings.Contains(output, "source: CREGHT_API_HOST environment variable (this command only)") {
		t.Fatalf("output = %q, want the environment source", output)
	}
}

// The first write stamps the workspace so later commands there need no prefix.
func TestSaveWorkspaceStateRecordsAPIHost(t *testing.T) {
	useTempConfigDir(t)
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("CREGHT_API_HOST", "https://talizen.com")

	if err := saveWorkspaceState(root, "p1/s1", map[string]snapshotEntry{}); err != nil {
		t.Fatalf("saveWorkspaceState: %v", err)
	}

	if got := readTestWorkspaceState(t, root).APIHost; got != "https://talizen.com" {
		t.Fatalf("api_host = %q, want the host the pull used", got)
	}
}

// ...and later writes leave it alone, so a one-off CREGHT_API_HOST override
// cannot silently repoint the workspace at another deployment.
func TestSaveWorkspaceStateKeepsRecordedAPIHost(t *testing.T) {
	useTempConfigDir(t)
	root := writeAPIHostWorkspace(t, "p1/s1", "https://talizen.com")
	t.Chdir(root)
	t.Setenv("CREGHT_API_HOST", "http://localhost:8433")

	if err := saveWorkspaceState(root, "p1/s1", map[string]snapshotEntry{}); err != nil {
		t.Fatalf("saveWorkspaceState: %v", err)
	}

	if got := readTestWorkspaceState(t, root).APIHost; got != "https://talizen.com" {
		t.Fatalf("api_host = %q, want the originally recorded host", got)
	}
}

// A login inside a workspace saves that host's token without hijacking the
// machine-wide default, the same way a login under CREGHT_API_HOST does not.
func TestSaveConfigKeepsDefaultWhenHostCameFromWorkspace(t *testing.T) {
	useTempConfigDir(t)
	writeTestConfig(t, `{"api_host":"https://creght.cn","tokens":{"https://creght.cn":"default-token"}}`)
	t.Chdir(writeAPIHostWorkspace(t, "p1/s1", "https://talizen.com"))

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	cfg.Token = "workspace-token"
	if err := saveConfig(cfg); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	saved := readTestConfig(t)
	if saved.APIHost != "https://creght.cn" {
		t.Fatalf("api_host = %q, want the saved default to stay put", saved.APIHost)
	}
	if saved.Tokens["https://talizen.com"] != "workspace-token" {
		t.Fatalf("tokens = %#v, want the workspace host's token stored", saved.Tokens)
	}
}

func TestConfigGetReportsWorkspaceDiscovery(t *testing.T) {
	useTempConfigDir(t)
	writeTestConfig(t, `{"api_host":"https://creght.cn"}`)
	root := writeAPIHostWorkspace(t, "p1/s1", "https://talizen.com")
	t.Chdir(root)

	output := captureStdout(t, func() {
		if err := runConfigGet(nil); err != nil {
			t.Fatalf("runConfigGet: %v", err)
		}
	})

	if !strings.Contains(output, "api_host\thttps://creght.cn") {
		t.Fatalf("output = %q, want the saved default", output)
	}
	want := "workspace " + root + " auto-discovers https://talizen.com from .creght/state.json"
	if !strings.Contains(output, want) {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestWarnAPIHostMismatchOnEnvOverride(t *testing.T) {
	useTempConfigDir(t)
	root := writeAPIHostWorkspace(t, "p1/s1", "https://talizen.com")
	t.Chdir(root)
	t.Setenv("CREGHT_API_HOST", "http://localhost:8433")

	output := captureStderr(t, func() { warnAPIHostMismatch(root) })

	if !strings.Contains(output, "was pulled from https://talizen.com") ||
		!strings.Contains(output, "http://localhost:8433") {
		t.Fatalf("stderr = %q, want a mismatch warning", output)
	}
}

func TestWarnAPIHostMismatchStaysQuietWhenHostsAgree(t *testing.T) {
	useTempConfigDir(t)
	root := writeAPIHostWorkspace(t, "p1/s1", "https://talizen.com")
	t.Chdir(root)

	output := captureStderr(t, func() { warnAPIHostMismatch(root) })

	if output != "" {
		t.Fatalf("stderr = %q, want silence", output)
	}
}
