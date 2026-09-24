package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func runtimeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/u/system/info":
			_, _ = w.Write([]byte(`{"render_config":{
				"import_map":{"three":"https://cdn/three@1"},
				"dev_import_map":{"three":"https://cdn/three@1?dev"},
				"ignore_import_map":["react"],
				"limits":{"public_file_max_bytes":30720},
				"something_new":{"a":1}
			}}`))
		case "/api/u/project/p1/site/s1/file_list":
			_, _ = w.Write([]byte(`{"list":[{"id":"f1","path":"/talizen.config.ts","body":"export default { importMap: { imports: { marked: \"https://cdn/marked\" } } }"}]}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
}

func TestRunRuntimePrintsPackagesAndPassesThroughRenderConfig(t *testing.T) {
	setupTestConfig(t)
	server := runtimeServer(t)
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	out := captureStdout(t, func() {
		if err := runRuntime(context.Background(), []string{"--site_id=p1/s1"}); err != nil {
			t.Fatalf("runRuntime: %v", err)
		}
	})

	var got struct {
		Packages     map[string]runtimePackage `json:"packages"`
		Limits       map[string]int64          `json:"limits"`
		SomethingNew map[string]int            `json:"something_new"`
		ImportMap    any                       `json:"import_map"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if p := got.Packages["three"]; p.Source != "builtin" || !p.SSR {
		t.Errorf("three = %+v, want builtin with ssr", p)
	}
	if p := got.Packages["marked"]; p.Source != "/talizen.config.ts" || p.SSR {
		t.Errorf("marked = %+v, want config-added without ssr", p)
	}
	if got.Limits["public_file_max_bytes"] != 30720 {
		t.Errorf("limits = %v, want passed through", got.Limits)
	}
	if got.SomethingNew["a"] != 1 {
		t.Errorf("fields the CLI does not know must still be printed, got %v", got.SomethingNew)
	}
	if got.ImportMap != nil {
		t.Errorf("import_map is folded into packages and must not be printed again")
	}
}

func TestRunRuntimeSection(t *testing.T) {
	setupTestConfig(t)
	server := runtimeServer(t)
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	out := captureStdout(t, func() {
		if err := runRuntime(context.Background(), []string{"limits", "--site_id=p1/s1"}); err != nil {
			t.Fatalf("runRuntime limits: %v", err)
		}
	})
	if strings.TrimSpace(out) != "{\n  \"public_file_max_bytes\": 30720\n}" {
		t.Errorf("limits section = %q", out)
	}

	err := runRuntime(context.Background(), []string{"nope", "--site_id=p1/s1"})
	if err == nil || !strings.Contains(err.Error(), "available: limits, packages, something_new") {
		t.Errorf("unknown section error = %v", err)
	}
}
