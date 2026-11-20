package notify

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Channel It is an adapter for notification channels. Implementations must**Stateless**: The same instance will be accessed by multiple channels
// Configure concurrent reuse, credentials must be passed through cfg parameter.
type Channel interface {
	// Kind Return channel type identifier, must match the key in the registry.
	Kind() string
	// Validate Called when saving configuration to validate required fields and format. Errors returned will be directly displayed to
	// The configurator, so the text should explain 'which field is missing' instead of a generic 'configuration invalid'.
	Validate(cfg map[string]any) error
	// Send Send one message, return**Actual number of delivered entries**with error.
	//
	// Why return count: Each platform has a message length limit; aggregated messages may be truncated when they don't fit in batches.
	// If the caller unconditionally marks the entire batch as delivered, those items that were intercepted will disappear.——Message does not show
	// Also shows success in delivery history; nowhere can you find a vulnerability that was never sent. After returning kept,
	// The caller only marks the first kept entries, leaving the rest for the next batch.
	//
	// Returns an error indicating delivery failure, where *PermanentError Indicates no retry.
	// Failure kept is meaningless, the caller should ignore it.
	Send(ctx context.Context, cfg map[string]any, m Message) (int, error)
	// DefaultRatePerMin Return the official recommended maximum deliveries per minute for this channel as a new channel instance
	// Default rate limit value at the time. Returning 0 indicates no known limits.
	DefaultRatePerMin() int
	// SecretKeys Return the key names belonging to credentials in this channel configuration. The values of these keys will be masked when the API echoes back.
	// When updating, if a mask value is received, retain the original value in the library. Only implementers should know which fields are credentials
	// (The entire Webhook address of Enterprise WeChat is the credential, while DingTalk's is just the secret),
	// So this knowledge must be provided by the channel, not guessed by the upper layer.
	SecretKeys() []string
	// DestinationKeys Return the key name in the channel configuration that decides 'where messages go'.
	//
	// Just like SecretKeys, it is a security-related item: the target address and credentials are two independent fields,
	// If allowing 'changing address while keeping credentials unchanged', anyone who can change channel config can steal real credentials from the database
	// Sent to your own controlled server; the mask configured for the channel loses its meaning.
	// See PrepareConfigUpdate for details.
	DestinationKeys() []string
}

// registry It's a channel registry. Deliberately using explicit literals instead of init() Self-registration: so 'what are the channels'
// See everything from one place, and new channels will expose omissions during compilation rather than relying on runtime side effects.
var registry = map[string]Channel{
	KindDingTalk: dingTalkChannel{},
	KindFeishu: feishuChannel{},
	KindWeCom: weComChannel{},
	KindWebhook: webhookChannel{},
	KindTelegram: telegramChannel{},
	KindEmail: emailChannel{},
}

// Get Get channel implementation by type.
func Get(kind string) (Channel, bool) {
	c, ok:= registry[kind]
	return c, ok
}

// ValidKind Verify if the report kind is a supported channel type.
func ValidKind(kind string) bool {
	_, ok:= registry[kind]
	return ok
}

// Kinds Return all supported channel types in dictionary order (for stable display in UI dropdown).
func Kinds() []string {
	out:= make([]string, 0, len(registry))
	for k:= range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PermanentError Mark a failed submission that shouldn't be retried: credential error, target rejection, illegal request body, etc.
// Retries only make sense for transient faults (network jitter, rate limiting, 429, 5xx from the other end); for permanent failures, repeatedly back off and retry
// It will neither succeed nor mask the true error in the retry logs.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent Mark err as permanent failure. When err is nil, return nil,
// Conveniently Written as, `return Permanent(someCheck())`.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent Check if the error chain carries a permanent failure mark.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// ---- Configuration read helper ----
//
// Channel configuration from database JSONB column, deserialized by encoding/json is map.[string]any,
// All values are float64, arrays are []any. These helper functions unify this layer of conversion and tolerate user
// Type deviation caused by leaving empty in the UI (like filling a port as a string).

// cfgString Take string configuration items and trim leading/trailing whitespaces.——It's easy to carry over with web form copy-paste.
func cfgString(cfg map[string]any, key string) string {
	v, ok:= cfg[key]
	if!ok {
		return ""
	}
	s, ok:= v.(string)
	if!ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// cfgInt Integer configuration item, compatible with float64 (JSON default) and string sources.
func cfgInt(cfg map[string]any, key string) int {
	switch v:= cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err:= strconv.Atoi(strings.TrimSpace(v))
		if err!= nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// cfgBool Retrieve boolean configuration item, compatible with string "true"/"1".
func cfgBool(cfg map[string]any, key string) bool {
	switch v:= cfg[key].(type) {
	case bool:
		return v
	case string:
		s:= strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}

// cfgStrings Fetch string array configuration item, automatically trim whitespace and discard empty strings.
func cfgStrings(cfg map[string]any, key string) []string {
	raw, ok:= cfg[key].([]any)
	if!ok {
		// Also accepts a single string for convenience when submitting forms with only one value.
		if s:= cfgString(cfg, key); s!= "" {
			return []string{s}
		}
		return nil
	}
	out:= make([]string, 0, len(raw))
	for _, v:= range raw {
		s, ok:= v.(string)
		if!ok {
			continue
		}
		if s = strings.TrimSpace(s); s!= "" {
			out = append(out, s)
		}
	}
	return out
}

// cfgMap Take string mapping configuration items (like custom HTTP headers), trim whitespace from keys, discard empty keys.
func cfgMap(cfg map[string]any, key string) map[string]string {
	raw, ok:= cfg[key].(map[string]any)
	if!ok {
		return nil
	}
	out:= make(map[string]string, len(raw))
	for k, v:= range raw {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		s, ok:= v.(string)
		if!ok {
			continue
		}
		out[k] = s
	}
	return out
}
