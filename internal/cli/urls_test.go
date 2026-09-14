package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func urlStubServer(t *testing.T, publishState string) *httptest.Server {
	t.Helper()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/u/system/info":
			fmt.Fprintf(w, `{"self_api_host":%q}`, server.URL)
		case "/api/u/project/p1/site/s1/publish/state":
			if publishState == "" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"no publish panel"}`))
				return
			}
			_, _ = w.Write([]byte(publishState))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	return server
}

const urlPublishStateBody = `{
	"current_version_id": 455,
	"current_version_no": 11,
	"system_domain": "demo.creght.cn",
	"domains": [
		{"id": 0, "domain": "demo.creght.cn", "system": true, "follow": true},
		{"id": 7, "domain": "www.example.com", "publish_version_id": 456, "publish_version_no": 12}
	],
	"publish_targets": ["demo.creght.cn"]
}`

func TestRunURLPrintsEveryAddressAndOpensNothing(t *testing.T) {
	setupTestConfig(t)
	server := urlStubServer(t, urlPublishStateBody)
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	opened := []string{}
	original := openBrowser
	openBrowser = func(rawURL string) error {
		opened = append(opened, rawURL)
		return nil
	}
	defer func() { openBrowser = original }()

	var err error
	output := captureStdout(t, func() {
		err = runURL(context.Background(), []string{"--site_id=p1/s1"})
	})
	if err != nil {
		t.Fatalf("runURL: %v", err)
	}

	host := strings.TrimPrefix(server.URL, "http://")
	for _, want := range []string{
		"Preview:", "http://s1.preview." + host + "/",
		"Live:", "http://demo.creght.cn/", "v11",
		"http://www.example.com/", "v12 (pinned)",
		"Editor:", "/teditor/project/p1/site/s1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	if len(opened) != 0 {
		t.Fatalf("runURL opened a browser without --open: %v", opened)
	}
}

func TestRunURLOpenLaunchesThePreviewURL(t *testing.T) {
	setupTestConfig(t)
	server := urlStubServer(t, urlPublishStateBody)
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	opened := []string{}
	original := openBrowser
	openBrowser = func(rawURL string) error {
		opened = append(opened, rawURL)
		return nil
	}
	defer func() { openBrowser = original }()

	var err error
	output := captureStdout(t, func() {
		err = runURL(context.Background(), []string{"--site_id=p1/s1", "--open"})
	})
	if err != nil {
		t.Fatalf("runURL --open: %v", err)
	}

	want := "http://s1.preview." + strings.TrimPrefix(server.URL, "http://") + "/"
	if len(opened) != 1 || opened[0] != want {
		t.Fatalf("opened = %v, want [%s]", opened, want)
	}
	// --open still prints, so the address survives in the scrollback.
	if !strings.Contains(output, want) {
		t.Fatalf("--open printed no addresses:\n%s", output)
	}
}

func TestRunURLJSONCarriesEveryAddress(t *testing.T) {
	setupTestConfig(t)
	server := urlStubServer(t, urlPublishStateBody)
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	var err error
	output := captureStdout(t, func() {
		err = runURL(context.Background(), []string{"--site_id=p1/s1", "--json"})
	})
	if err != nil {
		t.Fatalf("runURL --json: %v", err)
	}

	var got siteURLs
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, output)
	}
	if !strings.HasPrefix(got.Preview, "http://s1.preview.") {
		t.Fatalf("preview = %q", got.Preview)
	}
	if !strings.Contains(got.Editor, "/teditor/project/p1/site/s1") {
		t.Fatalf("editor = %q", got.Editor)
	}
	if len(got.Live) != 2 {
		t.Fatalf("live = %+v, want two domains", got.Live)
	}
	if got.Live[0].URL != "http://demo.creght.cn/" || got.Live[0].VersionNo != 11 || got.Live[0].Pinned {
		t.Fatalf("following domain = %+v", got.Live[0])
	}
	if got.Live[1].URL != "http://www.example.com/" || got.Live[1].VersionNo != 12 || !got.Live[1].Pinned {
		t.Fatalf("pinned domain = %+v", got.Live[1])
	}
}

// A site whose publish panel cannot be read is still worth answering for: the
// preview and editor addresses do not depend on it.
func TestRunURLKeepsPreviewWhenPublishStateFails(t *testing.T) {
	setupTestConfig(t)
	server := urlStubServer(t, "")
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	var err error
	output := captureStdout(t, func() {
		err = runURL(context.Background(), []string{"--site_id=p1/s1"})
	})
	if err != nil {
		t.Fatalf("runURL: %v", err)
	}
	if !strings.Contains(output, "s1.preview.") || !strings.Contains(output, "Editor:") {
		t.Fatalf("output dropped the addresses it could resolve:\n%s", output)
	}
	if !strings.Contains(output, "unavailable") {
		t.Fatalf("output does not mark the live addresses unavailable:\n%s", output)
	}
}

func TestPrintSiteURLsWithoutPublishedVersion(t *testing.T) {
	var out strings.Builder
	printSiteURLs(&out, siteURLs{
		Preview: "https://s1.preview.creght.cn/",
		Editor:  "https://creght.cn/teditor/project/p1/site/s1",
	})

	got := out.String()
	if !strings.Contains(got, "nothing published yet; run creght publish") {
		t.Fatalf("unpublished site output = %q", got)
	}
}

func TestLiveURLsFallBackToTheSystemDomain(t *testing.T) {
	live := liveURLs(creght.SitePublishState{
		SystemDomain:     "demo.creght.cn",
		CurrentVersionID: 455,
		CurrentVersionNo: 11,
	}, "https")

	if len(live) != 1 {
		t.Fatalf("live = %+v, want the system domain", live)
	}
	if live[0].URL != "https://demo.creght.cn/" || !live[0].System || live[0].VersionNo != 11 {
		t.Fatalf("system domain = %+v", live[0])
	}
}

func TestLiveURLsOnAnUnpublishedSite(t *testing.T) {
	live := liveURLs(creght.SitePublishState{
		Domains: []creght.SitePublishDomain{{Domain: "demo.creght.cn", System: true, Follow: true}},
	}, "https")

	if len(live) != 1 {
		t.Fatalf("live = %+v, want the domain with no version", live)
	}
	if note := liveURLNote(live[0]); note != "not published" {
		t.Fatalf("note = %q, want \"not published\"", note)
	}
}

func TestDomainURL(t *testing.T) {
	tests := []struct {
		scheme string
		domain string
		want   string
	}{
		{scheme: "https", domain: "demo.creght.cn", want: "https://demo.creght.cn/"},
		{scheme: "http", domain: "demo.localhost:8433", want: "http://demo.localhost:8433/"},
		{scheme: "", domain: "demo.creght.cn", want: "https://demo.creght.cn/"},
		{scheme: "https", domain: "https://demo.creght.cn", want: "https://demo.creght.cn/"},
		{scheme: "https", domain: "  ", want: ""},
	}

	for _, tt := range tests {
		if got := domainURL(tt.scheme, tt.domain); got != tt.want {
			t.Fatalf("domainURL(%q, %q) = %q, want %q", tt.scheme, tt.domain, got, tt.want)
		}
	}
}

func TestPreviewURLForHost(t *testing.T) {
	if got := previewURLForHost("https://creght.cn", "s1"); got != "https://s1.preview.creght.cn/" {
		t.Fatalf("previewURLForHost = %q", got)
	}
	if got := previewURLForHost("  ", "s1"); got != "" {
		t.Fatalf("previewURLForHost with no host = %q, want empty", got)
	}
}

func TestPrintPublishResultNamesDomainsAsURLs(t *testing.T) {
	var out strings.Builder
	printPublishResult(&out, "p1", "s1", creght.PublishVersionResult{
		VersionID: 456,
		VersionNo: 12,
		Targets:   []string{"demo.creght.cn", "www.example.com"},
	}, "https")

	got := out.String()
	for _, want := range []string{
		"Published p1/s1",
		"version 12 (id 456) is live on:",
		"  https://demo.creght.cn/",
		"  https://www.example.com/",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}
