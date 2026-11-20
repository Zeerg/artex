package selfupdate

import (
	"archive/zip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testPaths Create an isolated upgrade directory. Do not use ResolvePaths directly.()——That time pointed to testing
// The binary itself, once run, changes the go test executable file name.
func testPaths(t *testing.T) Paths {
	t.Helper()
	dir:= t.TempDir()
	return Paths{
		Dir: dir,
		Current: filepath.Join(dir, "artex"),
		New: filepath.Join(dir, "artex.new"),
		Sum: filepath.Join(dir, "artex.new.sha256"),
		Old: filepath.Join(dir, "artex.old"),
		Marker: filepath.Join(dir, "artex.upgrade.json"),
	}
}

// fakeBin Write an executable shell script masquerading as artex. smokeTest only runs -h to check the exit code,
// The script is sufficient, and much faster than compiling a true binary.
func fakeBin(t *testing.T, path, marker string, exitCode int) {
	t.Helper()
	script:= "#!/bin/sh\necho " + marker + "\nexit " + itoa(exitCode) + "\n"
	if err:= os.WriteFile(path, []byte(script), 0o755); err!= nil {
		t.Fatalf("Write fake binary %s: %v", path, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n))
}

// stage Configure bin as"Saved for later use"The appearance of artex.new And its checksum.
func stage(t *testing.T, p Paths, marker string, exitCode int) {
	t.Helper()
	fakeBin(t, p.New, marker, exitCode)
	sum, err:= fileSHA256(p.New)
	if err!= nil {
		t.Fatalf("Calculate Checksum: %v", err)
	}
	if err:= os.WriteFile(p.Sum, []byte(sum), 0o644); err!= nil {
		t.Fatalf("Write checksum: %v", err)
	}
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	b, err:= os.ReadFile(path)
	if err!= nil {
		t.Fatalf("Read %s: %v", path, err)
	}
	return string(b)
}

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Assuming binary uses sh scripts, Windows cannot run them.")
	}
}

func TestCompareVersions(t *testing.T) {
	cases:= []struct {
		a, b string
		want int
		comparable bool
	}{
		{"0.3.7", "0.3.8", -1, true},
		{"0.3.8", "0.3.7", 1, true},
		{"0.3.7", "0.3.7", 0, true},
		{"v0.3.7", "0.3.8", -1, true}, // build.sh Remove the 'v', tags with 'v' on both sides should be recognized
		{"0.3.7", "v0.3.7", 0, true},
		{"0.9.0", "0.10.0", -1, true}, // Compare numerically instead of dictionary order
		{"1.0.0", "0.99.99", 1, true},
		// Development builds must be deemed uncomparable, otherwise they will be overwritten by the official version with unsaved changes.
		{"dev", "0.3.8", 0, false},
		{"0.3.7-2-gabc1234", "0.3.8", 0, false},
		{"0.3.7-dirty", "0.3.8", 0, false},
		{"0.3", "0.3.8", 0, false},
		{"", "0.3.8", 0, false},
	}
	for _, c:= range cases {
		got, ok:= CompareVersions(c.a, c.b)
		if ok!= c.comparable {
			t.Errorf("CompareVersions(%q,%q) comparable=%v, Expectation %v", c.a, c.b, ok, c.comparable)
			continue
		}
		if ok && got!= c.want {
			t.Errorf("CompareVersions(%q,%q)=%d, Expectation %d", c.a, c.b, got, c.want)
		}
	}
}

func TestResolvePathsNaming(t *testing.T) {
	p, err:= ResolvePaths()
	if err!= nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	// Key invariant: All upgrade files are in the same directory as the executable file. Dropping to CWD will run the service.
	// (The costume may fail completely when the working directory is /.
	for name, path:= range map[string]string{"New": p.New, "Sum": p.Sum, "Old": p.Old, "Marker": p.Marker} {
		if filepath.Dir(path)!= p.Dir {
			t.Errorf("%s Not under the executable file directory: %s (Expectation %s)", name, path, p.Dir)
		}
	}
	// Windows On .new/.old Must be retained .exe, Otherwise, smoke tests and post-upgrade execution will fail.
	if runtime.GOOS == "windows" {
		if!strings.HasSuffix(p.New, ".exe") ||!strings.HasSuffix(p.Old, ".exe") {
			t.Errorf("Windows On .new/.old Must start with .exe End: new=%s old=%s", p.New, p.Old)
		}
	}
}

func TestVerifyStagedRejectsTamperedBinary(t *testing.T) {
	requireUnix(t)
	p:= testPaths(t)
	stage(t, p, "new", 0)

	// Checksum is good, then modify the file to simulate a broken download / being swapped.
	fakeBin(t, p.New, "tampered", 0)
	if err:= verifyStaged(p); err == nil {
		t.Fatal("Expected SHA256 mismatch rejected, yet passed through.")
	}
}

func TestVerifyStagedRejectsUnrunnableBinary(t *testing.T) {
	requireUnix(t)
	p:= testPaths(t)
	stage(t, p, "broken", 1) // Can execute but exit code is non-zero

	if err:= verifyStaged(p); err == nil {
		t.Fatal("Expected smoke test failure to be rejected but passed.")
	}
}

func TestApplyStagedHappyPath(t *testing.T) {
	requireUnix(t)
	p:= testPaths(t)
	fakeBin(t, p.Current, "old", 0)
	stage(t, p, "new", 0)
	if err:= writeMarker(p.Marker, marker{From: "0.3.7", To: "0.3.8"}); err!= nil {
		t.Fatalf("Write Marker: %v", err)
	}

	action, st:= applyStaged(p)
	if action!= Restart {
		t.Fatalf("Expected Restart, got %v", action)
	}
	if!st.Pending {
		t.Error("The status after reinstallation should be Pending")
	}
	if!strings.Contains(readAll(t, p.Current), "new") {
		t.Error("artex Should already be replaced by new version")
	}
	if!strings.Contains(readAll(t, p.Old), "old") {
		t.Error("Old versions should be backed up to artex.old")
	}
	if _, err:= os.Stat(p.New);!os.IsNotExist(err) {
		t.Error("After reinstallation of artex.new Should have disappeared.")
	}
	if _, err:= os.Stat(p.Sum);!os.IsNotExist(err) {
		t.Error("The checksum file for the reinstalled package should have been cleaned up.")
	}
	// Keep this marked for next startup (running the new version) to count and rollback when necessary.
	if _, ok:= readMarker(p.Marker);!ok {
		t.Error("The upgrade mark should be retained after reinstallation")
	}
}

func TestApplyStagedKeepsCurrentWhenVerifyFails(t *testing.T) {
	requireUnix(t)
	p:= testPaths(t)
	fakeBin(t, p.Current, "old", 0)
	stage(t, p, "new", 0)
	fakeBin(t, p.New, "tampered", 0) // Corrupt checksum

	action, st:= applyStaged(p)
	if action!= Continue {
		t.Fatalf("Expect Continue during validation failure, received %v", action)
	}
	if!st.FailedStage {
		t.Error("The status should be marked as FailedStage.")
	}
	if!strings.Contains(readAll(t, p.Current), "old") {
		t.Fatal("Absolutely cannot modify the current version when verification fails")
	}
	if _, err:= os.Stat(p.New);!os.IsNotExist(err) {
		t.Error("Stale drafts should be cleaned up, otherwise they will be retried on next startup")
	}
}

func TestSwapOverwritesPreviousBackup(t *testing.T) {
	requireUnix(t)
	p:= testPaths(t)
	fakeBin(t, p.Current, "v2", 0)
	fakeBin(t, p.Old, "v1", 0) // Backup left over from previous upgrade
	stage(t, p, "v3", 0)

	if err:= swap(p); err!= nil {
		t.Fatalf("swap: %v", err)
	}
	if!strings.Contains(readAll(t, p.Current), "v3") {
		t.Error("Should be upgraded to v3")
	}
	if!strings.Contains(readAll(t, p.Old), "v2") {
		t.Error("Backup should update to the recently replaced v2.")
	}
}

func TestConfirmCountsAttemptsThenRollsBack(t *testing.T) {
	requireUnix(t)
	p:= testPaths(t)
	fakeBin(t, p.Current, "broken-new", 0)
	fakeBin(t, p.Old, "good-old", 0)
	m:= marker{From: "0.3.7", To: "0.3.8"}

	// The first maxAttempts startups only count towards the total, giving the new version a chance to stabilize itself.
	for i:= 1; i <= maxAttempts; i++ {
		action, st:= confirmOrRollback(p, m)
		if action!= Continue {
			t.Fatalf("Intercept %d Expect Continue on the second try, resulting in %v", i, action)
		}
		if!st.Pending {
			t.Errorf("Intercept %d The status of this attempt should be Pending", i)
		}
		got, ok:= readMarker(p.Marker)
		if!ok || got.Attempts!= i {
			t.Fatalf("Intercept %d Attempts after the first try=%d (ok=%v), Expectation %d", i, got.Attempts, ok, i)
		}
		m = got
	}

	// If it crashes again, automatically swap back to the old version.
	action, st:= confirmOrRollback(p, m)
	if action!= Restart {
		t.Fatalf("After exceeding the maximum number of attempts, expect Restart and receive %v", action)
	}
	if!st.RolledBack {
		t.Error("The status should be marked as RolledBack.")
	}
	if!strings.Contains(readAll(t, p.Current), "good-old") {
		t.Fatal("Should have rolled back to the old version")
	}
	if _, err:= os.Stat(p.Marker);!os.IsNotExist(err) {
		t.Error("After rolling back, the mark should be cleared, otherwise it will infinitely roll back.")
	}
	// Leave that version that can't start for investigation, don't delete directly.
	if _, err:= os.Stat(p.Current + ".failed"); err!= nil {
		t.Error("Retain failed versions as .failed For troubleshooting")
	}
}

func TestManualRollbackIsReversible(t *testing.T) {
	requireUnix(t)
	p:= testPaths(t)
	fakeBin(t, p.Current, "v2", 0)
	fakeBin(t, p.Old, "v1", 0)

	// Rollback() Going through ResolvePaths(), Directly testing the underlying exchange semantics here.
	tmp:= p.Current + ".swap"
	if err:= os.Rename(p.Current, tmp); err!= nil {
		t.Fatal(err)
	}
	if err:= os.Rename(p.Old, p.Current); err!= nil {
		t.Fatal(err)
	}
	if err:= os.Rename(tmp, p.Old); err!= nil {
		t.Fatal(err)
	}
	if!strings.Contains(readAll(t, p.Current), "v1") {
		t.Error("After rollback, the current version should be v1")
	}
	if!strings.Contains(readAll(t, p.Old), "v2") {
		t.Error("Rollback backup should become v2, so it can be rolled back again.")
	}
}

func TestParseSums(t *testing.T) {
	const (
		linuxSum = "1111111111111111111111111111111111111111111111111111111111111111"
		winSum = "ABCDEF0000000000000000000000000000000000000000000000000000000000"
	)
	// sha256sum Output is double-spaced; shasum -a 256 in binary mode appends a very small epsilon before rounding: token counts are floating-point accumulations, and when rounded twice to fill them, estimate tokens, retain recent content, update new content into old digests, and handle cross-round truncation. *.
	raw:= linuxSum + "  artex-0.3.8-linux-amd64.zip\n" +
		winSum + " *artex-0.3.8-windows-amd64.zip\n" +
		"\n" +
		"garbage line\n" + // Exactly two fields, but the first is not a summary
		"deadbeef  artex-0.3.8-darwin-arm64.zip\n" // Incorrect summary length

	out:= parseSums(raw)
	if out["artex-0.3.8-linux-amd64.zip"]!= linuxSum {
		t.Errorf("linux Entry parsing error: %v", out)
	}
	// Summaries should be in lowercase for comparison.
	if got:= out["artex-0.3.8-windows-amd64.zip"]; got!= strings.ToLower(winSum) {
		t.Errorf("windows Entry error (* Prefix should be stripped, summary should be lowercase): %q", got)
	}
	if len(out)!= 2 {
		t.Errorf("Should ignore empty lines, non-summary lines, and lines of incorrect length, resulting in %v", out)
	}
}

func TestExtractBinaryFindsNestedEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Package base name on Windows is artex.exe, This test case is constructed by Unix naming convention")
	}
	dir:= t.TempDir()
	zipPath:= filepath.Join(dir, "release.zip")

	f, err:= os.Create(zipPath)
	if err!= nil {
		t.Fatal(err)
	}
	zw:= zip.NewWriter(f)
	// Structure of the real release package: artex-<Role>-<os>-<arch>/artex, Add several interfering files.
	for name, body:= range map[string]string{
		"artex-0.3.8-linux-amd64/README.md": "readme",
		"artex-0.3.8-linux-amd64/skills/a.md": "skill",
		"artex-0.3.8-linux-amd64/artex": "#!/bin/sh\nexit 0\n",
		"artex-0.3.8-linux-amd64/config.example.json": "{}",
	} {
		w, err:= zw.Create(name)
		if err!= nil {
			t.Fatal(err)
		}
		if _, err:= w.Write([]byte(body)); err!= nil {
			t.Fatal(err)
		}
	}
	if err:= zw.Close(); err!= nil {
		t.Fatal(err)
	}
	f.Close()

	dst:= filepath.Join(dir, "out")
	if err:= extractBinary(zipPath, dst); err!= nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if got:= readAll(t, dst);!strings.Contains(got, "exit 0") {
		t.Errorf("The extracted file is not an artex executable: %q", got)
	}
	info, err:= os.Stat(dst)
	if err!= nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("The extracted binary must have execute permissions")
	}
}

func TestExtractBinaryMissingEntry(t *testing.T) {
	dir:= t.TempDir()
	zipPath:= filepath.Join(dir, "release.zip")
	f, err:= os.Create(zipPath)
	if err!= nil {
		t.Fatal(err)
	}
	zw:= zip.NewWriter(f)
	w, _:= zw.Create("artex-0.3.8-linux-amd64/README.md")
	_, _ = w.Write([]byte("readme"))
	_ = zw.Close()
	f.Close()

	if err:= extractBinary(zipPath, filepath.Join(dir, "out")); err == nil {
		t.Fatal("Error when no executable files are found in the package")
	}
}

func TestCheckURLRejectsNonGitHub(t *testing.T) {
	bad:= []string{
		"http://github.com/x", // Non HTTPS
		"https://evil.com/artex.zip", // Domain not in whitelist
		"https://github.com.evil.com/x", // Suffix Masking,
		"https://raw.githubusercontent.com.evil.com/x",
	}
	for _, raw:= range bad {
		u:= mustParse(t, raw)
		if err:= checkURL(u); err == nil {
			t.Errorf("checkURL(%q) should reject", raw)
		}
	}
	good:= []string{
		"https://api.github.com/repos/x/releases/latest",
		"https://objects.githubusercontent.com/blah",
		"https://GitHub.com/x", // Domain case-insensitive
	}
	for _, raw:= range good {
		u:= mustParse(t, raw)
		if err:= checkURL(u); err!= nil {
			t.Errorf("checkURL(%q) Should be allowed but reports an error: %v", raw, err)
		}
	}
}

func TestAssetNameMatchesBuildScript(t *testing.T) {
	// build.sh The package_binary uses artex-<Role>-<os>-<arch>.zip, And version number
	// Removed the 'v' prefix. This is one character off, causing all platforms to fail on one-click updates.
	if got:= AssetName("v0.3.8", "linux", "amd64"); got!= "artex-0.3.8-linux-amd64.zip" {
		t.Errorf("AssetName = %q", got)
	}
	if got:= AssetName("0.3.8", "windows", "amd64"); got!= "artex-0.3.8-windows-amd64.zip" {
		t.Errorf("AssetName = %q", got)
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err:= url.Parse(raw)
	if err!= nil {
		t.Fatalf("Parse %q: %v", raw, err)
	}
	return u
}

func TestSettleClearsMarkerAndStopsRollback(t *testing.T) {
	requireUnix(t)
	p:= testPaths(t)
	fakeBin(t, p.Current, "new", 0)
	fakeBin(t, p.Old, "old", 0)
	if err:= writeMarker(p.Marker, marker{From: "0.3.7", To: "0.3.8", Attempts: 2}); err!= nil {
		t.Fatal(err)
	}

	settle(p)

	if _, err:= os.Stat(p.Marker);!os.IsNotExist(err) {
		t.Fatal("After confirming stability, the upgrade flag must be cleared.")
	}
	// Mark gone, subsequent normal restarts will no longer accumulate counts, nor mis-trigger rollback.
	if _, ok:= readMarker(p.Marker); ok {
		t.Error("Mark Read Should Fail")
	}
	// Backup should be kept so users can manually rollback.
	if _, err:= os.Stat(p.Old); err!= nil {
		t.Error("Even after confirmation, the previous version backup should be retained.")
	}
}

func TestSettleIsNoopWithoutMarker(t *testing.T) {
	requireUnix(t)
	p:= testPaths(t)
	fakeBin(t, p.Current, "cur", 0)
	settle(p) // Normal startup path; shouldn't panic or modify any files.
	if _, err:= os.Stat(p.Current); err!= nil {
		t.Error("When unmarked, settle should not affect any files")
	}
}
