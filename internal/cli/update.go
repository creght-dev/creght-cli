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
	"regexp"
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

	if *auto {
		release, ok := acquireAutoUpdateLock()
		if !ok {
			// Another worker is already installing. Leaving it to finish alone
			// is the whole point of the lock.
			return nil
		}
		defer release()
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

	// The background worker never shells out to npm: a global reinstall takes
	// the whole command offline for as long as it runs, and a detached worker
	// killed mid-install leaves it that way, with no working `creght` left to
	// repair it. It swaps the vendored binary instead, which is one rename.
	if root, ok := npmPackageRoot(exePath); ok {
		if *auto {
			err = updateNPMVendoredBinary(ctx, latest, exePath, root)
		} else {
			err = updateViaNPM(ctx, latest, root)
		}
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
//
// It is a variable so tests can point it at a temp file rather than have them
// swap the test binary out from under themselves.
var runningExecutable = func() (string, error) {
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

// updateNPMVendoredBinary updates an npm install without running npm, by
// swapping the binary the package vendors and then recording the new version in
// the package's own package.json.
//
// This is the background worker's path. `npm install -g` retires the whole
// package directory and rebuilds the bin symlink, so `creght` is missing for as
// long as the install runs — tens of seconds for a package that vendors every
// platform's binary — and a worker killed in that window leaves the command
// missing for good, with nothing left to run to fix it. Swapping the binary is
// a single rename: the wrapper and the symlink are never touched, and there is
// no instant at which the vendored binary does not exist.
//
// The cost is that the rest of the package — bin/creght.js above all — stays at
// the version npm installed. It is a thin, stable launcher, and a manual
// `creght update` still goes through npm; a release that changes the wrapper
// needs one.
func updateNPMVendoredBinary(ctx context.Context, latest string, exePath string, root string) error {
	fmt.Printf("Updating the npm install at %s in place\n", root)
	if err := updateBinaryInPlace(ctx, latest, exePath); err != nil {
		return err
	}

	// Without this the package would keep claiming the version npm installed,
	// so the next `npm update` would reinstall over the new binary.
	if err := setNPMPackageVersion(root, latest); err != nil {
		// The binary is already the new one and verified; a stale version field
		// costs at most one redundant reinstall later, so it must not turn a
		// finished update into a failure.
		fmt.Printf("Note: could not record %s in the package.json at %s: %v\n", latest, root, err)
	}
	return nil
}

// setNPMPackageVersion rewrites the version field of the npm package's
// package.json in place, leaving every other byte of the file alone.
func setNPMPackageVersion(root string, latest string) error {
	path := filepath.Join(root, "package.json")
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	loc := npmVersionFieldPattern.FindSubmatchIndex(body)
	if loc == nil {
		return fmt.Errorf("%s has no version field", path)
	}
	updated := make([]byte, 0, len(body)+len(latest))
	updated = append(updated, body[:loc[2]]...)
	updated = append(updated, latest...)
	updated = append(updated, body[loc[3]:]...)

	// The rewrite is textual, so prove it produced the intended package before
	// it lands: a mangled package.json would break npm for this package.
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(updated, &pkg); err != nil {
		return fmt.Errorf("rewriting the version field produced invalid JSON: %w", err)
	}
	if pkg.Name != npmPackageName || pkg.Version != latest {
		return fmt.Errorf("rewriting the version field produced %s@%s, want %s@%s", pkg.Name, pkg.Version, npmPackageName, latest)
	}

	return writeFileAtomic(path, updated, 0o644)
}

// npmVersionFieldPattern captures the value of the first "version" key, which in
// a package.json is the package's own.
var npmVersionFieldPattern = regexp.MustCompile(`"version"\s*:\s*"([^"]*)"`)

// writeFileAtomic writes body to path through a temp file in the same directory,
// so a reader never sees a half-written file and a failure leaves the original.
func writeFileAtomic(path string, body []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return err
	}

	return os.Rename(tmpPath, path)
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

	// Kept so a binary that turns out not to run can be put back. The checksum
	// proves the download arrived intact, not that it works here — a release
	// built for the wrong platform, or a kernel that refuses the image, would
	// otherwise leave the user with a command they cannot run and cannot update.
	previous, err := os.ReadFile(exePath)
	if err != nil {
		return fmt.Errorf("read the current binary at %s: %w", exePath, err)
	}

	if err := replaceExecutable(exePath, binary); err != nil {
		return err
	}
	if err := verifyInstalledBinary(exePath, latest); err != nil {
		if rollbackErr := rollbackExecutable(exePath, previous); rollbackErr != nil {
			return fmt.Errorf("%w; restoring the previous binary also failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("%w; the previous binary was restored", err)
	}

	fmt.Printf("Updated %s to %s.\n", exePath, latest)
	return nil
}

// verifyBinaryTimeout bounds the smoke test. The new binary only has to print
// its version; anything slower than this is a binary that does not work here.
const verifyBinaryTimeout = 30 * time.Second

// verifyInstalledBinary runs the freshly installed binary and checks that it
// reports the version it is supposed to be, so the caller can roll back before
// the user is left with a broken command.
func verifyInstalledBinary(path string, want string) error {
	ctx, cancel := context.WithTimeout(context.Background(), verifyBinaryTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "--version")
	// The smoke test must not start an auto-update of its own.
	cmd.Env = append(os.Environ(), autoUpdateEnvOptOut+"=1")
	// Captured rather than inherited: the worker's own stderr is update.log, and
	// what a binary says on its way out is the one clue to why a release is bad.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if detail := firstLine(stderr.String()); detail != "" {
			return fmt.Errorf("the newly installed binary does not run: %w: %s", err, detail)
		}
		return fmt.Errorf("the newly installed binary does not run: %w", err)
	}

	got := strings.TrimSpace(string(out))
	if compareVersions(got, want) != 0 {
		return fmt.Errorf("the newly installed binary reports version %q, want %q", got, want)
	}

	return nil
}

// firstLine trims body to its first non-empty line, so a binary that fails
// noisily still yields a one-line reason.
func firstLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}

	return ""
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
		old := replacedExecutablePath(path)
		_ = os.Remove(old)
		if err := os.Rename(path, old); err != nil {
			return fmt.Errorf("move the running binary aside: %w", err)
		}
		if err := os.Rename(tmpPath, path); err != nil {
			// Put the old binary back. Returning here with the path empty would
			// turn a failed update into a missing command, and the command that
			// repairs it is the one that just went missing.
			_ = os.Rename(old, path)
			return fmt.Errorf("replace %s: %w", path, err)
		}
		return nil
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}

	return nil
}

// replacedExecutablePath names where the Windows swap parks the running image.
func replacedExecutablePath(path string) string {
	return path + ".old"
}

// rollbackExecutable puts back the binary that was at path before the swap.
func rollbackExecutable(path string, previous []byte) error {
	// On Windows, moving the parked copy back is the restore that works even
	// when the previous binary is the image this process is running from —
	// which it is, for the update worker. Writing a fresh file there instead
	// would have to move the running image aside a second time, and Windows
	// will not let the same name be reused while it is still executing.
	if runtime.GOOS == "windows" {
		if err := restorePreviousExecutable(path); err == nil {
			return nil
		}
	}

	return replaceExecutable(path, previous)
}

// restorePreviousExecutable moves the copy the Windows swap parked aside back
// over the binary that replaced it.
func restorePreviousExecutable(path string) error {
	old := replacedExecutablePath(path)
	if _, err := os.Stat(old); err != nil {
		return err
	}
	// The binary being discarded is the one that just failed its smoke test, so
	// nothing is running it and it can go.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}

	return os.Rename(old, path)
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
