package cli

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/creght-dev/creght-cli/internal/creght"
)

// Each legacy command declares its flags twice: once on the cobra command,
// which parses argv first and rejects anything it does not know, and once on
// the runner's own flag.FlagSet. A flag added only to the runner compiles, passes
// a runner-level test, and is then refused with "unknown flag" by the real CLI —
// which is how version create --tag and logout --local_only shipped broken.
// TestRunnerFlagsAreRegistered catches a name declared in a runner that no
// cobra command registers.
func TestRunnerFlagsAreRegistered(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	declared := map[string]string{} // flag -> where a runner declares it
	registered := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				recv, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				name := firstStringArg(call)
				if name == "" {
					return true
				}
				switch recv.Name {
				case "fs":
					declared[name] = fset.Position(call.Pos()).String()
				case "flags":
					registered[name] = true
				}
				return true
			})
		}
	}
	if len(declared) == 0 || len(registered) == 0 {
		t.Fatalf("found %d runner flags and %d registered flags; the scan is broken", len(declared), len(registered))
	}

	var missing []string
	for name, pos := range declared {
		if !registered[name] {
			missing = append(missing, "--"+name+" (declared at "+pos+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("flags a runner parses but no cobra command registers, so the CLI rejects them as unknown:\n  %s", strings.Join(missing, "\n  "))
	}
}

// firstStringArg returns the first string-literal argument of a flag
// declaration: the name in fs.String("name", ...) or fs.StringVar(&v, "name", ...).
func firstStringArg(call *ast.CallExpr) string {
	for _, arg := range call.Args {
		lit, ok := arg.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return ""
		}
		return s
	}
	return ""
}

// publishServer answers version create and version list for site p1/s1 and
// records the body of the last publish/version request.
func publishServer(t *testing.T) (*httptest.Server, func() map[string]any) {
	t.Helper()
	var last map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/publish/version"):
			last = map[string]any{}
			if err := json.NewDecoder(r.Body).Decode(&last); err != nil {
				t.Errorf("decode publish/version: %v", err)
			}
			tag, _ := last["tag"].(string)
			_ = json.NewEncoder(w).Encode(creght.PublishVersionResult{VersionID: 456, VersionNo: 12, Tag: tag, Created: true})
		case strings.HasSuffix(r.URL.Path, "/publish/state"):
			_ = json.NewEncoder(w).Encode(creght.SitePublishState{
				Versions:         []creght.SiteVersion{{ID: 456, VersionNo: 12, Note: "x", Tag: "v135"}},
				CurrentVersionID: 456,
				CurrentVersionNo: 12,
			})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, func() map[string]any { return last }
}

func TestVersionCreateTagFromCommandLine(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	server, lastBody := publishServer(t)
	t.Setenv("CREGHT_API_HOST", server.URL)
	t.Setenv("CREGHT_TOKEN", "tok")

	out := captureStdout(t, func() {
		err := Run(context.Background(), []string{"version", "create", "--site_id=p1/s1", "--note=x", "--tag=v135"})
		if err != nil {
			t.Fatalf("version create --tag: %v", err)
		}
	})
	if got := lastBody()["tag"]; got != "v135" {
		t.Fatalf("request tag = %v, want v135 (body %v)", got, lastBody())
	}
	if !strings.Contains(out, "tag: v135") {
		t.Fatalf("output = %q, want the tag reported", out)
	}

	help := captureStdout(t, func() {
		if err := Run(context.Background(), []string{"version", "create", "--help"}); err != nil {
			t.Fatalf("version create --help: %v", err)
		}
	})
	if !strings.Contains(help, "--tag") {
		t.Fatalf("version create --help does not list --tag:\n%s", help)
	}
}

func TestVersionListJSONCarriesTag(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	server, _ := publishServer(t)
	t.Setenv("CREGHT_API_HOST", server.URL)
	t.Setenv("CREGHT_TOKEN", "tok")

	out := captureStdout(t, func() {
		if err := Run(context.Background(), []string{"version", "list", "--site_id=p1/s1", "--json"}); err != nil {
			t.Fatalf("version list --json: %v", err)
		}
	})
	var state struct {
		Versions []struct {
			Tag string `json:"tag"`
		} `json:"versions"`
	}
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if len(state.Versions) != 1 || state.Versions[0].Tag != "v135" {
		t.Fatalf("versions = %+v, want tag v135", state.Versions)
	}
}

func TestLogoutLocalOnlyFromCommandLine(t *testing.T) {
	useTempConfigDir(t)
	t.Chdir(t.TempDir())
	writeTestConfig(t, `{"api_host":"http://127.0.0.1:9","tokens":{"http://127.0.0.1:9":"tok"}}`)

	captureStdout(t, func() {
		if err := Run(context.Background(), []string{"logout", "--local_only"}); err != nil {
			t.Fatalf("logout --local_only: %v", err)
		}
	})
	if cfg := readTestConfig(t); len(cfg.Tokens) != 0 {
		t.Fatalf("tokens = %v, want the login forgotten", cfg.Tokens)
	}
}
