package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// telegramTextLimit It's Telegram sendMessage's text field limit (character count).
const telegramTextLimit = 4096

// telegramChannel Implement Telegram Bot API.
//
// Platform features:
// - Authentication is all in the URL path (e.g., /bot).<token>/sendMessage), No need for signature.
// - Use HTML parsing mode instead of MarkdownV2: MarkdownV2 requires escaping `_*[]()~`>#+-=|{}.!`
// 18 characters total, missing one rejects the entire message; HTML only needs to be escaped & < > Three.
// - Business errors are also hidden in HTTP 200 responses, determined by the ok field.
type telegramChannel struct{}

func (telegramChannel) Kind() string { return KindTelegram }

// Telegram Single chat 1 message/second, group 20 messages/minute. Take conservative value.
func (telegramChannel) DefaultRatePerMin() int { return 20 }

// Bot Token Is a complete credential; chat_id is just the recipient, not secret (it can't send messages without Token).
func (telegramChannel) SecretKeys() []string { return []string{"bot_token"} }

// base_url Decide which API endpoint the Token is sent to (e.g., custom reverse proxy), changing it requires re-attesting the Token.
func (telegramChannel) DestinationKeys() []string { return []string{"base_url"} }

func (telegramChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "bot_token") == "" {
		return errors.New("Missing Bot Token.")
	}
	if cfgString(cfg, "chat_id") == "" {
		return errors.New("Missing Chat ID")
	}
	if base:= cfgString(cfg, "base_url"); base!= "" {
		if err:= validateHTTPURL(base); err!= nil {
			return fmt.Errorf("API Invalid address: %w", err)
		}
	}
	return nil
}

func (c telegramChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err:= c.Validate(cfg); err!= nil {
		return 0, Permanent(err)
	}
	endpoint, err:= telegramEndpoint(cfg)
	if err!= nil {
		return 0, Permanent(err)
	}
	text, kept:= telegramHTML(m)
	payload:= map[string]any{
		"chat_id": cfgString(cfg, "chat_id"),
		"text": text,
		"parse_mode": "HTML",
		"disable_web_page_preview": false,
	}
	raw, err:= doJSON(ctx, "POST", endpoint, nil, payload)
	if err!= nil {
		return 0, err
	}
	var res struct {
		OK bool `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	if err:= json.Unmarshal(raw, &res); err!= nil {
		return 0, fmt.Errorf("Failed to parse Telegram response.: %w (%s)", err, snippet(raw))
	}
	if res.OK {
		return kept, nil
	}
	// 429 Rate limiting; backoff then retry works; others (400 parameter error, 401 token error, 403 blacklisted,
	// 404 chat These are configuration issues, retries will not self-heal.
	if res.ErrorCode == 429 {
		return 0, fmt.Errorf("Telegram Rate Limit: %s", res.Description)
	}
	return 0, Permanent(fmt.Errorf("Telegram Return Error %d: %s", res.ErrorCode, res.Description))
}

// telegramEndpoint Concatenate the sendMessage address. Use the official API when base_url is left empty.
// Used when building a custom Bot API reverse proxy (a common need in domestic networks).
func telegramEndpoint(cfg map[string]any) (string, error) {
	base:= cfgString(cfg, "base_url")
	if base == "" {
		base = "https://api.telegram.org"
	}
	base = strings.TrimSuffix(base, "/")
	token:= cfgString(cfg, "bot_token")
	raw:= base + "/bot" + token + "/sendMessage"
	u, err:= url.Parse(raw)
	if err!= nil {
		// Do not pass err: The address contains a Bot Token, and at this point, addr should not be echoed back.
		return "", fmt.Errorf("Failed to concatenate API address (API address:%s)", redactRequestTarget(base))
	}
	return u.String(), nil
}

// telegramHTML Renders HTML content, returns the main body and the number of entries actually written (see Channel.Send).
func telegramHTML(m Message) (string, int) {
	var b strings.Builder
	b.WriteString("<b>" + telegramEscape(markdownTitle(m)) + "</b>\n")
	if m.Batch {
		// Telegram The upper limit is**Character count**, Therefore, packaging should also be measured by character count (runeSize).
		footer:= ""
		if m.HomeURL!= "" {
			footer = fmt.Sprintf("\n\n<a href=\"%s\">View all in the platform</a>", telegramEscapeAttr(m.HomeURL))
		}
		kept:= packItemCount(m.Items, telegramTextLimit, telegramReservedRunes, footer, runeSize, func(it Item, idx int) string {
			return telegramBatchLine(it, idx+1)
		})
		items:= m.Items[:kept]
		b.Reset()
		b.WriteString("<b>" + telegramEscape(telegramBatchTitle(m, items, len(m.Items))) + "</b>")
		for i, it:= range items {
			b.WriteString("\n" + telegramEscape(telegramBatchLine(it, i+1)))
		}
		b.WriteString(footer)
		return TruncateHTML(b.String(), telegramTextLimit), kept
	}
	if len(m.Items) == 0 {
		return b.String(), 0
	}
	it:= m.Items[0]
	if it.IsStatusChange() {
		b.WriteString(fmt.Sprintf("\n<b>Status Change</b>: %s → %s",
			telegramEscape(StatusLabel(it.FromStatus)), telegramEscape(StatusLabel(it.ToStatus))))
	}
	if it.VulnClass!= "" && it.VulnClass!= it.Title() {
		b.WriteString("\n<b>type</b>: " + telegramEscape(it.VulnClass))
	}
	if a:= assetLine(it.Assets, maxAssetsShown); a!= "" {
		b.WriteString("\n<b>Asset</b>: " + telegramEscape(a))
	}
	if s:= OneLine(it.Summary, maxSummaryRunes); s!= "" {
		b.WriteString("\n<b>Failure</b>: " + telegramEscape(s))
	}
	if it.DetailURL!= "" {
		b.WriteString(fmt.Sprintf("\n\n<a href=\"%s\">view details</a>", telegramEscapeAttr(it.DetailURL)))
	}
	return TruncateHTML(b.String(), telegramTextLimit), 1
}

// telegramReservedRunes Reserve space for message title and possible truncation hints (by character count).
const telegramReservedRunes = 160

// telegramBatchLine Render one line from the summary (not escaped, to be uniformly escaped by the caller).
func telegramBatchLine(it Item, idx int) string {
	if a:= assetLine(it.Assets, maxAssetsShown); a!= "" {
		return fmt.Sprintf("%d. %s · %s — %s", idx, SeverityLabel(it.Severity), it.Title(), a)
	}
	return fmt.Sprintf("%d. %s · %s", idx, SeverityLabel(it.Severity), it.Title())
}

// telegramBatchTitle Render the title line for aggregated messages. The count uses**This line actually contains**the number of,
// Instead of this batch total——Otherwise, readers might assume that the numbers written in the message header are the entire thing.
func telegramBatchTitle(m Message, items []Item, total int) string {
	title:= fmt.Sprintf("Vulnerability Summary · Total %d Item", total)
	if extra:= total - len(items); extra > 0 {
		title += fmt.Sprintf("(first %d items shown; the rest %d continue in the next message)", len(items), extra)
	}
	if m.WindowMinutes > 0 {
		title = fmt.Sprintf("In the last %d minutes · %s", m.WindowMinutes, title)
	}
	return title
}

// telegramEscape Escape HTML text content.
// Telegram Only recognizes these three entities, after escaping &amp; Existing entities will be double-escaped.——This is exactly
// Expected behavior: We want to display raw characters, not allow users to inject HTML.
func telegramEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// telegramEscapeAttr Escape HTML attribute values. Beyond text escaping, handle quotes too——
// URL Content inside quotes will prematurely close the href attribute, turning the following content into an injection point.
func telegramEscapeAttr(s string) string {
	s = telegramEscape(s)
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
