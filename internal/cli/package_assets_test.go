package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func manifestZip(t *testing.T, data map[string]any) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("project_data.json")
	_ = json.NewEncoder(w).Encode(data)
	_ = zw.Close()
	return buf.Bytes()
}

func TestClientAssetsRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/site/1/a.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png-bytes"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	data, assets, err := readManifest(manifestZip(t, map[string]any{
		"version":             "2.0",
		"project":             map[string]any{"name": "demo"},
		"assets_not_included": true,
		"assets": []map[string]any{
			{"url": srv.URL + "/site/1/a.png", "key": "site/1/a.png", "size": 9},
			{"url": srv.URL + "/site/1/gone.png", "key": "site/1/gone.png"},
			{"url": "/site/1/a.png", "key": "site/1/rel.png"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	prog := newTransferProgress("x", 0)
	results := downloadAssets(context.Background(), assets, srv.URL, t.TempDir(), 0, prog)
	var out bytes.Buffer
	if _, err := writePackage(&out, data, assets, results); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		files[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	if string(files["assets/site/1/a.png"]) != "png-bytes" {
		t.Fatalf("asset content: %q", files["assets/site/1/a.png"])
	}
	if _, ok := files["assets/site/1/gone.png"]; ok {
		t.Fatal("failed asset must not be packed")
	}
	var pd struct {
		Project           map[string]any `json:"project"`
		Assets            []packageAsset `json:"assets"`
		MissingAssets     []string       `json:"missing_assets"`
		AssetsNotIncluded *bool          `json:"assets_not_included"`
	}
	if err := json.Unmarshal(files["project_data.json"], &pd); err != nil {
		t.Fatal(err)
	}
	if string(files["assets/site/1/rel.png"]) != "png-bytes" {
		t.Fatal("relative URL not resolved against the API host")
	}
	if pd.Project["name"] != "demo" || len(pd.Assets) != 2 || pd.Assets[0].ContentType != "image/png" || pd.Assets[0].Size != 9 {
		t.Fatalf("project_data: %+v", pd)
	}
	if len(pd.MissingAssets) != 1 || pd.AssetsNotIncluded != nil {
		t.Fatalf("missing=%v notIncluded=%v", pd.MissingAssets, pd.AssetsNotIncluded)
	}
}

func TestReadManifestRejectsUnsafeKey(t *testing.T) {
	_, _, err := readManifest(manifestZip(t, map[string]any{"assets": []map[string]any{{"url": "http://x/a", "key": "../etc/passwd"}}}))
	if err == nil {
		t.Fatal("expected error for unsafe key")
	}
}
