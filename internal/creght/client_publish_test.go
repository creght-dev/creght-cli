package creght

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// publishVersionRequest is what the CLI sends to the publish/version endpoint.
// create_only is a pointer so tests can tell "absent" from "false".
type publishVersionRequest struct {
	VersionID  int64  `json:"version_id"`
	Note       string `json:"note"`
	CreateOnly *bool  `json:"create_only"`
}

// publishVersionServer captures the publish/version request a client call makes
// and replies with the given JSON body.
func publishVersionServer(t *testing.T, responseBody string) (*httptest.Server, *publishVersionRequest) {
	t.Helper()
	var got publishVersionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/u/project/project-1/site/site-1/publish/version" {
			t.Errorf("path = %s, want version publish API", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(server.Close)
	return server, &got
}

func TestPublishSiteUsesVersionAPI(t *testing.T) {
	server, got := publishVersionServer(t, `{"version_id":123,"version_no":3,"created":true,"published":true,"changed":true,"targets":["demo.creght.cn"]}`)

	client := NewClient(server.URL, "")
	result, err := client.PublishSite(context.Background(), "project-1", "site-1", "  Release note  ")
	if err != nil {
		t.Fatalf("PublishSite: %v", err)
	}

	if got.VersionID != 0 {
		t.Fatalf("version_id = %d, want 0", got.VersionID)
	}
	if got.Note != "Release note" {
		t.Fatalf("note = %q, want trimmed note", got.Note)
	}
	// A plain publish must not set create_only, or the site would never go live.
	if got.CreateOnly != nil && *got.CreateOnly {
		t.Fatalf("create_only = true, want unset for publish")
	}
	if result.VersionID != 123 || result.VersionNo != 3 {
		t.Fatalf("result = %+v, want version 3 (id 123)", result)
	}
	if !result.Created || !result.Published || !result.Changed {
		t.Fatalf("result = %+v, want created+published+changed", result)
	}
	if len(result.Targets) != 1 || result.Targets[0] != "demo.creght.cn" {
		t.Fatalf("targets = %v, want [demo.creght.cn]", result.Targets)
	}
}

func TestCreateSiteVersionSnapshotsWithoutPublishing(t *testing.T) {
	server, got := publishVersionServer(t, `{"version_id":456,"version_no":12,"created":true}`)

	client := NewClient(server.URL, "")
	result, err := client.CreateSiteVersion(context.Background(), "project-1", "site-1", "  Add pricing page  ")
	if err != nil {
		t.Fatalf("CreateSiteVersion: %v", err)
	}

	if got.VersionID != 0 {
		t.Fatalf("version_id = %d, want 0 so the server snapshots the workspace", got.VersionID)
	}
	if got.CreateOnly == nil || !*got.CreateOnly {
		t.Fatalf("create_only = %v, want true so the live site does not move", got.CreateOnly)
	}
	if got.Note != "Add pricing page" {
		t.Fatalf("note = %q, want trimmed note", got.Note)
	}
	if result.VersionNo != 12 || result.VersionID != 456 {
		t.Fatalf("result = %+v, want version 12 (id 456)", result)
	}
	if result.Published {
		t.Fatalf("result.Published = true, want false for a create-only call")
	}
}

func TestPublishSiteVersionSendsVersionID(t *testing.T) {
	server, got := publishVersionServer(t, `{"version_id":456,"version_no":12,"published":true,"changed":true}`)

	client := NewClient(server.URL, "")
	result, err := client.PublishSiteVersion(context.Background(), "project-1", "site-1", 456, "Roll back nav")
	if err != nil {
		t.Fatalf("PublishSiteVersion: %v", err)
	}

	if got.VersionID != 456 {
		t.Fatalf("version_id = %d, want 456", got.VersionID)
	}
	if got.CreateOnly != nil && *got.CreateOnly {
		t.Fatalf("create_only = true, want unset when publishing an existing version")
	}
	if got.Note != "Roll back nav" {
		t.Fatalf("note = %q, want the publish note", got.Note)
	}
	if !result.Published || !result.Changed {
		t.Fatalf("result = %+v, want published+changed", result)
	}
}

// The server reads version_id 0 as "snapshot the workspace and publish that", so
// a non-positive id must never reach it from a publish-this-version call.
func TestPublishSiteVersionRejectsNonPositiveID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL.Path)
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	for _, versionID := range []int64{0, -1} {
		if _, err := client.PublishSiteVersion(context.Background(), "project-1", "site-1", versionID, ""); err == nil {
			t.Fatalf("PublishSiteVersion(%d) = nil error, want rejection", versionID)
		}
	}
}

func TestGetSitePublishState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/u/project/project-1/site/site-1/publish/state" {
			t.Errorf("path = %s, want publish state API", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"versions": [
				{"id":456,"version_no":12,"note":"Add pricing","from":"api_generate_version","created_at":"2026-08-05T14:31:22+08:00"},
				{"id":455,"version_no":11,"note":"Fix nav","from":"publish","created_at":"2026-08-05T11:02:00+08:00"}
			],
			"current_version_id": 455,
			"current_version_no": 11,
			"system_domain": "demo.creght.cn",
			"domains": [
				{"id":0,"domain":"demo.creght.cn","system":true,"follow":true},
				{"id":7,"domain":"www.example.com","publish_version_id":456,"publish_version_no":12}
			],
			"publish_targets": ["demo.creght.cn"],
			"has_changes": true,
			"workspace_changes": [{"path":"/page/Index.tsx","change_action":"update"}]
		}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	state, err := client.GetSitePublishState(context.Background(), "project-1", "site-1")
	if err != nil {
		t.Fatalf("GetSitePublishState: %v", err)
	}

	if len(state.Versions) != 2 {
		t.Fatalf("versions = %d, want 2", len(state.Versions))
	}
	if state.Versions[0].VersionNo != 12 || state.Versions[0].Note != "Add pricing" {
		t.Fatalf("newest version = %+v, want version 12 'Add pricing'", state.Versions[0])
	}
	if state.Versions[0].CreatedAt.IsZero() {
		t.Fatalf("created_at was not parsed")
	}
	if state.CurrentVersionID != 455 || state.CurrentVersionNo != 11 {
		t.Fatalf("live version = %d (no %d), want 455 (no 11)", state.CurrentVersionID, state.CurrentVersionNo)
	}
	if !state.HasChanges || len(state.WorkspaceChanges) != 1 {
		t.Fatalf("workspace changes = %v (has_changes %v), want one pending change", state.WorkspaceChanges, state.HasChanges)
	}
	if state.WorkspaceChanges[0].Path != "/page/Index.tsx" || state.WorkspaceChanges[0].ChangeAction != "update" {
		t.Fatalf("workspace change = %+v, want /page/Index.tsx update", state.WorkspaceChanges[0])
	}
	if len(state.Domains) != 2 || state.Domains[1].Follow {
		t.Fatalf("domains = %+v, want the custom domain pinned", state.Domains)
	}

	version, ok := state.FindVersionByNo(11)
	if !ok || version.ID != 455 {
		t.Fatalf("FindVersionByNo(11) = %+v, %v; want id 455", version, ok)
	}
	if _, ok := state.FindVersionByNo(99); ok {
		t.Fatalf("FindVersionByNo(99) found a version that is not listed")
	}
	version, ok = state.FindVersionByID(456)
	if !ok || version.VersionNo != 12 {
		t.Fatalf("FindVersionByID(456) = %+v, %v; want version_no 12", version, ok)
	}
}
