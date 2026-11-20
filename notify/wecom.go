package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// weComMarkdownLimit It is the hard limit (in bytes, not characters) for WeChat Work robot markdown content.
// This is the tightest restriction among all six channels, also the main reason for TruncateBytes.
const weComMarkdownLimit = 4096

// weComChannel Implement enterprise WeChat robot.
//
// Platform features:
// - Only authenticated via URL key, does not support signing——So the webhook address itself is all the credentials.
// - markdown content Limit 4096 **bytes**, Extremely long strings are rejected (not truncated). Chinese 3 bytes/char,
// Means the body can only have about 1000 characters, must be truncated by the client.
// - Rate limit 20 per minute, also relying on client-side rate limiting to catch up.
type weComChannel struct{}

func (weComChannel) Kind() string { return KindWeCom }

func (weComChannel) DefaultRatePerMin() int { return 20 }

// Enterprise WeChat has only one credential (key in URL) and does not support signing.——
// The entire address is all credentials, no other fields need masking.
func (weComChannel) SecretKeys() []string { return []string{"webhook"} }

// WeChat only has one field for Webhook, it's both the destination and credentials, so there is no concept of 'credentials left after changing address'.
func (weComChannel) DestinationKeys() []string { return []string{"webhook"} }

func (weComChannel) Validate(cfg map[string]any) error {
	hook:= cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("Missing Webhook address")
	}
	if err:= validateHTTPURL(hook); err!= nil {
		return fmt.Errorf("Webhook Invalid address: %w", err)
	}
	return nil
}

func (c weComChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err:= c.Validate(cfg); err!= nil {
		return 0, Permanent(err)
	}
	// Batch summary may be long (50 items × each on a line + Prefix), 4096 bytes are easy to exceed.
	// Truncation is done here instead of relying on the platform to report errors: being rejected means this batch is all lost, while truncation ensures at least the first few messages reach their destination.
	content, kept:= markdownBody(m, weComMarkdownLimit)
	payload:= map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]any{"content": content},
	}
	raw, err:= doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err!= nil {
		return 0, err
	}
	var res struct {
		ErrCode int `json:"errcode"`
		ErrMsg string `json:"errmsg"`
	}
	if err:= json.Unmarshal(raw, &res); err!= nil {
		return 0, fmt.Errorf("Failed to parse WeChat Enterprise response: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode!= 0 {
		// 45009 API call exceeds limit——the platform's rate limit window rolling, and retrying after backoff is valid,
		// So explicitly marked as retryable. Reaching here means the client's rate_per_min is set too aggressively,
		// Retrying is just a fallback; true fixes involve lowering the rate limit for this channel.
		if res.ErrCode == 45009 {
			return 0, fmt.Errorf("Enterprise WeChat rate limit %d: %s", res.ErrCode, res.ErrMsg)
		}
		// 93000 The webhook key is invalid.——Permanent failure, retries will not heal.
		return 0, Permanent(fmt.Errorf("Enterprise WeChat returned error %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}
