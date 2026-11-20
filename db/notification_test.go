package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Zeerg/artex/notify"
)

// All use cases in this file will truly connect to PostgreSQL (skip if no database). These SQL statements use
// FOR UPDATE SKIP LOCKED, make_interval, JSONB, Multi-line IN(...) Placeholder concatenation,
// All are written as 'compiled but may fail at runtime'; must run to verify.

func notifyTestDB(t *testing.T) *DB {
	t.Helper()
	d, err:= Open(testDSN(t))
	if err!= nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// newTestChannel Create a channel that automatically deletes upon test completion.
func newTestChannel(t *testing.T, d *DB, kind, mode string, filter string) *NotificationChannel {
	t.Helper()
	if filter == "" {
		filter = `{}`
	}
	ch:= &NotificationChannel{
		Name: "Test channel-" + t.Name(),
		Kind: kind,
		Mode: mode,
		Config: json.RawMessage(`{"webhook":"https://example.com/hook"}`),
		Filter: json.RawMessage(filter),
		RatePerMin: 100,
	}
	id, err:= d.SaveNotificationChannel(context.Background(), ch)
	if err!= nil {
		t.Fatalf("Failed to create channel: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })
	ch.ID = id
	return ch
}

// addTestEvent Write an event directly (without finding) for testing dispatch and delivery.
func addTestEvent(t *testing.T, d *DB, kind string, findingID int64, snap notify.Snapshot) int64 {
	t.Helper()
	snap.Kind = kind
	snap.FindingID = findingID
	id, err:= d.AddNotificationEvent(context.Background(), kind, findingID, snap)
	if err!= nil {
		t.Fatalf("Event write failed: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE id=$1`, id) })
	return id
}

func TestNotificationAssetNamesResolvesAndPreservesOrder(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()

	// Each category of assets has its own display format: domain, IP, URL.
	insertAsset:= func(query, value string) int64 {
		t.Helper()
		var id int64
		if err:= d.QueryRow(query, value).Scan(&id); err!= nil {
			t.Fatal(err)
		}
		return id
	}
	domID:= insertAsset(`INSERT INTO assets(type, domain) VALUES('subdomain',$1) RETURNING id`, "a.example.com")
	ipID:= insertAsset(`INSERT INTO assets(type, ip) VALUES('ip',$1) RETURNING id`, "10.1.2.3")
	svcID:= insertAsset(`INSERT INTO assets(type, url) VALUES('service',$1) RETURNING id`, "https://a.example.com/admin")
	t.Cleanup(func() {
		d.Exec(`DELETE FROM assets WHERE id IN ($1,$2,$3)`, domID, ipID, svcID)
	})

	// Intentionally pass parameters out of order, including a non-existent id.
	got, err:= d.NotificationAssetNames(ctx, []int64{svcID, 999999999, domID, ipID, svcID})
	if err!= nil {
		t.Fatalf("Failed to Parse Asset Name: %v", err)
	}
	want:= []string{"https://a.example.com/admin", "a.example.com", "10.1.2.3"}
	if len(got)!= len(want) {
		t.Fatalf("The number of asset names does not match, expecting %v Got %v", want, got)
	}
	for i:= range want {
		if got[i]!= want[i] {
			t.Fatalf("Order/value mismatch, expected %v Got %v", want, got)
		}
	}
}

// TestRecordNotificationEventTxUnwindsOnFailure This is the core use case of the savepoint mechanism:
// In a transaction, first make the write to notification_events fail (temporarily add a constant false constraint).
// Retry ① This function returns false ② Transaction did not enter aborted state, subsequent statements can still execute.
//
// If there are no save points, PostgreSQL will invalidate the entire transaction, subsequent any statement will be with
// "current transaction is aborted" Success——That's why 'a problem with the notification table caused
// Faulty path for 'vulnerability cannot be stored in the database'.
//
// Deliberately using here **ROLLBACK End instead of COMMIT.**: ALTER TABLE In PG, it is transactional,
// Once submitted, that temporary constraint will permanently remain in the schema, affecting all subsequent test cases together.
// Rollback can automatically undo DDL, no need for manual cleanup. Assertions only require 'transaction is still alive'.
// No real submission needed.
func TestRecordNotificationEventTxUnwindsOnFailure(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()

	// Defensive cleanup: If historical runs left this constraint behind, remove it first.
	if _, err:= d.Exec(`ALTER TABLE notification_events DROP CONSTRAINT IF EXISTS notify_test_never`); err!= nil {
		t.Fatal(err)
	}

	tx, err:= d.BeginTx(ctx, nil)
	if err!= nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // Revert temporary constraints, see function comments

	// NOT VALID: Only constrain the rows written subsequently; do not validate historical events in the database.
	// (Otherwise, existing rows would violate constraints that cannot be added.
	if _, err:= tx.ExecContext(ctx, `ALTER TABLE notification_events ADD CONSTRAINT notify_test_never CHECK (false) NOT VALID`); err!= nil {
		t.Fatalf("Failed to add temporary constraint: %v", err)
	}
	if RecordNotificationEventTx(ctx, tx, notify.EventFindingCreated, 1, notify.Snapshot{Severity: "high"}) {
		t.Fatal("Reporting a successful write even though it's bound to fail.")
	}
	// Critical assertion: Transactions can still be used.
	var one int
	if err:= tx.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err!= nil {
		t.Fatalf("The transaction has been polluted (savepoint not effective).: %v", err)
	}
	if err:= tx.Rollback(); err!= nil {
		t.Fatalf("Rollback failed: %v", err)
	}
	// Confirm that the DDL has been rolled back and does not leave any traps for subsequent test cases.
	var exists bool
	if err:= d.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='notify_test_never')`).Scan(&exists); err!= nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("Temporary constraints not rolled back will contaminate subsequent test cases")
	}
}

func TestFanOutRoutesEventsByFilter(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()

	all:= newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	onlyCritical:= newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"min_severity":"critical"}`)
	sqlOnly:= newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"vulnclass_include":["SQL"]}`)

	highSQL:= addTestEvent(t, d, notify.EventFindingCreated, 1001, notify.Snapshot{Severity: "high", VulnClass: "SQLCompleted"})
	lowXSS:= addTestEvent(t, d, notify.EventFindingCreated, 1002, notify.Snapshot{Severity: "low", VulnClass: "XSS"})
	criticalXSS:= addTestEvent(t, d, notify.EventFindingCreated, 1003, notify.Snapshot{Severity: "critical", VulnClass: "XSS"})

	if _, _, err:= d.FanOutPendingEvents(ctx, 100); err!= nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	cases:= []struct {
		name string
		eventID int64
		channel int64
		want bool
	}{
		{"All channels receive high", highSQL, all.ID, true},
		{"All channels receive low", lowXSS, all.ID, true},
		{"Only skip high severity channels", highSQL, onlyCritical.ID, false},
		{"Only critical findings are received on severe channels without CHECK.", criticalXSS, onlyCritical.ID, true},
		{"Only SQL channel receives SQL", highSQL, sqlOnly.ID, true},
		{"Only skip XSS for SQL channels.", lowXSS, sqlOnly.ID, false},
	}
	for _, tc:= range cases {
		t.Run(tc.name, func(t *testing.T) {
			var exists bool
			if err:= d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notification_deliveries WHERE event_id=$1 AND channel_id=$2)`,
				tc.eventID, tc.channel).Scan(&exists); err!= nil {
				t.Fatal(err)
			}
			if exists!= tc.want {
				t.Fatalf("Submission exists: Expectation %v Got %v", tc.want, exists)
			}
		})
	}

	// Reassign once more should not produce duplicate deliveries (fanned_out idempotent).
	events, deliveries, err:= d.FanOutPendingEvents(ctx, 100)
	if err!= nil {
		t.Fatal(err)
	}
	if events!= 0 || deliveries!= 0 {
		t.Fatalf("Already dispatched events should not be processed again, received events=%d deliveries=%d", events, deliveries)
	}
}

// TestFanOutMarksEventsWithNoMatchingChannel Cover the case where no channel is hit by an event.
// Such events must still be marked as dispatched otherwise they will forever remain in the dispatch pool and be rescan every tick.
func TestFanOutMarksEventsWithNoMatchingChannel(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()
	pick:= newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"vulnclass_include":["Mismatched types"]}`)
	_ = pick

	ev:= addTestEvent(t, d, notify.EventFindingCreated, 2001, notify.Snapshot{Severity: "high", VulnClass: "XSS"})
	_, deliveries, err:= d.FanOutPendingEvents(ctx, 100)
	if err!= nil {
		t.Fatal(err)
	}
	if deliveries!= 0 {
		t.Fatalf("Should not produce delivery, get %d", deliveries)
	}
	var fanned bool
	if err:= d.QueryRowContext(ctx, `SELECT fanned_out FROM notification_events WHERE id=$1`, ev).Scan(&fanned); err!= nil {
		t.Fatal(err)
	}
	if!fanned {
		t.Fatal("Events that do not hit the channel must also be marked as dispatched, otherwise they will be infinitely rescan.")
	}
}

func TestClaimRealtimeDeliveriesHonorsLeaseAndMode(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()

	realtime:= newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	digest:= newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)

	addTestEvent(t, d, notify.EventFindingCreated, 3001, notify.Snapshot{Severity: "high", VulnClass: "XSS"})
	if _, _, err:= d.FanOutPendingEvents(ctx, 100); err!= nil {
		t.Fatal(err)
	}

	// Real-time claims should only receive the one from the realtime channel; do not touch the digest channel.
	got, err:= d.ClaimRealtimeDeliveries(ctx, realtime.ID, 10, time.Minute)
	if err!= nil {
		t.Fatalf("Failed to claim: %v", err)
	}
	if len(got)!= 1 {
		t.Fatalf("Should get 1, got %d", len(got))
	}
	if got[0].State!= NotifyStateSending || got[0].Attempts!= 1 {
		t.Fatalf("After receiving, should be sending and attempts=1, Get state=%s attempts=%d", got[0].State, got[0].Attempts)
	}
	// The associated loaded rendering context must be complete (channel configuration + event snapshot + finding id).
	if got[0].Channel == nil || len(got[0].Channel.Config) == 0 {
		t.Fatal("The result received lacks channel configuration, rendering it unsuccessful.")
	}
	if got[0].FindingID!= 3001 {
		t.Fatalf("finding id Not carried out from events, got %d", got[0].FindingID)
	}

	// If the lease has not expired, the second claim should be empty.——This is 'the same line will not be delivered simultaneously by two dispatchers'
	// The guarantee.
	again, err:= d.ClaimRealtimeDeliveries(ctx, realtime.ID, 10, time.Minute)
	if err!= nil {
		t.Fatal(err)
	}
	if len(again)!= 0 {
		t.Fatalf("No duplicate claims within the lease period, received %d Item", len(again))
	}

	// digest Delivery of the channel should not be picked up in real-time.
	left, err:= d.ClaimRealtimeDeliveries(ctx, digest.ID, 10, time.Minute)
	if err!= nil {
		t.Fatal(err)
	}
	if len(left)!= 0 {
		t.Fatalf("Real-time claims should not receive digest channel deliveries, received %d Item", len(left))
	}
}

// TestClaimExpiredLeaseRecovers Crash recovery coverage: Process crashes during delivery leave sending
// Row, must be able to be claimed again after lease expiration, otherwise this delivery will forever be stuck.
func TestClaimExpiredLeaseRecovers(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()
	ch:= newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 4001, notify.Snapshot{Severity: "high"})
	if _, _, err:= d.FanOutPendingEvents(ctx, 100); err!= nil {
		t.Fatal(err)
	}
	first, err:= d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err!= nil || len(first)!= 1 {
		t.Fatalf("First claim failure: %v (%d Item)", err, len(first))
	}
	// Manually push lease to the past to simulate 'lease expired'.
	if _, err:= d.Exec(`UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE id=$1`, first[0].ID); err!= nil {
		t.Fatal(err)
	}
	second, err:= d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err!= nil {
		t.Fatal(err)
	}
	if len(second)!= 1 {
		t.Fatalf("Sending lines with expired lease should be re-claimable, get %d Item", len(second))
	}
	if second[0].Attempts!= 2 {
		t.Fatalf("Reclaiming should accumulate attempt counts, resulting in: %d", second[0].Attempts)
	}
}

func TestClaimSkipsDisabledChannel(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()
	ch:= newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 5001, notify.Snapshot{Severity: "high"})
	if _, _, err:= d.FanOutPendingEvents(ctx, 100); err!= nil {
		t.Fatal(err)
	}
	// Disabling marks existing pending deliveries as 'skipped'.
	if err:= d.SetNotificationChannelEnabled(ctx, ch.ID, false); err!= nil {
		t.Fatal(err)
	}
	var state string
	if err:= d.QueryRow(`SELECT state FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&state); err!= nil {
		t.Fatal(err)
	}
	if state!= NotifyStateSkipped {
		t.Fatalf("Mark existing queued deliveries for disabled channels as skipped, receive %s", state)
	}
	got, err:= d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err!= nil {
		t.Fatal(err)
	}
	if len(got)!= 0 {
		t.Fatalf("Inactive channels should not be able to be claimed, resulting in %d Item", len(got))
	}
}

func TestDigestBatchDueAndStableBatchID(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()
	ch:= newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)
	for i:= 0; i < 3; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(6000+i), notify.Snapshot{Severity: "high"})
	}
	if _, _, err:= d.FanOutPendingEvents(ctx, 100); err!= nil {
		t.Fatal(err)
	}

	// A new batch with age of 0 should not expire in a 30-minute cycle.
	due, err:= d.DigestBatchDue(ctx, ch.ID, 30*time.Minute)
	if err!= nil {
		t.Fatalf("Batch expiration check failure: %v", err)
	}
	if due {
		t.Fatal("Newly created batches should not expire immediately.")
	}

	// Push the creation times of three deliveries together to an old age, simulating a batch that has accumulated for a cycle.
	if _, err:= d.Exec(`UPDATE notification_deliveries SET created_at = now() - interval '40 minutes' WHERE channel_id=$1`, ch.ID); err!= nil {
		t.Fatal(err)
	}
	due, err = d.DigestBatchDue(ctx, ch.ID, 30*time.Minute)
	if err!= nil {
		t.Fatal(err)
	}
	if!due {
		t.Fatal("Batches exceeding the cycle should be deemed expired.")
	}

	batch, err:= d.ClaimDigestBatch(ctx, ch.ID, MaxDigestBatchSize, time.Minute)
	if err!= nil {
		t.Fatalf("Failed to receive summary batch: %v", err)
	}
	if len(batch)!= 3 {
		t.Fatalf("The summary should take all 3 at once and return %d Item", len(batch))
	}
	if batch[0].BatchID == nil {
		t.Fatal("The batch summary must write batch_id, otherwise it's unclear in history these were sent together")
	}
	firstBatchID:= *batch[0].BatchID
	for _, dl:= range batch {
		if dl.BatchID == nil || *dl.BatchID!= firstBatchID {
			t.Fatalf("All instances in the same batch should share the batch_id, received %v vs %d", dl.BatchID, firstBatchID)
		}
	}

	// Let these**Overall**Reclaim after failure reordering; batch_id must remain unchanged (COALESCE's effect):
	// Otherwise, one retry would erase the fact that 'this batch was sent together'.
	//
	// Must rearrange the batch as a whole, not just one line.——This is how the summary message is handled when the submission engine summarizes.
	// (A message represents a batch, success or failure together). Only rearranging one leaves the rest within the lease period,
	// Reclaiming only gets that one.
	allIDs:= make([]int64, 0, len(batch))
	for _, dl:= range batch {
		allIDs = append(allIDs, dl.ID)
	}
	if err:= d.RescheduleDeliveries(ctx, allIDs, time.Second, "Simulation Failure"); err!= nil {
		t.Fatal(err)
	}
	// Push the lease back in time to simulate the cooldown period has ended.
	if _, err:= d.Exec(`UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE channel_id=$1`, ch.ID); err!= nil {
		t.Fatal(err)
	}
	reclaimed, err:= d.ClaimDigestBatch(ctx, ch.ID, MaxDigestBatchSize, time.Minute)
	if err!= nil {
		t.Fatal(err)
	}
	if len(reclaimed)!= 3 {
		t.Fatalf("Reclaiming should get all three, resulting in %d", len(reclaimed))
	}
	if reclaimed[0].BatchID == nil || *reclaimed[0].BatchID!= firstBatchID {
		t.Fatalf("After retrying, batch_id should retain its original value %d, Got %v", firstBatchID, reclaimed[0].BatchID)
	}
}

func TestDeliveryStateTransitions(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()
	ch:= newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 7001, notify.Snapshot{Severity: "high"})
	if _, _, err:= d.FanOutPendingEvents(ctx, 100); err!= nil {
		t.Fatal(err)
	}
	got, err:= d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err!= nil || len(got)!= 1 {
		t.Fatalf("Failed to claim: %v (%d)", err, len(got))
	}
	id:= got[0].ID

	if err:= d.RescheduleDeliveries(ctx, []int64{id}, time.Second, "Network jitter"); err!= nil {
		t.Fatal(err)
	}
	var state, lastErr string
	if err:= d.QueryRow(`SELECT state, last_error FROM notification_deliveries WHERE id=$1`, id).Scan(&state, &lastErr); err!= nil {
		t.Fatal(err)
	}
	if state!= NotifyStatePending || lastErr!= "Network jitter" {
		t.Fatalf("After reordering should be pending and record the reason, receive state=%s err=%q", state, lastErr)
	}

	if err:= d.FailDeliveries(ctx, []int64{id}, "retries exhausted"); err!= nil {
		t.Fatal(err)
	}
	if err:= d.QueryRow(`SELECT state FROM notification_deliveries WHERE id=$1`, id).Scan(&state); err!= nil {
		t.Fatal(err)
	}
	if state!= NotifyStateFailed {
		t.Fatalf("Should be failed, got %s", state)
	}

	// Manual resending resets retry count and immediately expires old budget. Otherwise, it inherits the old failure budget.
	if err:= d.RetryNotificationDelivery(ctx, id); err!= nil {
		t.Fatalf("Failed to resend: %v", err)
	}
	var attempts int
	var next time.Time
	if err:= d.QueryRow(`SELECT state, attempts, next_attempt_at FROM notification_deliveries WHERE id=$1`, id).Scan(&state, &attempts, &next); err!= nil {
		t.Fatal(err)
	}
	if state!= NotifyStatePending || attempts!= 0 {
		t.Fatalf("After resending, should be pending and attempts=0, Get state=%s attempts=%d", state, attempts)
	}
	if next.After(time.Now().Add(time.Second)) {
		t.Fatal("Redraw should be immediately available")
	}

	// Delivered deliveries should not be resent.
	if err:= d.MarkDeliveriesSent(ctx, []int64{id}); err!= nil {
		t.Fatal(err)
	}
	if err:= d.RetryNotificationDelivery(ctx, id); err == nil {
		t.Fatal("Delivered deliveries should not allow resending")
	}
}

func TestListNotificationDeliveriesPagingAndFilter(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()
	ch:= newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	for i:= 0; i < 5; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(8000+i), notify.Snapshot{Severity: "high", Name: "Pagination Test"})
	}
	if _, _, err:= d.FanOutPendingEvents(ctx, 100); err!= nil {
		t.Fatal(err)
	}
	if _, err:= d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute); err!= nil {
		t.Fatal(err)
	}

	page1, total, err:= d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStateSending}, 1, 2)
	if err!= nil {
		t.Fatalf("Query failed: %v", err)
	}
	if total!= 5 {
		t.Fatalf("Total should be 5, got. %d", total)
	}
	if len(page1)!= 2 {
		t.Fatalf("Two per page, get %d", len(page1))
	}
	// New entries first: The ID of the first entry on the first page should be greater than that of the first entry on the second page.
	page2, _, err:= d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStateSending}, 2, 2)
	if err!= nil {
		t.Fatal(err)
	}
	if len(page2)!= 2 || page2[0].ID >= page1[0].ID {
		t.Fatalf("The pagination order should be new items first, resulting in page1.[0]=%d page2[0]=%d", page1[0].ID, page2[0].ID)
	}
	// The rendering context must be returned with history for the list to show 'what was pushed'.
	if page1[0].ChannelName == "" || page1[0].FindingID == 0 {
		t.Fatalf("History items missing display fields: %+v", page1[0])
	}

	// Filter by status: No pending items.
	pending, totalPending, err:= d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStatePending}, 1, 50)
	if err!= nil {
		t.Fatal(err)
	}
	if totalPending!= 0 || len(pending)!= 0 {
		t.Fatalf("There should not be any pending deliveries, received %d Item (total=%d)", len(pending), totalPending)
	}
}

func TestSetFindingStatusWithNotifyOnlyEmitsOnRealChange(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()

	tk, err:= d.CreateTask("Notification status change test", "Target", nil, 0, 0)
	if err!= nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	es:= d.Exploration(tk.ExplorationID)
	f, err:= es.RecordFinding(ctx, RecordFindingInput{
		TaskID: tk.ID, Worker: "test", VulnClass: "SQLCompleted", Name: "Status change use case.",
		Severity: "high", Summary: "Failure",
	})
	if err!= nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE finding_id=$1`, f.FindingID) })

	// When it is recorded in the database as a finding_created event, count it first as the baseline.
	var base int
	if err:= d.QueryRow(`SELECT count(*) FROM notification_events WHERE finding_id=$1`, f.FindingID).Scan(&base); err!= nil {
		t.Fatal(err)
	}
	if base < 1 {
		t.Fatal("Vulnerability registration should be done in the same transaction as a push event.")
	}

	// 'Change to the same state: Should not produce events (to avoid duplicate submissions causing push noise)',
	from, found, notified, err:= d.SetFindingStatusWithNotify(ctx, f.FindingID, "pending")
	if err!= nil ||!found {
		t.Fatalf("Status setting failed: found=%v err=%v", found, err)
	}
	if notified {
		t.Fatal("No push event should be registered when the status has not changed.")
	}
	if from!= "pending" {
		t.Fatalf("Should return the previous state as pending, got %q", from)
	}

	// True change: Should register the event and record from/to.
	from, found, notified, err = d.SetFindingStatusWithNotify(ctx, f.FindingID, "fixed")
	if err!= nil ||!found {
		t.Fatalf("Status setting failed: found=%v err=%v", found, err)
	}
	if!notified {
		t.Fatal("The status should be recorded for push events when it actually changes")
	}
	if from!= "pending" {
		t.Fatalf("from Should be pending, received %q", from)
	}
	var snapshot []byte
	if err:= d.QueryRow(`SELECT snapshot FROM notification_events WHERE finding_id=$1 AND kind=$2`,
		f.FindingID, notify.EventFindingStatusChanged).Scan(&snapshot); err!= nil {
		t.Fatalf("No status change event found: %v", err)
	}
	var snap notify.Snapshot
	if err:= json.Unmarshal(snapshot, &snap); err!= nil {
		t.Fatal(err)
	}
	if snap.FromStatus!= "pending" || snap.ToStatus!= "fixed" {
		t.Fatalf("The state transition in the snapshot is incorrect: %s → %s", snap.FromStatus, snap.ToStatus)
	}
	// Snapshots must include fields needed for rendering, otherwise status change messages will be empty shells.
	if snap.VulnClass!= "SQLCompleted" || snap.Severity!= "high" || snap.Name!= "Status change use case." {
		t.Fatalf("Snapshot missing rendering fields: %+v", snap)
	}
	var status string
	if err:= d.QueryRow(`SELECT status FROM findings WHERE id=$1`, f.FindingID).Scan(&status); err!= nil {
		t.Fatal(err)
	}
	if status!= "fixed" {
		t.Fatalf("The status should have been updated to fixed, resulting in: %s", status)
	}

	// Non-existent vulnerability: found.=false, no error.
	if _, found, _, err:= d.SetFindingStatusWithNotify(ctx, 999999999, "fixed"); err!= nil || found {
		t.Fatalf("Non-existent vulnerabilities should return found.=false And without errors, it should get 'found'.=%v err=%v", found, err)
	}
}

func TestNotificationStatsSnapshot(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()
	ch:= newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 9001, notify.Snapshot{Severity: "high"})
	if _, _, err:= d.FanOutPendingEvents(ctx, 100); err!= nil {
		t.Fatal(err)
	}
	stats, err:= d.NotificationStatsSnapshot(ctx)
	if err!= nil {
		t.Fatalf("Failed to count: %v", err)
	}
	if stats.Channels < 1 || stats.ChannelsOn < 1 {
		t.Fatalf("Incorrect channel count: %+v", stats)
	}
	if stats.Pending < 1 {
		t.Fatalf("Should be counted for pending delivery: %+v", stats)
	}
	// The age of newly created deliveries should be close to 0, not negative or extremely large values.
	if stats.BacklogAgeMS < 0 || stats.BacklogAgeMS > int64(time.Hour/time.Millisecond) {
		t.Fatalf("Invalid Stale Age: %d ms", stats.BacklogAgeMS)
	}
	_ = ch
}

func TestNotificationChannelCRUDRoundTrip(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()

	ch:= &NotificationChannel{
		Name: "CRUD Bidirectional",
		Kind: notify.KindEmail,
		Mode: NotifyModeDigest,
		Config: json.RawMessage(`{"host":"smtp.example.com","port":587,"from":"a@b.c","to":["x@y.z"]}`),
		Filter: json.RawMessage(`{"min_severity":"medium","on_status_change":true}`),
		RatePerMin: 42,
	}
	id, err:= d.SaveNotificationChannel(ctx, ch)
	if err!= nil {
		t.Fatalf("Creation Failed: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })

	got, err:= d.NotificationChannelByID(ctx, id)
	if err!= nil {
		t.Fatalf("Read failed: %v", err)
	}
	if got.Mode!= NotifyModeDigest || got.RatePerMin!= 42 || got.Name!= "CRUD Bidirectional" {
		t.Fatalf("Return fields do not match: %+v", got)
	}
	if!got.IsEnabled() {
		t.Fatal("Default should be enabled")
	}
	var cfg map[string]any
	if err:= json.Unmarshal(got.Config, &cfg); err!= nil {
		t.Fatal(err)
	}
	if cfg["host"]!= "smtp.example.com" {
		t.Fatalf("Configuration not correctly stored in the database: %v", cfg)
	}
	var filter notify.Filter
	if err:= json.Unmarshal(got.Filter, &filter); err!= nil {
		t.Fatal(err)
	}
	if filter.MinSeverity!= "medium" ||!filter.OnStatusChange {
		t.Fatalf("Filter conditions not correctly stored in the database.: %+v", filter)
	}

	// Read after updating.
	got.Name = "Renamed"
	off:= false
	got.Enabled = &off
	if _, err:= d.SaveNotificationChannel(ctx, got); err!= nil {
		t.Fatal(err)
	}
	after, err:= d.NotificationChannelByID(ctx, id)
	if err!= nil {
		t.Fatal(err)
	}
	if after.Name!= "Renamed" || after.IsEnabled() {
		t.Fatalf("Update not effective: %+v", after)
	}

	// Should report 'not found' instead of silent success after deletion.
	if err:= d.DeleteNotificationChannel(ctx, id); err!= nil {
		t.Fatal(err)
	}
	if _, err:= d.NotificationChannelByID(ctx, id); err!= ErrNotificationChannelNotFound {
		t.Fatalf("Expect ErrNotificationChannelNotFound, get %v", err)
	}
	if err:= d.DeleteNotificationChannel(ctx, id); err!= ErrNotificationChannelNotFound {
		t.Fatalf("Duplicate deletion should report non-existent, got %v", err)
	}
}

// TestSaveNotificationChannelKeepsExplicitZeroRate Lock down a previously wrong place:
// **0 It is a valid configuration, meaning 'no rate limit', cannot be overwritten by db layer as 'not specified' to default value**.
//
// Historical bug: SaveNotificationChannel wrote `if RatePerMin <= 0 { use default value }`,
// So docs/UI prompts/takeTokens all use '0=Unlimited flow' explanation, except for quietly changing this layer of writing to the database
// 20 (DingTalk/QQ/Telegram) or 100 (Feishu)——The operator thinks the rate limit has been lifted, but they are actually being held up,
// and there is no prompt. 'Not specified' and 'explicit 0' differ only in what can be expressed in the request body.
// So the default value is filled on the server side (see notifyCreateChannel), and only stored at the db level.
func TestSaveNotificationChannelKeepsExplicitZeroRate(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()

	// Explicit 0 (no rate limiting): Must be stored exactly as is.
	unlimited:= &NotificationChannel{
		Name: "Unlimited Rate Limiting", Kind: notify.KindDingTalk, RatePerMin: 0,
		Config: json.RawMessage(`{"webhook":"https://example.com/h"}`),
	}
	id, err:= d.SaveNotificationChannel(ctx, unlimited)
	if err!= nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })
	got, err:= d.NotificationChannelByID(ctx, id)
	if err!= nil {
		t.Fatal(err)
	}
	if got.RatePerMin!= 0 {
		t.Fatalf("Explicit 0 means no rate limiting, must be saved verbatim, resulting in %d", got.RatePerMin)
	}
	if got.Mode!= NotifyModeRealtime {
		t.Fatalf("Default mode should be 'realtime', received: %s", got.Mode)
	}

	// Negative values are illegal input; they should be rejected rather than silently changed to another value.
	bad:= &NotificationChannel{
		Name: "Rate limit", Kind: notify.KindDingTalk, RatePerMin: -1,
		Config: json.RawMessage(`{"webhook":"https://example.com/h"}`),
	}
	if _, err:= d.SaveNotificationChannel(ctx, bad); err == nil {
		t.Fatal("Negative rate limits should be rejected")
	}
}

// TestDeleteChannelCascadesDeliveries Lock foreign key behavior: When a channel is deleted, its delivery history disappears as well
// (Configuration gone, history unreadable), but the event itself must be left——It may still be referenced by other channels.
func TestDeleteChannelCascadesDeliveries(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()
	ch:= newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	ev:= addTestEvent(t, d, notify.EventFindingCreated, 9101, notify.Snapshot{Severity: "high"})
	if _, _, err:= d.FanOutPendingEvents(ctx, 100); err!= nil {
		t.Fatal(err)
	}
	var before int
	if err:= d.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&before); err!= nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("Precondition not met: no delivery has been produced")
	}
	if err:= d.DeleteNotificationChannel(ctx, ch.ID); err!= nil {
		t.Fatal(err)
	}
	var after int
	if err:= d.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&after); err!= nil {
		t.Fatal(err)
	}
	if after!= 0 {
		t.Fatalf("After deleting a channel, its deliveries should also be cascaded deleted. There are still %d Item", after)
	}
	var evExists bool
	if err:= d.QueryRow(`SELECT EXISTS(SELECT 1 FROM notification_events WHERE id=$1)`, ev).Scan(&evExists); err!= nil {
		t.Fatal(err)
	}
	if!evExists {
		t.Fatal("Deleting a channel should not delete the event itself")
	}
}

// TestClaimDigestBatchHonorsCallerLimit Cover an audit point pointed out by the audit log:
// Previously completely bypassed the token bucket in the summary channel.——allow Taken by takeTokens but unused,
// rate_per_min Has no effect on digest mode. Now limit also participates in constraints.
func TestClaimDigestBatchHonorsCallerLimit(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()
	ch:= newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)
	for i:= 0; i < 10; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(7000+i), notify.Snapshot{Severity: "high"})
	}
	if _, _, err:= d.FanOutPendingEvents(ctx, 100); err!= nil {
		t.Fatal(err)
	}
	// Get Limit=3: Only three can be claimed; the rest remain in the database.
	got, err:= d.ClaimDigestBatch(ctx, ch.ID, 3, time.Minute)
	if err!= nil {
		t.Fatalf("Failed to claim: %v", err)
	}
	if len(got)!= 3 {
		t.Fatalf("Should only claim 3 items based on the caller's rate limit, resulting in %d", len(got))
	}
	// limit=0 Indicates round quota exhausted: None should be claimed, no error should be reported.
	if got, err:= d.ClaimDigestBatch(ctx, ch.ID, 0, time.Minute); err!= nil || len(got)!= 0 {
		t.Fatalf("When the quota is 0, it should receive 0 items and no errors, returning %d Error err=%v", len(got), err)
	}
}

// TestFinishFindingRetestEmitsStatusChange Coverage audit points out an integrity gap:
// When retest conclusion is 'Fixed', the status does change, but that UPDATE writes directly to the database.
// Bypassed version with notifications——So configured a channel for on_status_change status transitions.
// Completely receive no push, status changes quietly on the interface, ops need to open the platform to know.
//
// This test case locks down all paths changing status to log status change events.
func TestFinishFindingRetestEmitsStatusChange(t *testing.T) {
	d:= notifyTestDB(t)
	ctx:= context.Background()

	tk, err:= d.CreateTask("Retest push test.", "Target", nil, 0, 0)
	if err!= nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	es:= d.Exploration(tk.ExplorationID)
	f, err:= es.RecordFinding(ctx, RecordFindingInput{
		TaskID: tk.ID, Worker: "test", VulnClass: "SQLCompleted", Name: "Retest the target",
		Severity: "high", Summary: "Failure",
	})
	if err!= nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE finding_id=$1`, f.FindingID) })

	// Create a retest record and directly push it to the completed state',
	rt, _, _, err:= d.CreateFindingRetest(ctx, f.FindingID, "Review")
	if err!= nil {
		t.Fatal(err)
	}
	if rt.ConversationID == nil {
		t.Fatal("Retest should be associated with a session")
	}
	// Retest must enter running before making a conclusion (consistent with real process).
	if ok, err:= d.StartFindingRetest(ctx, rt.ID); err!= nil ||!ok {
		t.Fatalf("Failed to start retest: ok=%v err=%v", ok, err)
	}
	if err:= d.RecordFindingRetestResult(ctx, *rt.ConversationID, "fixed", "Fixed", "Subnet"); err!= nil {
		t.Fatal(err)
	}
	if err:= d.FinishFindingRetest(rt.ID, "completed", ""); err!= nil {
		t.Fatalf("Failed to end retest: %v", err)
	}

	var status string
	if err:= d.QueryRow(`SELECT status FROM findings WHERE id=$1`, f.FindingID).Scan(&status); err!= nil {
		t.Fatal(err)
	}
	if status!= FindingFixed {
		t.Fatalf("Retest judgment should result in status fixed after repair, got. %s", status)
	}

	// Key assertion: There must be one status change event, and from/to must be correct.
	var snapshot []byte
	err = d.QueryRow(`SELECT snapshot FROM notification_events WHERE finding_id=$1 AND kind=$2 ORDER BY id DESC LIMIT 1`,
		f.FindingID, notify.EventFindingStatusChanged).Scan(&snapshot)
	if err!= nil {
		t.Fatalf("Retest should register status change push events if fixed (otherwise channels configured with on_status_change won't receive).: %v", err)
	}
	var snap notify.Snapshot
	if err:= json.Unmarshal(snapshot, &snap); err!= nil {
		t.Fatal(err)
	}
	if snap.FromStatus!= "pending" || snap.ToStatus!= FindingFixed {
		t.Fatalf("Snapshot status flow is incorrect: %s → %s", snap.FromStatus, snap.ToStatus)
	}
	// Snapshots must include rendering required fields, otherwise they will be empty shells when pushed out.
	if snap.Name!= "Retest the target" || snap.Severity!= "high" {
		t.Fatalf("Snapshot missing rendering fields: %+v", snap)
	}
}
