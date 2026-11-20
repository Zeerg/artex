package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Zeerg/artex/db"
	"github.com/Zeerg/artex/notify"
)

// This file is the HTTP interface for push functionality. All routes are under requireAuth (see Handler).()),
// Consistent with other management interfaces.

// notifyChannelDTO External representation of the channel.
//
// Config is**Masked**Configuration: The credential field is replaced with notify..MaskedPrefix Value at the beginning.
// Submitting the mask value as-is indicates 'this field hasn't changed', so the server retains the original value from the database.
// (See notify.MergeConfig).
type notifyChannelDTO struct {
	ID int64 `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Enabled bool `json:"enabled"`
	Mode string `json:"mode"`
	Config map[string]any `json:"config"`
	Filter notify.Filter `json:"filter"`
	RatePerMin int `json:"rate_per_min"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// SecretKeys Inform the frontend which fields are credentials, based on this render password boxes and 'leave empty if not changing' prompts.
	// Declared by the channel itself (notify.Channel.SecretKeys), Frontend should not hardcode channel knowledge.
	SecretKeys []string `json:"secret_keys"`
}

// notifyDeliveryDTO External representation of delivery history.
type notifyDeliveryDTO struct {
	ID int64 `json:"id"`
	FindingID int64 `json:"finding_id,string"`
	EventKind string `json:"event_kind"`
	ChannelID int64 `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	ChannelKind string `json:"channel_kind"`
	State string `json:"state"`
	Attempts int `json:"attempts"`
	LastError string `json:"last_error"`
	BatchID *int64 `json:"batch_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	SentAt *time.Time `json:"sent_at,omitempty"`
	NextAttempt time.Time `json:"next_attempt_at"`
	// Message title summary so that the history list can show what this post is about without expanding.
	Title string `json:"title"`
	Severity string `json:"severity"`
}

func toNotifyChannelDTO(ch *db.NotificationChannel) notifyChannelDTO {
	var cfg map[string]any
	if len(ch.Config) > 0 {
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	secrets:= []string{}
	if c, ok:= notify.Get(ch.Kind); ok {
		secrets = c.SecretKeys()
	}
	return notifyChannelDTO{
		ID: ch.ID,
		Name: ch.Name,
		Kind: ch.Kind,
		Enabled: ch.IsEnabled(),
		Mode: ch.Mode,
		Config: notify.MaskConfig(ch.Kind, cfg),
		Filter: notify.ParseFilter(ch.Filter),
		RatePerMin: ch.RatePerMin,
		CreatedAt: ch.CreatedAt,
		UpdatedAt: ch.UpdatedAt,
		SecretKeys: secrets,
	}
}

func toNotifyDeliveryDTO(dl *db.NotificationDelivery) notifyDeliveryDTO {
	snap, _:= parseSnapshot(dl)
	dto:= notifyDeliveryDTO{
		ID: dl.ID,
		FindingID: dl.FindingID,
		EventKind: dl.EventKind,
		ChannelID: dl.ChannelID,
		ChannelName: dl.ChannelName,
		ChannelKind: dl.ChannelKind,
		State: dl.State,
		Attempts: dl.Attempts,
		LastError: dl.LastError,
		BatchID: dl.BatchID,
		CreatedAt: dl.CreatedAt,
		SentAt: dl.SentAt,
		NextAttempt: dl.NextAttemptAt,
		Severity: snap.Severity,
	}
	if snap.Name!= "" {
		dto.Title = snap.Name
	} else {
		dto.Title = snap.VulnClass
	}
	return dto
}

// notifyMeta Return static metadata and global settings needed for the notification page in one request.
// To avoid the frontend making three requests just to render a dropdown box.
func (s *Server) notifyMeta(w http.ResponseWriter, r *http.Request) {
	pg:= s.pg(w)
	if pg == nil {
		return
	}
	kinds:= make([]map[string]any, 0, len(notify.Kinds()))
	for _, k:= range notify.Kinds() {
		ch, _:= notify.Get(k)
		kinds = append(kinds, map[string]any{
			"kind": k,
			"default_rate_per_min": ch.DefaultRatePerMin(),
			"secret_keys": ch.SecretKeys(),
		})
	}
	baseURL, _, _:= pg.GetSetting(settingNotifyPublicBaseURL)
	digest, _, _:= pg.GetSetting(settingNotifyDigestMinutes)
	stats, err:= pg.NotificationStatsSnapshot(r.Context())
	if err!= nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"kinds": kinds,
		"enabled": pg.GetBool(settingNotifyEnabled, true),
		"public_base_url": baseURL,
		"digest_interval_min": digest,
		"defaults": map[string]any{
			"digest_interval_min": notifyDefaultDigestMinutes,
		},
		"stats": stats,
	})
}

func (s *Server) notifyListChannels(w http.ResponseWriter, r *http.Request) {
	pg:= s.pg(w)
	if pg == nil {
		return
	}
	channels, err:= pg.ListNotificationChannels(r.Context())
	if err!= nil {
		writeErr(w, 500, err.Error())
		return
	}
	out:= make([]notifyChannelDTO, 0, len(channels))
	for _, ch:= range channels {
		out = append(out, toNotifyChannelDTO(ch))
	}
	writeJSON(w, 200, map[string]any{"channels": out})
}

// notifyChannelRequest It's the request body for creating/updating channels.
//
// All business fields use pointers to distinguish between 'not transmitted' and 'transmitted zero value': PATCH semantics,
// Fields not provided must retain the original value from the library.
type notifyChannelRequest struct {
	Name *string `json:"name"`
	Kind *string `json:"kind"`
	Enabled *bool `json:"enabled"`
	Mode *string `json:"mode"`
	Config map[string]any `json:"config"`
	Filter *notify.Filter `json:"filter"`
	RatePerMin *int `json:"rate_per_min"`
}

func (s *Server) notifyCreateChannel(w http.ResponseWriter, r *http.Request) {
	pg:= s.pg(w)
	if pg == nil {
		return
	}
	var req notifyChannelRequest
	if err:= json.NewDecoder(r.Body).Decode(&req); err!= nil {
		writeErr(w, 400, "Request body is not valid JSON: "+err.Error())
		return
	}
	if req.Kind == nil ||!notify.ValidKind(*req.Kind) {
		writeErr(w, 400, fmt.Sprintf("channel type is invalid, options:%s", strings.Join(notify.Kinds(), " / ")))
		return
	}
	name:= ""
	if req.Name!= nil {
		name = strings.TrimSpace(*req.Name)
	}
	if name == "" {
		writeErr(w, 400, "Missing channel name")
		return
	}
	channel, _:= notify.Get(*req.Kind)
	if err:= channel.Validate(req.Config); err!= nil {
		writeErr(w, 400, err.Error())
		return
	}
	ch:= &db.NotificationChannel{
		Name: name,
		Kind: *req.Kind,
		Enabled: req.Enabled,
		Mode: db.NotifyModeRealtime,
		RatePerMin: channel.DefaultRatePerMin(),
	}
	if req.Mode!= nil {
		if!db.ValidNotifyMode(*req.Mode) {
			writeErr(w, 400, "Push mode is invalid; options are: realtime / digest")
			return
		}
		ch.Mode = *req.Mode
	}
	if req.RatePerMin!= nil {
		// Use given value explicitly.——Including 0, which means 'no rate limit', is a valid configuration.
		if *req.RatePerMin < 0 {
			writeErr(w, 400, "Rate limit value cannot be negative")
			return
		}
		ch.RatePerMin = *req.RatePerMin
	}
	// Only apply the default value from the channel if the field is missing. The default value must be decided here, not at the db layer:
	// Only the request body can distinguish between 'not passing this field' and 'explicitly passing 0', which have completely different meanings.
	// (Former=Use default, otherwise=Unlimited rate limiting). The db layer treats 0 as unspecified, making unlimited rate limiting configurations unreachable.
	if req.RatePerMin == nil {
		ch.RatePerMin = channel.DefaultRatePerMin()
	}
	if req.Filter!= nil {
		// Write when validating restricted filter fields (like min_severity). See notify.Filter.Validate:
		// Typo in threshold causes filter to silently fail and become a full push; must be blocked at the entry point.
		if err:= req.Filter.Validate(); err!= nil {
			writeErr(w, 400, err.Error())
			return
		}
		raw, _:= json.Marshal(req.Filter)
		ch.Filter = raw
	}
	rawCfg, _:= json.Marshal(req.Config)
	ch.Config = rawCfg

	id, err:= pg.SaveNotificationChannel(r.Context(), ch)
	if err!= nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) notifyUpdateChannel(w http.ResponseWriter, r *http.Request) {
	pg:= s.pg(w)
	if pg == nil {
		return
	}
	id, ok:= pathInt(r, "id")
	if!ok {
		writeErr(w, 400, "Invalid channel id")
		return
	}
	current, err:= pg.NotificationChannelByID(r.Context(), id)
	if err!= nil {
		notifyChannelLookupErr(w, err)
		return
	}
	var req notifyChannelRequest
	if err:= json.NewDecoder(r.Body).Decode(&req); err!= nil {
		writeErr(w, 400, "Request body is not valid JSON: "+err.Error())
		return
	}

	// kind Allow modification, but changing the type means replacing the entire credential field set, which cannot be merged with old configuration.
	kind:= current.Kind
	if req.Kind!= nil {
		if!notify.ValidKind(*req.Kind) {
			writeErr(w, 400, fmt.Sprintf("channel type is invalid, options:%s", strings.Join(notify.Kinds(), " / ")))
			return
		}
		kind = *req.Kind
	}
	channel, _:= notify.Get(kind)

	var stored map[string]any
	if kind == current.Kind {
		if len(current.Config) > 0 {
			_ = json.Unmarshal(current.Config, &stored)
		}
	}
	if stored == nil {
		stored = map[string]any{}
	}
	// use PrepareConfigUpdate instead of raw MergeConfig: when the target address changes, the operator must be made aware
	// Re-identify the credential field, otherwise 'only change address, credentials reused' would send real credentials to the new address.
	merged, err:= notify.PrepareConfigUpdate(kind, stored, req.Config)
	if err!= nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err:= channel.Validate(merged); err!= nil {
		writeErr(w, 400, err.Error())
		return
	}
	rawCfg, _:= json.Marshal(merged)

	ch:= &db.NotificationChannel{
		ID: id,
		Name: current.Name,
		Kind: kind,
		Enabled: current.Enabled,
		Mode: current.Mode,
		Config: rawCfg,
		Filter: current.Filter,
		RatePerMin: current.RatePerMin,
	}
	if req.Name!= nil {
		if ch.Name = strings.TrimSpace(*req.Name); ch.Name == "" {
			writeErr(w, 400, "Channel name cannot be empty")
			return
		}
	}
	if req.Enabled!= nil {
		ch.Enabled = req.Enabled
	}
	if req.Mode!= nil {
		if!db.ValidNotifyMode(*req.Mode) {
			writeErr(w, 400, "Push mode is invalid; options are: realtime / digest")
			return
		}
		ch.Mode = *req.Mode
	}
	if req.RatePerMin!= nil {
		if *req.RatePerMin < 0 {
			writeErr(w, 400, "Rate limit value cannot be negative")
			return
		}
		ch.RatePerMin = *req.RatePerMin
	}
	if req.Filter!= nil {
		if err:= req.Filter.Validate(); err!= nil {
			writeErr(w, 400, err.Error())
			return
		}
		raw, _:= json.Marshal(req.Filter)
		ch.Filter = raw
	}

	// Use SetNotificationChannelEnabled instead of SaveNotificationChannel.
	// To allow 'deactivate' to mark existing queued deliveries as skipped, avoiding re-enabling when receiving
	// Batch of outdated backlog messages.
	enabledChanged:= ch.Enabled!= nil && current.Enabled!= nil && *ch.Enabled!= *current.Enabled
	if enabledChanged {
		// First update the configuration to the database (at this point enabled uses the old value to avoid prematurely triggering skip logic),
		// Switch separately. There is no concurrent window between these two steps: This interface is the only entry point to modify these two fields.
		prev:= ch.Enabled
		ch.Enabled = current.Enabled
		if _, err:= pg.SaveNotificationChannel(r.Context(), ch); err!= nil {
			writeErr(w, 500, err.Error())
			return
		}
		if err:= pg.SetNotificationChannelEnabled(r.Context(), id, *prev); err!= nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"id": id})
		return
	}
	if _, err:= pg.SaveNotificationChannel(r.Context(), ch); err!= nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) notifyDeleteChannel(w http.ResponseWriter, r *http.Request) {
	pg:= s.pg(w)
	if pg == nil {
		return
	}
	id, ok:= pathInt(r, "id")
	if!ok {
		writeErr(w, 400, "Invalid channel id")
		return
	}
	if err:= pg.DeleteNotificationChannel(r.Context(), id); err!= nil {
		notifyChannelLookupErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// notifyTestChannel Send a test message using the currently saved configuration.
//
// Directly calls channel Send without going through the delivery queue: The test's purpose is to immediately tell the user whether this configuration can
// The result is hidden in the delivery history when using a queue; the user has to go through it again to know if it succeeded.
// Therefore this interface is**Synchronize**The timeout upper limit is determined by the HTTP client in the notify package (15 seconds).
func (s *Server) notifyTestChannel(w http.ResponseWriter, r *http.Request) {
	pg:= s.pg(w)
	if pg == nil {
		return
	}
	id, ok:= pathInt(r, "id")
	if!ok {
		writeErr(w, 400, "Invalid channel id")
		return
	}
	ch, err:= pg.NotificationChannelByID(r.Context(), id)
	if err!= nil {
		notifyChannelLookupErr(w, err)
		return
	}
	channel, ok:= notify.Get(ch.Kind)
	if!ok {
		writeErr(w, 400, fmt.Sprintf("Channel Type %q Unregistered", ch.Kind))
		return
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if err:= channel.Validate(cfg); err!= nil {
		writeErr(w, 400, err.Error())
		return
	}
	msg:= notifyTestMessage(s.notifierBaseURL(pg))
	start:= time.Now()
	// Test message has only one delivery; no need for channel length limit on a single message.
	// Handled by truncation fallback, not involving segments.
	if _, err:= channel.Send(r.Context(), cfg, msg); err!= nil {
		// Return the original error from the channel to the user truthfully.——This is their only clue for debugging configuration.
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok": true,
		"latency_ms": time.Since(start).Milliseconds(),
	})
}

// notifyTestMessage Construct a test message. Deliberately use content that can be immediately recognized as a test:
// The person who received it should not misjudge it as a real vulnerability.
func notifyTestMessage(baseURL string) notify.Message {
	return notify.Message{
		Items: []notify.Item{{
			FindingID: 0,
			Name: "Test message · Channel configuration is normal",
			VulnClass: "Connectivity Test",
			Severity: "low",
			Summary: "This is a test message from ARTEX's push channel. Receiving it indicates that the channel configuration is available.",
			Assets: []string{"artex.example.com"},
			DetailURL: baseURL,
		}},
		HomeURL: baseURL,
	}
}

// notifierBaseURL External address used for backlink reading.
func (s *Server) notifierBaseURL(pg *db.DB) string {
	v, _, _:= pg.GetSetting(settingNotifyPublicBaseURL)
	return trimTrailingSlash(v)
}

func (s *Server) notifyListDeliveries(w http.ResponseWriter, r *http.Request) {
	pg:= s.pg(w)
	if pg == nil {
		return
	}
	f:= db.NotificationDeliveryFilter{
		State: r.URL.Query().Get("state"),
		EventKind: r.URL.Query().Get("event_kind"),
	}
	if v:= r.URL.Query().Get("channel_id"); v!= "" {
		f.ChannelID = int64(atoiDefault(v, 0))
	}
	page:= queryInt(r, "page", 1)
	pageSize:= queryInt(r, "page_size", 50)
	items, total, err:= pg.ListNotificationDeliveries(r.Context(), f, page, pageSize)
	if err!= nil {
		writeErr(w, 500, err.Error())
		return
	}
	out:= make([]notifyDeliveryDTO, 0, len(items))
	for _, dl:= range items {
		out = append(out, toNotifyDeliveryDTO(dl))
	}
	writeJSON(w, 200, map[string]any{"deliveries": out, "total": total, "page": page, "page_size": pageSize})
}

func (s *Server) notifyRetryDelivery(w http.ResponseWriter, r *http.Request) {
	pg:= s.pg(w)
	if pg == nil {
		return
	}
	id, ok:= pathInt(r, "id")
	if!ok {
		writeErr(w, 400, "Submission ID invalid")
		return
	}
	if err:= pg.RetryNotificationDelivery(r.Context(), id); err!= nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// notifyChannelLookupErr Translate 'channel not found' to 404, other errors to 500.
func notifyChannelLookupErr(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrNotificationChannelNotFound) {
		writeErr(w, 404, "Notification channel does not exist")
		return
	}
	writeErr(w, 500, err.Error())
}
