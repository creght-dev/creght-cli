package cli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// writeTokenHelper writes a shell script standing in for a host program's token
// helper: it appends its arguments to a log, one run per line, then runs body.
// It returns the CREGHT_TOKEN_COMMAND value and the log path. The directory
// has a space in it, so the value has to be quoted the way a real install
// path often is.
func writeTokenHelper(t *testing.T, body string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the helper is a shell script")
	}

	dir := filepath.Join(t.TempDir(), "token helper")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create helper dir: %v", err)
	}
	logPath := filepath.Join(dir, "calls.log")
	script := filepath.Join(dir, "helper.sh")
	content := "#!/bin/sh\necho \"$*\" >> '" + logPath + "'\n" + body + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}

	return `"` + script + `" creght token`, logPath
}

func readHelperCalls(t *testing.T, logPath string) []string {
	t.Helper()

	body, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read helper log: %v", err)
	}

	return strings.Split(strings.TrimRight(string(body), "\n"), "\n")
}

// In a site directory the helper is asked for the site's host — not for
// CREGHT_API_HOST — and its token outranks CREGHT_TOKEN.
func TestTokenCommandSuppliesTokenForWorkspaceHost(t *testing.T) {
	useTempConfigDir(t)
	server, lastAuth, _ := authStubServer(t)
	t.Chdir(writeAPIHostWorkspace(t, "p1/s1", server.URL))
	t.Setenv("CREGHT_API_HOST", "https://creght.cn")
	t.Setenv("CREGHT_TOKEN", "stale")
	command, logPath := writeTokenHelper(t, "echo '  good  '")
	t.Setenv(tokenCommandEnvVar, command)

	output := captureStdout(t, func() {
		if err := Run(context.Background(), []string{"whoami"}); err != nil {
			t.Fatalf("whoami: %v", err)
		}
	})

	if got := lastAuth.Load(); got != "Bearer good" {
		t.Fatalf("Authorization = %q, want the helper's trimmed token", got)
	}
	if calls := readHelperCalls(t, logPath); !reflect.DeepEqual(calls, []string{"creght token --host " + server.URL}) {
		t.Fatalf("helper calls = %q, want one call for the workspace host", calls)
	}
	if !strings.Contains(output, "Token:     "+tokenCommandEnvVar) {
		t.Fatalf("output = %q, want the token source named", output)
	}
}

func TestTokenCommandRunsOncePerHost(t *testing.T) {
	useTempConfigDir(t)
	command, logPath := writeTokenHelper(t, `echo "token-for-$4"`)
	t.Setenv(tokenCommandEnvVar, command)

	for range 3 {
		token, err := commandToken(context.Background(), "https://talizen.com/")
		if err != nil {
			t.Fatalf("commandToken: %v", err)
		}
		if token != "token-for-https://talizen.com" {
			t.Fatalf("token = %q", token)
		}
	}
	if _, err := commandToken(context.Background(), "https://creght.com"); err != nil {
		t.Fatalf("commandToken: %v", err)
	}

	want := []string{"creght token --host https://talizen.com", "creght token --host https://creght.com"}
	if calls := readHelperCalls(t, logPath); !reflect.DeepEqual(calls, want) {
		t.Fatalf("helper calls = %q, want %q", calls, want)
	}
}

// The helper knows why it has no token; its stderr reaches the user as is.
func TestTokenCommandFailureReportsItsStderr(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	server, _, calls := authStubServer(t)
	t.Setenv("CREGHT_API_HOST", server.URL)
	command, _ := writeTokenHelper(t, "echo 'talizen.com 没有连接，去 设置 → 连接 连上' >&2; exit 3")
	t.Setenv(tokenCommandEnvVar, command)

	var err error
	captureStdout(t, func() { err = Run(context.Background(), []string{"whoami"}) })

	if err == nil || !strings.Contains(err.Error(), "talizen.com 没有连接，去 设置 → 连接 连上") {
		t.Fatalf("err = %v, want the helper's stderr", err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("made %d requests without a token, want none", n)
	}
}

func TestTokenCommandRefusesLogin(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	t.Setenv(tokenCommandEnvVar, "helper token")

	err := Run(context.Background(), []string{"login"})
	if err == nil || !strings.Contains(err.Error(), tokenCommandEnvVar) {
		t.Fatalf("err = %v, want login refused because of %s", err, tokenCommandEnvVar)
	}
}

func TestSplitCommandLine(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{`annulo creght token`, []string{"annulo", "creght", "token"}},
		{`  "/Applications/My App/annulo"   creght token `, []string{"/Applications/My App/annulo", "creght", "token"}},
		{`'/opt/a b/x' "it's" 'say "hi"'`, []string{"/opt/a b/x", "it's", `say "hi"`}},
		{`a""b c''`, []string{"ab", "c"}},
		{`"" x`, []string{"", "x"}},
	}
	if runtime.GOOS == "windows" {
		cases = append(cases,
			struct {
				line string
				want []string
			}{`"C:\Program Files\Annulo\annulo.exe" creght token`, []string{`C:\Program Files\Annulo\annulo.exe`, "creght", "token"}},
			struct {
				line string
				want []string
			}{`\\server\share\x.exe`, []string{`\\server\share\x.exe`}},
		)
	} else {
		cases = append(cases,
			struct {
				line string
				want []string
			}{`/opt/a\ b/x "q\"uote" "C:\Program Files"`, []string{"/opt/a b/x", `q"uote`, `C:\Program Files`}},
		)
	}

	for _, c := range cases {
		got, err := splitCommandLine(c.line)
		if err != nil {
			t.Fatalf("splitCommandLine(%q): %v", c.line, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("splitCommandLine(%q) = %q, want %q", c.line, got, c.want)
		}
	}

	for _, bad := range []string{``, `   `, `"open`, `'open`} {
		if _, err := splitCommandLine(bad); err == nil {
			t.Fatalf("splitCommandLine(%q) succeeded, want an error", bad)
		}
	}
}
