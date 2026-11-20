package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Zeerg/artex/notify"
)

// This file is for IM push channel configuration and event layer. For task claiming and status flow, see
// db/notification_delivery.go.
//
// Two invariant conditions must be maintained when modifying this file:
//
// 1. Write vulnerability transaction(RecordFindingTx)Only call InsertNotificationEventTx for a single blind insert,
// Do not read any notification-related tables or perform filtering matches. Any read operations introduced here may be because
// could pollute or even terminate the vulnerability write transaction due to an incorrectly configured filter condition by the user.
// 2. Filter matches never error: malformed configurations are uniformly treated as 'hit'.(See notify.Match). Rather push more,
// Must not be missed.

// ErrNotificationChannelNotFound Channel does not exist.
var ErrNotificationChannelNotFound = errors.New("Notification channel does not exist")

// Submission status.
const (
	NotifyStatePending = "pending" // Therefore
	NotifyStateSending = "sending" // Already claimed by some dispatcher, lease not expired.
	NotifyStateSent = "sent" // Delivered
	NotifyStateFailed = "failed" // Retry exhausted or permanently failed, can manually resend
	NotifyStateSkipped = "skipped" // Channel is disabled, no longer sending
)

// Push mode.
const (
	NotifyModeRealtime = "realtime"
	NotifyModeDigest = "digest"
)

// ValidNotifyMode Whitelist validation push mode (with findings.status Similarly, don't use DB CHECK,
// For easy subsequent expansion).
func ValidNotifyMode(m string) bool {
	return m == NotifyModeRealtime || m == NotifyModeDigest
}

// NotificationChannel Is a channel instance configuration. Config and Filter keep original JSON,
// Parsing handed over to the notify package.——db Layers do not understand the meaning of their field names.
type NotificationChannel struct {
	ID int64 `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Mode string `json:"mode"`
	Config json.RawMessage `json:"config"`
	Filter json.RawMessage `json:"filter"`
	// Enabled Using pointers to distinguish between 'not passing this field' and 'explicitly passing false'.——
	// Frontend switch controls only submit changed fields.',
	Enabled *bool `json:"enabled,omitempty"`
	RatePerMin int `json:"rate_per_min"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsEnabled Return whether the channel is enabled; Enabled as nil (not loaded) is treated as enabled.
func (c *NotificationChannel) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// NotificationEvent It is an event fact.
type NotificationEvent struct {
	ID int64 `json:"id"`
	Kind string `json:"kind"`
	FindingID int64 `json:"finding_id"`
	Snapshot json.RawMessage `json:"snapshot"`
	CreatedAt time.Time `json:"created_at"`
}

const notificationChannelCols = `id, name, kind, enabled, config, mode, filter, rate_per_min, created_at, updated_at`

func scanNotificationChannel(sc interface{ Scan(...any) error }) (*NotificationChannel, error) {
	var c NotificationChannel
	var enabled bool
	if err:= sc.Scan(&c.ID, &c.Name, &c.Kind, &enabled, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin, &c.CreatedAt, &c.UpdatedAt); err!= nil {
		return nil, err
	}
	c.Enabled = &enabled
	return &c, nil
}

// ListNotificationChannels Return all channel instances, enabled ones at the front, same level by id.
// Sorting in SQL ensures a stable order for UI and dispatcher.
func (d *DB) ListNotificationChannels(ctx context.Context) ([]*NotificationChannel, error) {
	rows, err:= d.QueryContext(ctx, `SELECT `+notificationChannelCols+` FROM notification_channels
ORDER BY enabled DESC, id`)
	if err!= nil {
		return nil, err
	}
	defer rows.Close()
	out:= []*NotificationChannel{}
	for rows.Next() {
		c, err:= scanNotificationChannel(rows)
		if err!= nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NotificationChannelByID Fetch a single channel.
func (d *DB) NotificationChannelByID(ctx context.Context, id int64) (*NotificationChannel, error) {
	row:= d.QueryRowContext(ctx, `SELECT `+notificationChannelCols+` FROM notification_channels WHERE id=$1`, id)
	c, err:= scanNotificationChannel(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotificationChannelNotFound
	}
	return c, err
}

// SaveNotificationChannel Create or update a channel.
//
// During updates, only fields explicitly provided by the caller (non-nil / non-empty) are overwritten, allowing the frontend to submit partial changes.
// Modify the drawer form without needing to return config fields it doesn't display.——Returningwould instead cause
// 'Incident where mask value overwrites the real key',
func (d *DB) SaveNotificationChannel(ctx context.Context, c *NotificationChannel) (int64, error) {
	if c.Mode == "" {
		c.Mode = NotifyModeRealtime
	}
	// Intentionally here**not**Do not process 0 in any way: 0 is a valid configuration, meaning 'no rate limiting'.
	//
	// Previously written as `if c.RatePerMin <= 0 { c.RatePerMin = Default value }`, The original intention was 'not specified when
	// Provide a default security setting, but also swallow 'explicitly set to 0'.——Documentation, UI hints and
	// takeTokens All treat 0 as unlimited, except here quietly changing it to 20 (DingTalk/QQ/Telegram).
	// Or 100 (Feishu), the operator thinks the rate limiting has been lifted, but they are actually being limited at 20/minute with no notification.
	//
	// 'The difference between 'not specified' and 'explicit 0' is known only by the caller (missing field in request body vs explicitly passing 0),
	// So the default value is filled by the server layer when the field is missing, see notifyCreateChannel.
	if c.RatePerMin < 0 {
		return 0, errors.New("Rate limit value cannot be negative")
	}
	if c.Config == nil {
		c.Config = json.RawMessage(`{}`)
	}
	if c.Filter == nil {
		c.Filter = json.RawMessage(`{}`)
	}
	enabled:= c.IsEnabled()

	if c.ID == 0 {
		var id int64
		err:= d.QueryRowContext(ctx, `INSERT INTO notification_channels(name,kind,enabled,config,mode,filter,rate_per_min)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			c.Name, c.Kind, enabled, string(c.Config), c.Mode, string(c.Filter), c.RatePerMin).Scan(&id)
		return id, err
	}
	res, err:= d.ExecContext(ctx, `UPDATE notification_channels
SET name=$2, kind=$3, enabled=$4, config=$5, mode=$6, filter=$7, rate_per_min=$8
WHERE id=$1`,
		c.ID, c.Name, c.Kind, enabled, string(c.Config), c.Mode, string(c.Filter), c.RatePerMin)
	if err!= nil {
		return 0, err
	}
	if n, _:= res.RowsAffected(); n == 0 {
		return 0, ErrNotificationChannelNotFound
	}
	return c.ID, nil
}

// SetNotificationChannelEnabled Toggle start/stop.
//
// When disabling a channel, mark its undelivered deliveries as skipped: otherwise, when re-enabled,
// Suddenly receive a batch of 'old vulnerabilities' accumulated during downtime, which are outdated and easily misjudged as new.
func (d *DB) SetNotificationChannelEnabled(ctx context.Context, id int64, enabled bool) error {
	return d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		res, err:= tx.ExecContext(ctx, `UPDATE notification_channels SET enabled=$2 WHERE id=$1`, id, enabled)
		if err!= nil {
			return err
		}
		if n, _:= res.RowsAffected(); n == 0 {
			return ErrNotificationChannelNotFound
		}
		if!enabled {
			if _, err:= tx.ExecContext(ctx, `UPDATE notification_deliveries SET state=$2, last_error=$3
WHERE channel_id=$1 AND state IN ($4,$5)`,
				id, NotifyStateSkipped, "Channel is disabled", NotifyStatePending, NotifyStateSending); err!= nil {
				return err
			}
		}
		return nil
	})
}

// DeleteNotificationChannel Delete channel. Its delivery history is deleted with foreign key cascading.
// (Channel configuration is gone, history cannot be interpreted',
func (d *DB) DeleteNotificationChannel(ctx context.Context, id int64) error {
	res, err:= d.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=$1`, id)
	if err!= nil {
		return err
	}
	if n, _:= res.RowsAffected(); n == 0 {
		return ErrNotificationChannelNotFound
	}
	return nil
}

// RecordNotificationEventTx In the caller's transaction**Best Effort**Write one push event.
//
// This is the only notification-related change on the vulnerability write path: one INSERT, no reading of any tables, doesn't recognize channels,
// No filtering is performed. Transaction commit ensures atomic consistency between 'vulnerability stored in database' and 'task pushed'.
// No window where submission is successful but not enqueued, message permanently lost.
//
// Two key designs, neither of which were written offhand:
//
// 1. **Why use SAVEPOINT?**: PostgreSQL Any statement in the transaction causing an error will make the entire transaction enter
// aborted State, all subsequent statements (including COMMIT) will fail. So 'ignore this INSERT
// Errors that allow the caller to continue submitting in PG is not possible——Unless isolated by a save point,
// There is no save point, so there is only one option left: 'Full Rollback'.
//
// 2. **Why full rollback is wrong.**: Push is a convenience feature. Vulnerability records are the product itself. One notification
// Shouldn't allow high-risk vulnerabilities to remain in the database due to issues like old library migration or disk transient faults. So isolate here
// Error, log it, return false, let vulnerability write normally submit——The cost is losing this push.
// Returning bool instead of error is intentional: the caller shouldn't treat it as an error affecting write success.
func RecordNotificationEventTx(ctx context.Context, tx *sql.Tx, kind string, findingID int64, snap notify.Snapshot) bool {
	raw, err:= json.Marshal(snap)
	if err!= nil {
		log.Printf("[notify] Serialization push event failure finding=%d: %v", findingID, err)
		return false
	}
	if _, err:= tx.ExecContext(ctx, `SAVEPOINT notify_event`); err!= nil {
		log.Printf("[notify] Failed to establish savepoint finding=%d: %v", findingID, err)
		return false
	}
	if _, err:= tx.ExecContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3)`,
		kind, findingID, string(raw)); err!= nil {
		log.Printf("[notify] Failed to write push event finding.=%d (Vulnerability records are not affected: %v", findingID, err)
		// Rollback to save point, rescue transaction from aborted state.
		if _, rbErr:= tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT notify_event`); rbErr!= nil {
			log.Printf("[notify] Rolling back to the save point failed finding.=%d: %v", findingID, rbErr)
		}
		return false
	}
	// Release savepoint to avoid accumulating unnecessary savepoints in long transactions.
	_, _ = tx.ExecContext(ctx, `RELEASE SAVEPOINT notify_event`)
	return true
}

// AddNotificationEvent It is an independent transaction version of InsertNotificationEventTx, for use by
// Call points within existing transactions use (like channel's 'send test message', it does not have a real finding).
func (d *DB) AddNotificationEvent(ctx context.Context, kind string, findingID int64, snap notify.Snapshot) (int64, error) {
	raw, err:= json.Marshal(snap)
	if err!= nil {
		return 0, fmt.Errorf("Failed to serialize notification event snapshot.: %w", err)
	}
	var id int64
	err = d.QueryRowContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3) RETURNING id`,
		kind, findingID, string(raw)).Scan(&id)
	return id, err
}

// FanOutPendingEvents Distribute unassigned vulnerability events to the current enabled channels as delivery tasks,
// Returns the number of events processed in this round and the number of new deliveries made.
//
// All operations in one transaction: events are fetched with FOR UPDATE SKIP LOCKED, and multiple processes can run simultaneously
// Each also gets different rows (the archive queue in the project uses the same method, see
// db/task_archives.go of completeNextArchiveJob).
//
// Filtering conditions are deliberately placed on the Go side rather than SQL: channel filter conditions are a set of optional fields in JSONB,
// Using SQL to express six combinations of matching makes queries difficult to maintain, while the number of channels is 'a few per person'.
// Full load and then compare line by line in memory is faster and better for testing.
//
// Events not hitting any channels will also be marked fanned_out. ——Otherwise, it will stay in the pending dispatch set forever.
// Each tick is re-scanned.
func (d *DB) FanOutPendingEvents(ctx context.Context, limit int) (eventCount, deliveryCount int, err error) {
	if limit <= 0 {
		limit = 200
	}
	tx, err:= d.BeginTx(ctx, nil)
	if err!= nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // Submission is no-op after success

	channels, err:= listEnabledNotificationChannelsTx(ctx, tx)
	if err!= nil {
		return 0, 0, err
	}
	rows, err:= tx.QueryContext(ctx, `SELECT id, kind, finding_id, snapshot FROM notification_events
WHERE NOT fanned_out ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
	if err!= nil {
		return 0, 0, err
	}
	var (
		events []NotificationEvent
		parsedSnaps []notify.Snapshot
	)
	for rows.Next() {
		var ev NotificationEvent
		if err:= rows.Scan(&ev.ID, &ev.Kind, &ev.FindingID, &ev.Snapshot); err!= nil {
			rows.Close()
			return 0, 0, err
		}
		var snap notify.Snapshot
		// Our snapshot is self-written, theoretically resolvable; parsing failures do not block delivery flow,
		// But this event will be skipped by all channels with filtering conditions because all fields are empty.——Rather push less.
		// Also, it should not allow a bad line to block the entire queue.
		_ = json.Unmarshal(ev.Snapshot, &snap)
		// kind 'Based on inline value: The snapshot version is a render copy that may have been written by an older version.',
		snap.Kind = ev.Kind
		events = append(events, ev)
		parsedSnaps = append(parsedSnaps, snap)
	}
	rows.Close()
	if err:= rows.Err(); err!= nil {
		return 0, 0, err
	}
	if len(events) == 0 {
		return 0, 0, tx.Commit()
	}

	type pending struct {
		eventID int64
		channelID int64
	}
	var toInsert []pending
	for i, snap:= range parsedSnaps {
		for _, ch:= range channels {
			if!notify.Match(notify.ParseFilter(ch.Filter), snap) {
				continue
			}
			toInsert = append(toInsert, pending{eventID: events[i].ID, channelID: ch.ID})
		}
	}
	if len(toInsert) > 0 {
		var (
			vals []string
			args []any
		)
		for _, p:= range toInsert {
			vals = append(vals, fmt.Sprintf("($%d,$%d)", len(args)+1, len(args)+2))
			args = append(args, p.eventID, p.channelID)
		}
		if _, err:= tx.ExecContext(ctx, `INSERT INTO notification_deliveries(event_id,channel_id) VALUES `+strings.Join(vals, ","), args...); err!= nil {
			return 0, 0, err
		}
	}

	// Mark this round of events as dispatched. Events that don't hit any channels are also marked (see function comments).
	ids:= make([]string, 0, len(events))
	markArgs:= make([]any, 0, len(events))
	for _, ev:= range events {
		markArgs = append(markArgs, ev.ID)
		ids = append(ids, fmt.Sprintf("$%d", len(markArgs)))
	}
	if _, err:= tx.ExecContext(ctx, `UPDATE notification_events SET fanned_out=true WHERE id IN (`+strings.Join(ids, ",")+`)`, markArgs...); err!= nil {
		return 0, 0, err
	}
	return len(events), len(toInsert), tx.Commit()
}

// listEnabledNotificationChannelsTx In a transaction, retrieve active channels. There are few of them,
// No pagination or caching added.——Caching introduces an additional timing issue of 'when does the configuration take effect.'
func listEnabledNotificationChannelsTx(ctx context.Context, tx *sql.Tx) ([]*NotificationChannel, error) {
	rows, err:= tx.QueryContext(ctx, `SELECT id, name, kind, config, mode, filter, rate_per_min
FROM notification_channels WHERE enabled ORDER BY id`)
	if err!= nil {
		return nil, err
	}
	defer rows.Close()
	out:= []*NotificationChannel{}
	for rows.Next() {
		var c NotificationChannel
		if err:= rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin); err!= nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// NotificationAssetNames Parse asset id into a short display name for message push.
//
// Return order matches input parameters, length may be less than input (non-existent ids are skipped). Maintaining input parameter order is
// To ensure that messages about the same vulnerability maintain consistent asset order across multiple deliveries——Otherwise retry after receiving a message in
// The order of assets has changed, which may be misinterpreted as 'assets have changed'.
func (d *DB) NotificationAssetNames(ctx context.Context, ids []int64) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph, args:= placeholders(1, ids)
	rows, err:= d.QueryContext(ctx, `SELECT id, type, domain, ip, url, app_name, bundle_id FROM assets WHERE id IN (`+ph+`)`, args...)
	if err!= nil {
		return nil, err
	}
	defer rows.Close()
	labels:= map[int64]string{}
	for rows.Next() {
		var (
			id int64
			typ string
			domain, ip, url sql.NullString
			appName, bundleID sql.NullString
		)
		if err:= rows.Scan(&id, &typ, &domain, &ip, &url, &appName, &bundleID); err!= nil {
			return nil, err
		}
		labels[id] = assetDisplayName(typ, domain.String, ip.String, url.String, appName.String, bundleID.String)
	}
	if err:= rows.Err(); err!= nil {
		return nil, err
	}
	out:= make([]string, 0, len(ids))
	seen:= map[int64]bool{}
	for _, id:= range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if label, ok:= labels[id]; ok && label!= "" {
			out = append(out, label)
		}
	}
	return out, nil
}

// assetDisplayName Select the most distinctive identifier by asset type',
// Fallback to an empty string, let the caller decide how to present assets without a name.——This function does not fabricate placeholders,
// Otherwise «asset#42'This noise will mix into push messages, readers may think it's a real domain.
func assetDisplayName(typ, domain, ip, url, appName, bundleID string) string {
	pick:= func(vals ...string) string {
		for _, v:= range vals {
			if strings.TrimSpace(v)!= "" {
				return v
			}
		}
		return ""
	}
	switch typ {
	case "root_domain", "subdomain":
		return domain
	case "ip":
		return ip
	case "app":
		return pick(appName, bundleID)
	case "service", "endpoint":
		return pick(url, domain, ip)
	default:
		return pick(domain, ip, url, appName)
	}
}

// SetFindingStatusWithNotify Update vulnerability disposition status and log a status change in the same transaction.
// Push event.
//
// Return from=State before change; found.=Does the vulnerability exist; notified.=Whether the event registration was successful.
//
// Three deliberate actions:
// - Do not log events when the status does not actually change. The frontend drawer submits the same value repeatedly, or automated scripts
// Idempotent replay should not produce push noise.
// - Return 'found' when no vulnerability exists.=false And do not write anything, let the caller translate it to 404.
// - Event registration failure does not affect status update (see RecordNotificationEventTx save point notes),
// So notified=false If the status has already been changed successfully, the caller should not report an error.
func (d *DB) SetFindingStatusWithNotify(ctx context.Context, id int64, status string) (from string, found bool, notified bool, err error) {
	err = d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		from, found, _, notified, txErr = SetFindingStatusTx(ctx, tx, id, status)
		return txErr
	})
	return from, found, notified, err
}

// SetFindingStatusTx In**Caller's transaction**Internally update vulnerability status and log state change events.
//
// Extracting into transaction-level functions allows all status-changing paths to share the same semantics——only previously
// patchFinding Walks the version with notification, while**When retest conclusion is 'Fixed'** (finding_retests
// That one `UPDATE findings SET status=...`)It is directly writing to the database, so it was configured with
// `on_status_change` The channel does not receive push notifications for this state transition; the status on the interface quietly changes,
// Operations only discover the platform when opened.
//
// Return from=Before change status, found.=Does the vulnerability exist, changed?=Has the status really changed?
// notified=Whether the event registration was successful (registration failure does not affect status update; see RecordNotificationEventTx).
func SetFindingStatusTx(ctx context.Context, tx *sql.Tx, id int64, status string) (from string, found bool, changed bool, notified bool, err error) {
	var (
		vulnclass, name, severity, summary string
		taskID sql.NullInt64
		assetIDs []byte
	)
	scanErr:= tx.QueryRowContext(ctx, `SELECT vulnclass, name, severity, summary, task_id, asset_ids, status
FROM findings WHERE id=$1 FOR UPDATE`, id).
		Scan(&vulnclass, &name, &severity, &summary, &taskID, &assetIDs, &from)
	if scanErr == sql.ErrNoRows {
		return "", false, false, false, nil
	}
	if scanErr!= nil {
		return "", false, false, false, scanErr
	}
	found = true
	if from == status {
		// Do not log events if status has really changed: Duplicate submissions of the same value, idempotent replays should not
		// Produces push noise.
		return from, true, false, false, nil
	}
	if _, err:= tx.ExecContext(ctx, `UPDATE findings SET status=$2 WHERE id=$1`, id, status); err!= nil {
		return from, true, false, false, err
	}
	var assets []int64
	_ = json.Unmarshal(assetIDs, &assets)
	notified = RecordNotificationEventTx(ctx, tx, notify.EventFindingStatusChanged, id, notify.Snapshot{
		Kind: notify.EventFindingStatusChanged,
		FindingID: id,
		TaskID: taskID.Int64,
		VulnClass: vulnclass,
		Name: name,
		Severity: severity,
		Summary: summary,
		AssetIDs: assets,
		FromStatus: from,
		ToStatus: status,
	})
	return from, true, true, notified, nil
}

// NotificationStats Overview count at the top of the notification page.
type NotificationStats struct {
	Channels int `json:"channels"`
	ChannelsOn int `json:"channels_on"`
	Pending int `json:"pending"`
	Failed int `json:"failed"`
	SentToday int `json:"sent_today"`
	BacklogAgeMS int64 `json:"backlog_age_ms"` // Age in milliseconds of the oldest pending delivery
}

// NotificationStatsSnapshot Summarize the health of the notification system.
// BacklogAgeMS This is the most direct indicator of whether 'push' is stuck——Much more useful than pending count,
// Because the difference between queuing 3 and queuing 3 can be from 3 seconds to 3 hours.
func (d *DB) NotificationStatsSnapshot(ctx context.Context) (*NotificationStats, error) {
	var s NotificationStats
	if err:= d.QueryRowContext(ctx, `SELECT
    (SELECT count(*) FROM notification_channels),
    (SELECT count(*) FROM notification_channels WHERE enabled),
    (SELECT count(*) FROM notification_deliveries WHERE state IN ($1,$2)),
    (SELECT count(*) FROM notification_deliveries WHERE state=$3),
    (SELECT count(*) FROM notification_deliveries WHERE state=$4 AND sent_at >= date_trunc('day', now())),
    COALESCE((SELECT EXTRACT(EPOCH FROM (now() - min(created_at))) * 1000 FROM notification_deliveries WHERE state=$1), 0)::bigint`,
		NotifyStatePending, NotifyStateSending, NotifyStateFailed, NotifyStateSent).
		Scan(&s.Channels, &s.ChannelsOn, &s.Pending, &s.Failed, &s.SentToday, &s.BacklogAgeMS); err!= nil {
		return nil, err
	}
	return &s, nil
}
