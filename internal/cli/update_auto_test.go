package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func swapUpdateStateDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	original := updateStateDir
	updateStateDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { updateStateDir = original })

	return dir
}

func TestAutoUpdateDueThrottlesToOncePerInterval(t *testing.T) {
	swapUpdateStateDir(t)
	t.Cleanup(swapVersion("0.12.1"))

	if !autoUpdateDue() {
		t.Fatalf("a start with no recorded check must be due")
	}

	if err := saveUpdateState(updateState{LastCheckAt: time.Now()}); err != nil {
		t.Fatalf("saveUpdateState: %v", err)
	}
	if autoUpdateDue() {
		t.Fatalf("a start within the interval must not be due")
	}

	if err := saveUpdateState(updateState{LastCheckAt: time.Now().Add(-2 * autoUpdateCheckInterval)}); err != nil {
		t.Fatalf("saveUpdateState: %v", err)
	}
	if !autoUpdateDue() {
		t.Fatalf("a start past the interval must be due")
	}
}

// A stamp from a clock that has since gone backwards must not block checks
// until the wall clock catches up with it.
func TestAutoUpdateDueRepairsFutureStamp(t *testing.T) {
	swapUpdateStateDir(t)
	t.Cleanup(swapVersion("0.12.1"))

	if err := saveUpdateState(updateState{LastCheckAt: time.Now().Add(24 * time.Hour)}); err != nil {
		t.Fatalf("saveUpdateState: %v", err)
	}
	if !autoUpdateDue() {
		t.Fatalf("a future stamp must count as due")
	}
}

func TestAutoUpdateDueSkipsDevBuild(t *testing.T) {
	swapUpdateStateDir(t)
	// version is "dev" in tests unless swapped.
	if autoUpdateDue() {
		t.Fatalf("a dev build must never auto-update")
	}
}

func TestAutoUpdateDueHonorsOptOut(t *testing.T) {
	swapUpdateStateDir(t)
	t.Cleanup(swapVersion("0.12.1"))
	t.Setenv(autoUpdateEnvOptOut, "1")

	if autoUpdateDue() {
		t.Fatalf("%s must disable the background check", autoUpdateEnvOptOut)
	}
}

func TestNotifyAutoUpdatePrintsOnceOnNewVersion(t *testing.T) {
	swapUpdateStateDir(t)
	t.Cleanup(swapVersion("0.13.0"))

	stamp := time.Now().Add(-10 * time.Minute)
	seed := updateState{LastCheckAt: stamp, UpdatedFrom: "0.12.1", UpdatedTo: "0.13.0", UpdatedAt: time.Now()}
	if err := saveUpdateState(seed); err != nil {
		t.Fatalf("saveUpdateState: %v", err)
	}

	var buf bytes.Buffer
	notifyAutoUpdate(&buf)
	if !strings.Contains(buf.String(), "0.12.1") || !strings.Contains(buf.String(), "0.13.0") {
		t.Fatalf("notice = %q, want both versions", buf.String())
	}

	buf.Reset()
	notifyAutoUpdate(&buf)
	if buf.Len() != 0 {
		t.Fatalf("second run printed %q, want the notice shown exactly once", buf.String())
	}

	// Clearing the notice must not lose the throttle stamp.
	state, err := loadUpdateState()
	if err != nil {
		t.Fatalf("loadUpdateState: %v", err)
	}
	if !state.LastCheckAt.Equal(stamp) {
		t.Fatalf("LastCheckAt = %v, want %v", state.LastCheckAt, stamp)
	}
}

// While an older binary is still the one running (another copy on PATH, or the
// swap not yet effective) the notice must stay pending for the run that is
// actually on the new version.
func TestNotifyAutoUpdateKeepsNoticeForOlderRunningBinary(t *testing.T) {
	swapUpdateStateDir(t)
	t.Cleanup(swapVersion("0.12.1"))

	if err := saveUpdateState(updateState{UpdatedFrom: "0.12.1", UpdatedTo: "0.13.0"}); err != nil {
		t.Fatalf("saveUpdateState: %v", err)
	}

	var buf bytes.Buffer
	notifyAutoUpdate(&buf)
	if buf.Len() != 0 {
		t.Fatalf("printed %q while running the old binary", buf.String())
	}

	state, err := loadUpdateState()
	if err != nil {
		t.Fatalf("loadUpdateState: %v", err)
	}
	if state.UpdatedTo != "0.13.0" {
		t.Fatalf("UpdatedTo = %q, want the notice kept pending", state.UpdatedTo)
	}
}

// A run already past the recorded version (a manual update in between) clears
// the stale notice without claiming anything.
func TestNotifyAutoUpdateClearsSilentlyWhenAhead(t *testing.T) {
	swapUpdateStateDir(t)
	t.Cleanup(swapVersion("0.14.0"))

	if err := saveUpdateState(updateState{UpdatedFrom: "0.12.1", UpdatedTo: "0.13.0"}); err != nil {
		t.Fatalf("saveUpdateState: %v", err)
	}

	var buf bytes.Buffer
	notifyAutoUpdate(&buf)
	if buf.Len() != 0 {
		t.Fatalf("printed %q, want silence for a version already passed", buf.String())
	}

	state, err := loadUpdateState()
	if err != nil {
		t.Fatalf("loadUpdateState: %v", err)
	}
	if state.UpdatedTo != "" {
		t.Fatalf("UpdatedTo = %q, want the stale notice cleared", state.UpdatedTo)
	}
}

// A dev build must not consume a notice that belongs to the installed binary.
func TestNotifyAutoUpdateLeavesNoticeAloneOnDevBuild(t *testing.T) {
	swapUpdateStateDir(t)

	if err := saveUpdateState(updateState{UpdatedFrom: "0.12.1", UpdatedTo: "0.13.0"}); err != nil {
		t.Fatalf("saveUpdateState: %v", err)
	}

	var buf bytes.Buffer
	notifyAutoUpdate(&buf)
	if buf.Len() != 0 {
		t.Fatalf("printed %q on a dev build", buf.String())
	}

	state, err := loadUpdateState()
	if err != nil {
		t.Fatalf("loadUpdateState: %v", err)
	}
	if state.UpdatedTo != "0.13.0" {
		t.Fatalf("UpdatedTo = %q, want the notice kept for the installed binary", state.UpdatedTo)
	}
}

func TestRecordAutoUpdateKeepsThrottleStamp(t *testing.T) {
	swapUpdateStateDir(t)

	stamp := time.Now().Add(-5 * time.Minute)
	if err := saveUpdateState(updateState{LastCheckAt: stamp}); err != nil {
		t.Fatalf("saveUpdateState: %v", err)
	}

	if err := recordAutoUpdate("0.12.1", "0.13.0"); err != nil {
		t.Fatalf("recordAutoUpdate: %v", err)
	}

	state, err := loadUpdateState()
	if err != nil {
		t.Fatalf("loadUpdateState: %v", err)
	}
	if state.UpdatedFrom != "0.12.1" || state.UpdatedTo != "0.13.0" {
		t.Fatalf("recorded %q -> %q", state.UpdatedFrom, state.UpdatedTo)
	}
	if !state.LastCheckAt.Equal(stamp) {
		t.Fatalf("LastCheckAt = %v, want %v", state.LastCheckAt, stamp)
	}
	if state.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt not set")
	}
}

// The worker guard: `update --auto` on a dev build exits clean without touching
// state, instead of failing like a manual update does.
func TestRunUpdateAutoIsSilentNoopOnDevBuild(t *testing.T) {
	swapUpdateStateDir(t)
	server := releaseAPIServer(t, "v0.13.0")
	t.Cleanup(swapReleaseAPIBaseURL(server.URL))

	var err error
	_ = captureStdout(t, func() { err = runUpdate(context.Background(), []string{"--auto"}) })
	if err != nil {
		t.Fatalf("runUpdate --auto on dev = %v, want nil", err)
	}

	state, err := loadUpdateState()
	if err != nil {
		t.Fatalf("loadUpdateState: %v", err)
	}
	if state.UpdatedTo != "" {
		t.Fatalf("UpdatedTo = %q, want no recorded install", state.UpdatedTo)
	}
}

func TestLoadUpdateStateTreatsCorruptFileAsEmpty(t *testing.T) {
	dir := swapUpdateStateDir(t)

	if err := os.WriteFile(filepath.Join(dir, "update-state.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}

	state, err := loadUpdateState()
	if err != nil {
		t.Fatalf("loadUpdateState: %v", err)
	}
	if !state.LastCheckAt.IsZero() || state.UpdatedTo != "" {
		t.Fatalf("state = %+v, want empty", state)
	}
}

// Two starts can race the throttle stamp and both spawn a worker. Only one may
// install: concurrent installs are what leaves a half-updated install behind.
func TestAcquireAutoUpdateLockAdmitsOneWorkerAtATime(t *testing.T) {
	swapUpdateStateDir(t)

	release, ok := acquireAutoUpdateLock()
	if !ok {
		t.Fatalf("the first worker must get the lock")
	}
	if _, ok := acquireAutoUpdateLock(); ok {
		t.Fatalf("a second worker must not get the lock while the first holds it")
	}

	release()
	release2, ok := acquireAutoUpdateLock()
	if !ok {
		t.Fatalf("the lock must be free again once it is released")
	}
	release2()
}

// A worker killed mid-install (a reboot, a sleep, an OOM) never releases its
// lock. A lock nobody will release must not disable updates for good.
func TestAcquireAutoUpdateLockTakesOverAnAbandonedLock(t *testing.T) {
	dir := swapUpdateStateDir(t)

	path := filepath.Join(dir, "update.lock")
	if err := os.WriteFile(path, []byte("999999\n"), 0o600); err != nil {
		t.Fatalf("seed lock: %v", err)
	}
	stale := time.Now().Add(-2 * autoUpdateLockStaleAfter)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatalf("age the lock: %v", err)
	}

	release, ok := acquireAutoUpdateLock()
	if !ok {
		t.Fatalf("a lock older than the stale window must be taken over")
	}
	release()
}

func TestRunUpdateAutoDoesNothingWhileAnotherWorkerHoldsTheLock(t *testing.T) {
	swapUpdateStateDir(t)
	t.Cleanup(swapVersion("0.12.1"))

	release, ok := acquireAutoUpdateLock()
	if !ok {
		t.Fatalf("seed the lock: not acquired")
	}
	t.Cleanup(release)

	// No release server is configured, so a worker that got past the lock would
	// try to reach GitHub and fail rather than return quietly.
	output := captureStdout(t, func() {
		if err := runUpdate(context.Background(), []string{"--auto"}); err != nil {
			t.Fatalf("runUpdate --auto: %v", err)
		}
	})
	if strings.TrimSpace(output) != "" {
		t.Fatalf("output = %q, want the locked-out worker to do nothing", output)
	}
}

// The notice goes only to a terminal: a program that captured stderr together
// with --json output got an unparseable stream.
func TestIsTerminalFalseForPipesAndFiles(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(w) {
		t.Fatal("a pipe counts as a terminal")
	}
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("a regular file counts as a terminal")
	}
}
