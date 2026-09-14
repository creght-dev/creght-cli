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

// releaseDownloadServer serves one release asset and its checksums.txt, the way
// the GitHub download host does.
func releaseDownloadServer(t *testing.T, releaseVersion string, binary string) *httptest.Server {
	t.Helper()

	assetName := releaseAssetName(releaseVersion, runtime.GOOS, runtime.GOARCH)
	archive := testTarGz(t, map[string]string{"creght": binary})
	sum := sha256.Sum256(archive)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetName)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, assetName):
			_, _ = w.Write(archive)
		case strings.HasSuffix(r.URL.Path, "checksums.txt"):
			_, _ = w.Write([]byte(sums))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

func swapReleaseDownloadURL(url string) func() {
	original := releaseDownloadURL
	releaseDownloadURL = url
	return func() { releaseDownloadURL = original }
}

func swapRunningExecutable(t *testing.T, path string) {
	t.Helper()

	original := runningExecutable
	runningExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { runningExecutable = original })
}

// shellBinary is a stand-in for the real binary in the tests that have to run
// what was installed.
func shellBinary(body string) string {
	return "#!/bin/sh\n" + body + "\n"
}

// An installed binary that does not run, or is not the version it claimed to
// be, must not be the state the user is left in: the command that repairs a
// broken install is the command that just broke.
func TestUpdateBinaryInPlaceRestoresThePreviousBinaryWhenTheNewOneFailsToRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the smoke test stands a shell script in for the binary")
	}

	cases := []struct {
		name      string
		installed string
	}{
		{name: "does not run", installed: shellBinary("exit 1")},
		{name: "is the wrong version", installed: shellBinary("echo 0.11.0")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := releaseDownloadServer(t, "0.13.0", tc.installed)
			t.Cleanup(swapReleaseDownloadURL(server.URL))

			dir := t.TempDir()
			exePath := filepath.Join(dir, "creght")
			previous := shellBinary("echo 0.12.1")
			if err := os.WriteFile(exePath, []byte(previous), 0o755); err != nil {
				t.Fatalf("seed binary: %v", err)
			}

			var err error
			_ = captureStdout(t, func() {
				err = updateBinaryInPlace(context.Background(), "0.13.0", exePath)
			})

			if err == nil || !strings.Contains(err.Error(), "previous binary was restored") {
				t.Fatalf("err = %v, want a rollback", err)
			}
			body, readErr := os.ReadFile(exePath)
			if readErr != nil {
				t.Fatalf("read binary: %v", readErr)
			}
			if string(body) != previous {
				t.Fatalf("body = %q, want the previous binary back", body)
			}
			info, statErr := os.Stat(exePath)
			if statErr != nil {
				t.Fatalf("stat: %v", statErr)
			}
			if info.Mode().Perm()&0o111 == 0 {
				t.Fatalf("mode = %v, want the restored binary to stay executable", info.Mode())
			}
		})
	}
}

func TestUpdateBinaryInPlaceKeepsABinaryThatRunsAndReportsTheNewVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the smoke test stands a shell script in for the binary")
	}
	server := releaseDownloadServer(t, "0.13.0", shellBinary("echo 0.13.0"))
	t.Cleanup(swapReleaseDownloadURL(server.URL))

	dir := t.TempDir()
	exePath := filepath.Join(dir, "creght")
	if err := os.WriteFile(exePath, []byte(shellBinary("echo 0.12.1")), 0o755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}

	var err error
	_ = captureStdout(t, func() {
		err = updateBinaryInPlace(context.Background(), "0.13.0", exePath)
	})
	if err != nil {
		t.Fatalf("updateBinaryInPlace: %v", err)
	}

	body, readErr := os.ReadFile(exePath)
	if readErr != nil {
		t.Fatalf("read binary: %v", readErr)
	}
	if string(body) != shellBinary("echo 0.13.0") {
		t.Fatalf("body = %q, want the new binary", body)
	}
	// Neither the temp file nor a rollback copy may be left beside it.
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("read dir: %v", readErr)
	}
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries, want only the binary", len(entries))
	}
}

// writeInstalledNPMPackage builds an npm install the way npm leaves it: the
// package.json npm shipped, plus the vendored binary for this platform.
func writeInstalledNPMPackage(t *testing.T, installedVersion string, binary string) (root string, exePath string) {
	t.Helper()

	root = t.TempDir()
	binDir := filepath.Join(root, "vendor", "darwin-arm64")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create vendor dir: %v", err)
	}
	exePath = filepath.Join(binDir, "creght")
	if err := os.WriteFile(exePath, []byte(binary), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	pkg := fmt.Sprintf("{\n  \"name\": %q,\n  \"version\": %q,\n  \"bin\": {\n    \"creght\": \"bin/creght.js\"\n  }\n}\n", npmPackageName, installedVersion)
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	return root, exePath
}

// fakeNPMOnPath puts an npm on PATH that records having been run, so a test can
// assert whether the update shelled out to it.
func fakeNPMOnPath(t *testing.T) (marker string) {
	t.Helper()

	dir := t.TempDir()
	marker = filepath.Join(dir, "npm-was-run")
	script := shellBinary(fmt.Sprintf("echo ran > %q", marker))
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake npm: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return marker
}

func npmWasRun(t *testing.T, marker string) bool {
	t.Helper()

	_, err := os.Stat(marker)
	return err == nil
}

// The background worker must not run `npm install -g`: it takes the command
// offline for as long as the reinstall runs, and a worker killed in that window
// leaves it offline for good.
func TestUpdateAutoSwapsTheVendoredBinaryWithoutRunningNPM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the smoke test stands a shell script in for the binary")
	}
	swapUpdateStateDir(t)
	t.Cleanup(swapVersion("0.12.1"))
	api := releaseAPIServer(t, "v0.13.0")
	t.Cleanup(swapReleaseAPIBaseURL(api.URL))
	download := releaseDownloadServer(t, "0.13.0", shellBinary("echo 0.13.0"))
	t.Cleanup(swapReleaseDownloadURL(download.URL))
	marker := fakeNPMOnPath(t)

	root, exePath := writeInstalledNPMPackage(t, "0.12.1", shellBinary("echo 0.12.1"))
	swapRunningExecutable(t, exePath)

	var err error
	_ = captureStdout(t, func() { err = runUpdate(context.Background(), []string{"--auto"}) })
	if err != nil {
		t.Fatalf("runUpdate --auto: %v", err)
	}

	if npmWasRun(t, marker) {
		t.Fatalf("the background worker ran npm")
	}
	body, readErr := os.ReadFile(exePath)
	if readErr != nil {
		t.Fatalf("read binary: %v", readErr)
	}
	if string(body) != shellBinary("echo 0.13.0") {
		t.Fatalf("body = %q, want the new binary", body)
	}

	// The package must not keep claiming the version npm installed, or the next
	// `npm update` would reinstall over the new binary.
	pkg, readErr := os.ReadFile(filepath.Join(root, "package.json"))
	if readErr != nil {
		t.Fatalf("read package.json: %v", readErr)
	}
	if !strings.Contains(string(pkg), `"version": "0.13.0"`) {
		t.Fatalf("package.json = %q, want version 0.13.0", pkg)
	}

	state, stateErr := loadUpdateState()
	if stateErr != nil {
		t.Fatalf("loadUpdateState: %v", stateErr)
	}
	if state.UpdatedFrom != "0.12.1" || state.UpdatedTo != "0.13.0" {
		t.Fatalf("state = %+v, want the notice for the next start", state)
	}
}

// A manual update is interactive and can be retried, so it keeps going through
// npm and leaves the whole package — the bin/creght.js wrapper included — at the
// version it says it is.
func TestManualUpdateStillGoesThroughNPM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake npm on PATH is a shell script")
	}
	swapUpdateStateDir(t)
	t.Cleanup(swapVersion("0.12.1"))
	api := releaseAPIServer(t, "v0.13.0")
	t.Cleanup(swapReleaseAPIBaseURL(api.URL))
	marker := fakeNPMOnPath(t)

	_, exePath := writeInstalledNPMPackage(t, "0.12.1", shellBinary("echo 0.12.1"))
	swapRunningExecutable(t, exePath)

	var err error
	_ = captureStdout(t, func() { err = runUpdate(context.Background(), nil) })
	if err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	if !npmWasRun(t, marker) {
		t.Fatalf("a manual update must hand the install to npm")
	}
}

func TestSetNPMPackageVersionRewritesOnlyTheVersionField(t *testing.T) {
	root, _ := writeInstalledNPMPackage(t, "0.12.1", "binary")
	path := filepath.Join(root, "package.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read package.json: %v", err)
	}

	if err := setNPMPackageVersion(root, "0.13.0"); err != nil {
		t.Fatalf("setNPMPackageVersion: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read package.json: %v", err)
	}
	want := strings.Replace(string(before), `"version": "0.12.1"`, `"version": "0.13.0"`, 1)
	if string(after) != want {
		t.Fatalf("package.json =\n%s\nwant\n%s", after, want)
	}
}

func TestSetNPMPackageVersionRejectsAPackageItDoesNotRecognize(t *testing.T) {
	root := t.TempDir()
	pkg := `{"name":"something-else","version":"0.12.1"}`
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	if err := setNPMPackageVersion(root, "0.13.0"); err == nil {
		t.Fatalf("err = nil, want a refusal to rewrite a foreign package")
	}
}
