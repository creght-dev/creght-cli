package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeUIRefsServer(t *testing.T, gotSearch *map[string]any) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/func/refs.vocab":
			w.Write([]byte(`{"result":{"industry":["教育 / 培训"],"siteType":["商城"],"pageType":["EDM 邮件"],"style":["极简"]}}`))
		case "/func/refs.search":
			if gotSearch != nil {
				json.NewDecoder(r.Body).Decode(gotSearch)
			}
			w.Write([]byte(`{"result":{"matchedBy":["industry"],"dropped":["siteType"],"list":[
				{"id":"r1","url":"` + srv.URL + `/img/a.jpg","width":1440,"height":900,"source":"example.com","pageType":"首页","style":["极简"],"summary":"白底大字"}]}}`))
		case "/img/a.jpg":
			if r.URL.Query().Get("w") != "1200" {
				t.Errorf("image not resized: %s", r.URL.RawQuery)
			}
			w.Write([]byte("jpeg"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CREGHT_UI_REFS_URL", srv.URL)
	return srv
}

func TestRefsSearchSavesAndRenamesConditions(t *testing.T) {
	var got map[string]any
	fakeUIRefsServer(t, &got)
	dir := t.TempDir()

	out := captureStdout(t, func() {
		if err := runRefs(context.Background(), []string{"search", "--industry=教育 / 培训", "--site_type=商城", "--limit=3", "--save=" + dir}); err != nil {
			t.Fatal(err)
		}
	})
	if got["industry"] != "教育 / 培训" || got["siteType"] != "商城" || got["limit"] != float64(3) {
		t.Fatalf("search input = %v", got)
	}
	saved := filepath.Join(dir, "r1.jpg")
	if b, err := os.ReadFile(saved); err != nil || string(b) != "jpeg" {
		t.Fatalf("saved file: %v %q", err, b)
	}
	for _, want := range []string{"dropped: site_type", "example.com", "saved: " + saved} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRefsSearchRejectsValueOutsideVocab(t *testing.T) {
	fakeUIRefsServer(t, nil)
	err := runRefs(context.Background(), []string{"search", "--industry=SaaS"})
	if err == nil || !strings.Contains(err.Error(), "教育 / 培训") {
		t.Fatalf("err = %v", err)
	}
	if err := runRefs(context.Background(), []string{"search"}); err == nil || !strings.Contains(err.Error(), "--industry is required") {
		t.Fatalf("err = %v", err)
	}
}
