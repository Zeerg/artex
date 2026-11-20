package selfupdate

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"
	"time"
)

// sumsAsset Is release.yml Checksum list generated, covering all zip files in Release.
const sumsAsset = "SHA256SUMS"

// maxBinarySize Limit the size of extracted binaries to prevent malformed zip files from filling the disk.
const maxBinarySize = 512 << 20 // 512 MiB

// Phase which is a stage of the upgrade process and directly used as the phase field in SSE events.
type Phase string

const (
	PhaseIdle Phase = "idle"
	PhaseDownload Phase = "downloading"
	PhaseVerify Phase = "verifying"
	PhaseExtract Phase = "extracting"
	PhaseStaged Phase = "staged"
	PhaseFailed Phase = "failed"
)

// Progress Provided by the caller to push progress to the frontend. pct is only meaningful during the download phase (0-100).
// Pass -1 for other stages.
type Progress func(ph Phase, pct int, msg string)

// Stage Download the current platform release package for the specified Release, verify it, and temporarily store the new binary as artex.new.
//
// It uses a complete zip instead of raw binary data. There are two reasons for this: the existing Release's SHA256SUMS already
// Only overwrite zip, no need to change CI for zip compatibility with already released historical versions; zip includes
// skills/, Left a slot for future synchronization of built-in skills. The cost is just downloading those few hundred KB of skills.
//
// Function return indicates temporary storage completion. The caller subsequently closes gracefully and exits with ExitRestart.
func Stage(ctx context.Context, c *http.Client, rel *Release, currentVersion string, prog Progress) error {
	if prog == nil {
		prog = func(Phase, int, string) {}
	}
	p, err:= ResolvePaths()
	if err!= nil {
		return err
	}
	if err:= checkWritable(p.Dir); err!= nil {
		return err
	}

	name:= AssetName(rel.TagName, runtime.GOOS, runtime.GOARCH)
	asset, ok:= rel.FindAsset(name)
	if!ok {
		return fmt.Errorf("This version does not provide %s/%s The release package (missing %s)", runtime.GOOS, runtime.GOARCH, name)
	}

	prog(PhaseDownload, 0, "Get checksum list…")
	sums, err:= fetchSums(ctx, c, rel)
	if err!= nil {
		return err
	}
	want, ok:= sums[name]
	if!ok {
		return fmt.Errorf("%s Not included %s, Refuse to install unverified binaries", sumsAsset, name)
	}

	// All temporary files are placed in the target directory to ensure that the final rename is an atomic operation within the same file system.
	// (Cross-device rename will fail, while /tmp is often an independent mount point.
	zipPath:= p.New + ".zip.part"
	binPath:= p.New + ".part"
	defer func() {
		_ = os.Remove(zipPath)
		_ = os.Remove(binPath)
	}()

	prog(PhaseDownload, 0, fmt.Sprintf("Download %s (%s)…", name, humanSize(asset.Size)))
	got, err:= download(ctx, c, asset, zipPath, prog)
	if err!= nil {
		return err
	}

	prog(PhaseVerify, -1, "Verify SHA256…")
	if!strings.EqualFold(got, want) {
		return fmt.Errorf("SHA256 Mismatch: Expected %s, Actually %s (Download corrupted or tampered with)", short(want), short(got))
	}

	prog(PhaseExtract, -1, "Unzip and Perform Smoke Test…")
	if err:= extractBinary(zipPath, binPath); err!= nil {
		return err
	}
	if err:= smokeTest(binPath); err!= nil {
		return fmt.Errorf("New version cannot run on the current system: %w", err)
	}

	// Save your own SHA256 separately: You'll need to verify it again before switching outfits next time.
	// To prevent files from being modified or corrupted during the time between staging and restart.
	binSum, err:= fileSHA256(binPath)
	if err!= nil {
		return fmt.Errorf("Calculate the new binary checksum.: %w", err)
	}
	if err:= os.WriteFile(p.Sum, []byte(binSum), 0o644); err!= nil {
		return fmt.Errorf("Write checksum: %w", err)
	}
	if err:= os.Rename(binPath, p.New); err!= nil {
		_ = os.Remove(p.Sum)
		return fmt.Errorf("Temporarily Save New Version: %w", err)
	}

	if err:= writeMarker(p.Marker, marker{
		From: currentVersion,
		To: strings.TrimPrefix(rel.TagName, "v"),
		StagedAt: time.Now().Unix(),
	}); err!= nil {
		// Marking only affects automatic rollback capability; the draft is already in place and will not be interrupted by this.
		prog(PhaseStaged, -1, "Warning: Failed to write upgrade marker, this upgrade will have no automatic rollback protection")
	}

	prog(PhaseStaged, 100, "New version ready, restarting….")
	return nil
}

// fetchSums Downloading and parsing SHA256SUMS, returning file name → Hexadecimal summary.
func fetchSums(ctx context.Context, c *http.Client, rel *Release) (map[string]string, error) {
	asset, ok:= rel.FindAsset(sumsAsset)
	if!ok {
		return nil, fmt.Errorf("This Release does not have %s, Unable to verify integrity, reject upgrade.", sumsAsset)
	}
	body, err:= get(ctx, c, asset.downloadURL())
	if err!= nil {
		return nil, fmt.Errorf("Download %s: %w", sumsAsset, err)
	}
	defer body.Close()

	raw, err:= io.ReadAll(io.LimitReader(body, 1<<20))
	if err!= nil {
		return nil, fmt.Errorf("Read %s: %w", sumsAsset, err)
	}
	out:= parseSums(string(raw))
	if len(out) == 0 {
		return nil, fmt.Errorf("%s Content is empty or format cannot be recognized", sumsAsset)
	}
	return out, nil
}

// parseSums Parse a sha256sum-style manifest and return the filename → Hexadecimal summary.
//
// The first field must be 64-bit hexadecimal for inclusion. Only by"Exactly two fields"Insufficient judgment——
// Any line with two words of explanation text will be treated as a valid entry, stuffing garbage values into the summary table,
// The real asset might match the wrong summary.
func parseSums(raw string) map[string]string {
	out:= map[string]string{}
	for line:= range strings.Lines(raw) {
		// Format as "<sha256>  <filename>" (sha256sum Use double spaces; shasum's binary
		// Pattern will append to file names * Prefix).
		fields:= strings.Fields(strings.TrimSpace(line))
		if len(fields)!= 2 ||!isHexSHA256(fields[0]) {
			continue
		}
		name:= strings.TrimPrefix(fields[1], "*")
		if name == "" {
			continue
		}
		out[name] = strings.ToLower(fields[0])
	}
	return out
}

func isHexSHA256(s string) bool {
	if len(s)!= 64 {
		return false
	}
	for _, c:= range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// download Write assets to dst while calculating SHA256 and reporting progress by Content-Length.
func download(ctx context.Context, c *http.Client, a Asset, dst string, prog Progress) (string, error) {
	body, err:= get(ctx, c, a.downloadURL())
	if err!= nil {
		return "", fmt.Errorf("Download %s: %w", a.Name, err)
	}
	defer body.Close()

	f, err:= os.Create(dst)
	if err!= nil {
		return "", fmt.Errorf("Creating temporary file: %w", err)
	}
	defer f.Close()

	h:= sha256.New()
	pw:= &progressWriter{total: a.Size, prog: prog, name: a.Name, last: time.Now()}
	if _, err:= io.Copy(io.MultiWriter(f, h, pw), body); err!= nil {
		return "", fmt.Errorf("Download interrupted.: %w", err)
	}
	if err:= f.Sync(); err!= nil {
		return "", fmt.Errorf("disk write failed: %w", err)
	}
	if a.Size > 0 && pw.written!= a.Size {
		return "", fmt.Errorf("download incomplete: expected %d bytes, got %d bytes", a.Size, pw.written)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// get Initiate a GET constrained by the whitelist, returning the response body.
func get(ctx context.Context, c *http.Client, rawURL string) (io.ReadCloser, error) {
	req, err:= http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err!= nil {
		return nil, err
	}
	if err:= checkURL(req.URL); err!= nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "artex-selfupdate")
	req.Header.Set("Accept", "application/octet-stream")
	setUpdateAuth(req)
	resp, err:= c.Do(req)
	if err!= nil {
		return nil, err
	}
	if resp.StatusCode!= http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// extractBinary Extract artex executable from the release package.
//
// Package structure is artex-<Role>-<os>-<arch>/artex, But here it's based on**Base Name**Match instead of completing
// Path: The version number appears once in the package name; a single character typo results in an entire upgrade failure. Look for the base name for greater resilience.
func extractBinary(zipPath, dst string) error {
	want:= "artex"
	if runtime.GOOS == "windows" {
		want = "artex.exe"
	}
	zr, err:= zip.OpenReader(zipPath)
	if err!= nil {
		return fmt.Errorf("Open release package: %w", err)
	}
	defer zr.Close()

	for _, entry:= range zr.File {
		if entry.FileInfo().IsDir() ||!strings.EqualFold(path.Base(entry.Name), want) {
			continue
		}
		rc, err:= entry.Open()
		if err!= nil {
			return fmt.Errorf("Read %s: %w", entry.Name, err)
		}
		defer rc.Close()

		f, err:= os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err!= nil {
			return fmt.Errorf("Write new binary: %w", err)
		}
		defer f.Close()

		n, err:= io.Copy(f, io.LimitReader(rc, maxBinarySize+1))
		if err!= nil {
			return fmt.Errorf("Unzip %s: %w", entry.Name, err)
		}
		if n > maxBinarySize {
			return fmt.Errorf("Executable files in the release package exceed %s, Refuse to decompress", humanSize(maxBinarySize))
		}
		if n == 0 {
			return fmt.Errorf("Within the package %s It's an empty file.", want)
		}
		return f.Sync()
	}
	return fmt.Errorf("No package found for release %s", want)
}

// checkWritable Confirm directory writability in advance. Without this step, non-root operation or binary placed in system
// When downloading directories, it fails at the moment of switching after downloading dozens of MB.
func checkWritable(dir string) error {
	probe, err:= os.CreateTemp(dir, ".artex-update-probe-*")
	if err!= nil {
		return fmt.Errorf("program directory %s Unwritable, cannot automatically update (please check permissions or manually upgrade): %w", dir, err)
	}
	name:= probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

// progressWriter Count written bytes and limit frequency reporting to avoid pushing an SSE for each 32KiB chunk.
type progressWriter struct {
	total int64
	written int64
	name string
	prog Progress
	last time.Time
}

func (w *progressWriter) Write(b []byte) (int, error) {
	w.written += int64(len(b))
	if time.Since(w.last) < 300*time.Millisecond {
		return len(b), nil
	}
	w.last = time.Now()
	pct:= -1
	if w.total > 0 {
		pct = int(w.written * 100 / w.total)
	}
	w.prog(PhaseDownload, pct, fmt.Sprintf("Downloading %s / %s", humanSize(w.written), humanSize(w.total)))
	return len(b), nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp:= int64(unit), 0
	for v:= n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12] + "…"
	}
	return sum
}
