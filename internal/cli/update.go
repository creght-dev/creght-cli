package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	releaseOwner   = "creght-dev"
	releaseRepo    = "creght-cli"
	npmPackageName = "creght-cli"
)

// updateHTTPTimeout bounds the release-lookup call to the GitHub API. A
// self-update that hangs is worse than one that fails: the user is usually
// running it before doing something else.
const updateHTTPTimeout = 60 * time.Second

// downloadHTTPTimeout bounds each fetch from the release download host — the
// archive and checksums.txt. The archive is tens of megabytes, and the host
// itself can be slow to even respond on some networks (observed: a tiny
// checksums.txt blowing a 60-second budget), so both get the generous bound.
const downloadHTTPTimeout = 15 * time.Minute

// releaseBaseURL and apiBaseURL are variables so tests can point them at a local
// server instead of GitHub.
var (
	releaseAPIBaseURL  = "https://api.github.com"
	releaseDownloadURL = "https://github.com"
)

func runUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	checkOnly := fs.Bool("check", false, "report the latest release without installing it")
	auto := fs.Bool("auto", false, "run as the detached background auto-update worker")
	err := fs.Parse(args)
	if err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("update does not accept positional arguments")
	}

	current := strings.TrimSpace(version)
	latest, err := latestReleaseVersion(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("Installed: %s\n", current)
	fmt.Printf("Latest:    %s\n", latest)

	if *checkOnly {
		if current != "dev" && compareVersions(current, latest) >= 0 {
			fmt.Println("Already up to date.")
		}
		return nil
	}

	// A dev build is someone's local `go build`; replacing it with a release
	// binary would silently throw away what they are working on.
	if current == "dev" {
		if *auto {
			// The spawner skips dev builds already; this is a second guard so a
			// stray worker can never overwrite one either.
			return nil
		}
		return fmt.Errorf("this is a local dev build, not an installed release; build from source instead of self-updating")
	}
	if compareVersions(current, latest) >= 0 {
		fmt.Println("Already up to date.")
		return nil
	}

	exePath, err := runningExecutable()
	if err != nil {
		return err
	}

	if root, ok := npmPackageRoot(exePath); ok {
		err = updateViaNPM(ctx, latest, root)
	} else {
		err = updateBinaryInPlace(ctx, latest, exePath)
	}
	if err != nil {
		return err
	}

	// The worker's own output lands in update.log, so the user learns about the
	// install from the notice the next start prints.
	if *auto {
		if err := recordAutoUpdate(current, latest); err != nil {
			return fmt.Errorf("record the auto-update for the next start: %w", err)
		}
	}
	return nil
}

// runningExecutable resolves the binary this process was started from, following
// symlinks so a Homebrew-style symlinked path is replaced at its real location
// rather than turned into a regular file.
func runningExecutable() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate the running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}

	return exePath, nil
}

// npmPackageRoot reports whether the running binary is the one the npm package
// vendors, and where that package lives.
//
// The npm package ships as bin/creght.js plus vendor/<platform>-<arch>/creght,
// so the layout alone is not proof — the package.json name is checked too, or a
// stray vendor/ directory elsewhere would be mistaken for an npm install.
func npmPackageRoot(exePath string) (string, bool) {
	vendorDir := filepath.Dir(filepath.Dir(exePath))
	if filepath.Base(vendorDir) != "vendor" {
		return "", false
	}
	root := filepath.Dir(vendorDir)

	body, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return "", false
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &pkg); err != nil || pkg.Name != npmPackageName {
		return "", false
	}

	return root, true
}

// updateViaNPM hands the update back to npm rather than overwriting the vendored
// binary directly. Writing the binary alone would leave the package's
// package.json claiming the old version, so the next `npm update` or reinstall
// would silently undo it.
func updateViaNPM(ctx context.Context, latest string, root string) error {
	npm, err := exec.LookPath("npm")
	if err != nil {
		return fmt.Errorf("this build was installed by npm (%s), but npm is not on PATH; "+
			"run npm install -g %s@%s yourself", root, npmPackageName, latest)
	}

	spec := npmPackageName + "@" + latest
	fmt.Printf("Updating the npm install at %s\n", root)
	fmt.Printf("Running: npm install -g %s\n", spec)

	cmd := exec.CommandContext(ctx, npm, "install", "-g", spec)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm install -g %s: %w", spec, err)
	}

	fmt.Printf("Updated to %s.\n", latest)
	return nil
}

// updateBinaryInPlace downloads the release archive for this platform, checks it
// against the release checksums, and swaps the binary.
//
// The checksum is not optional politeness: this writes an executable that the
// user will run, so a truncated or tampered download must fail loudly rather
// than land on disk.
func updateBinaryInPlace(ctx context.Context, latest string, exePath string) error {
	assetName := releaseAssetName(latest, runtime.GOOS, runtime.GOARCH)
	base := fmt.Sprintf("%s/%s/%s/releases/download/v%s", releaseDownloadURL, releaseOwner, releaseRepo, latest)

	fmt.Printf("Downloading %s\n", assetName)
	archive, err := httpGetBytesTimeout(ctx, base+"/"+assetName, downloadHTTPTimeout)
	if err != nil {
		return fmt.Errorf("download %s: %w", assetName, err)
	}

	sums, err := httpGetBytesTimeout(ctx, base+"/checksums.txt", downloadHTTPTimeout)
	if err != nil {
		return fmt.Errorf("download checksums.txt: %w", err)
	}
	if err := verifyChecksum(archive, assetName, string(sums)); err != nil {
		return err
	}

	binary, err := extractBinary(archive, assetName)
	if err != nil {
		return err
	}
	if err := replaceExecutable(exePath, binary); err != nil {
		return err
	}

	fmt.Printf("Updated %s to %s.\n", exePath, latest)
	return nil
}

func releaseAssetName(version string, goos string, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}

	return fmt.Sprintf("creght_%s_%s_%s.%s", version, goos, goarch, ext)
}

// verifyChecksum matches the download against the release's checksums.txt, whose
// lines are "<sha256>  <filename>".
func verifyChecksum(archive []byte, assetName string, sums string) error {
	want := ""
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 2 && fields[1] == assetName {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("checksums.txt does not list %s; refusing to install an unverified binary", assetName)
	}

	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", assetName, got, want)
	}

	return nil
}

// extractBinary pulls the creght executable out of the release archive.
func extractBinary(archive []byte, assetName string) ([]byte, error) {
	if strings.HasSuffix(assetName, ".zip") {
		return extractFromZip(archive)
	}
	return extractFromTarGz(archive)
}

func extractFromTarGz(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	defer gz.Close()

	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if filepath.Base(header.Name) != "creght" || header.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("read creght from archive: %w", err)
		}
		return body, nil
	}

	return nil, fmt.Errorf("archive does not contain a creght binary")
}

func extractFromZip(archive []byte) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	for _, file := range reader.File {
		if filepath.Base(file.Name) != "creght.exe" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("read creght.exe from archive: %w", err)
		}
		defer rc.Close()
		body, err := io.ReadAll(rc)
		if err != nil {
			return nil, fmt.Errorf("read creght.exe from archive: %w", err)
		}
		return body, nil
	}

	return nil, fmt.Errorf("archive does not contain a creght.exe binary")
}

// replaceExecutable swaps the binary at path for body.
//
// The new file is written beside the target and renamed over it, so a failed
// download or a full disk never leaves a half-written executable in place. On
// Windows the running image cannot be replaced, so the old one is moved aside
// first and cleaned up on the next run.
func replaceExecutable(path string, body []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".creght-update-*")
	if err != nil {
		return fmt.Errorf("write to %s: %w (is it writable? re-run with the permissions the install needs)", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("write new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write new binary: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("make new binary executable: %w", err)
	}

	if runtime.GOOS == "windows" {
		old := path + ".old"
		_ = os.Remove(old)
		if err := os.Rename(path, old); err != nil {
			return fmt.Errorf("move the running binary aside: %w", err)
		}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}

	return nil
}

// latestReleaseVersion asks GitHub for the newest release tag, without the "v".
func latestReleaseVersion(ctx context.Context) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", releaseAPIBaseURL, releaseOwner, releaseRepo)
	body, err := httpGetBytes(ctx, url)
	if err != nil {
		return "", fmt.Errorf("check the latest release: %w", err)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return "", fmt.Errorf("parse the latest release: %w", err)
	}
	tag := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	if tag == "" {
		return "", fmt.Errorf("the latest release has no tag name")
	}

	return tag, nil
}

func httpGetBytes(ctx context.Context, url string) ([]byte, error) {
	return httpGetBytesTimeout(ctx, url, updateHTTPTimeout)
}

func httpGetBytesTimeout(ctx context.Context, url string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "creght-cli/"+version)
	req.Header.Set("Accept", "application/octet-stream, application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}

	return io.ReadAll(resp.Body)
}

// compareVersions orders two dotted numeric versions, returning -1, 0, or 1.
//
// Release tags here are plain x.y.z, so this deliberately does not implement
// full semver: anything it cannot parse as a number sorts as 0, which keeps an
// unexpected tag from being read as newer than it is.
func compareVersions(a string, b string) int {
	aParts := versionParts(a)
	bParts := versionParts(b)
	for i := 0; i < len(aParts) || i < len(bParts); i++ {
		var aPart, bPart int
		if i < len(aParts) {
			aPart = aParts[i]
		}
		if i < len(bParts) {
			bPart = bParts[i]
		}
		if aPart != bPart {
			if aPart < bPart {
				return -1
			}
			return 1
		}
	}

	return 0
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	fields := strings.Split(v, ".")
	parts := make([]int, 0, len(fields))
	for _, field := range fields {
		n, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil {
			n = 0
		}
		parts = append(parts, n)
	}

	return parts
}
