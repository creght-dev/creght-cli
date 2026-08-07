package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadGitCredentialRequest(t *testing.T) {
	// git 的实际输入形态：key=value 行，空行结束。
	in := strings.NewReader("protocol=https\nhost=test.creght.cn\npath=api/git/project/p1/site/s1\n\nignored=yes\n")
	request, err := readGitCredentialRequest(in)
	if err != nil {
		t.Fatalf("readGitCredentialRequest: %v", err)
	}
	if request["protocol"] != "https" || request["host"] != "test.creght.cn" {
		t.Fatalf("request = %#v", request)
	}
	// 空行之后的内容不属于本次请求。
	if _, ok := request["ignored"]; ok {
		t.Fatal("keys after the blank line must not be read")
	}
}

func TestReadGitCredentialRequestSkipsMalformedLines(t *testing.T) {
	request, err := readGitCredentialRequest(strings.NewReader("host=a.example\ngarbage\n\n"))
	if err != nil {
		t.Fatalf("readGitCredentialRequest: %v", err)
	}
	if len(request) != 1 || request["host"] != "a.example" {
		t.Fatalf("request = %#v", request)
	}
}

func TestGitCloneURLKeepsAPIPrefix(t *testing.T) {
	// /api 前缀不是装饰：反代只把 /api/* 转给 API 服务。
	got, err := gitCloneURL("https://test.creght.cn", "p4gtbxx11wgi", "p4gtbxzaz2oi")
	if err != nil {
		t.Fatalf("gitCloneURL: %v", err)
	}
	want := "https://test.creght.cn/api/git/project/p4gtbxx11wgi/site/p4gtbxzaz2oi"
	if got != want {
		t.Fatalf("gitCloneURL = %s, want %s", got, want)
	}
}

func TestStripScheme(t *testing.T) {
	for in, want := range map[string]string{
		"https://test.creght.cn": "test.creght.cn",
		"http://localhost:8433":  "localhost:8433",
		"https://creght.cn/":     "creght.cn",
		"creght.cn":              "creght.cn",
		"  https://creght.cn  ":  "creght.cn",
	} {
		if got := stripScheme(in); got != want {
			t.Fatalf("stripScheme(%q) = %q, want %q", in, got, want)
		}
	}
}

// writeTestConfig points the CLI's config lookup at a temp dir so token
// resolution can be tested without touching the developer's real credentials.
func writeTestConfig(t *testing.T, cfg Config) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	path, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bs, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, bs, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestTokenForGitHost(t *testing.T) {
	writeTestConfig(t, Config{
		APIHost: "https://creght.cn",
		Token:   "prod-token",
		Tokens: map[string]string{
			"https://creght.cn":      "prod-token",
			"https://test.creght.cn": "test-token",
		},
	})

	cases := []struct {
		name     string
		protocol string
		host     string
		want     string
	}{
		{"exact match", "https", "test.creght.cn", "test-token"},
		{"production", "https", "creght.cn", "prod-token"},
		// 配置里存的是 https，请求以 http 来也应命中同一主机。
		{"scheme mismatch falls back to host", "http", "test.creght.cn", "test-token"},
		// 未知主机必须返回空，让 git 回退到自己询问，而不是把别的站点的 token 发出去。
		{"unknown host yields nothing", "https", "github.com", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := tokenForGitHost(c.protocol, c.host)
			if err != nil {
				t.Fatalf("tokenForGitHost: %v", err)
			}
			if got != c.want {
				t.Fatalf("tokenForGitHost(%s, %s) = %q, want %q", c.protocol, c.host, got, c.want)
			}
		})
	}
}

func TestQuoteForGitConfig(t *testing.T) {
	if got := quoteForGitConfig("/usr/local/bin/creght"); got != "/usr/local/bin/creght" {
		t.Fatalf("unquoted path changed: %q", got)
	}
	// 路径带空格时不加引号，git 会把它拆成命令 + 参数。
	got := quoteForGitConfig("/Users/a b/creght")
	if got != `"/Users/a b/creght"` {
		t.Fatalf("quoteForGitConfig = %q", got)
	}
}

// TestGitCloneResetsInheritedHelpers 锁住那个微妙的顺序：空值必须排在我们的
// helper 之前，否则系统继承来的 osxkeychain 会先应答，`creght login` 换过的
// token 会输给钥匙串里的旧值。
//
// 这个性质现在归 clone 管 —— `git clone -c` 会把这两条写进新仓库自己的 config，
// 所以 clone 一次就够，不需要再有一个改全局配置的 setup 步骤。
func TestGitCloneResetsInheritedHelpers(t *testing.T) {
	args := gitCredentialConfigArgs("https://test.creght.cn")
	if len(args) != 4 {
		t.Fatalf("args = %#v, want two -c settings", args)
	}
	if args[0] != "-c" || args[2] != "-c" {
		t.Fatalf("args = %#v, want each setting introduced by -c", args)
	}

	const key = "credential.https://test.creght.cn.helper"
	if args[1] != key+"=" {
		t.Fatalf("first setting = %q, want %q so inherited helpers are reset before ours", args[1], key+"=")
	}
	if !strings.HasPrefix(args[3], key+"=") || !strings.Contains(args[3], "git credential") {
		t.Fatalf("second setting = %q, want our helper", args[3])
	}
	// URL 限定：不能碰到 github.com 之类的凭证。
	if !strings.Contains(args[1], "test.creght.cn") {
		t.Fatalf("setting is not scoped to the host: %q", args[1])
	}
}

func writeGitConfig(t *testing.T, home string, key string, value string) {
	t.Helper()
	cmd := exec.Command("git", "config", "--global", "--add", key, value)
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed git config: %v\n%s", err, out)
	}
}
