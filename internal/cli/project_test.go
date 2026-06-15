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
