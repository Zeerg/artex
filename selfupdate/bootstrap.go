package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// smokeEnv Let sub-processes triggered by smoke tests skip Bootstrap directly.
//
// Strictly speaking, not adding it won't cause an issue: child processes' os.Executable() Is artex.new, Derived from
// All paths include .new prefix, unable to reach real upgrade files. But relying on such coincidences is too fragile,
// Explicit short-circuiting is clear and saves an unnecessary disk probe by a child process.
const smokeEnv = "ARTEX_SELFUPDATE_SMOKE"

// Action This is a Bootstrap instruction to main.
type Action int

const (
	// Continue: Start server normally.
	Continue Action = iota
	// Restart: 'Immediately exit with ExitRestart to let the watchdog script restart.',
	Restart
)

// State Describe this startup's upgrade status for /api/update/check to truthfully inform the frontend
// "Was the last upgrade successful or rolled back?".
type State struct {
	Pending bool // Not yet confirmed stable after reinstallation
	RolledBack bool // This startup just executed automatic rollback this time
	FailedStage bool // Draft validation/smoke test failed, discarded
	Detail string // User-friendly one-sentence explanation
}

// Bootstrap Must run at the beginning of main, before any listening ports are opened or databases are connected.
//
// Three scenarios:
//
//	① Temporary file artex exists.new → List And Pair + Smoke test, if passes switch and require restart; if fails discard continue running old version
//	② Only marked files left → Indicate just installed, one attempt; continuous failures enough times will trigger rollback
//	③ Nothing → Normal Startup,
func Bootstrap() (Action, State) {
	if os.Getenv(smokeEnv)!= "" {
		return Continue, State{}
	}
	p, err:= ResolvePaths()
	if err!= nil {
		log.Printf("[update] Skip bootstrapping:%v", err)
		return Continue, State{}
	}

	if _, err:= os.Stat(p.New); err == nil {
		return applyStaged(p)
	}

	m, ok:= readMarker(p.Marker)
	if!ok {
		return Continue, State{}
	}
	return confirmOrRollback(p, m)
}

// applyStaged Handle"Temporary file exists"In this scenario: if the verification passes, change the attire; if it fails, discard it.
//
// This is the only place in the entire upgrade chain where executable files are overwritten, also the last gate.——Smoke test blocked
// Issues such as corrupted downloads, wrong architecture selection, missing dynamic links. Once you let through a binary that doesn't run,
// The watchdog script will repeatedly restart it without pause, and the Go code has no chance to run, so automatic rollback is not possible.
func applyStaged(p Paths) (Action, State) {
	m, _:= readMarker(p.Marker)

	if err:= verifyStaged(p); err!= nil {
		log.Printf("[update] The temporarily saved new version has failed validation and been discarded. Continuing with the current version:%v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "New version validation failed, discarded:" + err.Error()}
	}

	if err:= swap(p); err!= nil {
		log.Printf("[update] Installation failed, continuing to run the current version:%v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "Installation Failed:" + err.Error()}
	}

	// Upgrade successful. Retain marker, pass to next start (running the new version) to confirm stability.
	m.Attempts = 0
	if m.StagedAt == 0 {
		m.StagedAt = time.Now().Unix()
	}
	if err:= writeMarker(p.Marker, m); err!= nil {
		log.Printf("[update] Failed to write upgrade marker (losing automatic rollback capability):%v", err)
	}
	log.Printf("[update] Already Worn On %s, Exit to restart (exit %d)", orUnknown(m.To), ExitRestart)
	return Restart, State{Pending: true}
}

// confirmOrRollback Handle"Startup after dressing change": Cumulative attempt count, replace with old version if limit exceeded.
//
// Counting only increments after Go code runs, so it covers"Can execute but crashes during initialization"
// (Faults like configuration incompatibility, port being occupied, DB migration failure)"Unable to exec" From before the disguise
// The smoketest blocks it, together they make up the whole thing.
func confirmOrRollback(p Paths, m marker) (Action, State) {
	m.Attempts++
	if m.Attempts > maxAttempts {
		if err:= rollback(p); err!= nil {
			// If rollback fails, don't restart again; otherwise, it will enter an infinite restart loop. Clear the flag,
			// Let the process start based on its current state.——If it doesn't start up, users should at least see the reason in the logs.
			log.Printf("[update] Continuous new version %d Failed twice, and rollback failed:%v", maxAttempts, err)
			_ = os.Remove(p.Marker)
			return Continue, State{Detail: "New version startup failed and rollback failed:" + err.Error()}
		}
		log.Printf("[update] Continuous new version %d Second startup failed, rolled back to %s, Exit to restart (exit %d)",
			maxAttempts, orUnknown(m.From), ExitRestart)
		_ = os.Remove(p.Marker)
		return Restart, State{RolledBack: true, Detail: fmt.Sprintf("New version startup failed, rolled back to %s", orUnknown(m.From))}
	}
	if err:= writeMarker(p.Marker, m); err!= nil {
		log.Printf("[update] Failed to mark the update/upgrade:%v", err)
	}
	log.Printf("[update] New version starting (round %d/%d After this attempt), stable operation will confirm upgrade",
		m.Attempts, maxAttempts)
	return Continue, State{Pending: true}
}

// Settle Confirm that the new version is stable and remove the upgrade marker.
//
// Called by main after listening on HTTP for a delay: only counts if it lives through this period, otherwise the mark stays where it is,
// Continue accumulating attempt counts on next startup until rollback is triggered.
func Settle() {
	p, err:= ResolvePaths()
	if err!= nil {
		return
	}
	settle(p)
}

func settle(p Paths) {
	if _, ok:= readMarker(p.Marker);!ok {
		return // Not a startup after upgrade, nothing to do
	}
	if err:= os.Remove(p.Marker); err!= nil &&!errors.Is(err, os.ErrNotExist) {
		log.Printf("[update] Failed to clear upgrade flag:%v", err)
		return
	}
	log.Printf("[update] New versions run stably, upgrade complete (previous version retained as %s)", p.Old)
}

// SettleDelay Is a judgment"The new version survived"Required runtime duration.
const SettleDelay = 30 * time.Second

// verifyStaged Validate draft: first compare SHA256, then truly pull and run it.
func verifyStaged(p Paths) error {
	want, err:= os.ReadFile(p.Sum)
	if err!= nil {
		return fmt.Errorf("Reading checksum: %w", err)
	}
	got, err:= fileSHA256(p.New)
	if err!= nil {
		return fmt.Errorf("Calculate Checksum: %w", err)
	}
	if!strings.EqualFold(strings.TrimSpace(string(want)), got) {
		return errors.New("SHA256 Mismatch (download corrupted or tampered with)")
	}
	return smokeTest(p.New)
}

// smokeTest Start new binary with -h and confirm it really executes on the current system.
// This can block download truncation, architecture selection errors (exec format error), missing dependencies, etc.
func smokeTest(bin string) error {
	if err:= os.Chmod(bin, 0o755); err!= nil {
		return fmt.Errorf("Grant execution permissions.: %w", err)
	}
	ctx, cancel:= context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd:= exec.CommandContext(ctx, bin, "-h")
	cmd.Env = append(os.Environ(), smokeEnv+"=1")
	out, err:= cmd.CombinedOutput()
	if ctx.Err()!= nil {
		return errors.New("Smoke test timeout (new binary unresponsive)")
	}
	if err!= nil {
		snippet:= strings.TrimSpace(string(out))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		return fmt.Errorf("Smoke test failed: %v: %s", err, snippet)
	}
	return nil
}

// swap Replace current binary with the new draft version.
//
// Unix Both Windows and Linux allow renaming a running executable file (Windows prohibits deleting and
// Coverage, rename not in the list), so no platform division is needed here, nor do we need to stop ourselves first.
func swap(p Paths) error {
	// Windows The rename does not overwrite existing targets, left over from previous upgrades .old Must be cleared first.
	if err:= os.Remove(p.Old); err!= nil &&!errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Clean up old backups %s: %w", p.Old, err)
	}
	if err:= os.Rename(p.Current, p.Old); err!= nil {
		return fmt.Errorf("Backup current version: %w", err)
	}
	if err:= os.Rename(p.New, p.Current); err!= nil {
		// Reinstallation failed but the current version has been moved; it must be restored to its original state, otherwise there will be no executable file next time.
		if rerr:= os.Rename(p.Old, p.Current); rerr!= nil {
			return fmt.Errorf("Failed to load new version(%v), And failed to recover the current version: %w", err, rerr)
		}
		return fmt.Errorf("Load new version: %w", err)
	}
	_ = os.Remove(p.Sum)
	return nil
}

// rollback Swap back the old version of the backup.
func rollback(p Paths) error {
	if _, err:= os.Stat(p.Old); err!= nil {
		return fmt.Errorf("No backup to roll back %s: %w", p.Old, err)
	}
	// Move the new version that can't start to .failed Left for investigation, not directly deleted.
	failed:= p.Current + ".failed"
	_ = os.Remove(failed)
	if err:= os.Rename(p.Current, failed); err!= nil {
		return fmt.Errorf("Remove failed versions: %w", err)
	}
	if err:= os.Rename(p.Old, p.Current); err!= nil {
		return fmt.Errorf("Restoring old version: %w", err)
	}
	return nil
}

// Rollback Implementation for /api/update/rollback: actively roll back to the previous version.
// Only do costume change, restart is handed over to the daemon script (the caller then exits gracefully with ExitRestart).
func Rollback() error {
	p, err:= ResolvePaths()
	if err!= nil {
		return err
	}
	if _, err:= os.Stat(p.Old); err!= nil {
		return errors.New("No previous version to rollback from (" + p.Old + " Does not exist)")
	}
	cleanStaged(p)
	if err:= smokeTest(p.Old); err!= nil {
		return fmt.Errorf("Previous version could not execute, rollback rejected.: %w", err)
	}
	// Swap current with backup: after rollback, it can roll back again.
	tmp:= p.Current + ".swap"
	_ = os.Remove(tmp)
	if err:= os.Rename(p.Current, tmp); err!= nil {
		return fmt.Errorf("Remove the current version.: %w", err)
	}
	if err:= os.Rename(p.Old, p.Current); err!= nil {
		_ = os.Rename(tmp, p.Current)
		return fmt.Errorf("Load previous version: %w", err)
	}
	if err:= os.Rename(tmp, p.Old); err!= nil {
		log.Printf("[update] Rolling back and organizing backups failed (does not affect operation):%v", err)
	}
	_ = os.Remove(p.Marker)
	return nil
}

// HasBackup Does the report have a rollbackable previous version for the frontend to decide whether to show the rollback button.
func HasBackup() bool {
	p, err:= ResolvePaths()
	if err!= nil {
		return false
	}
	_, err = os.Stat(p.Old)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	f, err:= os.Open(path)
	if err!= nil {
		return "", err
	}
	defer f.Close()
	h:= sha256.New()
	if _, err:= io.Copy(h, f); err!= nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown version"
	}
	return s
}
