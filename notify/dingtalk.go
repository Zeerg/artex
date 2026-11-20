package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
)

// dingTalkChannel Implement DingTalk custom robot.
//
// Platform features (determining the implementation choices here):
// - Single robot rate limit 20 per minute, overage is silently discarded (HTTP may still return 200),
// So rate limiting must be done on the client side, see DefaultRatePerMin.
// - Security settings three options: sign / custom keyword / IP whitelist. Signing is the only one not dependent
// The message content scheme, so it only supports signing (also supports bare webhooks with none of the three enabled).
// - Both success and failure return HTTP 200, distinguished by errcode in the body.——Do not check errcode
// It will record delivery failures as successes.
type dingTalkChannel struct{}

func (dingTalkChannel) Kind() string { return KindDingTalk }

func (dingTalkChannel) DefaultRatePerMin() int { return 20 }

// The DingTalk Webhook address includes access_token, which is itself a credential, so the entire thing is masked.
func (dingTalkChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// The target is the DingTalk Webhook URL itself; changing the address must also re-endorse and sign the new address.
func (dingTalkChannel) DestinationKeys() []string { return []string{"webhook"} }

func (dingTalkChannel) Validate(cfg map[string]any) error {
	hook:= cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("Missing Webhook address")
	}
	if err:= validateHTTPURL(hook); err!= nil {
		return fmt.Errorf("Webhook Invalid address: %w", err)
	}
	return nil
}

// Send Deliver one message. Use ActionCard (with buttons) if there's a backlink and it's a single item, otherwise use markdown.
func (c dingTalkChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	hook:= cfgString(cfg, "webhook")
	if err:= c.Validate(cfg); err!= nil {
		return 0, Permanent(err)
	}
	endpoint, err:= dingTalkSignedURL(hook, cfgString(cfg, "secret"), time.Now())
	if err!= nil {
		return 0, Permanent(err)
	}

	title:= markdownTitle(m)
	// DingTalk markdown body has no explicit byte limit, but still enforces a limit to prevent abnormal expansion of evidence fields.
	text, kept:= markdownBody(m, 20000)

	var payload any
	if!m.Batch && len(m.Items) == 1 && m.Items[0].DetailURL!= "" {
		payload = map[string]any{
			"msgtype": "actionCard",
			"actionCard": map[string]any{
				"title": title,
				"text": text,
				"btnOrientation": "0",
				"singleTitle": "view details",
				"singleURL": m.Items[0].DetailURL,
			},
		}
	} else {
		payload = map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]any{"title": title, "text": text},
		}
	}

	raw, err:= doJSON(ctx, "POST", endpoint, nil, payload)
	if err!= nil {
		return 0, err
	}
	// DingTalk hides business errors in a 200 response.
	var res struct {
		ErrCode int `json:"errcode"`
		ErrMsg string `json:"errmsg"`
	}
	if err:= json.Unmarshal(raw, &res); err!= nil {
		return 0, fmt.Errorf("Failed to parse DingTalk response: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode!= 0 {
		// 301000 Signature verification failed, 310000 is keyword mismatch——All are configuration errors,
		// Retrying will not heal.
		return 0, Permanent(fmt.Errorf("DingTalk returned error %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}

// dingTalkSignedURL Append timestamp and sign parameters to the webhook according to the official signing rules.
//
// Rule: Pending signature string = timestamp + "\n" + secret, HMAC-SHA256 To**The key is also secret.**,
// Result base64 encoded and URL encoded. Timestamp is in milliseconds. Secret returns as-is when empty,
// To support robots without signature verification.
func dingTalkSignedURL(hook, secret string, now time.Time) (string, error) {
	if secret == "" {
		return hook, nil
	}
	ts:= strconv.FormatInt(now.UnixMilli(), 10)
	mac:= hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign:= base64.StdEncoding.EncodeToString(mac.Sum(nil))

	u, err:= url.Parse(hook)
	if err!= nil {
		// Do not pass through err: url..Parse Error text should include the full address (including access_token).
		return "", fmt.Errorf("Failed to parse Webhook address.: %s", redactRequestTarget(hook))
	}
	q:= u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// validateHTTPURL Verify address availability, protocol support, and perform an internal network judgment on literal IP targets.
//
// Two points to note:
//
// 1. **Error messages must be redacted**. url.Parse What is returned by itself *url.Error, Its Error() With
// **Full original address.**, and in these addresses of these few, credentials (DingTalk access_token,
// WeChat key, Telegram bot token, Feishu hook id). Previously, this directly `return err`,
// So the error 'invalid address format' takes the credentials with it, flowing to the test interface's 400 response,
// Deliver the last_error of storing data, server logs, and delivery history interface each time.
//
// 2. **Literal IP directly judged as internal**, Domain left for dialing stage judgment (blockInternalDial is the final one).
// The point of effect, which can also override DNS rebinding). This is done once so that when saving the configuration, a prompt can be obtained,
// Instead of waiting for the first delivery failure.
//
// Limiting protocols is defensive: file.:///gopher:// Such as making http.Client Produces Unexpected
// Behavior (although blocked by scheme check, there's no reason to relax this aspect).
func validateHTTPURL(raw string) error {
	u, err:= url.Parse(raw)
	if err!= nil {
		return fmt.Errorf("Address cannot be resolved (%s)", redactRequestTarget(raw))
	}
	if u.Scheme!= "http" && u.Scheme!= "https" {
		return fmt.Errorf("Only supports http/https, received %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("Missing hostname")
	}
	if ip:= net.ParseIP(u.Hostname()); ip!= nil && isBlockedDialIP(ip) &&!allowLocalTargets() {
		return fmt.Errorf("Refuse delivery to this machine/local network address. %s (If delivery to local services is required, set %s=1)", ip, AllowLocalTargetsEnv)
	}
	return nil
}
