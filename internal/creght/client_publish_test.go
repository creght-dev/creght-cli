package creght

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublishSiteUsesVersionAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/u/project/project-1/site/site-1/publish/version" {
			t.Fatalf("path = %s, want version publish API", r.URL.Path)
		}

		var body struct {
			VersionID int64  `json:"version_id"`
			Note      string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.VersionID != 0 {
			t.Fatalf("version_id = %d, want 0", body.VersionID)
		}
		if body.Note != "Release note" {
			t.Fatalf("note = %q, want trimmed note", body.Note)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version_id":123,"version_no":3,"created":true,"published":true}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	if err := client.PublishSite(context.Background(), "project-1", "site-1", "  Release note  "); err != nil {
		t.Fatalf("PublishSite: %v", err)
	}
}
