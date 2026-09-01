package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a    string
		b    string
		want int
	}{
		{"0.12.1", "0.13.0", -1},
		{"0.13.0", "0.12.1", 1},
		{"0.13.0", "0.13.0", 0},
		{"v0.13.0", "0.13.0", 0},
		{"0.9.0", "0.10.0", -1},
		{"1.0", "1.0.0", 0},
		{"0.13.0-rc1", "0.13.0", 0},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestReleaseAssetName(t *testing.T) {
	if got := releaseAssetName("0.13.0", "darwin", "arm64"); got != "creght_0.13.0_darwin_arm64.tar.gz" {
		t.Errorf("darwin asset = %q", got)
	}
	if got := releaseAssetName("0.13.0", "windows", "amd64"); got != "creght_0.13.0_windows_amd64.zip" {
		t.Errorf("windows asset = %q", got)
	}
}

func TestVerifyChecksumAcceptsMatchingDigest(t *testing.T) {
	archive := []byte("archive body")
	sum := sha256.Sum256(archive)
	sums := fmt.Sprintf("deadbeef  other_file.tar.gz\n%s  creght_0.13.0_darwin_arm64.tar.gz\n", hex.EncodeToString(sum[:]))

	if err := verifyChecksum(archive, "creght_0.13.0_darwin_arm64.tar.gz", sums); err != nil {
		t.Fatalf("verifyChecksum: %v", err)
	}
}

// A tampered or truncated download must not reach disk as an executable.
func TestVerifyChecksumRejectsMismatch(t *testing.T) {
	sum := sha256.Sum256([]byte("the real archive"))
	sums := fmt.Sprintf("%s  creght_0.13.0_darwin_arm64.tar.gz\n", hex.EncodeToString(sum[:]))

	err := verifyChecksum([]byte("something else"), "creght_0.13.0_darwin_arm64.tar.gz", sums)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
}

func TestVerifyChecksumRejectsUnlistedAsset(t *testing.T) {
	err := verifyChecksum([]byte("body"), "creght_0.13.0_darwin_arm64.tar.gz", "deadbeef  other.tar.gz\n")
	if err == nil || !strings.Contains(err.Error(), "does not list") {
		t.Fatalf("err = %v, want a missing-entry error", err)
	}
}

func testTarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		header := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}

	return buf.Bytes()
}

func TestExtractBinaryFindsCreghtInArchive(t *testing.T) {
	archive := testTarGz(t, map[string]string{"README.md": "docs", "creght": "binary body"})

	body, err := extractBinary(archive, "creght_0.13.0_linux_amd64.tar.gz")
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if string(body) != "binary body" {
		t.Fatalf("body = %q", body)
	}
}

func TestExtractBinaryRejectsArchiveWithoutBinary(t *testing.T) {
	archive := testTarGz(t, map[string]string{"README.md": "docs"})

	_, err := extractBinary(archive, "creght_0.13.0_linux_amd64.tar.gz")
	if err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("err = %v, want a missing-binary error", err)
	}
}

func TestReplaceExecutableSwapsContentAndKeepsItExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the rename-aside path is exercised on Windows only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "creght")
	if err := os.WriteFile(path, []byte("old binary"), 0o755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}

	if err := replaceExecutable(path, []byte("new binary")); err != nil {
		t.Fatalf("replaceExecutable: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	if string(body) != "new binary" {
		t.Fatalf("body = %q", body)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("mode = %v, want an executable bit", info.Mode())
	}
	// The temp file must not be left behind next to the binary.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries, want only the binary", len(entries))
	}
}

// writeNPMLayout builds the npm package's vendor/<platform>-<arch>/creght layout.
func writeNPMLayout(t *testing.T, packageName string) string {
	t.Helper()

	root := t.TempDir()
	binDir := filepath.Join(root, "vendor", "darwin-arm64")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create vendor dir: %v", err)
	}
	exePath := filepath.Join(binDir, "creght")
	if err := os.WriteFile(exePath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	pkg := fmt.Sprintf(`{"name":%q,"version":"0.12.1"}`, packageName)
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	return exePath
}

func TestNPMPackageRootDetectsVendoredBinary(t *testing.T) {
	exePath := writeNPMLayout(t, "creght-cli")

	root, ok := npmPackageRoot(exePath)
	if !ok {
		t.Fatalf("npmPackageRoot did not recognize the npm layout")
	}
	if root != filepath.Dir(filepath.Dir(filepath.Dir(exePath))) {
		t.Fatalf("root = %q", root)
	}
}

// The vendor/ layout alone is not proof; a package of another name is not ours.
func TestNPMPackageRootRejectsForeignPackage(t *testing.T) {
	exePath := writeNPMLayout(t, "something-else")

	if _, ok := npmPackageRoot(exePath); ok {
		t.Fatalf("npmPackageRoot accepted a foreign package")
	}
}

func TestNPMPackageRootRejectsStandaloneBinary(t *testing.T) {
	dir := t.TempDir()
	exePath := filepath.Join(dir, "creght")
	if err := os.WriteFile(exePath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	if _, ok := npmPackageRoot(exePath); ok {
		t.Fatalf("npmPackageRoot accepted a standalone binary")
	}
}

func releaseAPIServer(t *testing.T, tag string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"tag_name":%q}`, tag)
	}))
	t.Cleanup(server.Close)

	return server
}

func TestLatestReleaseVersionStripsTagPrefix(t *testing.T) {
	server := releaseAPIServer(t, "v0.13.0")
	t.Cleanup(swapReleaseAPIBaseURL(server.URL))

	got, err := latestReleaseVersion(context.Background())
	if err != nil {
		t.Fatalf("latestReleaseVersion: %v", err)
	}
	if got != "0.13.0" {
		t.Fatalf("version = %q, want 0.13.0", got)
	}
}

func swapReleaseAPIBaseURL(url string) func() {
	original := releaseAPIBaseURL
	releaseAPIBaseURL = url
	return func() { releaseAPIBaseURL = original }
}

func swapVersion(v string) func() {
	original := version
	version = v
	return func() { version = original }
}

func TestUpdateCheckReportsBothVersionsWithoutInstalling(t *testing.T) {
	server := releaseAPIServer(t, "v0.13.0")
	t.Cleanup(swapReleaseAPIBaseURL(server.URL))
	t.Cleanup(swapVersion("0.12.1"))

	output := captureStdout(t, func() {
		if err := runUpdate(context.Background(), []string{"--check"}); err != nil {
			t.Fatalf("runUpdate: %v", err)
		}
	})

	if !strings.Contains(output, "Installed: 0.12.1") || !strings.Contains(output, "Latest:    0.13.0") {
		t.Fatalf("output = %q", output)
	}
	if strings.Contains(output, "Already up to date") {
		t.Fatalf("output = %q, want no up-to-date claim when behind", output)
	}
}

func TestUpdateReportsUpToDate(t *testing.T) {
	server := releaseAPIServer(t, "v0.13.0")
	t.Cleanup(swapReleaseAPIBaseURL(server.URL))
	t.Cleanup(swapVersion("0.13.0"))

	output := captureStdout(t, func() {
		if err := runUpdate(context.Background(), nil); err != nil {
			t.Fatalf("runUpdate: %v", err)
		}
	})

	if !strings.Contains(output, "Already up to date.") {
		t.Fatalf("output = %q", output)
	}
}

// A local `go build` must never be silently replaced by a release binary.
func TestUpdateRefusesToOverwriteDevBuild(t *testing.T) {
	server := releaseAPIServer(t, "v0.13.0")
	t.Cleanup(swapReleaseAPIBaseURL(server.URL))
	t.Cleanup(swapVersion("dev"))

	var err error
	_ = captureStdout(t, func() { err = runUpdate(context.Background(), nil) })

	if err == nil || !strings.Contains(err.Error(), "local dev build") {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

func TestUpdateRejectsPositionalArguments(t *testing.T) {
	err := runUpdate(context.Background(), []string{"0.13.0"})
	if err == nil || !strings.Contains(err.Error(), "positional") {
		t.Fatalf("err = %v", err)
	}
}
