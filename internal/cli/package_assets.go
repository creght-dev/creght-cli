package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creght-dev/creght-cli/internal/creght"
)

// Client-side asset download for `creght export`.
//
// Servers with export capability >= 3 can return just the project data and the
// list of referenced asset files. The CLI then downloads the files itself and
// writes the same package format the server would have produced, so `creght
// import` does not care which way a package was made. Downloading here instead
// of on the server keeps big projects from tying up the server, and lets the
// CLI show real progress, download in parallel and retry.

const (
	packageAssetMaxBytes = 200 << 20 // same limit as the server: bigger files stay at their old URL
	packageAssetWorkers  = 4
	packageAssetRetries  = 3
)

type packageAsset struct {
	URL         string `json:"url"`
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

var assetHTTP = &http.Client{Timeout: 10 * time.Minute}

// exportWithClientAssets fetches the manifest, downloads the assets and writes
// the finished package to file. It returns the package size.
func exportWithClientAssets(ctx context.Context, client *creght.Client, apiHost, projectID string, siteIDs, with []string, file string) (int64, error) {
	var manifest bytes.Buffer
	if _, err := client.ExportProjectPackage(ctx, projectID, siteIDs, with, true, &manifest); err != nil {
		return 0, err
	}
	data, assets, err := readManifest(manifest.Bytes())
	if err != nil {
		return 0, err
	}

	tmpDir, err := os.MkdirTemp("", "creght-export-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmpDir)

	var total int64
	for _, a := range assets {
		total += a.Size
	}
	prog := newTransferProgress("downloading assets", total)
	prog.filesTotal = len(assets)
	results := downloadAssets(ctx, assets, apiHost, tmpDir, prog)
	prog.done()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}

	tmp := file + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	n, werr := writePackage(out, data, assets, results)
	if cerr := out.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return 0, werr
	}
	if err := os.Rename(tmp, file); err != nil {
		return 0, err
	}
	var failed int
	for _, r := range results {
		if r.err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  not packed (stays at its old URL): %s: %v\n", r.asset.URL, r.err)
		}
	}
	fmt.Printf("  assets: %d packed", len(assets)-failed)
	if failed > 0 {
		fmt.Printf(", %d not packed", failed)
	}
	fmt.Println()
	return n, nil
}

// readManifest returns project_data.json as a generic map (so fields this CLI
// does not know about are kept as they are) and its asset list.
func readManifest(zipBytes []byte) (map[string]json.RawMessage, []packageAsset, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, nil, fmt.Errorf("read manifest: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != "project_data.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, nil, err
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, nil, err
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(raw, &data); err != nil {
			return nil, nil, fmt.Errorf("parse project_data.json: %w", err)
		}
		var assets []packageAsset
		if a := data["assets"]; len(a) > 0 {
			if err := json.Unmarshal(a, &assets); err != nil {
				return nil, nil, fmt.Errorf("parse asset list: %w", err)
			}
		}
		for _, a := range assets {
			if !validAssetKey(a.Key) {
				return nil, nil, fmt.Errorf("unsafe asset path in manifest: %q", a.Key)
			}
		}
		return data, assets, nil
	}
	return nil, nil, errors.New("manifest has no project_data.json")
}

func validAssetKey(key string) bool {
	return key != "" && !strings.HasPrefix(key, "/") && !strings.Contains(key, "\\") && path.Clean(key) == key && !strings.HasPrefix(key, "../") && key != ".."
}

type assetResult struct {
	asset       packageAsset
	file        string
	size        int64
	contentType string
	err         error
}

// downloadAssets downloads every asset into dir. Relative URLs (a self-hosted
// deployment without its own domain yet serves assets at /creght-assets/...)
// are resolved against apiHost.
func downloadAssets(ctx context.Context, assets []packageAsset, apiHost, dir string, prog *transferProgress) []assetResult {
	results := make([]assetResult, len(assets))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < packageAssetWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				a := assets[i]
				r := assetResult{asset: a, file: filepath.Join(dir, fmt.Sprintf("%d", i))}
				for attempt := 1; ; attempt++ {
					r.size, r.contentType, r.err = downloadAsset(ctx, absoluteAssetURL(apiHost, a.URL), r.file, prog)
					if r.err == nil || attempt >= packageAssetRetries || ctx.Err() != nil || errors.Is(r.err, errAssetTooLarge) {
						break
					}
					time.Sleep(time.Duration(attempt) * time.Second)
				}
				results[i] = r
				prog.fileDone()
			}
		}()
	}
	for i := range assets {
		select {
		case jobs <- i:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

func absoluteAssetURL(apiHost, u string) string {
	if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") {
		return strings.TrimSuffix(apiHost, "/") + u
	}
	return u
}

var errAssetTooLarge = fmt.Errorf("larger than %d MB", packageAssetMaxBytes>>20)

func downloadAsset(ctx context.Context, url, file string, prog *transferProgress) (int64, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := assetHTTP.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > packageAssetMaxBytes {
		return 0, "", errAssetTooLarge
	}
	f, err := os.Create(file)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	// A retry starts over; take back the bytes the failed attempt counted.
	var counted int64
	n, err := io.Copy(f, progressReader{r: io.LimitReader(resp.Body, packageAssetMaxBytes+1), p: prog, counted: &counted})
	if err != nil {
		prog.add(-int(counted))
		return 0, "", err
	}
	if n > packageAssetMaxBytes {
		prog.add(-int(counted))
		return 0, "", errAssetTooLarge
	}
	return n, resp.Header.Get("Content-Type"), nil
}

// writePackage writes assets/<key> for every downloaded file, then
// project_data.json with the final asset list. Files that could not be
// downloaded go to missing_assets and keep pointing at their old URL after
// import, the same as a server-side export.
func writePackage(w io.Writer, data map[string]json.RawMessage, assets []packageAsset, results []assetResult) (int64, error) {
	cw := &countingWriter{w: w}
	zw := zip.NewWriter(cw)
	var packed []packageAsset
	var missing []string
	if m := data["missing_assets"]; len(m) > 0 {
		_ = json.Unmarshal(m, &missing)
	}
	for _, r := range results {
		if r.err != nil {
			missing = append(missing, r.asset.URL)
			continue
		}
		// Images and videos are already compressed: store, do not deflate.
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: "assets/" + r.asset.Key, Method: zip.Store, Modified: time.Now()})
		if err != nil {
			return 0, err
		}
		f, err := os.Open(r.file)
		if err != nil {
			return 0, err
		}
		_, err = io.Copy(fw, f)
		f.Close()
		if err != nil {
			return 0, err
		}
		a := r.asset
		a.Size = r.size
		if r.contentType != "" {
			a.ContentType = r.contentType
		}
		packed = append(packed, a)
	}
	var err error
	if data["assets"], err = json.Marshal(packed); err != nil {
		return 0, err
	}
	if data["missing_assets"], err = json.Marshal(missing); err != nil {
		return 0, err
	}
	delete(data, "assets_not_included")
	jf, err := zw.Create("project_data.json")
	if err != nil {
		return 0, err
	}
	if err := json.NewEncoder(jf).Encode(data); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	return cw.n, nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}
