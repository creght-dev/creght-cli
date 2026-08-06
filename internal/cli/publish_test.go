package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestReleaseTag(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
		wantErr bool
	}{
		{
			name:    "plain semver",
			version: "0.1.0",
			want:    "v0.1.0",
		},
		{
			name:    "already prefixed",
			version: "v0.1.0",
			want:    "v0.1.0",
		},
		{
			name:    "trim spaces",
			version: " 0.1.0 ",
			want:    "v0.1.0",
		},
		{
			name:    "dev cannot publish",
			version: "dev",
			wantErr: true,
		},
		{
			name:    "empty cannot publish",
			version: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := releaseTag(tt.version)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("releaseTag(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

func TestRunLogoutDeletesConfig(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	cfgPath, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	err = os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	if err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	server, revoked := logoutStubServer(t)
	err = os.WriteFile(cfgPath, []byte(`{"api_host":"`+server.URL+`","token":"test"}`), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	err = runLogout(context.Background(), nil)
	if err != nil {
		t.Fatalf("runLogout: %v", err)
	}

	// logout 必须真的去服务端吊销，而不只是删本地文件。
	if got := revoked(); got != "Bearer test" {
		t.Fatalf("revoke Authorization = %q, want Bearer test", got)
	}

	_, err = os.Stat(cfgPath)
	if !os.IsNotExist(err) {
		t.Fatalf("config still exists or stat failed: %v", err)
	}
}

func TestRunLogoutRemovesCurrentAPIHostOnly(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	server, _ := logoutStubServer(t)
	t.Setenv("CREGHT_API_HOST", server.URL)

	cfgPath, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	err = os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	if err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	err = os.WriteFile(cfgPath, []byte(`{
		"api_host": "`+server.URL+`",
		"token": "com-token",
		"tokens": {
			"https://creght.cn": "cn-token",
			"`+server.URL+`": "com-token"
		}
	}`), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	err = runLogout(context.Background(), nil)
	if err != nil {
		t.Fatalf("runLogout: %v", err)
	}

	cfg, err := loadRawConfig()
	if err != nil {
		t.Fatalf("loadRawConfig: %v", err)
	}
	if cfg.Tokens["https://creght.cn"] != "cn-token" {
		t.Fatalf("cn token = %q, want cn-token", cfg.Tokens["https://creght.cn"])
	}
	if _, ok := cfg.Tokens[server.URL]; ok {
		t.Fatalf("current host token still exists")
	}
}

// logoutStubServer 接受吊销请求并记下它带的 Authorization。
func logoutStubServer(t *testing.T) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/p/logout" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		authorization = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`"ok"`))
	}))
	t.Cleanup(server.Close)
	return server, func() string {
		mu.Lock()
		defer mu.Unlock()
		return authorization
	}
}

// 吊销失败时必须中止并保留本地配置：删掉就再也没有 token 可以用来重试吊销，
// 而报「已登出」是谎——凭据其实还能用。
func TestRunLogoutKeepsConfigWhenRevokeFails(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	cfgPath, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	// 指向一个连不上的地址，模拟离线或服务端故障。
	if err := os.WriteFile(cfgPath, []byte(`{"api_host":"http://127.0.0.1:9","token":"test"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	err = runLogout(context.Background(), nil)
	if err == nil {
		t.Fatal("expected logout to fail when the token cannot be revoked")
	}
	if !strings.Contains(err.Error(), "local_only") {
		t.Fatalf("error should point at the --local_only escape hatch, got: %v", err)
	}
	if _, statErr := os.Stat(cfgPath); statErr != nil {
		t.Fatalf("config must survive a failed revoke so it can be retried: %v", statErr)
	}
}

// --local_only 是离线时的出口：只忘掉本地，并明确说明 token 仍然有效。
func TestRunLogoutLocalOnlySkipsRevoke(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	cfgPath, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"api_host":"http://127.0.0.1:9","token":"test"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := runLogout(context.Background(), []string{"--local_only"}); err != nil {
		t.Fatalf("runLogout --local_only: %v", err)
	}
	if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
		t.Fatalf("config should be removed: %v", statErr)
	}
}

func TestRunLogoutMissingConfigSucceeds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	err := runLogout(context.Background(), nil)
	if err != nil {
		t.Fatalf("runLogout: %v", err)
	}
}
