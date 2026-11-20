package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// This file handles the claiming and status transition of delivery tasks.
//
// It uses 'lease' instead of long transactions: it sets the row to sending and pushes next_attempt_at into the future.
// Lease expiration time, do network delivery after submitting the transaction. This way, no database lock is held during delivery——
// Network requests may take seconds (client timeout 15 seconds), holding row locks prevents other write operations on the same database from proceeding.
//
// The cost is that if the process crashes during delivery, it will stop at sending. This is**Self-healing**: After lease expiration
// next_attempt_at Falling back to the past, the next round of claiming will pick up the same row again (see claim conditions).
// state IN ('pending','sending')). Retry count already when claimed +1, So crashes won't cause
// Infinite retries——MaxNotifyAttempts After the second attempt, it falls into failed and requires manual handling.

// MaxNotifyAttempts This is the maximum number of attempts per delivery (including the first).
// Defined here rather than in the delivery engine: It's a strategy of the state machine itself, the engine is just the executor.
const MaxNotifyAttempts = 3

// MaxDigestBatchSize What is the maximum number of submissions that can be merged in a single batch.
//
// The reason is resources: within a summary cycle, if several ten thousand vulnerabilities are found (completely possible——Full scan once
// This allows it to do so), without an upper limit, claiming all rows into memory and rendering them as one extremely long message,
// Then truncated by half due to the channel length limit——Wastes memory and**Silent Loss**Vulnerabilities that were truncated.
// After setting an upper bound, excess parts stay in the database as the next batch, naturally sent out in the next cycle, won't be lost.
//
// The basis for choosing 500: it renders as a message within the WeChat Work 4096-byte limit."Content is readable"The scale of;
// Even larger, it just delays where the truncation occurs.
const MaxDigestBatchSize = 500

// NotificationDelivery A delivery task containing rendering required channel configuration and event snapshot.
type NotificationDelivery struct {
	ID int64 `json:"id"`
	EventID int64 `json:"event_id"`
	ChannelID int64 `json:"channel_id"`
	State string `json:"state"`
	Attempts int `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
	LastError string `json:"last_error"`
	BatchID *int64 `json:"batch_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	SentAt *time.Time `json:"sent_at,omitempty"`
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
	// Jointly loaded rendering context, no JSON (assembled by server layer DTO).
	Channel *NotificationChannel `json:"-"`
	// FindingID/EventKind From events to allow direct jump to vulnerability details in historical lists.
	FindingID int64 `json:"finding_id,string"`
	EventKind string `json:"event_kind"`
	// ChannelName/ChannelKind Redundant field for list display, saving frontend from secondary queries.
	ChannelName string `json:"channel_name"`
	ChannelKind string `json:"channel_kind"`
}

const notificationDeliveryCols = `d.id, d.event_id, d.channel_id, d.state, d.attempts, d.next_attempt_at,
       d.last_error, d.batch_id, d.created_at, d.sent_at`

// joinedDeliveryQuery It's the unified read shape for submitted lines: submit. + event snapshot + Channel configuration.
// Rendering a message requires all three parts; querying separately will result in three round trips.
const joinedDeliveryQuery = `SELECT ` + notificationDeliveryCols + `,
       e.snapshot, e.kind, e.finding_id,
       c.id, c.name, c.kind, c.enabled, c.config, c.mode, c.filter, c.rate_per_min
FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id
JOIN notification_channels c ON c.id = d.channel_id`

func scanNotificationDelivery(sc interface{ Scan(...any) error }) (*NotificationDelivery, error) {
	var (
		dl NotificationDelivery
		lastErr sql.NullString
		batchID sql.NullInt64
		sentAt sql.NullTime
		snapshot []byte
		eventKind string
		channel NotificationChannel
		chEnabled bool
	)
	if err:= sc.Scan(&dl.ID, &dl.EventID, &dl.ChannelID, &dl.State, &dl.Attempts, &dl.NextAttemptAt,
		&lastErr, &batchID, &dl.CreatedAt, &sentAt,
		&snapshot, &eventKind, &dl.FindingID,
		&channel.ID, &channel.Name, &channel.Kind, &chEnabled, &channel.Config, &channel.Mode, &channel.Filter, &channel.RatePerMin); err!= nil {
		return nil, err
	}
	dl.LastError = lastErr.String
	if batchID.Valid {
		dl.BatchID = &batchID.Int64
	}
	if sentAt.Valid {
		dl.SentAt = &sentAt.Time
	}
	dl.Snapshot = json.RawMessage(snapshot)
	dl.EventKind = eventKind
	dl.ChannelName = channel.Name
	dl.ChannelKind = channel.Kind
	channel.Enabled = &chEnabled
	dl.Channel = &channel
	return &dl, nil
}

// claimQuery Describe one claim: first select candidates and lock them, then set them as sending and
// Extend lease. The position of lease in sel is determined by the caller $n Placeholder and pass parameters on its own.
type claimQuery struct {
	sql string
	args []any
}

// ClaimRealtimeDeliveries Claim a batch of expired real-time deliveries from a channel, up to limit items.
//
// Intentionally press**Single channel**Claim instead of 'claim a batch globally and then pick': rate limit gate in delivery engine by channel
// Maintenance — only know how many messages this channel can send in this round before going for the same amount of rows, rate limiting will not consume
// Retry count. If reversed and claimed then discarded, lines already counted once attempts are blocked by rate limiting,
// 3 The budget will be completely exhausted by pure waiting and end up in failed.
//
// Condition contains 'expired lease sending'——That's where it recovers from crashes. Lease must be significantly greater than a single
// Worst-case submission time (channel HTTP client timeout 15 seconds), otherwise the same line will be processed by two dispatchers
// Deliver simultaneously. Block disabled channels: disabling marks existing deliveries as skipped,
// Add another layer of defense here to avoid missing the net when disabling and claiming concurrently.
func (d *DB) ClaimRealtimeDeliveries(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	return d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now()
  AND c.enabled AND c.mode = $4
ORDER BY dd.next_attempt_at, dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $5`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, NotifyModeRealtime, limit},
	}, nil)
}

// DigestBatchDue Report whether this channel has accumulated enough batches for delivery: there are pending deliveries and**The oldest one**
// Age has reached the summary cycle.
//
// The basis for judgment is the age of the oldest delivery rather than wall clock time: this prevents newly created channels from being affected by alignment to the hour.
// Immediately spit out a single 'summary', even if there are long-batched tasks that will not wait another round.
//
// Separated from ClaimDigestBatch because of different semantics: This function only answers 'should it be sent',
// While claiming to take this channel**Refresh**Pending release (including those not yet old enough)——Otherwise one cycle
// It will be split into multiple messages, losing its meaning when aggregated.
func (d *DB) DigestBatchDue(ctx context.Context, channelID int64, minAge time.Duration) (bool, error) {
	var due bool
	err:= d.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM notification_deliveries d
  JOIN notification_channels c ON c.id = d.channel_id
  WHERE d.channel_id = $1 AND d.state IN ($2,$3) AND c.enabled
  GROUP BY d.channel_id
  HAVING min(d.created_at) <= now() - make_interval(secs => $4)
)`, channelID, NotifyStatePending, NotifyStateSending, int64(minAge.Seconds())).Scan(&due)
	return due, err
}

// ClaimDigestBatch Claim the current pending deliveries of a channel as a batch.
// A single batch can have at most MaxDigestBatchSize items.
//
// All deliveries in the same batch share batch_id, using the smallest id in the set (stable, readable,
// No additional serialization). When retrying, use COALESCE to retain the original batch number, making 'these N are sent together'
// Remains true after multiple retries.
//
// Fetch the first N records in ascending order by id rather than randomly: the earliest generated delivery will be sent out first, avoiding accumulation issues.
// 'New vulnerabilities first, old vulnerabilities always at the back' of hunger.
func (d *DB) ClaimDigestBatch(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	// limit is**Memory Upper Bound**, The caller passes MaxDigestBatchSize; add another constraint here,
	// To prevent the caller from passing in a larger value.
	//
	// Intentionally not accepting 'rate limit' as batch size: rate limiting units are message counts.——One batch only sends.
	// One message consumes one token, deducted by the server layer's takeTokens——For 'one-time use'
	// 'Vulnerability' refers to two different units of measurement. Once, to make rate_per_min effective for digest, every round
	// Request budget passed in as batch size, resulting rate.=20/min Each channel installs only one vulnerability per batch.
	// digest Degradate to real-time push with summary text. To change rate limiting, modify 'takeTokens''s 'want'.
	// Do not touch here.
	if limit > MaxDigestBatchSize {
		limit = MaxDigestBatchSize
	}
	out, err:= d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now() AND c.enabled
ORDER BY dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $4`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, limit},
	}, func(tx *sql.Tx, ids []int64) error {
		batchID:= ids[0]
		for _, id:= range ids {
			if id < batchID {
				batchID = id
			}
		}
		ph, idArgs:= placeholders(2, ids)
		_, err:= tx.ExecContext(ctx, `UPDATE notification_deliveries SET batch_id = COALESCE(batch_id, $1)
WHERE id IN (`+ph+`)`, append([]any{batchID}, idArgs...)...)
		return err
	})
	return out, err
}

// claimDeliveries Execute «select + Setting sending to extend lease. + Read the full line within a single transaction.
// postClaim This is an optional additional step (used to write batch_id in summary batches).
func (d *DB) claimDeliveries(ctx context.Context, lease time.Duration, cq claimQuery, postClaim func(*sql.Tx, []int64) error) ([]*NotificationDelivery, error) {
	tx, err:= d.BeginTx(ctx, nil)
	if err!= nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // Submission is no-op after success

	ids, err:= selectForClaim(ctx, tx, cq.sql, cq.args...)
	if err!= nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, tx.Commit()
	}
	// Set sending and push next_attempt_at to the future: this future moment is the lease expiration time,
	// 'The lease has not expired and the retry time has not arrived, so they share the same condition expression. No new column is needed.
	ph, idArgs:= placeholders(3, ids)
	if _, err:= tx.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=attempts+1, next_attempt_at=now()+make_interval(secs => $2)
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateSending, lease.Seconds()}, idArgs...)...); err!= nil {
		return nil, err
	}
	if postClaim!= nil {
		if err:= postClaim(tx, ids); err!= nil {
			return nil, err
		}
	}
	out, err:= loadDeliveriesTx(ctx, tx, ids)
	if err!= nil {
		return nil, err
	}
	return out, tx.Commit()
}

func selectForClaim(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err:= tx.QueryContext(ctx, query, args...)
	if err!= nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err:= rows.Scan(&id); err!= nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func loadDeliveriesTx(ctx context.Context, tx *sql.Tx, ids []int64) ([]*NotificationDelivery, error) {
	ph, args:= placeholders(1, ids)
	rows, err:= tx.QueryContext(ctx, joinedDeliveryQuery+` WHERE d.id IN (`+ph+`) ORDER BY d.id`, args...)
	if err!= nil {
		return nil, err
	}
	defer rows.Close()
	out:= []*NotificationDelivery{}
	for rows.Next() {
		dl, err:= scanNotificationDelivery(rows)
		if err!= nil {
			return nil, err
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

// MarkDeliveriesSent Mark a batch of deliveries as delivered.
func (d *DB) MarkDeliveriesSent(ctx context.Context, ids []int64) error {
	ph, args:= placeholders(2, ids)
	if len(args) == 0 {
		return nil
	}
	_, err:= d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, sent_at=now(), last_error='' WHERE id IN (`+ph+`)`, append([]any{NotifyStateSent}, args...)...)
	return err
}

// RescheduleDeliveries Return a batch of deliveries to 'pending' and push back the retry time.
//
// Returning to pending instead of introducing a new intermediate state is to make 'how many attempts left' controlled by one place
// Expression (MaxNotifyAttempts), to avoid the state machine's branches expanding with retry strategies.
func (d *DB) RescheduleDeliveries(ctx context.Context, ids []int64, delay time.Duration, errMsg string) error {
	ph, args:= placeholders(4, ids)
	if len(args) == 0 {
		return nil
	}
	_, err:= d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, next_attempt_at=now()+make_interval(secs => $2), last_error=$3
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, delay.Seconds(), truncateNotifyError(errMsg)}, args...)...)
	return err
}

// DeferDeliveries Batch return to pending, immediately available again, and**Revoke the attempt counted when claiming**.
//
// Purpose is single: when summarizing messages and segmenting them by channel length limits, entries not fitting this batch should be carried over to the next batch.
// That was not a failure, so it should not consume retry budget.——Attempts are already optimistically incremented when claiming. +1 Done,
// This must be subtracted. Otherwise, a backlog of 500 items would be cut into 25 segments of 20 each.
// The final entry is marked as failed by MaxNotifyAttempts in the third segment, but they never produced any errors.
//
// GREATEST(...,0) Catch the case where someone manually resends and clears attempts, then walks back here.
// Prevent count from becoming negative.
func (d *DB) DeferDeliveries(ctx context.Context, ids []int64, reason string) error {
	ph, args:= placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err:= d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=GREATEST(attempts-1, 0), next_attempt_at=now(), last_error=$2
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, truncateNotifyError(reason)}, args...)...)
	return err
}

// FailDeliveries Mark a batch of deliveries as ultimately failed and wait for manual resending in the delivery history.
func (d *DB) FailDeliveries(ctx context.Context, ids []int64, errMsg string) error {
	// Placeholder from $3 Start:$1 It is state,$2 Is last_error.
	ph, args:= placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err:= d.ExecContext(ctx, `UPDATE notification_deliveries SET state=$1, last_error=$2 WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateFailed, truncateNotifyError(errMsg)}, args...)...)
	return err
}

// RetryNotificationDelivery Manually resend a delivery: reset to pending, clear retry count,
// Immediately expired. Clear count is intentional——Manually clicking 'Resend' means that the reasons for previous failures have been processed,
// Retaining old count limits it doesn't make sense.
func (d *DB) RetryNotificationDelivery(ctx context.Context, id int64) error {
	res, err:= d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$2, attempts=0, next_attempt_at=now(), last_error=''
WHERE id=$1 AND state IN ($3,$4)`, id, NotifyStatePending, NotifyStateFailed, NotifyStateSkipped)
	if err!= nil {
		return err
	}
	if n, _:= res.RowsAffected(); n == 0 {
		return fmt.Errorf("Submit %d Does not exist or the current status does not allow resending", id)
	}
	return nil
}

// NotificationDeliveryFilter Is a query condition for delivery history.
type NotificationDeliveryFilter struct {
	ChannelID int64
	State string
	EventKind string
}

func (f NotificationDeliveryFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.ChannelID > 0 {
		args = append(args, f.ChannelID)
		conds = append(conds, fmt.Sprintf("d.channel_id=$%d", len(args)))
	}
	if f.State!= "" {
		args = append(args, f.State)
		conds = append(conds, fmt.Sprintf("d.state=$%d", len(args)))
	}
	if f.EventKind!= "" {
		args = append(args, f.EventKind)
		conds = append(conds, fmt.Sprintf("e.kind=$%d", len(args)))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListNotificationDeliveries Paginated return of delivery history, new ones first.
func (d *DB) ListNotificationDeliveries(ctx context.Context, f NotificationDeliveryFilter, page, pageSize int) ([]*NotificationDelivery, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	where, args:= f.where()

	var total int
	if err:= d.QueryRowContext(ctx, `SELECT count(*) FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id`+where, args...).Scan(&total); err!= nil {
		return nil, 0, err
	}

	q:= fmt.Sprintf("%s%s ORDER BY d.id DESC LIMIT $%d OFFSET $%d",
		joinedDeliveryQuery, where, len(args)+1, len(args)+2)
	rows, err:= d.QueryContext(ctx, q, append(args, pageSize, (page-1)*pageSize)...)
	if err!= nil {
		return nil, 0, err
	}
	defer rows.Close()
	out:= []*NotificationDelivery{}
	for rows.Next() {
		dl, err:= scanNotificationDelivery(rows)
		if err!= nil {
			return nil, 0, err
		}
		out = append(out, dl)
	}
	return out, total, rows.Err()
}

// truncateNotifyError Truncate error information to a length acceptable by the column. The response body returned by the channel may be very long
// (Especially when hitting a generic Webhook on self-hosted services. Not truncating would cause the history list payload to bloat.
func truncateNotifyError(msg string) string {
	const max = 500
	if len(msg) <= max {
		return msg
	}
	// Back off at character boundaries to avoid leaving half a UTF-8 character causing frontend display issues.
	cut:= max
	for cut > 0 &&!isUTF8Start(msg[cut]) {
		cut--
	}
	return msg[:cut] + "…"
}

func isUTF8Start(b byte) bool { return b&0xC0!= 0x80 }

// placeholders Generate from start $n Placeholder strings and corresponding parameters for IN (...) Use.
// For example, start=3, ids=[7,8] → "$3,$4", [7,8].
func placeholders(start int, ids []int64) (string, []any) {
	ph:= make([]string, 0, len(ids))
	args:= make([]any, 0, len(ids))
	for i, id:= range ids {
		ph = append(ph, fmt.Sprintf("$%d", start+i))
		args = append(args, id)
	}
	return strings.Join(ph, ","), args
}
