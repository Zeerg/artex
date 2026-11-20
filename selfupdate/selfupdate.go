// Package selfupdate implements ARTEX The page's one-click update: Pull the new version from GitHub Release
// Binary, checksum, temp store, and atomically swap on next start.
//
// Overall division (see start.sh / start.bat):
//
//	Startup script = Foolish guardian loop, only responsible for"Restart based on exit code after process exits."
//	This Package = All error-prone logic (download / SHA256 verification / smoke test / reinstallation / failure rollback).
//
// The reason we placed dressing in Go instead of scripts is because sha256 checksum smoke tests do not work well in sh and bat.
// Need to write two sets (sha256sum / shasum / certutil), and this is precisely the most error-prone part.——Switch to Previous
// A non-running binary, the watchdog process will faithfully repeatedly restart it, and the user can only manually save on the machine.
//
// One complete upgrade goes through three process starts:
//
//	① Old server received /api/update/apply → Download Verification, → Save artex.new → exit 75
//	② Script re-launch old version → Bootstrap Found artex.new → List And Pair+Smoke Test → High-risk → exit 75
//	③ Script restarted, it is now the new version → Bootstrap Record one attempt → Clear the flag after successful startup
//
// Roll back to old version on any failure:② If verification fails, delete the draft and continue with the old version;③ Did not survive three consecutive times
// Automatically clears marked (crashes) and automatically sets artex.old Go back.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart is"Please have the daemon restart me."exit code (EX_TEMPFAIL). When the startup script sees it,
// It will immediately rerun without counting towards crash backoff. 0 means the user normally stops (script exits loop), others are considered crashes.
const ExitRestart = 75

// maxAttempts Number of allowed start attempts after switching to new version. New version resets count each time it starts +1, Survive
// settleDelay Then clear the mark; failing maxAttempts times indicates the new version can't start at all, automatically rolling back.
const maxAttempts = 3

// Paths All files involved in this upgrade are uniformly mounted at**Directory of the executable file**Down.
// Intentionally not using CWD: When running service-wise, the working directory might be / or any path; using CWD would cause temporary files to land
// Elsewhere, the reinstallation logic directly fails.
type Paths struct {
	Dir string // Directory of the executable file
	Current string // The currently running binary is artex/artex..exe
	New string // Draft new version artex.new / artex.new.exe
	Sum string // New version of sha256 (hex) artex.new.sha256 / artex.new.exe.sha256
	Old string // Backup the old version before reinstallation artex.old / artex.old.exe
	Marker string // Upgrade status marker artex.upgrade.json
}

// ResolvePaths Derive all upgrade paths based on the current executable file.
//
// Windows On .new/.old must also include .exe suffix, otherwise smoke tests and post-install execution will fail,
// So strip the suffix first then append, making the naming symmetric between platforms.
func ResolvePaths() (Paths, error) {
	exe, err:= os.Executable()
	if err!= nil {
		return Paths{}, fmt.Errorf("Locate Executable File: %w", err)
	}
	if resolved, err:= filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir:= filepath.Dir(exe)
	name:= filepath.Base(exe)
	ext:= filepath.Ext(name) // Windows Above ".exe", Unix Usually empty
	stem:= strings.TrimSuffix(name, ext)

	join:= func(suffix string) string { return filepath.Join(dir, stem+suffix+ext) }
	return Paths{
		Dir: dir,
		Current: exe,
		New: join(".new"),
		Sum: join(".new") + ".sha256",
		Old: join(".old"),
		Marker: filepath.Join(dir, stem+".upgrade.json"),
	}, nil
}

// marker Record the progress of a reinstallation to trigger automatic rollback if the new version doesn't start.
type marker struct {
	From string `json:"from"` // Version before upgrade
	To string `json:"to"` // Target Version,
	Attempts int `json:"attempts"` // Number of times the service has been attempted to start after reinstallation.
	StagedAt int64 `json:"staged_at"`
}

func readMarker(path string) (marker, bool) {
	b, err:= os.ReadFile(path)
	if err!= nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m)!= nil {
		return marker{}, false
	}
	return m, true
}

func writeMarker(path string, m marker) error {
	b, err:= json.MarshalIndent(m, "", "  ")
	if err!= nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// cleanStaged Clears draft. Walks through on successful reinstallation, validation failure, or user cancellation to avoid residual files.
// artex.new Retried at next startup.
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// CompareVersions Compare two version numbers and return -1/0/1 (a<b / a==b / a>b).
// ok=false Indicates that at least one side is not a comparable version number (e.g., a local development build "dev" Or
// git describe Generated "0.3.7-2-gabc1234-dirty"), The caller should disable one-click update at this time,
// Otherwise, it will use the development build"Build"Release to production, overwriting uncommitted changes.
func CompareVersions(a, b string) (int, bool) {
	av, aok:= parseVersion(a)
	bv, bok:= parseVersion(b)
	if!aok ||!bok {
		return 0, false
	}
	for i:= range 3 {
		if av[i]!= bv[i] {
			if av[i] < bv[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion Parse "v0.3.7" / "0.3.7" Version number in this form is [3]int.
//
// Only accept pure three-part format: build..sh In non-tag builds, use git describe to produce
// "0.3.7-2-gabc1234" These versions with suffixes must be deemed uncomparable, not treated as
// 0.3.7 —— Otherwise, development builds may be misjudged as"Already latest"Or be covered by the official version.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return [3]int{}, false
	}
	parts:= strings.Split(s, ".")
	if len(parts)!= 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p:= range parts {
		n, err:= strconv.Atoi(p)
		if err!= nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InDocker Report whether the process is running in a container. Under Docker, reinstallation writes to the writable layer of the container.
// `docker compose up -d` Rebuilding the container will revert to the version in the image.——This is expected behavior.
// (When users were already pulling a new image), but the frontend should be able to explain this clearly.
func InDocker() bool {
	if _, err:= os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err:= os.ReadFile("/proc/1/cgroup")
	if err!= nil {
		return false
	}
	s:= string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd")
}
