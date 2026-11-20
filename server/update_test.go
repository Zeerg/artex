package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Zeerg/artex/selfupdate"
)

// releaseCache It is the layer protecting GitHub quota: unauthenticated API has only 60 times/hour/IP,
// And the top bar's"There is a new version."Hint that each full page load checks once. Cache invalidation causes multiple users to open several
// The tab will exhaust its quota, and after that, even if you want to update, you can't find it anymore.

func newTestCache(fetch func(context.Context, *http.Client) (*selfupdate.Release, error)) *releaseCache {
	return &releaseCache{fetch: fetch}
}

func TestReleaseCacheServesFromCache(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	for range 5 {
		rel, err := c.get(t.Context(), nil, false)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if rel.TagName != "v0.3.8" {
			t.Fatalf("TagName = %q", rel.TagName)
		}
	}
	if calls != 1 {
		t.Errorf("5 Each query should only source 1 time, actually. %d Time", calls)
	}
}

func TestReleaseCacheForceBypasses(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// Users must get real-time results when clicking 'Check Update', otherwise the newly released version will only be visible after the cache expires.
	if _, err := c.get(t.Context(), nil, true); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("force Should bypass cache, expecting to source twice, actually %d Time", calls)
	}
}

func TestReleaseCacheExpiresAfterTTL(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// Roll back the exfiltration time to just past the expiration date to simulate TTL reaching the point.
	c.at = time.Now().Add(-releaseTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("TTL After expiration, it should re-source, expecting 2 attempts, but actually %d Time", calls)
	}
}

func TestReleaseCacheUsesShorterTTLForErrors(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return nil, errors.New("github Unreachable")
	})

	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("Expected return error")
	}
	// Failure results should also be cached for a while, otherwise waiting for GitHub to become unreachable will result in each page load timing out once.
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("Expected return error")
	}
	if calls != 1 {
		t.Errorf("Errors should be short-term cached, expecting to source once, actually. %d Time", calls)
	}

	// But an incorrect TTL must be significantly shorter than a successful one, allowing quick recovery after network recovery.
	if releaseErrTTL >= releaseTTL {
		t.Fatalf("Error TTL(%v) must be shorter than successful TTL(%v)", releaseErrTTL, releaseTTL)
	}
	c.at = time.Now().Add(-releaseErrTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("Expected return error")
	}
	if calls != 2 {
		t.Errorf("Retry after error TTL expires, expect 2 times, actually %d Time", calls)
	}
}

func TestReleaseCacheDoesNotPoisonOnCallerCancel(t *testing.T) {
	good := &selfupdate.Release{TagName: "v0.3.8"}
	c := newTestCache(func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return good, nil
	})
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}

	// Closing the visitor's tab cancels the request. This doesn't mean GitHub has a problem; never assume it does."Cancelled"
	// Write to cache.——Otherwise, each visitor will receive a mysterious error for the next 30 minutes.
	c.fetch = func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return nil, ctx.Err()
	}
	c.at = time.Now().Add(-releaseTTL - time.Second) // Expire cache, force it to source.

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx, nil, false); err == nil {
		t.Fatal("When the caller cancels, propagate the error to it")
	}

	// Key invariant: The canceled one should leave no trace——Cache has neither"Cancelled"This error,
	// It still retains the good result of the last time.
	if c.err != nil {
		t.Fatalf("Cancel error should not be written to cache, received %v", c.err)
	}
	if c.rel == nil || c.rel.TagName != "v0.3.8" {
		t.Fatalf("Cache should retain the previous good result. %+v", c.rel)
	}

	// The cancellation did not yield any new data, so the next visitor should re-source.——and still be able to get results normally,
	// Not affected by the cancellation of the last time.
	c.fetch = func(context.Context, *http.Client) (*selfupdate.Release, error) {
		return good, nil
	}
	rel, err := c.get(t.Context(), nil, false)
	if err != nil {
		t.Fatalf("Normal requests after cancellation should not report errors: %v", err)
	}
	if rel == nil || rel.TagName != "v0.3.8" {
		t.Fatalf("Should get a normal result, got %+v", rel)
	}
}
