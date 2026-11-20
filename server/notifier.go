package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Zeerg/artex/db"
	"github.com/Zeerg/artex/notify"
)

// Global setting key (exists in the settings key-value table, no need for a new table).
const (
	// settingNotifyEnabled This is the push toggle switch. Default on: it is used for maintenance mode one-click bleeding stop.
	// Not a condition for enabling the function——The true enabling condition is 'whether there is a channel'.
	settingNotifyEnabled = "notify_enabled"
	// settingNotifyPublicBaseURL External access address for generating vulnerability details backlink.
	// (Like https://artex.example.com). Leave empty if no link button should be included in the message.
	// No reusable external address configuration in the project, so adding a new one here.
	settingNotifyPublicBaseURL = "notify_public_base_url"
	// settingNotifyDigestMinutes It's the cycle of a summary mode (minutes).
	settingNotifyDigestMinutes = "notify_digest_interval_min"
)

const (
	// notifyTick The polling interval for the delivery engine. 3 seconds is the upper limit of this engine's real-time performance,
	// Also a major source of delay between 'vulnerability ingestion' and 'message arrival in IM'.
	notifyTick = 3 * time.Second
	// notifyLease Duration of the lease when claiming delivery. Must be significantly greater than the worst-case time for a single delivery.
	// (notify Package HTTP client timeout 15 seconds), otherwise the same line will appear twice
	// dispatcher Submit simultaneously.
	notifyLease = 3 * time.Minute
	// notifyFanOutPerTick Limits the number of events dispatched per round to avoid overwhelming channels when first enabled.
	// Expand all accumulated history into delivery tasks at once.
	notifyFanOutPerTick = 200
	// notifyDefaultDigestMinutes Default value for summary cycle.
	notifyDefaultDigestMinutes = 30
	// notifyUnlimitedBurstPerTick Upper limit per round when no rate limiting on channel.
	// The meaning of existence is to prevent 'one channel configured without rate limiting'. + Scanning for over a thousand vulnerabilities'
	// Single-round loop dragged into long-term blocking.
	notifyUnlimitedBurstPerTick = 50
	// notifyMaxSendsPerChannelPerTick It's the maximum number of deliveries per round for a single channel.
	//
	// This limit is set by**Lease duration**Retroactively: When claiming a line, it assigns a lease (notifyLease) = 3 Minutes),
	// If a batch of serially submitted items takes the worst-case time to exceed the lease, the remaining items will expire before they finish sending.
	// It doesn't matter within a single process (Run runs as a single goroutine serially, tick does not re-enter), but**Two
	// Process connected to the same library.**When the other end reclaims expired rows and repeats sending, it will also return the id
	// attempts Double increment, failing if tasks are still being delivered in the original process.
	//
	// Values: 3-minute lease / 30-second single-time timeout = 6 is**Exactly fills the lease**, Zero remainder,
	// Cannot be taken; take 5 to leave 30 seconds of buffer time for the worst case. This relationship is determined by
	// TestNotifyTickBudgetFitsWithinLease Pin——Change notifyLease,
	// notifySendTimeout Any of these values will cause that assertion to fail.
	notifyMaxSendsPerChannelPerTick = 5
	// notifySendTimeout Timeout for a single delivery. It also decides the value of the previous constant,
	// Their product cannot exceed notifyLease, see TestNotifyTickBudgetFitsWithinLease.
	notifySendTimeout = 30 * time.Second
)

// notifyBackoff Retry backoff sequence for failures, with the index as the number of attempts made.
// 3 Second chance (including first) with db.MaxNotifyAttempts Corresponding, both must be changed together.
var notifyBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
}

// Notifier It is a delivery engine for vulnerability pushes.
//
// Runs alongside Scheduler as an independent goroutine (see server)..New). Intentionally not reused
// Scheduler The tick: the real-time requirement for push messages (3 seconds) differs from the trigger's business rhythm,
// and their failures do not affect each other——Pushing should not affect agent triggering.
type Notifier struct {
	s *Server
	pg *db.DB

	// mu Protect buckets. Fewer channels, lower competition, one mutex lock is sufficient,
	// It's not worth introducing more granular structure for it.
	mu sync.Mutex
	buckets map[int64]*notifyBucket
}

// notifyBucket Single-channel token bucket.
//
// Using a token bucket instead of a sliding window that resets counts every minute because the latter's boundary effects are terrible:
// Sending 20 messages at the end of the window and then 20 more in the next instant is considered 40 messages per second by the platform,
// It will be rate-limited; the token bucket replenishes at a constant rate, naturally avoiding such surges.
type notifyBucket struct {
	tokens float64
	lastFill time.Time
}

func newNotifier(s *Server) *Notifier {
	return &Notifier{s: s, pg: s.m.pg, buckets: map[int64]*notifyBucket{}}
}

// Run Loop until ctx ends. By server.New Start once.
func (n *Notifier) Run(ctx context.Context) {
	if n.pg == nil {
		return
	}
	t:= time.NewTicker(notifyTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.step(ctx)
		}
	}
}

// step Runs a round: dispatches new events first, then delivers overdue tasks.
//
// Any step failure logs only, no loop interruption.——System failures must never be upgraded to process-level issues.
// Each tick is independent, and the next round will naturally retry.
func (n *Notifier) step(ctx context.Context) {
	if!n.enabled() {
		return
	}
	if _, _, err:= n.pg.FanOutPendingEvents(ctx, notifyFanOutPerTick); err!= nil {
		log.Printf("[notify] Event dispatch failed: %v", err)
		return
	}
	channels, err:= n.pg.ListNotificationChannels(ctx)
	if err!= nil {
		log.Printf("[notify] Failed to read channel: %v", err)
		return
	}
	baseURL:= n.publicBaseURL()
	for _, ch:= range channels {
		if!ch.IsEnabled() {
			continue
		}
		// The unit of measurement for the token bucket is**message count** (equivalent to HTTP request count), not the number of vulnerability entries.
		// In real-time mode, both are the same (one vulnerability one message); in summary mode, all vulnerabilities are consolidated.
		// One message, so only one token is consumed.
		//
		// Both modes first ask the token bucket, then request according to the quota.——The order cannot be reversed; otherwise, it will be blocked by rate limiting.
		// Delivery has already consumed the retry count.
		now:= time.Now()
		if ch.Mode == db.NotifyModeDigest {
			tokens, claimLimit:= digestTickPlan()
			if n.takeTokens(ch.ID, ch.RatePerMin, tokens, now) <= 0 {
				continue
			}
			n.stepDigest(ctx, ch, claimLimit, baseURL)
			continue
		}
		allow:= n.takeTokens(ch.ID, ch.RatePerMin, notifyMaxSendsPerChannelPerTick, now)
		if allow <= 0 {
			continue
		}
		n.stepRealtime(ctx, ch, allow, baseURL)
	}
}

// digestTickPlan Return the summary channel's token consumption and batch size upper limit for this round.
//
// Two return values are**Two different dimensions**, This is exactly the reason for making it a function:
//
// - tokens It's the number of messages. One batch of vulnerabilities is combined into one message and sent once via HTTP request, so it's always 1.
// rate_per_min Therefore still effective on digest (up to 100 summary messages per minute).
// - claimLimit Maximum number of vulnerabilities this batch can install. It is constrained by memory limits and not request budgets.
//
// In order for rate_per_min to take effect on digest, we previously allocated request budgets
// (notifyMaxSendsPerChannelPerTick, Passed down directly as the batch size.
// The consequence is rate_per_min=20 The channel only replenishes 1 token in a 3-second tick, so each summary
// message only contains 1 vulnerability——digest Degraded into 'real-time push with summary text', what readers receive is a string of
// ''Added 1 new vulnerability in the last 30 minutes,' while db.MaxDigestBatchSize Never reachable.
//
// This symptom is difficult to discover in end-to-end tests (existing test cases manually pass a large limit to
// stepDigest, Bypassed the quota calculation in step), so decision-making is handled here.
// TestDigestTickPlanDecouplesBatchSizeFromSendBudget Pin directly.
func digestTickPlan() (tokens, claimLimit int) {
	return 1, db.MaxDigestBatchSize
}

// stepRealtime Fetch and deliver real-time tasks from a channel, one vulnerability per message.
func (n *Notifier) stepRealtime(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	deliveries, err:= n.pg.ClaimRealtimeDeliveries(ctx, ch.ID, allow, notifyLease)
	if err!= nil {
		log.Printf("[notify] Failed to claim real-time delivery channel.=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok:= n.adapt(ch)
	if!ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("Channel Type %q Unregistered", ch.Kind))
		return
	}
	for _, dl:= range deliveries {
		msg, err:= n.renderSingle(ctx, dl, baseURL)
		if err!= nil {
			// Rendering failure is a local data issue, retrying won't help.
			_ = n.pg.FailDeliveries(ctx, []int64{dl.ID}, err.Error())
			continue
		}
		n.send(ctx, channel, cfg, msg, []*db.NotificationDelivery{dl})
	}
}

// stepDigest At the end of a batch, aggregate messages to be sent for a certain channel into one message.
func (n *Notifier) stepDigest(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	window:= n.digestInterval()
	due, err:= n.pg.DigestBatchDue(ctx, ch.ID, window)
	if err!= nil {
		log.Printf("[notify] Batch summary failure channel=%d: %v", ch.ID, err)
		return
	}
	if!due {
		return
	}
	deliveries, err:= n.pg.ClaimDigestBatch(ctx, ch.ID, allow, notifyLease)
	if err!= nil {
		log.Printf("[notify] Failed to receive summary batch channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok:= n.adapt(ch)
	if!ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("Channel Type %q Unregistered", ch.Kind))
		return
	}
	msg, included, err:= n.renderBatch(ctx, deliveries, baseURL, int(window.Minutes()))
	if err!= nil {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), err.Error())
		return
	}
	// Explicitly mark failed deliveries for snapshots that are broken or couldn't enter messages. Not doing so leaves them
	// included Neither entering messages nor failing into the failure list——Their status will be updated when sent successfully.
	// The subsequent batch marking is missed, forever stuck in sending until the lease expires and is repeatedly claimed.
	if skipped:= excludeDeliveries(deliveries, included); len(skipped) > 0 {
		reason:= "Unable to parse event snapshot, this vulnerability cannot be rendered into a message."
		if fErr:= n.pg.FailDeliveries(ctx, deliveryIDs(skipped), reason); fErr!= nil {
			log.Printf("[notify] Mark bad snapshot delivery failure channel=%s ids=%v: %v", ch.Kind, deliveryIDs(skipped), fErr)
		}
		log.Printf("[notify] Skip %d A snapshot channel that cannot be parsed.=%d", len(skipped), ch.ID)
	}
	// Only pass messages to send: included[i] With msg.Items[i] Strictly correspond,
	// send Use this mapping to place 'the first K items from the channel report' on the correct delivery row.
	n.send(ctx, channel, cfg, msg, included)
}

// send Deliver and transition status based on results.
//
// A batch submission (may contain dozens in summary mode) shares one send result: either delivered or entire batch retried.
// Do not retry one by one——Summary message is one, resending part of it will mess up batch semantics.
//
// The only exception is**Segment due to channel length limit.**: The channel reports it only installed the first K items,
// So the Kth+1 Items must remain for the next batch instead of being marked successful together. Otherwise, those that were cut off
// Those vulnerabilities are neither in the message nor in the failed list, disappearing completely.
func (n *Notifier) send(ctx context.Context, channel notify.Channel, cfg map[string]any, msg notify.Message, deliveries []*db.NotificationDelivery) {
	// Set an upper limit for single deliveries to avoid one channel getting stuck and dragging down the remaining channels of this round.
	sendCtx, cancel:= context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()
	delivered, err:= channel.Send(sendCtx, cfg, msg)
	if err == nil && delivered > 0 {
		if delivered > len(deliveries) {
			// The number of channel reports cannot exceed the delivery count; if it happens, it means the rendering layer calculated wrong.
			// Handle all deliveries and log issues instead of mixing them with 'delivered'.
			log.Printf("[notify] Number of channel reports received %d Exceeds delivery count %d channel=%s, Handle all deliveries.",
				delivered, len(deliveries), channel.Kind())
			delivered = len(deliveries)
		}
		sent, rest:= deliveries[:delivered], deliveries[delivered:]
		if err:= n.pg.MarkDeliveriesSent(ctx, deliveryIDs(sent)); err!= nil {
			log.Printf("[notify] Mark delivery failure channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(sent), err)
		}
		if len(rest) > 0 {
			// This message has reached the channel length limit: the remaining ones will be immediately requeued, to be sent by the next tick.
			// Use DeferDeliveries instead of RescheduleDeliveries —— This is not a failure,
			// Should not consume retry budget (already optimistic when claiming). +1 Done, it will be deducted there).
			if err:= n.pg.DeferDeliveries(ctx, deliveryIDs(rest),
				fmt.Sprintf("This message has reached the channel length limit, only delivered before %d Item, the rest to be left for the next batch", delivered)); err!= nil {
				log.Printf("[notify] Segment resending queue failure channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(rest), err)
			}
		}
		return
	}
	if err == nil {
		// The channel neither reported an error nor mentioned how many were delivered. Handle as failure (go through backoff).
		// To prevent this delivery from being repeatedly claimed but never marked.
		err = fmt.Errorf("Channel undelivered count (delivered=%d)", delivered)
	}

	// Failure handling**One by one**Decide based on individual vulnerabilities rather than the maximum number of attempts per batch.
	//
	// Was `if maxAttempts(deliveries) >= MaxNotifyAttempts` Entirely rejected, but in batches
	// The number of attempts varies for each item: an old delivery that has already retried twice (attempts=2)Will deliver the same batch of
	// Entirely new delivery (attempts=1)Drag into failed together——A new vulnerability that hasn't retried even once is permanently lost.
	// Contrary to the initial intent of not letting old flows drag new ones down.
	permanent:= notify.IsPermanent(err)
	var failIDs, exhaustedIDs []int64
	byDelay:= map[time.Duration][]int64{}
	for _, dl:= range deliveries {
		switch {
		case permanent:
			failIDs = append(failIDs, dl.ID)
		case dl.Attempts >= db.MaxNotifyAttempts:
			exhaustedIDs = append(exhaustedIDs, dl.ID)
		default:
			delay:= notifyBackoff[min(dl.Attempts, len(notifyBackoff)-1)]
			byDelay[delay] = append(byDelay[delay], dl.ID)
		}
	}

	if len(failIDs) > 0 {
		if fErr:= n.pg.FailDeliveries(ctx, failIDs, err.Error()); fErr!= nil {
			log.Printf("[notify] Error marking failure status channel=%s ids=%v: %v", channel.Kind(), failIDs, fErr)
		}
	}
	if len(exhaustedIDs) > 0 {
		reason:= fmt.Sprintf("Cancel %d Still fails after retries: %s", db.MaxNotifyAttempts, err)
		if fErr:= n.pg.FailDeliveries(ctx, exhaustedIDs, reason); fErr!= nil {
			log.Printf("[notify] Error marking failure status channel=%s ids=%v: %v", channel.Kind(), exhaustedIDs, fErr)
		}
	}
	// Rearrange by delay grouping: there are only 3 levels of backoff, and the number of groups is naturally small, so it's not necessary to send each one individually
	// UPDATE (That would cause a 500 batch to generate 500 round trips.
	for delay, group:= range byDelay {
		if rErr:= n.pg.RescheduleDeliveries(ctx, group, delay, err.Error()); rErr!= nil {
			log.Printf("[notify] Rearranging failed channel delivery.=%s ids=%v: %v", channel.Kind(), group, rErr)
		}
	}
	if len(failIDs)+len(exhaustedIDs) > 0 {
		log.Printf("[notify] Failed submission channel=%d kind=%s Permanent failure=%d retries exhausted=%d Pending retry=%d: %s",
			deliveries[0].ChannelID, channel.Kind(), len(failIDs), len(exhaustedIDs), len(byDelay), err)
	}
}

// excludeDeliveries Return all that are not in keep (by pointer identity comparison).
// To find submissions that didn't enter messages——They must be explicitly handled and cannot remain in a gray area.
func excludeDeliveries(all, keep []*db.NotificationDelivery) []*db.NotificationDelivery {
	inKeep:= make(map[*db.NotificationDelivery]bool, len(keep))
	for _, dl:= range keep {
		inKeep[dl] = true
	}
	var out []*db.NotificationDelivery
	for _, dl:= range all {
		if!inKeep[dl] {
			out = append(out, dl)
		}
	}
	return out
}

// adapt Fetch channel implementation and parse its configuration.
// Return ok=false Indicates type not registered, delivery should directly fail instead of infinite retries.
func (n *Notifier) adapt(ch *db.NotificationChannel) (notify.Channel, map[string]any, bool) {
	channel, ok:= notify.Get(ch.Kind)
	if!ok {
		return nil, nil, false
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		// Provide an empty map when configuration parsing fails: the channel's own Validate will report which field is missing.
		// That error is more instructive for users to fix than JSON parsing errors.
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return channel, cfg, true
}

// renderSingle Render a single vulnerability message.
func (n *Notifier) renderSingle(ctx context.Context, dl *db.NotificationDelivery, baseURL string) (notify.Message, error) {
	snap, err:= parseSnapshot(dl)
	if err!= nil {
		return notify.Message{}, err
	}
	item, err:= n.itemFor(ctx, snap, baseURL)
	if err!= nil {
		return notify.Message{}, err
	}
	return notify.Message{Items: []notify.Item{item}, HomeURL: baseURL}, nil
}

// renderBatch Render summary message. Parse snapshots one by one——Skip the broken one and move on,
// Don't let it drag the entire batch down.
//
// Return value included with msg.Items **Strictly one-to-one correspondence** (The i-th delivery ↔ The i-th item).
// This corresponds to a hard requirement: the caller decides the first K deliveries based on 'channel feedback installing the first K'.
// Marked as delivered. If here it skips a bad snapshot but doesn't remove the skipped delivery from included,
// Indexing will be off——Bad entries that should have failed will be marked as delivered, while good ones may be incorrectly flagged as undelivered.
// Bad ones explicitly marked as failed by the caller, see stepDigest.
func (n *Notifier) renderBatch(ctx context.Context, deliveries []*db.NotificationDelivery, baseURL string, windowMinutes int) (notify.Message, []*db.NotificationDelivery, error) {
	items:= make([]notify.Item, 0, len(deliveries))
	included:= make([]*db.NotificationDelivery, 0, len(deliveries))
	for _, dl:= range deliveries {
		snap, err:= parseSnapshot(dl)
		if err!= nil {
			// Bad snapshots do not enter messages, nor included——Its handling is the responsibility of the caller
			// (Explicitly mark as failed, not mixed in with 'delivered' to pass inspection.
			log.Printf("[notify] Skip snapshots that cannot be parsed during batch aggregation=%d: %v", dl.ID, err)
			continue
		}
		item, err:= n.itemFor(ctx, snap, baseURL)
		if err!= nil {
			return notify.Message{}, nil, err
		}
		items = append(items, item)
		included = append(included, dl)
	}
	if len(items) == 0 {
		return notify.Message{}, nil, fmt.Errorf("Batch summary %d All deliveries cannot be parsed", len(deliveries))
	}
	return notify.Message{
		Items: items,
		Batch: true,
		WindowMinutes: windowMinutes,
		HomeURL: baseURL,
	}, included, nil
}

// itemFor Render event snapshots into pending push items, parsing asset names and detail links simultaneously.
func (n *Notifier) itemFor(ctx context.Context, snap notify.Snapshot, baseURL string) (notify.Item, error) {
	assets, err:= n.pg.NotificationAssetNames(ctx, snap.AssetIDs)
	if err!= nil {
		// 'Asset name resolution failure should not prevent push: It is much lighter than not receiving a notification.',
		// Just one less line of asset in the message.
		log.Printf("[notify] Failed to parse asset name finding.=%d: %v", snap.FindingID, err)
	}
	item:= notify.Item{
		FindingID: snap.FindingID,
		Name: snap.Name,
		VulnClass: snap.VulnClass,
		Severity: snap.Severity,
		Summary: snap.Summary,
		Assets: assets,
		FromStatus: snap.FromStatus,
		ToStatus: snap.ToStatus,
	}
	if baseURL!= "" {
		// Details page route can be found in web/src/app/(main)/function/findings/detail/page.tsx,
		// Reads vulnerability ID from query parameter id.
		item.DetailURL = fmt.Sprintf("%s/function/findings/detail?id=%d", baseURL, snap.FindingID)
	}
	return item, nil
}

// takeTokens Take a token from the channel bucket**At most want**Token, returning the actual number received.
//
// one token = A message (one HTTP request). The caller should pass as many as needed in real-time mode;
// In summary mode, only one message is sent per batch of vulnerabilities, pass 1.
//
// The bucket capacity is the channel's limit per minute, replenished at a constant rate. ratePerMin<=0 Indicates no rate limiting,
// Return a limited but sufficiently large value to prevent single-round loops from being infinitely accumulated and dragged down.
//
// want This limit is necessary: without it, you can only empty the bucket entirely, while the caller still has a per-round limit,
// Extra tokens that are not used will disappear into thin air before being replenished next time.——Accumulated sudden capacity can never be reached.
// Even if 'this round has no pending deliveries', it will still deduct a charge.
func (n *Notifier) takeTokens(channelID int64, ratePerMin, want int, now time.Time) int {
	if want <= 0 {
		return 0
	}
	if ratePerMin <= 0 {
		return min(want, notifyUnlimitedBurstPerTick)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	b:= n.buckets[channelID]
	if b == nil {
		b = &notifyBucket{tokens: float64(ratePerMin), lastFill: now}
		n.buckets[channelID] = b
	}
	// Supplement by the actual time passed, rate is ratePerMin/60 per second.
	if elapsed:= now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens = minF(float64(ratePerMin), b.tokens+elapsed*float64(ratePerMin)/60)
		b.lastFill = now
	}
	// Add an extremely small epsilon before rounding: token counts are floating-point accumulations, and when rounded twice to fill them, estimate tokens, retain recent content, update new content into old digests, and handle cross-round truncation.
	// 0.5 + 0.5 May be 0.9999999999, Direct int() Will be truncated to 0——
	// A full bucket that can't take out tokens. 1e-9 is much smaller than a token, so it won't miss any real debt.
	take:= min(int(b.tokens+1e-9), want)
	if take <= 0 {
		return 0
	}
	b.tokens -= float64(take)
	return take
}

// enabled Read total switch.
func (n *Notifier) enabled() bool {
	return n.pg.GetBool(settingNotifyEnabled, true)
}

// publicBaseURL Return the external address used for backlink, remove trailing slash.
func (n *Notifier) publicBaseURL() string {
	v, ok, err:= n.pg.GetSetting(settingNotifyPublicBaseURL)
	if err!= nil ||!ok {
		return ""
	}
	return trimTrailingSlash(v)
}

// digestInterval Return the summary cycle. If illegal or not configured, fall back to the default value.
func (n *Notifier) digestInterval() time.Duration {
	v, ok, err:= n.pg.GetSetting(settingNotifyDigestMinutes)
	if err!= nil ||!ok {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	m:= 0
	if _, err:= fmt.Sscanf(v, "%d", &m); err!= nil || m <= 0 {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	return time.Duration(m) * time.Minute
}

// parseSnapshot Parse snapshot corresponding to delivery event.
func parseSnapshot(dl *db.NotificationDelivery) (notify.Snapshot, error) {
	var snap notify.Snapshot
	if len(dl.Snapshot) == 0 {
		return snap, fmt.Errorf("Submit %d Event snapshot is empty.", dl.ID)
	}
	if err:= json.Unmarshal(dl.Snapshot, &snap); err!= nil {
		return snap, fmt.Errorf("Parse delivery %d Event snapshot failed: %w", dl.ID, err)
	}
	if snap.Kind == "" {
		// Event type based on event behavior, the snapshot may have been written by an old version.
		snap.Kind = dl.EventKind
	}
	return snap, nil
}

func deliveryIDs(deliveries []*db.NotificationDelivery) []int64 {
	out:= make([]int64, 0, len(deliveries))
	for _, dl:= range deliveries {
		out = append(out, dl.ID)
	}
	return out
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
