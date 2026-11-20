package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// feishuChannel Implement Feishu (including Lark) custom bot using interactive cards.
//
// Platform features:
// - Signature algorithm with DingTalk**Different**, And is easily misspelled, see feishuSign comments.
// - Just like DingTalk, business errors are stuffed in the HTTP 200 body (code!= 0).
// - Card header supports color templates, using level mapping for coloring, making severity immediately apparent in the message list.
type feishuChannel struct{}

func (feishuChannel) Kind() string { return KindFeishu }

// Feishu custom robot up to 5 times/second, equivalent to 100 times/minute.
func (feishuChannel) DefaultRatePerMin() int { return 100 }

// Webhook The last segment of the address is the robot's unique identifier, a credential.
func (feishuChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// Similarly: Changing Webhook address must re-endorse the new address's signature key.
func (feishuChannel) DestinationKeys() []string { return []string{"webhook"} }

func (feishuChannel) Validate(cfg map[string]any) error {
	hook:= cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("Missing Webhook address")
	}
	if err:= validateHTTPURL(hook); err!= nil {
		return fmt.Errorf("Webhook Invalid address: %w", err)
	}
	return nil
}

func (c feishuChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err:= c.Validate(cfg); err!= nil {
		return 0, Permanent(err)
	}
	card, kept:= feishuCard(m)
	payload:= map[string]any{
		"msg_type": "interactive",
		"card": card,
	}
	// Signing parameters and messages are at the same level, and only appear when a secret is configured.
	if secret:= cfgString(cfg, "secret"); secret!= "" {
		ts:= strconv.FormatInt(time.Now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = feishuSign(ts, secret)
	}
	raw, err:= doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err!= nil {
		return 0, err
	}
	var res struct {
		Code int `json:"code"`
		Msg string `json:"msg"`
		// Some versions of Feishu hooks use these field names for compatibility.
		StatusCode int `json:"StatusCode"`
		StatusMessage string `json:"StatusMessage"`
	}
	if err:= json.Unmarshal(raw, &res); err!= nil {
		return 0, fmt.Errorf("Failed to parse Feishu response: %w (%s)", err, snippet(raw))
	}
	if res.Code!= 0 {
		return 0, Permanent(fmt.Errorf("Feishu returns error. %d: %s", res.Code, res.Msg))
	}
	if res.StatusCode!= 0 {
		return 0, Permanent(fmt.Errorf("Feishu returns error. %d: %s", res.StatusCode, res.StatusMessage))
	}
	return kept, nil
}

// feishuSign Calculate signature according to Feishu's official rules.
//
// Here, it's particularly easy to fall into a trap: The official example is
//
//	hmac.new(string_to_sign.encode(), digestmod=sha256)
//
// That is to say **key = timestamp + "\n" + secret, message Empty**, Instead of Intuitive
// 'key=secret, message=stringToSign'——That's exactly DingTalk's algorithm. The algorithms are reversed on both sides,
// Writing an implementation based on another's will inevitably result in signature verification failure (error 19021).
func feishuSign(timestamp, secret string) string {
	stringToSign:= timestamp + "\n" + secret
	mac:= hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// feishuSeverityTemplate Map vulnerability levels to card header color templates.
// Unknown level uses grey——Don't use blue to avoid confusion with low.
func feishuSeverityTemplate(severity string) string {
	switch severity {
	case "critical":
		return "red"
	case "high":
		return "orange"
	case "medium":
		return "yellow"
	case "low":
		return "blue"
	default:
		return "grey"
	}
}

// feishuMaxCardBytes Conservative upper limit for card content. Feishu has a volume limit, exceeding it rejects the entire message;
// Take a value clearly below the official limit, including JSON packaging overhead.
const feishuMaxCardBytes = 24000

// feishuCard Construct an interactive card, return the card and**Actual number of written entries**.
// kept The purpose is the same as markdownBody: only entries that actually enter the card should be marked as delivered.
func feishuCard(m Message) (map[string]any, int) {
	elements:= []any{}
	kept:= 0
	if m.Batch {
		// First package the entire thing and then add the header: The header should say 'the rest will continue in the next message'.
		// N Must come from the actual installed count.
		kept = packItemCount(m.Items, feishuMaxCardBytes, markdownReservedBytes, "", byteSize, func(it Item, idx int) string {
			return feishuBatchLine(it, idx+1)
		})
		items:= m.Items[:kept]
		elements = append(elements, feishuMarkdownDiv(markdownBatchIntro(m, items, len(m.Items))))
		for i, it:= range items {
			elements = append(elements, feishuMarkdownDiv(feishuBatchLine(it, i+1)))
		}
		if m.HomeURL!= "" {
			elements = append(elements, feishuButton("View all in the platform", m.HomeURL))
		}
	} else if len(m.Items) > 0 {
		kept = 1
		it:= m.Items[0]
		elements = append(elements, feishuMarkdownDiv(feishuItemLines(it)))
		if it.DetailURL!= "" {
			elements = append(elements, feishuButton("view details", it.DetailURL))
		}
	}

	card:= map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{"title": map[string]any{"tag": "plain_text", "content": markdownTitle(m)}},
		"elements": elements,
	}
	if len(m.Items) > 0 {
		card["header"].(map[string]any)["template"] = feishuSeverityTemplate(m.Items[0].Severity)
	}
	return card, kept
}

func feishuMarkdownDiv(content string) map[string]any {
	return map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": content}}
}

func feishuButton(label, url string) map[string]any {
	return map[string]any{
		"tag": "action",
		"actions": []any{map[string]any{
			"tag": "button",
			"text": map[string]any{"tag": "lark_md", "content": label},
			"url": url,
			"type": "primary",
		}},
	}
}

// feishuItemLines Render the lark_md body for a single vulnerability.
//
// lark_md Be used to probe ports as they are part of the same text format family (markdown), parsing links and emphasis similarly. External
// All fields are passed through markdownText (single-line + Escape)——Otherwise, a single vulnerability title could occupy the entire message header (summary row).
// Turn into clickable external links in Feishu.
func feishuItemLines(it Item) string {
	out:= fmt.Sprintf("**%s · %s**", SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if it.IsStatusChange() {
		out += fmt.Sprintf("\n**Status Change**: %s → %s",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass!= "" && it.VulnClass!= it.Title() {
		out += fmt.Sprintf("\n**type**: %s", markdownText(it.VulnClass, 0))
	}
	if a:= assetLine(it.Assets, maxAssetsShown); a!= "" {
		out += fmt.Sprintf("\n**Asset**: %s", markdownText(a, 0))
	}
	if it.Summary!= "" {
		if s:= markdownText(it.Summary, maxSummaryRunes); s!= "" {
			out += fmt.Sprintf("\n**Failure**: %s", s)
		}
	}
	return out
}

// feishuBatchLine Render one item in the summary card.
func feishuBatchLine(it Item, index int) string {
	line:= fmt.Sprintf("**%d. %s · %s**", index, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if a:= assetLine(it.Assets, maxAssetsShown); a!= "" {
		line += " — " + markdownText(a, 0)
	}
	return line
}
