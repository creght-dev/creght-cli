package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunProjectCreate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfgPath, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"token":"test-token"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/u/project" {
			t.Fatalf("path = %s, want /api/u/project", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}

		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"project_123"}`))
	}))
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	output := captureStdout(t, func() {
		err = runProjectCreate(context.Background(), []string{
			"--name=  Test Project  ",
			"--from_id=source_123",
			"--tpl_id=42",
		})
	})
	if err != nil {
		t.Fatalf("runProjectCreate: %v", err)
	}

	if got := gotBody["name"]; got != "Test Project" {
		t.Fatalf("name = %#v, want Test Project", got)
	}
	if got := gotBody["from_id"]; got != "source_123" {
		t.Fatalf("from_id = %#v, want source_123", got)
	}
	if got := gotBody["tpl_id"]; got != float64(42) {
		t.Fatalf("tpl_id = %#v, want 42", got)
	}
	if !strings.Contains(output, "Created project project_123\tTest Project") {
		t.Fatalf("output = %q", output)
	}
}

func TestRunProjectCreateRequiresName(t *testing.T) {
	err := runProjectCreate(context.Background(), []string{"--name=  "})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "requires --name") {
		t.Fatalf("error = %v", err)
	}
}

func TestRootHelpPrintsCurrentAPIHostFromEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CREGHT_API_HOST", "https://creght.cn")

	output := captureStdout(t, func() {
		err := Run(context.Background(), []string{"-h"})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	})

	if !strings.Contains(output, "Current API host: https://creght.cn") {
		t.Fatalf("output = %q", output)
	}
}

func TestLoadConfigAllowsEnvAPIHostOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CREGHT_API_HOST", "https://creght.cn")

	cfgPath, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"api_host":"https://creght.cn","token":"test-token"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.APIHost != "https://creght.cn" {
		t.Fatalf("APIHost = %q, want https://creght.cn", cfg.APIHost)
	}
	if cfg.Token != "test-token" {
		t.Fatalf("Token = %q, want test-token", cfg.Token)
	}
}

func TestLoadConfigSelectsTokenForCurrentAPIHost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CREGHT_API_HOST", "https://creght.com/")

	cfgPath, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{
		"api_host": "https://creght.cn",
		"token": "cn-token",
		"tokens": {
			"https://creght.cn": "cn-token",
			"https://creght.com": "com-token"
		}
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.APIHost != "https://creght.com" {
		t.Fatalf("APIHost = %q, want https://creght.com", cfg.APIHost)
	}
	if cfg.Token != "com-token" {
		t.Fatalf("Token = %q, want com-token", cfg.Token)
	}
}

func TestLoadConfigDoesNotReuseLegacyTokenForDifferentAPIHost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CREGHT_API_HOST", "https://creght.com")

	cfgPath, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"api_host":"https://creght.cn","token":"cn-token"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Token != "" {
		t.Fatalf("Token = %q, want empty", cfg.Token)
	}
}

func TestSaveConfigPreservesTokensForOtherAPIHosts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfgPath, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{
		"api_host": "https://creght.cn",
		"token": "cn-token",
		"tokens": {
			"https://creght.cn": "cn-token"
		}
	}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	err = saveConfig(Config{
		APIHost: "https://creght.com/",
		Token:   "com-token",
	})
	if err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	bs, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(bs, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.Tokens["https://creght.cn"] != "cn-token" {
		t.Fatalf("cn token = %q, want cn-token", cfg.Tokens["https://creght.cn"])
	}
	if cfg.Tokens["https://creght.com"] != "com-token" {
		t.Fatalf("com token = %q, want com-token", cfg.Tokens["https://creght.com"])
	}
	if cfg.Token != "com-token" {
		t.Fatalf("Token = %q, want com-token", cfg.Token)
	}
	// No CREGHT_API_HOST here, so the caller really did choose this host and the
	// saved default follows it.
	if cfg.APIHost != "https://creght.com" {
		t.Fatalf("APIHost = %q, want https://creght.com", cfg.APIHost)
	}
}

// TestSaveConfigKeepsDefaultAPIHostWhenEnvOverrides pins the rule that makes
// CREGHT_API_HOST a per-command override: a login prefixed with it must add that
// host's token without redirecting every later command to it.
func TestSaveConfigKeepsDefaultAPIHostWhenEnvOverrides(t *testing.T) {
	useTempConfigDir(t)
	t.Setenv("CREGHT_API_HOST", "https://creght.com")

	writeTestConfig(t, `{
		"api_host": "https://creght.cn",
		"token": "cn-token",
		"tokens": {
			"https://creght.cn": "cn-token"
		}
	}`)

	err := saveConfig(Config{APIHost: "https://creght.com", Token: "com-token"})
	if err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	cfg := readTestConfig(t)
	if cfg.APIHost != "https://creght.cn" {
		t.Fatalf("APIHost = %q, want https://creght.cn", cfg.APIHost)
	}
	if cfg.Token != "cn-token" {
		t.Fatalf("Token = %q, want cn-token", cfg.Token)
	}
	if cfg.Tokens["https://creght.com"] != "com-token" {
		t.Fatalf("com token = %q, want com-token", cfg.Tokens["https://creght.com"])
	}
}

func TestSaveConfigOnFirstLoginWithEnvRecordsBuiltInDefault(t *testing.T) {
	useTempConfigDir(t)
	t.Setenv("CREGHT_API_HOST", "http://localhost:8433")

	err := saveConfig(Config{APIHost: "http://localhost:8433", Token: "local-token"})
	if err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	cfg := readTestConfig(t)
	if cfg.APIHost != defaultAPIHostValue {
		t.Fatalf("APIHost = %q, want %s", cfg.APIHost, defaultAPIHostValue)
	}
	if cfg.Tokens["http://localhost:8433"] != "local-token" {
		t.Fatalf("local token = %q, want local-token", cfg.Tokens["http://localhost:8433"])
	}
	if cfg.Token != "" {
		t.Fatalf("Token = %q, want empty", cfg.Token)
	}
}

func TestDeleteConfigKeepsDefaultAPIHostWhenEnvOverrides(t *testing.T) {
	useTempConfigDir(t)
	t.Setenv("CREGHT_API_HOST", "https://creght.com")

	writeTestConfig(t, `{
		"api_host": "https://creght.cn",
		"token": "cn-token",
		"tokens": {
			"https://creght.cn": "cn-token",
			"https://creght.com": "com-token"
		}
	}`)

	if err := deleteConfig(); err != nil {
		t.Fatalf("deleteConfig: %v", err)
	}

	cfg := readTestConfig(t)
	if cfg.APIHost != "https://creght.cn" {
		t.Fatalf("APIHost = %q, want https://creght.cn", cfg.APIHost)
	}
	if cfg.Token != "cn-token" {
		t.Fatalf("Token = %q, want cn-token", cfg.Token)
	}
	if _, ok := cfg.Tokens["https://creght.com"]; ok {
		t.Fatalf("com token still saved")
	}
}

// TestDeleteConfigMovesRemovedDefaultDeterministically covers the one case where
// the default does have to move — it was the host logged out of — and checks the
// replacement is not whichever host map iteration happened to reach first.
func TestDeleteConfigMovesRemovedDefaultDeterministically(t *testing.T) {
	for i := 0; i < 8; i++ {
		useTempConfigDir(t)

		writeTestConfig(t, `{
			"api_host": "https://creght.cn",
			"token": "cn-token",
			"tokens": {
				"https://creght.cn": "cn-token",
				"https://creght.com": "com-token",
				"https://talizen.com": "talizen-token"
			}
		}`)

		if err := deleteConfig(); err != nil {
			t.Fatalf("deleteConfig: %v", err)
		}

		cfg := readTestConfig(t)
		if cfg.APIHost != "https://creght.com" {
			t.Fatalf("APIHost = %q, want https://creght.com", cfg.APIHost)
		}
		if cfg.Token != "com-token" {
			t.Fatalf("Token = %q, want com-token", cfg.Token)
		}
	}
}

func TestRunConfigSetMovesDefaultAPIHostDespiteEnv(t *testing.T) {
	useTempConfigDir(t)
	t.Setenv("CREGHT_API_HOST", "http://localhost:8433")

	writeTestConfig(t, `{
		"api_host": "https://creght.cn",
		"token": "cn-token",
		"tokens": {
			"https://creght.cn": "cn-token",
			"https://creght.com": "com-token"
		}
	}`)

	output := captureStdout(t, func() {
		if err := runConfig(context.Background(), []string{"set", "api_host=https://creght.com/"}); err != nil {
			t.Fatalf("runConfig set: %v", err)
		}
	})
	if !strings.Contains(output, "api_host\thttps://creght.com") {
		t.Fatalf("output = %q", output)
	}

	cfg := readTestConfig(t)
	if cfg.APIHost != "https://creght.com" {
		t.Fatalf("APIHost = %q, want https://creght.com", cfg.APIHost)
	}
	if cfg.Token != "com-token" {
		t.Fatalf("Token = %q, want com-token", cfg.Token)
	}
	if cfg.Tokens["https://creght.cn"] != "cn-token" {
		t.Fatalf("cn token = %q, want cn-token", cfg.Tokens["https://creght.cn"])
	}
}

func TestRunConfigSetRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown key", []string{"set", "web_host=https://creght.cn"}, "only settable key is api_host"},
		{"no value", []string{"set", "api_host"}, "expects <key>=<value>"},
		{"empty value", []string{"set", "api_host="}, "invalid api_host"},
		{"not a url", []string{"set", "api_host=creght.cn"}, "invalid api_host"},
		{"unknown subcommand", []string{"reset"}, "unknown config subcommand"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempConfigDir(t)

			err := runConfig(context.Background(), tc.args)
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestRunConfigGetReportsSavedDefaultAndEnvOverride(t *testing.T) {
	useTempConfigDir(t)
	t.Setenv("CREGHT_API_HOST", "http://localhost:8433")

	writeTestConfig(t, `{"api_host":"https://creght.cn","token":"cn-token"}`)

	output := captureStdout(t, func() {
		if err := runConfig(context.Background(), []string{"get"}); err != nil {
			t.Fatalf("runConfig get: %v", err)
		}
	})

	if !strings.Contains(output, "api_host\thttps://creght.cn") {
		t.Fatalf("output = %q, want the saved default", output)
	}
	if !strings.Contains(output, "CREGHT_API_HOST=http://localhost:8433 overrides it for this command only") {
		t.Fatalf("output = %q, want the override note", output)
	}
}

// useTempConfigDir points configPath() at a directory of this test's own, and
// fails if it did not land there.
//
// Overriding HOME alone is not enough: os.UserConfigDir prefers XDG_CONFIG_HOME
// on Linux, and GitHub's runners set it, so HOME-only tests all share one real
// config file there and leak state into each other. That is invisible on macOS,
// where HOME does decide the path — it cost a red CI run to find, hence the
// check rather than just the two Setenv calls.
func useTempConfigDir(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))

	path, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
		t.Fatalf("config path %q escaped the test dir %q; the test would read the real config", path, dir)
	}
}

func writeTestConfig(t *testing.T, content string) {
	t.Helper()

	path, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func readTestConfig(t *testing.T) Config {
	t.Helper()

	path, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	bs, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(bs, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}

	return cfg
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = original
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}

	return string(out)
}
