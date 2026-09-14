package cli

// Auto-update. The CLI is not resident — every invocation runs and exits — so
// a regular start spawns `creght update --auto` as a detached background
// process. The worker outlives its parent, downloads the release, and swaps
// the binary in place; the NEXT start therefore runs the new version, and
// prints the one-line notice the worker left behind.
//
// Checks are throttled to one per autoUpdateCheckInterval via last_check_at in
// update-state.json (next to config.json). The stamp is written before the
// worker is spawned, so a worker that fails — offline, an unwritable install
// dir — is not retried until the interval passes. Two CLI starts can still race
// the stamp and both spawn a worker; update.lock in the same directory then
// lets exactly one of them install, and the loser exits without touching the
// binary.
//
// The one invariant the whole flow is built around: at no point may there be no
// working creght. The install itself is a single rename over the binary, the new
// binary must print its own version before the update counts as done, and a
// binary that fails that check is rolled back — because the command that would
// repair a broken install is the one that just broke.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const autoUpdateCheckInterval = time.Hour

// autoUpdateLockStaleAfter is how long a lock file is honoured before it is
// treated as abandoned. A worker killed mid-install (a reboot, a sleep, an OOM)
// leaves its lock behind, and a lock nobody will ever release must not disable
// updates for good. It is well past the worst-case download.
const autoUpdateLockStaleAfter = 30 * time.Minute

// autoUpdateEnvOptOut disables the background check and install entirely when
// set to any non-empty value. Manual `creght update` keeps working.
const autoUpdateEnvOptOut = "CREGHT_NO_AUTO_UPDATE"

// updateStateDir is a variable so tests can point the state file at a temp dir.
var updateStateDir = func() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("get user config dir: %w", err)
	}
	return filepath.Join(dir, "creght"), nil
}

type updateState struct {
	// LastCheckAt throttles the background check.
	LastCheckAt time.Time `json:"last_check_at"`
	// UpdatedFrom/UpdatedTo are the pending "was auto-updated" notice, written
	// by the background worker and cleared once a start has shown it.
	UpdatedFrom string    `json:"updated_from,omitempty"`
	UpdatedTo   string    `json:"updated_to,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
}

func updateStatePath() (string, error) {
	dir, err := updateStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "update-state.json"), nil
}

func loadUpdateState() (updateState, error) {
	path, err := updateStatePath()
	if err != nil {
		return updateState{}, err
	}
	bs, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return updateState{}, nil
	}
	if err != nil {
		return updateState{}, err
	}
	var state updateState
	if err := json.Unmarshal(bs, &state); err != nil {
		// A corrupt state file must never wedge the CLI; start over from empty.
		return updateState{}, nil
	}
	return state, nil
}

func saveUpdateState(state updateState) error {
	path, err := updateStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	bs, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, bs, 0o600)
}

func updateLockPath() (string, error) {
	dir, err := updateStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "update.lock"), nil
}

// acquireAutoUpdateLock takes the single-flight lock the background worker
// installs under, and reports whether it got it. Two workers installing at once
// is the kind of interleaving that leaves a half-updated install behind, so a
// worker that does not get the lock simply exits: the next start will check
// again.
func acquireAutoUpdateLock() (release func(), ok bool) {
	path, err := updateLockPath()
	if err != nil {
		return nil, false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if os.IsExist(err) {
		info, statErr := os.Stat(path)
		if statErr != nil || time.Since(info.ModTime()) < autoUpdateLockStaleAfter {
			return nil, false
		}
		// The holder is gone; take the lock over.
		if err := os.Remove(path); err != nil {
			return nil, false
		}
		file, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	if err != nil {
		return nil, false
	}

	fmt.Fprintf(file, "%d\n", os.Getpid())
	_ = file.Close()

	return func() { _ = os.Remove(path) }, true
}

// cleanupReplacedExecutable removes the copy of the previous binary that the
// Windows swap has to park beside the running image, since the running one
// cannot be replaced there. It is dead weight from the moment the process that
// left it exited, and every later start is free to delete it.
//
// It cannot delete one out from under an update that is still running: Windows
// refuses to unlink an image a live process is executing, which is the same rule
// that forces the rename-aside in the first place.
func cleanupReplacedExecutable() {
	if runtime.GOOS != "windows" {
		return
	}
	exePath, err := runningExecutable()
	if err != nil {
		return
	}
	_ = os.Remove(replacedExecutablePath(exePath))
}

// recordAutoUpdate is called by the background worker after a successful
// install, so the next start can tell the user what happened.
func recordAutoUpdate(from string, to string) error {
	state, err := loadUpdateState()
	if err != nil {
		return err
	}
	state.UpdatedFrom = from
	state.UpdatedTo = to
	state.UpdatedAt = time.Now()
	return saveUpdateState(state)
}

// notifyAutoUpdate prints the pending notice a background worker left, then
// clears it so it shows exactly once. While an older binary is still the one
// running — another copy on PATH, or the swap not yet effective — the notice
// is kept pending for the run that is actually on the new version, so the
// message never claims a version the user is not on.
func notifyAutoUpdate(w io.Writer) {
	if os.Getenv(autoUpdateEnvOptOut) != "" {
		// The opt-out covers the notice too, which also keeps the worker's
		// version smoke test — it runs with the opt-out set — from consuming a
		// notice into its own captured output.
		return
	}
	current := strings.TrimSpace(version)
	if current == "dev" {
		// A dev build is outside the auto-update flow; consuming the notice
		// here would hide it from the installed binary it belongs to.
		return
	}
	state, err := loadUpdateState()
	if err != nil || state.UpdatedTo == "" {
		return
	}
	if compareVersions(current, state.UpdatedTo) < 0 {
		return
	}
	from, to := state.UpdatedFrom, state.UpdatedTo
	state.UpdatedFrom, state.UpdatedTo, state.UpdatedAt = "", "", time.Time{}
	if err := saveUpdateState(state); err != nil {
		// A notice that cannot be cleared would repeat on every run.
		return
	}
	if current == to {
		fmt.Fprintf(w, "creght was auto-updated from %s to %s in the background. Set %s=1 to disable auto-update.\n", from, to, autoUpdateEnvOptOut)
	}
}

// autoUpdateDue reports whether this start should kick off a background check.
func autoUpdateDue() bool {
	if strings.TrimSpace(version) == "dev" {
		return false
	}
	if os.Getenv(autoUpdateEnvOptOut) != "" {
		return false
	}
	state, err := loadUpdateState()
	if err != nil {
		return false
	}
	elapsed := time.Since(state.LastCheckAt)
	// A negative elapsed means the stamp is from a clock that has since gone
	// backwards; re-stamping with the current clock repairs the state.
	return elapsed < 0 || elapsed >= autoUpdateCheckInterval
}

// startAutoUpdateIfDue spawns `creght update --auto` detached from this
// process, so the download continues after the command exits and the next
// start picks up the new binary. It never blocks and never fails the command
// it runs alongside.
func startAutoUpdateIfDue() {
	if !autoUpdateDue() {
		return
	}
	state, err := loadUpdateState()
	if err != nil {
		return
	}
	state.LastCheckAt = time.Now()
	if err := saveUpdateState(state); err != nil {
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		return
	}
	// Deliberately not CommandContext: the worker must survive this process.
	cmd := exec.Command(exePath, "update", "--auto")
	// The worker's output goes to update.log for debugging, truncated each
	// run; with no log file it goes to the null device, never the terminal.
	if dir, err := updateStateDir(); err == nil {
		if log, err := os.OpenFile(filepath.Join(dir, "update.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
			defer log.Close()
			cmd.Stdout = log
			cmd.Stderr = log
		}
	}
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release()
}
