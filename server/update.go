package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/Zeerg/artex/selfupdate"
)

// HTTP page for single-click update of the page. The actual download/verification/installation logic is all in the selfupdate package,
// Here it only handles authentication boundaries, concurrency mutual exclusion, progress broadcasting, as well as"It's time to exit."Tell main.
//
// Restart is not completed by this process. Store the new version and then restart with selfupdate..ExitRestart Exit,
// By daemon script (start)..sh / start.bat, Docker Below is ENTRYPOINT) re-launch.

// restartCh Close upon upgrade readiness or rollback completion. main receives it and exits with ExitRestart.
var (
	restartOnce sync.Once
	restartCh = make(chan struct{})
)

// RestartRequested Return one in"Please exit and allow the daemon process to restart me"Closed channel.
func RestartRequested() <-chan struct{} { return restartCh }

func requestRestart() { restartOnce.Do(func() { close(restartCh) }) }

// bootState This is for selfupdate during this run..Bootstrap Conclusion (upgrade successful / just rolled back /
// The draft is discarded), injected by main to truthfully inform the frontend of the last upgrade's outcome for /api/update/check.
var (
	bootStateMu sync.Mutex
	bootState selfupdate.State
)

// SetBootUpdateState Called once by main on startup.
func SetBootUpdateState(st selfupdate.State) {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	bootState = st
}

func bootUpdateState() selfupdate.State {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	return bootState
}

// releaseCache Caches the latest version query result from GitHub.
//
// Top bar's"There is a new version."The prompt will be checked once per full page load, and unauthenticated GitHub API is
// 60 times per hour per IP——Without caching, opening several tabs or refreshing the page a few times will exhaust your quota,
// Afterward, trying to update becomes impossible. The user must explicitly click"Check for Updates"Force bypassing cache when possible.
type releaseCache struct {
	mu sync.Mutex
	rel *selfupdate.Release
	err error
	at time.Time
	// fetch A data retrieval function, kept for testing injection points; if nil, it follows the real GitHub query.
	fetch func(context.Context, *http.Client) (*selfupdate.Release, error)
}

const (
	releaseTTL = 30 * time.Minute
	// Cache failure results for a short while; otherwise, GitHub being unreachable means waiting for timeout on every page load;
	// But TTL should be short, so it can quickly recover after network restoration.
	releaseErrTTL = 2 * time.Minute
	// Query timeout. NewClient's 30-minute timeout is for downloading the entire package; querying versions shouldn't wait so long.
	releaseTimeout = 20 * time.Second
)

var relCache = &releaseCache{}

// get Return the latest Release; if cached, do not access the network.
//
// It holds the lock during data retrieval: concurrent requests queue up for the same query result instead of making separate GitHub calls.
// (When multiple tabs simultaneously query upon page load, it's the most likely time to trigger rate limiting).
func (c *releaseCache) get(ctx context.Context, client *http.Client, force bool) (*selfupdate.Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if!force {
		ttl:= releaseTTL
		if c.err!= nil {
			ttl = releaseErrTTL
		}
		if!c.at.IsZero() && time.Since(c.at) < ttl {
			return c.rel, c.err
		}
	}

	fetch:= c.fetch
	if fetch == nil {
		fetch = selfupdate.FetchLatest
	}
	ctx, cancel:= context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	rel, err:= fetch(ctx, client)
	// The request was canceled (user closed the tab) doesn't mean there's a problem with GitHub, don't write it into the cache,
	// Otherwise, the next visitor will receive a mysterious one"Cancelled"Error.
	if err!= nil && ctx.Err()!= nil && errors.Is(ctx.Err(), context.Canceled) {
		return c.rel, err
	}
	c.rel, c.err, c.at = rel, err, time.Now()
	return rel, err
}

// updateProgress Progress message sent to the frontend.
type updateProgress struct {
	Phase selfupdate.Phase `json:"phase"`
	Percent int `json:"percent"` // Only meaningful during the download phase; otherwise, it is -1
	Message string `json:"message"`
	Version string `json:"version,omitempty"`
	Error string `json:"error,omitempty"`
}

// updateHub Hold the progress of a single upgrade and broadcast it to SSE subscribers.
//
// running Simultaneously serving as mutual exclusion: during upgrade, another POST /api/update/apply directly returns 409,
// Avoid two goroutines writing to the same artex simultaneously.new Write.
type updateHub struct {
	mu sync.Mutex
	running bool
	cur updateProgress
	subs map[chan updateProgress]struct{}
}

var updHub = &updateHub{
	cur: updateProgress{Phase: selfupdate.PhaseIdle, Percent: -1},
	subs: map[chan updateProgress]struct{}{},
}

// begin Attempt to seize upgrade privileges; returns false if already in progress.
func (h *updateHub) begin(version string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return false
	}
	h.running = true
	h.cur = updateProgress{Phase: selfupdate.PhaseDownload, Percent: 0, Message: "Preparing…", Version: version}
	h.fanout(h.cur)
	return true
}

// finish End one upgrade. err is nil means staging succeeded, waiting for restart.
func (h *updateHub) finish(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running = false
	if err!= nil {
		h.cur = updateProgress{Phase: selfupdate.PhaseFailed, Percent: -1, Message: "Update failed", Error: err.Error(), Version: h.cur.Version}
	} else {
		h.cur = updateProgress{Phase: selfupdate.PhaseStaged, Percent: 100, Message: "New version ready, restarting….", Version: h.cur.Version}
	}
	h.fanout(h.cur)
}

func (h *updateHub) publish(ph selfupdate.Phase, pct int, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cur = updateProgress{Phase: ph, Percent: pct, Message: msg, Version: h.cur.Version}
	h.fanout(h.cur)
}

// fanout Must hold h.mu When called. Subscriber channel is buffered; if full, it will discard——
// Progress is transient and disposable information; never let a stuck SSE connection block the upgrade itself.
func (h *updateHub) fanout(p updateProgress) {
	for ch:= range h.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

func (h *updateHub) snapshot() (updateProgress, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur, h.running
}

func (h *updateHub) subscribe() (<-chan updateProgress, func()) {
	ch:= make(chan updateProgress, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}

// updateCheck Query latest formal version on GitHub and compare with current version.
//
// Frontend will also directly connect to the API.github.com (GitHub CORS is *), but**Based on this interface**:
// Download is done by the backend, so only the backend can access GitHub for updates. The browser can connect but the server cannot.
// This situation is very common (server in the internal network, or proxy only configured in browsers), so point updates will inevitably fail,
// It's better to report the error honestly at this step.
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	current:= BuildVersion
	mode:= "binary"
	if selfupdate.InDocker() {
		mode = "docker"
	}
	boot:= bootUpdateState()
	out:= map[string]any{
		"current": current,
		"mode": mode,
		"os": runtime.GOOS,
		"arch": runtime.GOARCH,
		"has_backup": selfupdate.HasBackup(),
		"repo": selfupdate.Repo,
		"boot_notice": boot.Detail,
		"rolled_back": boot.RolledBack,
	}

	// Top bar prompt to use cache (default); user clicks"Check for Updates"With force=1 Force source.
	force:= r.URL.Query().Get("force")!= ""
	client:= selfupdate.NewClient(s.m.GlobalProxy())
	rel, err:= relCache.get(r.Context(), client, force)
	if err!= nil {
		out["error"] = err.Error()
		writeJSON(w, 200, out)
		return
	}

	latest:= rel.TagName
	out["latest"] = latest
	out["notes"] = rel.Body
	out["html_url"] = rel.HTMLURL
	if!rel.PublishedAt.IsZero() {
		out["published_at"] = rel.PublishedAt.Format(time.RFC3339)
	}

	asset:= selfupdate.AssetName(latest, runtime.GOOS, runtime.GOARCH)
	out["asset"] = asset
	if a, ok:= rel.FindAsset(asset); ok {
		out["asset_available"] = true
		out["size"] = a.Size
	} else {
		out["asset_available"] = false
	}

	cmp, comparable:= selfupdate.CompareVersions(current, latest)
	out["comparable"] = comparable
	out["has_update"] = comparable && cmp < 0
	if!comparable {
		// Development builds (dev / git describe with suffix) do not have comparable version numbers. Passing will only
		// Overwrite the local binary being debugged with the official version, so no update is given directly.
		out["reason"] = fmt.Sprintf("current version %q Not a formal release version, one-click update disabled.", current)
	}
	writeJSON(w, 200, out)
}

// updateApply Download and temporarily store new version, then exit process to let watchdog script restart.
//
// Immediately return 202, actual work runs in a background goroutine: The entire package download may take several minutes,
// Will be cut off by reverse proxy timeout on the request. Progress goes /api/update/stream.
func (s *Server) updateApply(w http.ResponseWriter, r *http.Request) {
	current:= BuildVersion

	// Use cache: Ensure the installed version is the one the user sees and confirms on the interface.
	client:= selfupdate.NewClient(s.m.GlobalProxy())
	rel, err:= relCache.get(r.Context(), client, false)
	if err!= nil {
		writeErr(w, 502, err.Error())
		return
	}
	cmp, comparable:= selfupdate.CompareVersions(current, rel.TagName)
	if!comparable {
		writeErr(w, 400, fmt.Sprintf("current version %q Not a formal release version, one-click update disabled.", current))
		return
	}
	if cmp >= 0 {
		writeErr(w, 400, fmt.Sprintf("Already at the latest version %s", current))
		return
	}
	if!updHub.begin(rel.TagName) {
		writeErr(w, 409, "An update is already in progress")
		return
	}

	go func() {
		// Intentionally use s.ctx Instead of the request's ctx: The HTTP response ends the request immediately,
		// Downloading it will be canceled immediately.
		err:= selfupdate.Stage(s.ctx, client, rel, current, func(ph selfupdate.Phase, pct int, msg string) {
			updHub.publish(ph, pct, msg)
		})
		updHub.finish(err)
		if err!= nil {
			log.Printf("[update] Update failed: %v", err)
			return
		}
		log.Printf("[update] %s → %s Already cached, about to exit to complete reinstallation.", current, rel.TagName)
		// Leave a little time to push the last progress update to the frontend before triggering the exit.
		time.Sleep(1500 * time.Millisecond)
		requestRestart()
	}()

	writeJSON(w, 202, map[string]any{"ok": true, "target": rel.TagName})
}

// updateRollback Manually revert to the previous version (backup artex before dressing)..old).
func (s *Server) updateRollback(w http.ResponseWriter, r *http.Request) {
	if _, running:= updHub.snapshot(); running {
		writeErr(w, 409, "An update is currently underway, so rolling back is not possible.")
		return
	}
	if err:= selfupdate.Rollback(); err!= nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[update] Already manually rolled back to previous version, about to exit to complete switch")
	writeJSON(w, 202, map[string]any{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		requestRestart()
	}()
}

// updateStream Pushing update progress via SSE.
func (s *Server) updateStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok:= w.(http.Flusher)
	if!ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub:= updHub.subscribe()
	defer unsub()

	send:= func(p updateProgress) {
		b, _:= json.Marshal(p)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	// Prepend current status, see ongoing upgrade immediately after page refresh.
	cur, _:= updHub.snapshot()
	send(cur)

	ctx:= r.Context()
	ping:= time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok:= <-ch:
			if!ok {
				return
			}
			send(p)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
