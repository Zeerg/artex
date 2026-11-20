package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Zeerg/artex/db"
	"github.com/Zeerg/artex/notify"
)

// This file covers the end-to-end behavior of push functionality: vulnerability exfiltration → Item) → Original → True send HTTP.
//
// A security note: These test cases**Do not call the global Notifier..step()**, Only for those created by yourself
// Channel calls stepRealtime/stepDigest. The reason for step() Will iterate through all enabled channels in the database.——
// Running tests on a development library with real DingTalk/Qiwei robot integration. Global step will forget to add a backoff level during testing—code won't error but will make the 4th and 5th retries use the last interval.
// The generated vulnerabilities are truly pushed to those groups. Channel calls strictly limit the impact surface to the fake receiving end created for testing.
//
// Cleanup: Delete events (cascading delete deliveries) and channels produced by this use case at the end of the test case to avoid channel backlog.
//
// Assertion criteria: stepRealtime/stepDigest does not return values, internal logging is recorded, so here the assertion is
// **Observable external behavior** (Assume what the receiving end received and where the delivery line landed), not the function's
// Return value——This is closer to the actual call path than stubbing return values.

// notifyFixture Common device for this file's use cases.
type notifyFixture struct {
	s *Server
	pg *db.DB
	request func(method, path, body string) *httptest.ResponseRecorder
	n *Notifier
	// Custom task/exploration cases: Use cases record vulnerabilities here and isolate data from other use cases.
	taskID int64
	expID int64
	// cleanupMark Events generated after this will be deleted during cleanup.
	cleanupMark int64
}

func newNotifyFixture(t *testing.T) *notifyFixture {
	t.Helper()
	// All fake receivers in this file run on 127.0.0.1 Up, while delivery defaults to rejecting loopback addresses.
	// (Prevent SSRF attacks targeting local services and cloud metadata). Test explicitly opening this switch;
	// The behavior of 'default deny' is guarded by notify's ssrf_test..go Coverage.
	t.Setenv(notify.AllowLocalTargetsEnv, "1")
	s, _, request:= trafficEvidenceServer(t)
	pg:= s.m.pg

	// Create a custom task: The task created by the shared device trafficEvidenceServer cannot get the exploration id,
	// Vulnerability records must provide it.
	task, err:= s.m.CreateTask("Notification push test", "Push behavior verification", nil, 0, 0)
	if err!= nil {
		t.Fatal(err)
	}
	taskID, err:= strconv.ParseInt(task.ID, 10, 64)
	if err!= nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Exec(`DELETE FROM tasks WHERE id=$1`, taskID) })

	var mark int64
	if err:= pg.QueryRow(`SELECT COALESCE(max(id),0) FROM notification_events`).Scan(&mark); err!= nil {
		t.Fatal(err)
	}
	// Make the device self-contained: mark events that existed before setting up the fixture as dispatched once.
	//
	// Why must we do: FanOutPendingEvents is**global**It will return all unassigned events in the library
	// Expand to all matching channels. And the shared device trafficEvidenceServer will record a vulnerability
	// (It returns that initial finding, so other use cases may also have remnants. Without isolation,
	// These stray events will be dispatched to this use case's channel, allowing assertions like 'should have N deliveries' to work.
	// Sometimes right, sometimes wrong——And the mistake depends on the order of use case execution, harder to debug than direct failure.
	if _, err:= pg.Exec(`UPDATE notification_events SET fanned_out = true WHERE id <= $1 AND NOT fanned_out`, mark); err!= nil {
		t.Fatal(err)
	}

	f:= &notifyFixture{s: s, pg: pg, request: request, n: newNotifier(s), taskID: taskID, expID: task.ExpID, cleanupMark: mark}
	t.Cleanup(func() {
		if _, err:= pg.Exec(`DELETE FROM notification_events WHERE id > $1`, f.cleanupMark); err!= nil {
			t.Logf("Failed to clean up notification events: %v", err)
		}
	})
	// The main switch must be on (other cases may have turned it off).
	if err:= pg.SetBool(settingNotifyEnabled, true); err!= nil {
		t.Fatal(err)
	}
	return f
}

// record Walk the real evidence write path and land one vulnerability, return finding id.
// This path will eventually**Same transaction**Registering push events.——This is the hook point of this feature.
func (f *notifyFixture) record(t *testing.T, vulnclass, severity string) int64 {
	t.Helper()
	out, err:= f.s.evidenceStore().Record(context.Background(), db.RecordFindingInput{
		TaskID: f.taskID,
		ExplorationID: f.expID,
		Worker: "test",
		VulnClass: vulnclass,
		Name: vulnclass,
		Severity: severity,
		Summary: vulnclass + " Summary of",
		Evidence: "poc",
	}, nil)
	if err!= nil {
		t.Fatalf("Failed to record vulnerability: %v", err)
	}
	return out.FindingID
}

// channel Read channel configuration (for stepX calls per channel).
func (f *notifyFixture) channel(t *testing.T, id int64) *db.NotificationChannel {
	t.Helper()
	ch, err:= f.pg.NotificationChannelByID(context.Background(), id)
	if err!= nil {
		t.Fatalf("Read channel failed: %v", err)
	}
	return ch
}

// deliver Dispatch event and only deliver one round for the specified channel.
func (f *notifyFixture) deliver(t *testing.T, chID int64, baseURL string) {
	t.Helper()
	ctx:= context.Background()
	if _, _, err:= f.pg.FanOutPendingEvents(ctx, 500); err!= nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	f.n.stepRealtime(ctx, f.channel(t, chID), 50, baseURL)
}

// createChannel Create a channel through an HTTP interface, covering the validation path of the interface itself.
func (f *notifyFixture) createChannel(t *testing.T, payload map[string]any) int64 {
	t.Helper()
	raw, _:= json.Marshal(payload)
	r:= f.request("POST", "/api/notify/channels", string(raw))
	if r.Code!= 200 {
		t.Fatalf("Failed to create channel %d: %s", r.Code, r.Body)
	}
	var res struct {
		ID int64 `json:"id"`
	}
	if err:= json.Unmarshal(r.Body.Bytes(), &res); err!= nil || res.ID == 0 {
		t.Fatalf("Channel creation returned an exception: %s (%v)", r.Body, err)
	}
	t.Cleanup(func() { f.pg.Exec(`DELETE FROM notification_channels WHERE id=$1`, res.ID) })
	return res.ID
}

// fakeWebhook A fake receiver to record the received request body.
type fakeWebhook struct {
	*httptest.Server
	mu sync.Mutex
	bodies []map[string]any
}

func newFakeWebhook(t *testing.T) *fakeWebhook {
	t.Helper()
	f:= &fakeWebhook{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _:= io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeWebhook) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func (f *fakeWebhook) body(t *testing.T, i int) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.bodies) {
		t.Fatalf("Assume receiver only received. %d For requests, unable to get the %d Item", len(f.bodies), i)
	}
	return f.bodies[i]
}

func (f *fakeWebhook) last(t *testing.T) map[string]any {
	t.Helper()
	if f.count() == 0 {
		t.Fatal("Fake receiver did not receive any requests")
	}
	return f.body(t, f.count()-1)
}

// markdownText Extract the body from the request, compatible with differences in field names across platforms:
// DingTalk markdown uses `text`, ActionCard Use `text`, Enterprise WeChat markdown uses `content`.
func markdownText(t *testing.T, body map[string]any) string {
	t.Helper()
	for _, key:= range []string{"markdown", "actionCard"} {
		section, ok:= body[key].(map[string]any)
		if!ok {
			continue
		}
		for _, field:= range []string{"text", "content"} {
			if s, ok:= section[field].(string); ok && s!= "" {
				return s
			}
		}
	}
	t.Fatalf("There is no recognizable body in the request body: %v", body)
	return ""
}

// agePendingBatch Age the pending deliveries of this channel for testing batch expiration.
func (f *notifyFixture) agePendingBatch(t *testing.T, chID int64) {
	t.Helper()
	if _, err:= f.pg.Exec(`UPDATE notification_deliveries SET created_at = now() - interval '2 hours'
WHERE channel_id=$1 AND state=$2`, chID, db.NotifyStatePending); err!= nil {
		t.Fatal(err)
	}
}

func TestNotifyEndToEndRealtimeDelivery(t *testing.T) {
	f:= newNotifyFixture(t)
	hook:= newFakeWebhook(t)
	chID:= f.createChannel(t, map[string]any{
		"name": "real-time push",
		"kind": notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	f.record(t, "SQLCompleted", "high")
	f.deliver(t, chID, "")

	if hook.count()!= 1 {
		t.Fatalf("Should send 1 message, actually %d", hook.count())
	}
	text:= markdownText(t, hook.last(t))
	for _, want:= range []string{"SQLCompleted", "Retest", "Failure"} {
		if!strings.Contains(text, want) {
			t.Fatalf("Missing message body %q:\n%s", want, text)
		}
	}
	// Delivery should be transferred to sent.
	var pending int
	if err:= f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1 AND state <> $2`,
		chID, db.NotifyStateSent).Scan(&pending); err!= nil {
		t.Fatal(err)
	}
	if pending!= 0 {
		t.Fatalf("Still exists after submission %d Not marked as sent", pending)
	}
}

func TestNotifyChannelAPIMasksSecretsAndPreservesOnUpdate(t *testing.T) {
	f:= newNotifyFixture(t)
	chID:= f.createChannel(t, map[string]any{
		"name": "Mask case",
		"kind": notify.KindDingTalk,
		"config": map[string]any{"webhook": "https://oapi.dingtalk.com/robot/send?access_token=abc123456", "secret": "SECabcdef123456"},
	})

	r:= f.request("GET", "/api/notify/channels", "")
	if r.Code!= 200 {
		t.Fatalf("List Channels Failed %d: %s", r.Code, r.Body)
	}
	if strings.Contains(r.Body.String(), "abc123456") || strings.Contains(r.Body.String(), "SECabcdef123456") {
		t.Fatalf("Interface echo leaked credentials: %s", r.Body)
	}
	var listed struct {
		Channels []struct {
			ID int64 `json:"id"`
			Config map[string]any `json:"config"`
			SecretKeys []string `json:"secret_keys"`
		} `json:"channels"`
	}
	if err:= json.Unmarshal(r.Body.Bytes(), &listed); err!= nil {
		t.Fatal(err)
	}
	var mine *struct {
		ID int64 `json:"id"`
		Config map[string]any `json:"config"`
		SecretKeys []string `json:"secret_keys"`
	}
	for i:= range listed.Channels {
		if listed.Channels[i].ID == chID {
			mine = &listed.Channels[i]
		}
	}
	if mine == nil {
		t.Fatal("Newly created channels do not appear in the list.")
	}
	if!notify.IsMasked(fmt.Sprint(mine.Config["webhook"])) ||!notify.IsMasked(fmt.Sprint(mine.Config["secret"])) {
		t.Fatalf("Credential fields should be masked values.: %v", mine.Config)
	}
	if len(mine.SecretKeys) == 0 {
		t.Fatal("The interface should inform the frontend which fields are credentials.")
	}

	// PATCH Only rename + Return masked credentials: True credentials must be retained exactly.
	body, _:= json.Marshal(map[string]any{
		"name": "After renaming",
		"config": map[string]any{"webhook": fmt.Sprint(mine.Config["webhook"]), "secret": fmt.Sprint(mine.Config["secret"])},
	})
	if r:= f.request("PATCH", fmt.Sprintf("/api/notify/channels/%d", chID), string(body)); r.Code!= 200 {
		t.Fatalf("Update failed %d: %s", r.Code, r.Body)
	}
	cfg:= f.channelConfig(t, chID)
	if cfg["webhook"]!= "https://oapi.dingtalk.com/robot/send?access_token=abc123456" {
		t.Fatalf("Masked return overwrites real credentials: %v", cfg["webhook"])
	}
	if cfg["secret"]!= "SECabcdef123456" {
		t.Fatalf("Masked return covers secret.: %v", cfg["secret"])
	}
	if f.channel(t, chID).Name!= "After renaming" {
		t.Fatal("Name not updated")
	}

	// Explicitly clearing secret should take effect (different from 'return masked'=Keep unchanged (').
	body, _ = json.Marshal(map[string]any{"config": map[string]any{"secret": ""}})
	if r:= f.request("PATCH", fmt.Sprintf("/api/notify/channels/%d", chID), string(body)); r.Code!= 200 {
		t.Fatalf("Failed to clear secret %d: %s", r.Code, r.Body)
	}
	if _, still:= f.channelConfig(t, chID)["secret"]; still {
		t.Fatal("An empty string should clear the secret")
	}
}

func (f *notifyFixture) channelConfig(t *testing.T, id int64) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err:= json.Unmarshal(f.channel(t, id).Config, &cfg); err!= nil {
		t.Fatal(err)
	}
	return cfg
}

func TestNotifyChannelAPICreateValidation(t *testing.T) {
	f:= newNotifyFixture(t)
	cases:= []struct {
		name string
		payload map[string]any
		wantSub string
	}{
		{"Type illegal", map[string]any{"name": "x", "kind": "nope", "config": map[string]any{}}, "Invalid channel type"},
		{"Missing name", map[string]any{"kind": notify.KindDingTalk, "config": map[string]any{"webhook": "https://e.com/h"}}, "Missing channel name"},
		{"Missing webhook", map[string]any{"name": "x", "kind": notify.KindDingTalk, "config": map[string]any{}}, "Webhook"},
		{"webhook protocol illegal", map[string]any{"name": "x", "kind": notify.KindDingTalk, "config": map[string]any{"webhook": "file:///etc/passwd"}}, "Webhook Invalid address"},
		{"Pattern illegal", map[string]any{"name": "x", "kind": notify.KindDingTalk, "mode": "sometimes", "config": map[string]any{"webhook": "https://e.com/h"}}, "Invalid push mode"},
	}
	for _, tc:= range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _:= json.Marshal(tc.payload)
			r:= f.request("POST", "/api/notify/channels", string(raw))
			if r.Code!= 400 {
				t.Fatalf("should return 400, got %d: %s", r.Code, r.Body)
			}
			if!strings.Contains(r.Body.String(), tc.wantSub) {
				t.Fatalf("Error Message Should Mention %q, Got %s", tc.wantSub, r.Body)
			}
		})
	}
	if r:= f.request("DELETE", "/api/notify/channels/99999999", ""); r.Code!= 404 {
		t.Fatalf("Deleting a non-existent channel should return 404, get %d", r.Code)
	}
}

func TestNotifyFilterBlocksBelowThreshold(t *testing.T) {
	f:= newNotifyFixture(t)
	hook:= newFakeWebhook(t)
	chID:= f.createChannel(t, map[string]any{
		"name": "Only severe",
		"kind": notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
		"filter": map[string]any{"min_severity": "critical"},
	})
	f.record(t, "Low-risk issue", "low")
	if _, _, err:= f.pg.FanOutPendingEvents(context.Background(), 500); err!= nil {
		t.Fatal(err)
	}
	var n int
	if err:= f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&n); err!= nil {
		t.Fatal(err)
	}
	if n!= 0 {
		t.Fatalf("Vulnerabilities below the threshold should not produce delivery, got %d Item", n)
	}
	f.n.stepRealtime(context.Background(), f.channel(t, chID), 50, "")
	if hook.count()!= 0 {
		t.Fatal("Filtered vulnerabilities should not send messages.")
	}
}

func TestNotifyDigestBatchesMultipleFindingsIntoOneMessage(t *testing.T) {
	f:= newNotifyFixture(t)
	hook:= newFakeWebhook(t)
	chID:= f.createChannel(t, map[string]any{
		"name": "Batch push",
		"kind": notify.KindDingTalk,
		"mode": db.NotifyModeDigest,
		"config": map[string]any{"webhook": hook.URL},
	})
	for i:= 0; i < 3; i++ {
		f.record(t, fmt.Sprintf("summarize vulnerabilities%d", i+1), "high")
	}
	ctx:= context.Background()
	if _, _, err:= f.pg.FanOutPendingEvents(ctx, 500); err!= nil {
		t.Fatal(err)
	}
	ch:= f.channel(t, chID)

	// Not expired: do not send.
	f.n.stepDigest(ctx, ch, 50, "")
	if hook.count()!= 0 {
		t.Fatal("Summarize batches that have not expired yet.")
	}

	// After aging batch: three combined into one message.
	f.agePendingBatch(t, chID)
	f.n.stepDigest(ctx, ch, 50, "")
	if got:= hook.count(); got!= 1 {
		t.Fatalf("Three should be summarized into one message, actually sent %d Item", got)
	}
	text:= markdownText(t, hook.last(t))
	if!strings.Contains(text, "near") ||!strings.Contains(text, "3 Vulnerabilities") {
		t.Fatalf("Missing number/time window text in summary message:\n%s", text)
	}
	for i:= 1; i <= 3; i++ {
		if!strings.Contains(text, fmt.Sprintf("summarize vulnerabilities%d", i)) {
			t.Fatalf("Missing message in summary. %d Item:\n%s", i, text)
		}
	}
	// All in the same batch should share batch_id.
	var distinct, total int
	if err:= f.pg.QueryRow(`SELECT count(DISTINCT batch_id), count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&distinct, &total); err!= nil {
		t.Fatal(err)
	}
	if total!= 3 || distinct!= 1 {
		t.Fatalf("The three deliveries should share one batch_id, resulting in distinct=%d total=%d", distinct, total)
	}
}

func TestNotifyDisabledChannelDoesNotSend(t *testing.T) {
	f:= newNotifyFixture(t)
	hook:= newFakeWebhook(t)
	chID:= f.createChannel(t, map[string]any{
		"name": "Disable channel",
		"kind": notify.KindDingTalk,
		"enabled": false,
		"config": map[string]any{"webhook": hook.URL},
	})
	f.record(t, "Vulnerabilities during downtime", "critical")
	if _, _, err:= f.pg.FanOutPendingEvents(context.Background(), 500); err!= nil {
		t.Fatal(err)
	}
	var n int
	if err:= f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&n); err!= nil {
		t.Fatal(err)
	}
	if n!= 0 {
		t.Fatalf("Channels that are disabled should not produce deliveries, resulting in %d Item", n)
	}
}

func TestNotifyStatusChangeDelivery(t *testing.T) {
	f:= newNotifyFixture(t)
	hook:= newFakeWebhook(t)
	chID:= f.createChannel(t, map[string]any{
		"name": "Status change subscription",
		"kind": notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
		"filter": map[string]any{"on_status_change": true},
	})
	finding:= f.record(t, "Status change use case.", "high")
	r:= f.request("PATCH", fmt.Sprintf("/api/exploration/findings/%d", finding), `{"status":"fixed"}`)
	if r.Code!= 200 {
		t.Fatalf("Change status failed %d: %s", r.Code, r.Body)
	}
	f.deliver(t, chID, "")

	// Vulnerability detailed report writing: automatically triggered when a vulnerability is found, fetch evidence and execution process to write a Markdown report and rewrite it back.
	// State change was actually created later but is order-independent; search fully.
	found:= false
	for i:= 0; i < hook.count(); i++ {
		text:= markdownText(t, hook.body(t, i))
		if strings.Contains(text, "Status Change") && strings.Contains(text, "Fixed") {
			found = true
		}
	}
	if!found {
		t.Fatalf("No status change received. → 'Fixed' messages (total %d Original", hook.count())
	}
}

func TestNotifyStatusChangeSuppressedByDefault(t *testing.T) {
	f:= newNotifyFixture(t)
	hook:= newFakeWebhook(t)
	chID:= f.createChannel(t, map[string]any{
		"name": "Do not subscribe to status changes.",
		"kind": notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	finding:= f.record(t, "Do not subscribe to changes", "high")
	if r:= f.request("PATCH", fmt.Sprintf("/api/exploration/findings/%d", finding), `{"status":"false_positive"}`); r.Code!= 200 {
		t.Fatalf("Change status failed %d: %s", r.Code, r.Body)
	}
	if _, _, err:= f.pg.FanOutPendingEvents(context.Background(), 500); err!= nil {
		t.Fatal(err)
	}
	var n int
	if err:= f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id
WHERE d.channel_id=$1 AND e.kind=$2`, chID, notify.EventFindingStatusChanged).Scan(&n); err!= nil {
		t.Fatal(err)
	}
	if n!= 0 {
		t.Fatalf("Channels that should not receive status change deliveries for unsubscription status changes, receiving %d Item", n)
	}
}

func TestNotifyTestMessageEndpoint(t *testing.T) {
	f:= newNotifyFixture(t)
	hook:= newFakeWebhook(t)
	chID:= f.createChannel(t, map[string]any{
		"name": "Test send",
		"kind": notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	if r:= f.request("POST", fmt.Sprintf("/api/notify/channels/%d/test", chID), ""); r.Code!= 200 {
		t.Fatalf("Test sending failed %d: %s", r.Code, r.Body)
	}
	if hook.count()!= 1 {
		t.Fatalf("The receiving end should receive 1 test message and get %d", hook.count())
	}
	// Test messages must be immediately recognizable as tests, not misinterpreted as real vulnerabilities.
	if text:= markdownText(t, hook.last(t));!strings.Contains(text, "Test") {
		t.Fatalf("Test messages should be marked as test: %s", text)
	}
	// When the configuration breaks, return the original error of the channel to the user truthfully.
	badID:= f.createChannel(t, map[string]any{
		"name": "Bad address",
		"kind": notify.KindDingTalk,
		"config": map[string]any{"webhook": "http://127.0.0.1:1/hook"},
	})
	if r:= f.request("POST", fmt.Sprintf("/api/notify/channels/%d/test", badID), ""); r.Code!= 502 {
		t.Fatalf("Delivery failure should return 502, received %d: %s", r.Code, r.Body)
	}
}

func TestNotifyDeliveriesHistoryAndRetry(t *testing.T) {
	f:= newNotifyFixture(t)
	// Pointing to an address that will inevitably fail, creating a failed delivery.
	chID:= f.createChannel(t, map[string]any{
		"name": "Failure retry",
		"kind": notify.KindDingTalk,
		"config": map[string]any{"webhook": "http://127.0.0.1:1/hook"},
	})
	f.record(t, "A failing push", "high")
	ctx:= context.Background()
	if _, _, err:= f.pg.FanOutPendingEvents(ctx, 500); err!= nil {
		t.Fatal(err)
	}
	ch:= f.channel(t, chID)
	// Retrying budget exhausted.
	for i:= 0; i < db.MaxNotifyAttempts; i++ {
		f.n.stepRealtime(ctx, ch, 50, "")
		if _, err:= f.pg.Exec(`UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE channel_id=$1`, chID); err!= nil {
			t.Fatal(err)
		}
	}
	var state string
	if err:= f.pg.QueryRow(`SELECT state FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&state); err!= nil {
		t.Fatal(err)
	}
	if state!= db.NotifyStateFailed {
		t.Fatalf("After retries are exhausted, it should be 'failed', received: %s", state)
	}

	r:= f.request("GET", fmt.Sprintf("/api/notify/deliveries?channel_id=%d&state=failed", chID), "")
	if r.Code!= 200 {
		t.Fatalf("Query History Failed %d: %s", r.Code, r.Body)
	}
	var hist struct {
		Deliveries []struct {
			ID int64 `json:"id"`
			State string `json:"state"`
			LastError string `json:"last_error"`
			Attempts int `json:"attempts"`
			Title string `json:"title"`
		} `json:"deliveries"`
		Total int `json:"total"`
	}
	if err:= json.Unmarshal(r.Body.Bytes(), &hist); err!= nil {
		t.Fatal(err)
	}
	if hist.Total!= 1 || len(hist.Deliveries)!= 1 {
		t.Fatalf("Expect to find 1 failed delivery and receive total=%d len=%d", hist.Total, len(hist.Deliveries))
	}
	if hist.Deliveries[0].LastError == "" {
		t.Fatal("Historical records should include failure reasons so users can troubleshoot.")
	}
	if hist.Deliveries[0].Attempts < db.MaxNotifyAttempts {
		t.Fatalf("The number of attempts should be recorded, got %d", hist.Deliveries[0].Attempts)
	}
	if hist.Deliveries[0].Title!= "A failing push" {
		t.Fatalf("History should include vulnerability titles, got %q", hist.Deliveries[0].Title)
	}

	// Manual resend: should go back to pending and reset the count.
	if r:= f.request("POST", fmt.Sprintf("/api/notify/deliveries/%d/retry", hist.Deliveries[0].ID), ""); r.Code!= 200 {
		t.Fatalf("Failed to resend %d: %s", r.Code, r.Body)
	}
	var attempts int
	if err:= f.pg.QueryRow(`SELECT state, attempts FROM notification_deliveries WHERE id=$1`, hist.Deliveries[0].ID).Scan(&state, &attempts); err!= nil {
		t.Fatal(err)
	}
	if state!= db.NotifyStatePending || attempts!= 0 {
		t.Fatalf("After resending, should be pending and attempts=0, Got %s/%d", state, attempts)
	}
}

func TestNotifyMetaAndSettingsRoundTrip(t *testing.T) {
	f:= newNotifyFixture(t)
	r:= f.request("GET", "/api/notify/meta", "")
	if r.Code!= 200 {
		t.Fatalf("meta Success: %s", r.Body)
	}
	var meta struct {
		Kinds []struct {
			Kind string `json:"kind"`
			SecretKeys []string `json:"secret_keys"`
		} `json:"kinds"`
	}
	if err:= json.Unmarshal(r.Body.Bytes(), &meta); err!= nil {
		t.Fatal(err)
	}
	if len(meta.Kinds)!= len(notify.Kinds()) {
		t.Fatalf("meta Should list all %d Channels received %d", len(notify.Kinds()), len(meta.Kinds))
	}
	for _, k:= range meta.Kinds {
		if len(k.SecretKeys) == 0 {
			t.Errorf("Channel %s Credential field not reported", k.Kind)
		}
	}

	// Three global settings round trip. Trailing slashes should be normalized, otherwise the backlink will concatenate "//function/...".
	if r:= f.request("PUT", "/api/settings", `{"notify_public_base_url":"https://artex.example.com/","notify_digest_interval_min":15,"notify_enabled":true}`); r.Code!= 200 {
		t.Fatalf("Write settings failed %d: %s", r.Code, r.Body)
	}
	t.Cleanup(func() {
		f.pg.Exec(`DELETE FROM settings WHERE key IN ($1,$2)`, settingNotifyPublicBaseURL, settingNotifyDigestMinutes)
	})
	payload:= f.s.settingsPayload()
	if payload["notify_public_base_url"]!= "https://artex.example.com" {
		t.Fatalf("Backlink address not normalized: %v", payload["notify_public_base_url"])
	}
	if payload["notify_digest_interval_min"]!= 15 {
		t.Fatalf("Summary cycle not effective.: %v", payload["notify_digest_interval_min"])
	}

	// Illegal values should be rejected.
	for _, body:= range []string{
		`{"notify_public_base_url":"ftp://x"}`,
		`{"notify_digest_interval_min":0}`,
		`{"notify_digest_interval_min":99999}`,
	} {
		if r:= f.request("PUT", "/api/settings", body); r.Code!= 400 {
			t.Errorf("%s should return 400, got %d", body, r.Code)
		}
	}
}

// TestNotifyDeepLinkUsesPublicBaseURL Override link concatenation: when public_base_url is configured
// A single message must use an ActionCard with buttons, and the link should point to the vulnerability details page.
func TestNotifyDeepLinkUsesPublicBaseURL(t *testing.T) {
	f:= newNotifyFixture(t)
	hook:= newFakeWebhook(t)
	chID:= f.createChannel(t, map[string]any{
		"name": "Backlink",
		"kind": notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	finding:= f.record(t, "Vulnerability with return chain", "high")
	f.deliver(t, chID, "https://artex.example.com")

	body:= hook.last(t)
	card, _:= body["actionCard"].(map[string]any)
	if card == nil {
		t.Fatalf("When there is a backlink, apply ActionCard to get msgtype=%v", body["msgtype"])
	}
	want:= fmt.Sprintf("https://artex.example.com/function/findings/detail?id=%d", finding)
	if card["singleURL"]!= want {
		t.Fatalf("Incorrect Backlink,\nExpectation %s\nGot %v", want, card["singleURL"])
	}
}

// TestNotifyNoDeepLinkWithoutBaseURL Reverse override: shouldn't produce bad links when no external address is configured
// (For example, pointing to localhost or relative paths), should return plain markdown.
func TestNotifyNoDeepLinkWithoutBaseURL(t *testing.T) {
	f:= newNotifyFixture(t)
	hook:= newFakeWebhook(t)
	chID:= f.createChannel(t, map[string]any{
		"name": "No backlink",
		"kind": notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	f.record(t, "Vulnerability without a backlink", "high")
	f.deliver(t, chID, "")

	body:= hook.last(t)
	if body["msgtype"]!= "markdown" {
		t.Fatalf("When no external address is configured, send markdown and get %v", body["msgtype"])
	}
	if text:= markdownText(t, body); strings.Contains(text, "view details") {
		t.Fatalf("Should not appear detail links when no external address is configured:\n%s", text)
	}
}

// TestNotifyDigestSegmentsAndDefersRemainder End-to-end evidence for 'silent loss' repair.
//
// Summary messages are subject to channel length limits (WeChat 4096 bytes); when not fitting in one batch, must**By line**Split:
// Those marks that were put into this one have been delivered, the rest return to the queue for the next one. The previous implementation was to batch marks
// High-risk——Those that were truncated and neither in the message nor in the failed list, delivery history still shows success,
// Vulnerability gone.
//
// Assert four things: ① Only marks the actual installed count ② Others are still pending ③ Delayed items
// **No retry attempts consumed** ④ Running another round will send out the remaining ones (won't deadlock).
func TestNotifyDigestSegmentsAndDefersRemainder(t *testing.T) {
	f:= newNotifyFixture(t)
	hook:= newFakeWebhook(t)
	// Use WeChat Work: markdown has a limit of 4096 bytes, which is the tightest among the six channels.
	chID:= f.createChannel(t, map[string]any{
		"name": "segment summary",
		"kind": notify.KindWeCom,
		"mode": db.NotifyModeDigest,
		"config": map[string]any{"webhook": hook.URL},
	})
	const total = 60
	// Title longer to ensure 60 items far exceed 4096 bytes, will definitely split.
	longName:= strings.Repeat("Too long vulnerability name", 6)
	for i:= 0; i < total; i++ {
		f.record(t, longName+strconv.Itoa(i+1), "high")
	}
	ctx:= context.Background()
	if _, _, err:= f.pg.FanOutPendingEvents(ctx, 500); err!= nil {
		t.Fatal(err)
	}
	f.agePendingBatch(t, chID)
	ch:= f.channel(t, chID)

	f.n.stepDigest(ctx, ch, 50, "")
	if hook.count()!= 1 {
		t.Fatalf("Should only send one message, resulting in %d", hook.count())
	}

	var sent, pending int
	if err:= f.pg.QueryRow(`SELECT
    count(*) FILTER (WHERE state=$2),
    count(*) FILTER (WHERE state=$3)
  FROM notification_deliveries WHERE channel_id=$1`, chID, db.NotifyStateSent, db.NotifyStatePending).
		Scan(&sent, &pending); err!= nil {
		t.Fatal(err)
	}
	if sent == 0 {
		t.Fatal("Entries should be marked as delivered.")
	}
	if pending == 0 {
		t.Fatalf("Batch %d It's impossible to fit everything into 4096 bytes, so there should be remaining data to send; sent=%d", total, sent)
	}
	if sent+pending!= total {
		t.Fatalf("Entry count mismatch: sent=%d pending=%d total=%d (Neither delivered nor pending=Missing)", sent, pending, total)
	}
	// The message body must truthfully inform how many are not included in this one.
	if text:= markdownText(t, hook.last(t));!strings.Contains(text, "The rest") {
		t.Fatalf("The message should indicate that there are more items not included in this one.:\n%.400s", text)
	}

	// Delayed items cannot consume retry budget; attempts are optimistic when claimed. +1, Subtract when delaying.
	var maxAttempts int
	if err:= f.pg.QueryRow(`SELECT COALESCE(max(attempts),0) FROM notification_deliveries
WHERE channel_id=$1 AND state=$2`, chID, db.NotifyStatePending).Scan(&maxAttempts); err!= nil {
		t.Fatal(err)
	}
	if maxAttempts > 0 {
		t.Fatalf("Delayed items should not consume retry attempts (otherwise several after would fail), get attempts=%d", maxAttempts)
	}

	// Run repeatedly until convergence. Asserts that**Delivered to all finally**And indeed divided into multiple rounds——
	// This is stronger than 'second round finished': it proves that segmentation will not deadlock or drop remaining items.
	rounds:= 0
	for {
		var undelivered int
		if err:= f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries
WHERE channel_id=$1 AND state <> $2 AND state <> $3`, chID, db.NotifyStateSent, db.NotifyStateFailed).
			Scan(&undelivered); err!= nil {
			t.Fatal(err)
		}
		if undelivered == 0 {
			break
		}
		rounds++
		if rounds > total+5 {
			t.Fatalf("segmented delivery did not converge %d Rotation still has %d Items pending resolution", rounds, undelivered)
		}
		before:= hook.count()
		f.n.stepDigest(ctx, ch, 50, "")
		if hook.count() == before {
			t.Fatalf("Intercept %d no progress in the round, remaining %d Will permanently hang", rounds, undelivered)
		}
	}
	if rounds < 2 {
		t.Fatalf("A 4096-byte message cannot be contained. %d Long title vulnerability, should be issued in multiple rounds. Only used %d Loop", total, rounds)
	}
	// Each subsequent round after the first should be**Continuously send**, There are no entries rejected by the channel.
	var failed int
	if err:= f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1 AND state=$2`,
		chID, db.NotifyStateFailed).Scan(&failed); err!= nil {
		t.Fatal(err)
	}
	if failed!= 0 {
		t.Fatalf("Assuming the receiver always returns success, there should be no failure items, resulting in %d", failed)
	}
}

// TestNotifyBackoffTableMatchesAttemptBudget It is a drift assertion.
//
// Retry budget (db.MaxNotifyAttempts)Separate packages for backoff sequence (notifyBackoff) and SeedPromptIfEmpty:
// the former is the state machine's strategy, while the latter is the engine's execution tick. If only one of them is changed,——For example, increase the budget to 5.
// Forgot to add backoff level——The code will not throw an error; instead, it will reuse the last interval for retries 4 and 5.
// Appears as 'retry rhythm inexplicably slowed down', hard to link during investigation.
// Assert equal lengths of both, exposing this drift in CI.
func TestNotifyBackoffTableMatchesAttemptBudget(t *testing.T) {
	if len(notifyBackoff)!= db.MaxNotifyAttempts {
		t.Fatalf("Backoff Level(%d)With maximum attempt count.(%d)Inconsistent——Changing one requires changing the other",
			len(notifyBackoff), db.MaxNotifyAttempts)
	}
	// Backoff interval must be monotonically non-decreasing, otherwise retries will become more frequent and actually exacerbate rate limiting.
	for i:= 1; i < len(notifyBackoff); i++ {
		if notifyBackoff[i] < notifyBackoff[i-1] {
			t.Fatalf("Backoff interval must be monotonically non-decreasing: The %d record %v < Intercept %d record %v",
				i, notifyBackoff[i], i-1, notifyBackoff[i-1])
		}
	}
}

// TestNotifyRateLimitDoesNotConsumeRetryBudget Lock the order of 'first get token then claim'.
// If it goes the wrong way (take and then discard), the delivery that was blocked by rate limiting has already counted one attempt,
// Budget will be completely exhausted by pure waiting, ending up in failed.
func TestNotifyRateLimitDoesNotConsumeRetryBudget(t *testing.T) {
	// Only test token bucket itself, no need for Server (also shouldn't create one).
	n:= &Notifier{buckets: map[int64]*notifyBucket{}}
	now:= time.Now()
	// One per minute: When full, up to one per minute.
	if got:= n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now); got!= 1 {
		t.Fatalf("When full, take 1 token per minute, getting %d", got)
	}
	if got:= n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Millisecond)); got!= 0 {
		t.Fatalf("Return 0 immediately after token exhaustion, obtaining %d", got)
	}
	if got:= n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(30*time.Second)); got!= 0 {
		t.Fatalf("Halfway should not fill a token, got %d", got)
	}
	if got:= n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Minute)); got!= 1 {
		t.Fatalf("One token should be replenished after one cycle, resulting in %d", got)
	}
	// Unlimited channels follow a finite limit to avoid being infinitely backed up and dragged down.
	if got:= n.takeTokens(2, 0, notifyUnlimitedBurstPerTick+10, now); got!= notifyUnlimitedBurstPerTick {
		t.Fatalf("Unlimited should return the limit per round. %d, Got %d", notifyUnlimitedBurstPerTick, got)
	}
	// Token buckets between channels are independent.
	if got:= n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Millisecond)); got!= 0 {
		t.Fatalf("Bucket for channel 1 should still be empty, resulting in %d", got)
	}
}

// TestNotifyTakeTokensKeepsUnusedTokens Lock the semantics of 'only take want'.
//
// The previous implementation emptied the bucket before truncating by the caller, thus rate=100/min Channel bucket full,
// Only using 5, discarding the remaining 95 tokens directly; similarly, this channel will be deducted even if there are no pending deliveries.
// The result is that the comment claims 'can flush rate_per_min items at once when backlogged' cannot be done under any circumstances.
func TestNotifyTakeTokensKeepsUnusedTokens(t *testing.T) {
	n:= &Notifier{buckets: map[int64]*notifyBucket{}}
	now:= time.Now()
	// The bucket starts full (100), and only needs 5 this round.
	if got:= n.takeTokens(1, 100, 5, now); got!= 5 {
		t.Fatalf("want=5 Should exactly take 5 tokens, resulting in: %d", got)
	}
	// 'Critical assertion: The remaining 95 must still be in the bucket, not emptied and discarded.',
	// Do not advance time, ensuring only existing items are retrieved rather than replenishments.
	if got:= n.takeTokens(1, 100, 95, now); got!= 95 {
		t.Fatalf("The remaining tokens should still be usable (expected 95), got %d——Bucket emptied in a full round.", got)
	}
	if got:= n.takeTokens(1, 100, 1, now); got!= 0 {
		t.Fatalf("Bucket is exhausted, should return 0, received %d", got)
	}
	// want<=0 No tokens deducted (no charge for empty rounds).
	n2:= &Notifier{buckets: map[int64]*notifyBucket{}}
	if got:= n2.takeTokens(1, 20, 0, now); got!= 0 {
		t.Fatalf("want=0 should return 0, got %d", got)
	}
	if got:= n2.takeTokens(1, 20, 20, now); got!= 20 {
		t.Fatalf("want=0 That should not consume a token, so it should still be able to get 20 and receive %d", got)
	}
}

// TestDigestTickPlanDecouplesBatchSizeFromSendBudget Pin the two dimensions of the summary mode.
//
// Once the batch size is tied to each round's request budget, rate_per_min=20 channels can only be in each
// 3 Refill 1 token every second tick, so each summary message only contains 1 vulnerability.——Functionally equivalent to none
// Summarized, while the message header still reads 'New vulnerability added in the last 30 minutes.' This degradation does not cause an error.
// Existing end-to-end test cases also cannot show this (they manually pass a large enough limit to stepDigest,
// Skipped step's quota calculation), so asserting the decision itself directly here.
func TestDigestTickPlanDecouplesBatchSizeFromSendBudget(t *testing.T) {
	tokens, claimLimit:= digestTickPlan()
	// Batch = One message = One request = A token. The unit of the token is a message, not a vulnerability.
	if tokens!= 1 {
		t.Fatalf("Aggregate a batch that sends only one message, should exactly consume 1 token and get %d", tokens)
	}
	if claimLimit!= db.MaxDigestBatchSize {
		t.Fatalf("Batch size summary should be memory upper bound db.MaxDigestBatchSize=%d, Got %d",
			db.MaxDigestBatchSize, claimLimit)
	}
	// Key relationship: Batch size must be far greater than the request budget per round. Once they are of the same magnitude,
	// It's confusing to mix 'sending several messages' with 'batching several vulnerabilities into one number'.
	if claimLimit <= notifyMaxSendsPerChannelPerTick {
		t.Fatalf("Batch size for aggregation %d Should not be affected by round request budget %d Lateral Movement——"+
			"The request budget is derived from the lease in reverse; it's a different dimension from 'how many requests to send' versus 'how many vulnerabilities to package'.",
			claimLimit, notifyMaxSendsPerChannelPerTick)
	}
}

// TestNotifyTickBudgetFitsWithinLease Another drift prevention assertion.
//
// The upper limit of delivery items per channel per round (notifyMaxSendsPerChannelPerTick) is derived from the lease duration:
// The worst time for serial submission in one round must be < If there is no lease, otherwise the subsequent leases will expire before they are finished.
// In a multi-instance deployment, the other end will re-take them and resend. These three constants are in different positions,
// Changing any one may break relationships without any error message.——So it's stuck here.
func TestNotifyTickBudgetFitsWithinLease(t *testing.T) {
	worst:= time.Duration(notifyMaxSendsPerChannelPerTick) * notifySendTimeout
	if worst >= notifyLease {
		t.Fatalf("Worst-case time for one channel per round %v Should not reach or exceed the lease. %v"+
			" (notifyMaxSendsPerChannelPerTick=%d × notifySendTimeout=%v)——"+
			"Any of these three constants require synchronous checking of the other two.",
			worst, notifyLease, notifyMaxSendsPerChannelPerTick, notifySendTimeout)
	}
}
