package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/creght-dev/creght-cli/internal/creght"
)

// authStubServer accepts only token "good" and records every Authorization it saw.
func authStubServer(t *testing.T) (*httptest.Server, *atomic.Value, *atomic.Int32) {
	t.Helper()

	var lastAuth atomic.Value
	lastAuth.Store("")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		lastAuth.Store(r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"请登录后操作"}`))
			return
		}
		switch r.URL.Path {
		case "/api/u/profile":
			_, _ = w.Write([]byte(`{"id":"42","username":"ada","email":"ada@example.com"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(server.Close)

	return server, &lastAuth, &calls
}

func TestEnvTokenOutranksSavedToken(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	writeTestConfig(t, `{"api_host":"https://creght.cn","tokens":{"https://creght.cn":"saved"}}`)
	t.Setenv("CREGHT_TOKEN", " env ")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Token != "env" || cfg.TokenSource != tokenSourceEnv {
		t.Fatalf("token = %q source = %v, want env token from CREGHT_TOKEN", cfg.Token, cfg.TokenSource)
	}
	// The saved token stays where it was, so nothing writing the config back
	// can lose it or replace it with the borrowed one.
	if cfg.Tokens["https://creght.cn"] != "saved" {
		t.Fatalf("tokens = %v, want the saved token untouched", cfg.Tokens)
	}
}

func TestEnvTokenWorksWithoutConfigFile(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	t.Setenv("CREGHT_TOKEN", "env")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Token != "env" || cfg.TokenSource != tokenSourceEnv {
		t.Fatalf("token = %q source = %v, want env token", cfg.Token, cfg.TokenSource)
	}
}

func TestRejectedEnvTokenNamesItsSource(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	server, lastAuth, _ := authStubServer(t)
	t.Setenv("CREGHT_API_HOST", server.URL)
	t.Setenv("CREGHT_TOKEN", "expired")

	client, _, err := clientFromConfig()
	if err != nil {
		t.Fatalf("clientFromConfig: %v", err)
	}
	_, err = client.GetProfile(context.Background())
	if !errors.Is(err, creght.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if got := lastAuth.Load(); got != "Bearer expired" {
		t.Fatalf("Authorization = %q, want the env token", got)
	}
	for _, want := range []string{"请登录后操作", "comes from CREGHT_TOKEN", "does not refresh it", "has to supply a new one"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to contain %q", err, want)
		}
	}
}

func TestRejectedSavedTokenSuggestsLogin(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	server, _, _ := authStubServer(t)
	writeTestConfig(t, `{"api_host":"`+server.URL+`","tokens":{"`+server.URL+`":"stale"}}`)

	client, _, err := clientFromConfig()
	if err != nil {
		t.Fatalf("clientFromConfig: %v", err)
	}
	_, err = client.GetProfile(context.Background())
	if err == nil || !strings.Contains(err.Error(), "run creght login") || strings.Contains(err.Error(), "CREGHT_TOKEN") {
		t.Fatalf("err = %v, want the creght login hint only", err)
	}
}

func TestLogoutWithEnvTokenTouchesNothing(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	server, _, calls := authStubServer(t)
	content := `{"api_host":"` + server.URL + `","tokens":{"` + server.URL + `":"saved","https://creght.cn":"cn"}}`
	writeTestConfig(t, content)
	t.Setenv("CREGHT_TOKEN", "good")

	err := runLogout(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "CREGHT_TOKEN") {
		t.Fatalf("err = %v, want logout refused because of CREGHT_TOKEN", err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("logout made %d requests; the borrowed token must not be revoked", n)
	}
	path, _ := configPath()
	bs, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(bs) != content {
		t.Fatalf("config = %s, want it unchanged", bs)
	}
}

func TestLoginRefusedWithEnvToken(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	server, _, calls := authStubServer(t)
	t.Setenv("CREGHT_API_HOST", server.URL)
	t.Setenv("CREGHT_TOKEN", "good")

	err := runLogin(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "CREGHT_TOKEN") {
		t.Fatalf("err = %v, want login refused because of CREGHT_TOKEN", err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("login made %d requests, want none", n)
	}
}

func TestWhoamiReportsEnvToken(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	server, _, _ := authStubServer(t)
	t.Setenv("CREGHT_API_HOST", server.URL)
	t.Setenv("CREGHT_TOKEN", "good")

	output := captureStdout(t, func() {
		if err := Run(context.Background(), []string{"whoami"}); err != nil {
			t.Fatalf("whoami: %v", err)
		}
	})
	for _, want := range []string{"API host:  " + server.URL, "Token:     CREGHT_TOKEN environment variable", "ada", "id 42"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output = %q, want %q", output, want)
		}
	}

	output = captureStdout(t, func() {
		if err := Run(context.Background(), []string{"whoami", "--json"}); err != nil {
			t.Fatalf("whoami --json: %v", err)
		}
	})
	if !strings.Contains(output, `"token_source": "env"`) {
		t.Fatalf("output = %q, want token_source env", output)
	}
}

func TestWhoamiWithoutTokenFails(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	server, _, calls := authStubServer(t)
	t.Setenv("CREGHT_API_HOST", server.URL)

	var err error
	output := captureStdout(t, func() {
		err = Run(context.Background(), []string{"whoami"})
	})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err = %v, want not logged in", err)
	}
	if !strings.Contains(output, "Token:     none") {
		t.Fatalf("output = %q, want the missing token named", output)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("whoami made %d requests with no token, want none", n)
	}
}

func TestRootHelpNamesEnvToken(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	t.Setenv("CREGHT_TOKEN", "good")

	output := captureStdout(t, func() {
		if err := Run(context.Background(), []string{"-h"}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	})
	if !strings.Contains(output, "Token: CREGHT_TOKEN environment variable") {
		t.Fatalf("output = %q, want the env token named", output)
	}
	if !strings.Contains(output, "CREGHT_TOKEN=<token>") {
		t.Fatalf("output = %q, want CREGHT_TOKEN documented", output)
	}
}
