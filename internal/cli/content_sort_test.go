package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// contentSortServer 起一个假服务端，捕获 create/update 实际提交的 content 请求体。
func contentSortServer(t *testing.T, method string, got *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/u/project/project-1/cms_list":
			_, _ = w.Write([]byte(`{"total":1,"list":[{"id":"app-1","key":"posts","name":"Posts"}]}`))
		case "/api/u/project/project-1/cms/app-1/content":
			if r.Method != method {
				t.Errorf("method = %s, want %s", r.Method, method)
			}
			if err := json.NewDecoder(r.Body).Decode(got); err != nil {
				t.Errorf("decode content: %v", err)
			}
			if method == http.MethodPost {
				_, _ = w.Write([]byte(`{"id":"content-1"}`))
			} else {
				_, _ = w.Write([]byte(`{"ok":true}`))
			}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
}

func writeContentFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "content.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// content update 以前只在外层 cobra 声明了 --sort，内层 flag.FlagSet 没有，
// 于是 --sort 直接报 "flag provided but not defined"。
func TestRunContentUpdateAcceptsSortFlag(t *testing.T) {
	setupTestConfig(t)
	dataPath := writeContentFile(t, `{"body":{"title":"Hi"}}`)

	var got map[string]any
	server := contentSortServer(t, http.MethodPut, &got)
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	captureStdout(t, func() {
		if err := runContentUpdate(context.Background(), []string{
			"--site_id=project-1/site-1",
			"--collection=posts",
			"--id=content-1",
			"--data=" + dataPath,
			"--sort=15",
		}); err != nil {
			t.Fatalf("runContentUpdate: %v", err)
		}
	})

	if got["sort"] != float64(15) {
		t.Fatalf("sort = %#v, want 15", got["sort"])
	}
}

// 不传 --sort 时不能把文件里的 sort 覆盖成 0（覆盖后会被 omitempty 丢掉，
// 平台就当没给排序，把条目追加到末尾）。
func TestRunContentCreateKeepsSortFromDataFile(t *testing.T) {
	setupTestConfig(t)
	dataPath := writeContentFile(t, `{"slug":"hello","sort":15,"body":{"title":"Hi"}}`)

	var got map[string]any
	server := contentSortServer(t, http.MethodPost, &got)
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	captureStdout(t, func() {
		if err := runContentCreate(context.Background(), []string{
			"--site_id=project-1/site-1",
			"--collection=posts",
			"--data=" + dataPath,
		}); err != nil {
			t.Fatalf("runContentCreate: %v", err)
		}
	})

	if got["sort"] != float64(15) {
		t.Fatalf("sort = %#v, want 15 preserved from --data", got["sort"])
	}
}

// 显式 --sort=0 必须以哨兵值上线：服务端按非空字段算更新列，裸的 0 会被当成
// “没传 sort”而忽略，只有哨兵值会被翻译回真正的 0。
func TestRunContentUpdateSendsZeroSortAsSentinel(t *testing.T) {
	setupTestConfig(t)
	dataPath := writeContentFile(t, `{"sort":15,"body":{"title":"Hi"}}`)

	var got map[string]any
	server := contentSortServer(t, http.MethodPut, &got)
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	captureStdout(t, func() {
		if err := runContentUpdate(context.Background(), []string{
			"--site_id=project-1/site-1",
			"--collection=posts",
			"--id=content-1",
			"--data=" + dataPath,
			"--sort=0",
		}); err != nil {
			t.Fatalf("runContentUpdate: %v", err)
		}
	})

	raw, ok := got["sort"]
	if !ok {
		t.Fatalf("sort key missing from request body: %#v", got)
	}
	if raw != float64(creght.EmptyNumberSentinel) {
		t.Fatalf("sort = %#v, want sentinel %d", raw, creght.EmptyNumberSentinel)
	}
}

// create 侧不能用哨兵：服务端 create 不做哨兵翻译，会把魔数原样写进库。
// 显式 --sort=0 在 create 上等于“让服务端自动追加到末尾”，即不带该字段，
// 并且要盖掉 --data 里的 sort。
func TestRunContentCreateZeroSortOmitsFieldInsteadOfSentinel(t *testing.T) {
	setupTestConfig(t)
	dataPath := writeContentFile(t, `{"sort":15,"body":{"title":"Hi"}}`)

	var got map[string]any
	server := contentSortServer(t, http.MethodPost, &got)
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	captureStdout(t, func() {
		if err := runContentCreate(context.Background(), []string{
			"--site_id=project-1/site-1",
			"--collection=posts",
			"--data=" + dataPath,
			"--sort=0",
		}); err != nil {
			t.Fatalf("runContentCreate: %v", err)
		}
	})

	if raw, ok := got["sort"]; ok {
		t.Fatalf("sort should be absent on create --sort=0, got %#v", raw)
	}
}

// update 是局部更新，--data 可省略：只改 sort 时不必把整个 body 重新提交。
func TestRunContentUpdateWithoutDataFile(t *testing.T) {
	setupTestConfig(t)

	var got map[string]any
	server := contentSortServer(t, http.MethodPut, &got)
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	captureStdout(t, func() {
		if err := runContentUpdate(context.Background(), []string{
			"--site_id=project-1/site-1",
			"--collection=posts",
			"--id=content-1",
			"--sort=15",
		}); err != nil {
			t.Fatalf("runContentUpdate: %v", err)
		}
	})

	if got["sort"] != float64(15) {
		t.Fatalf("sort = %#v, want 15", got["sort"])
	}
	if got["id"] != "content-1" {
		t.Fatalf("id = %#v, want content-1", got["id"])
	}
	if _, ok := got["body"]; ok {
		t.Fatalf("body should be absent when --data is omitted, got %#v", got["body"])
	}
}

// --data/--slug/--sort 全都没给时，提交的是空 patch，服务端只会回
// "no fields were updated"。提前报错比让用户去猜那个响应更有用。
func TestRunContentUpdateRequiresSomethingToChange(t *testing.T) {
	setupTestConfig(t)
	t.Setenv("CREGHT_API_HOST", "http://127.0.0.1:1")

	err := runContentUpdate(context.Background(), []string{
		"--site_id=project-1/site-1",
		"--collection=posts",
		"--id=content-1",
	})
	if err == nil {
		t.Fatal("expected an error when nothing was passed to change")
	}
	if !strings.Contains(err.Error(), "--data, --slug, or --sort") {
		t.Fatalf("error = %v", err)
	}
}

// 完全不传 sort（flag 和文件都没有）时请求体里不应出现 sort 键，
// 让 create 走平台默认、update 保留原值。
func TestRunContentUpdateOmitsUnsetSort(t *testing.T) {
	setupTestConfig(t)
	dataPath := writeContentFile(t, `{"body":{"title":"Hi"}}`)

	var got map[string]any
	server := contentSortServer(t, http.MethodPut, &got)
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	captureStdout(t, func() {
		if err := runContentUpdate(context.Background(), []string{
			"--site_id=project-1/site-1",
			"--collection=posts",
			"--id=content-1",
			"--data=" + dataPath,
		}); err != nil {
			t.Fatalf("runContentUpdate: %v", err)
		}
	})

	if _, ok := got["sort"]; ok {
		t.Fatalf("sort should be absent, got %#v", got["sort"])
	}
}

// table record create：非零值照发，0 等于“自动追加到末尾”（服务端 create 用
// sort==0 触发 max+10），不传同样不带该字段。
func TestRunTableRecordCreateSortFlagSemantics(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantSort any
		wantKey  bool
	}{
		{name: "explicit zero means append last", args: []string{"--sort=0"}, wantKey: false},
		{name: "explicit value", args: []string{"--sort=7"}, wantSort: float64(7), wantKey: true},
		{name: "unset", args: nil, wantKey: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupTestConfig(t)
			dataPath := filepath.Join(t.TempDir(), "record.json")
			if err := os.WriteFile(dataPath, []byte(`{"name":"Ada"}`), 0o644); err != nil {
				t.Fatal(err)
			}

			var got map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/p/project/project-1/table_list":
					_, _ = w.Write([]byte(`{"total":1,"list":[{"id":"table-1","key":"appointments"}]}`))
				case "/api/p/project/project-1/table/table-1/record":
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Errorf("decode record: %v", err)
					}
					_, _ = w.Write([]byte(`{"id":"record-1"}`))
				default:
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
			}))
			defer server.Close()
			t.Setenv("CREGHT_API_HOST", server.URL)

			args := append([]string{
				"--site_id=project-1/site-1",
				"--table=appointments",
				"--data=" + dataPath,
			}, tc.args...)
			captureStdout(t, func() {
				if err := runTableRecordCreate(context.Background(), args); err != nil {
					t.Fatalf("runTableRecordCreate: %v", err)
				}
			})

			raw, ok := got["sort"]
			if ok != tc.wantKey {
				t.Fatalf("sort present = %v, want %v (body %#v)", ok, tc.wantKey, got)
			}
			if tc.wantKey && raw != tc.wantSort {
				t.Fatalf("sort = %#v, want %#v", raw, tc.wantSort)
			}
		})
	}
}

// 记录更新没有 content 那套哨兵翻译，sort:0 会被服务端忽略、哨兵又会被原样入库，
// 所以这里应该显式报错而不是静默无操作。
func TestRunTableRecordUpdateRejectsZeroSort(t *testing.T) {
	setupTestConfig(t)
	dataPath := filepath.Join(t.TempDir(), "record.json")
	if err := os.WriteFile(dataPath, []byte(`{"name":"Ada"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREGHT_API_HOST", "http://127.0.0.1:1")

	err := runTableRecordUpdate(context.Background(), []string{
		"--site_id=project-1/site-1",
		"--table=appointments",
		"--id=record-1",
		"--data=" + dataPath,
		"--sort=0",
	})
	if err == nil {
		t.Fatal("expected --sort=0 to be rejected for table record update")
	}
	if !strings.Contains(err.Error(), "--sort=0 is not supported") {
		t.Fatalf("error = %v", err)
	}
}
