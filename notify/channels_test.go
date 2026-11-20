package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// singleMsg Construct a single message with quotes and newlines. Deliberately use content that `"` And `\n` The title/summary:
// This is exactly what makes template interpolation produce the easiest illegal JSON input.
func singleMsg() Message {
	return Message{
		Items: []Item{{
			FindingID: 42,
			Name: `Login page "SQLCompleted" Risk`,
			VulnClass: "SQLCompleted",
			Severity: "high",
			Summary: "Parameter id\nUnfiltered leading to injection",
			Assets: []string{"a.example.com", "b.example.com"},
			DetailURL: "https://artex.local/function/findings/detail?id=42",
		}},
	}
}

// batchMsg Construct a batch of summary messages.
func batchMsg(n int) Message {
	m:= Message{Batch: true, WindowMinutes: 30, HomeURL: "https://artex.local/function/findings"}
	for i:= 0; i < n; i++ {
		m.Items = append(m.Items, Item{
			FindingID: int64(i + 1),
			Name: "Vulnerability" + itoa(i+1),
			VulnClass: "XSS",
			Severity: "medium",
			Summary: "Reflected Cross-Site Scripting",
			Assets: []string{"target.example.com"},
		})
	}
	return m
}

// capturePost Set up a fake receiver to send back the received request body and headers to the assertion function.
func capturePost(t *testing.T, respBody string, assert func(t *testing.T, body map[string]any, r *http.Request)) *httptest.Server {
	t.Helper()
	srv:= httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _:= io.ReadAll(r.Body)
		var body map[string]any
		if len(raw) > 0 {
			if err:= json.Unmarshal(raw, &body); err!= nil {
				t.Errorf("Request body is not valid JSON: %v\nImported: %s", err, raw)
			}
		}
		if assert!= nil {
			assert(t, body, r)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDingTalkSendsActionCardWhenLinkPresent(t *testing.T) {
	srv:= capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msgtype"]!= "actionCard" {
			t.Fatalf("When there is a callback, send an actionCard and receive %v", body["msgtype"])
		}
		card, _:= body["actionCard"].(map[string]any)
		if card["singleURL"]!= "https://artex.local/function/findings/detail?id=42" {
			t.Errorf("Lost backlink: %v", card["singleURL"])
		}
	})
	if _, err:= (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg()); err!= nil {
		t.Fatalf("Submission Failed: %v", err)
	}
}

func TestDingTalkFallsBackToMarkdownForBatch(t *testing.T) {
	srv:= capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msgtype"]!= "markdown" {
			t.Fatalf("The summary message should be in markdown, resulting in %v", body["msgtype"])
		}
		md, _:= body["markdown"].(map[string]any)
		if!strings.Contains(md["text"].(string), "In the last 30 minutes") {
			t.Errorf("Summary body missing time window: %v", md["text"])
		}
	})
	if _, err:= (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, batchMsg(3)); err!= nil {
		t.Fatalf("Submission Failed: %v", err)
	}
}

// TestDingTalkBusinessErrorIsPermanent Lock down the determination of 'HTTP 200 but errcode non-0'.
// Not checking errcode will record delivery failures as successes——This is a common pitfall in various domestic IM platforms.
func TestDingTalkBusinessErrorIsPermanent(t *testing.T) {
	srv:= capturePost(t, `{"errcode":310000,"errmsg":"keywords not in content"}`, nil)
	_, err:= (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg())
	if err == nil {
		t.Fatal("errcode Non-zero should report an error")
	}
	if!IsPermanent(err) {
		t.Fatalf("Keyword mismatch is a configuration error and should be marked as permanent failure, resulting in %v", err)
	}
	if!strings.Contains(err.Error(), "310000") {
		t.Errorf("Error messages should include platform error codes, resulting in %v", err)
	}
}

func TestWeComTruncatesCJKWithinByteLimit(t *testing.T) {
	var contentLen int
	srv:= capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		md, _:= body["markdown"].(map[string]any)
		content, _:= md["content"].(string)
		contentLen = len(content)
		if!utf8.ValidString(content) {
			t.Fatal("Truncated to an invalid UTF-8——WeChat Enterprise will reject the entire message")
		}
	})
	// Generate a long enough Chinese summary that will definitely exceed 4096 bytes.
	m:= batchMsg(200)
	if _, err:= (weComChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, m); err!= nil {
		t.Fatalf("Submission Failed: %v", err)
	}
	if contentLen > weComMarkdownLimit {
		t.Fatalf("Content %d Byte count exceeds WeChat Work limit %d", contentLen, weComMarkdownLimit)
	}
	if contentLen == 0 {
		t.Fatal("Empty main text")
	}
}

func TestWeComRateLimitIsRetryableButKeyErrorIsPermanent(t *testing.T) {
	limited:= capturePost(t, `{"errcode":45009,"errmsg":"api freq out of limit"}`, nil)
	_, err:= (weComChannel{}).Send(context.Background(), map[string]any{"webhook": limited.URL}, singleMsg())
	if err == nil || IsPermanent(err) {
		t.Fatalf("45009 Rolling window rate limiting, should be retryable and result in %v", err)
	}

	badKey:= capturePost(t, `{"errcode":93000,"errmsg":"invalid webhook url"}`, nil)
	_, err = (weComChannel{}).Send(context.Background(), map[string]any{"webhook": badKey.URL}, singleMsg())
	if err == nil ||!IsPermanent(err) {
		t.Fatalf("93000 Is key invalid, retry won't self-heal, should be permanent failure, get %v", err)
	}
}

func TestFeishuCardStructureAndSign(t *testing.T) {
	const secret = "SECtest123"
	srv:= capturePost(t, `{"code":0,"msg":"success"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msg_type"]!= "interactive" {
			t.Fatalf("Should send an interactive card and receive %v", body["msg_type"])
		}
		card, _:= body["card"].(map[string]any)
		header, _:= card["header"].(map[string]any)
		if header["template"]!= "orange" {
			t.Errorf("high The level should be orange color, resulting in %v", header["template"])
		}
		// Configured with secret must carry signature parameter, otherwise Feishu will reject it with code 19021.
		if body["sign"] == nil || body["timestamp"] == nil {
			t.Fatalf("Missing signature parameter.: %v", body)
		}
		// The card element should contain a button whose URL points to the vulnerability details.
		elements, _:= card["elements"].([]any)
		foundButton:= false
		for _, e:= range elements {
			em, _:= e.(map[string]any)
			if em["tag"]!= "action" {
				continue
			}
			actions, _:= em["actions"].([]any)
			for _, a:= range actions {
				am, _:= a.(map[string]any)
				if am["url"] == "https://artex.local/function/findings/detail?id=42" {
					foundButton = true
				}
			}
		}
		if!foundButton {
			t.Fatal("No button pointing to the details page in the card.")
		}
	})
	cfg:= map[string]any{"webhook": srv.URL, "secret": secret}
	if _, err:= (feishuChannel{}).Send(context.Background(), cfg, singleMsg()); err!= nil {
		t.Fatalf("Submission Failed: %v", err)
	}
}

func TestFeishuWithoutSecretOmitsSign(t *testing.T) {
	srv:= capturePost(t, `{"code":0,"msg":"success"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["sign"]!= nil || body["timestamp"]!= nil {
			t.Fatalf("When no secret is configured, signing parameters should not be included: %v", body)
		}
	})
	if _, err:= (feishuChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg()); err!= nil {
		t.Fatalf("Submission Failed: %v", err)
	}
}

func TestTelegramEscapesHTMLInUntrustedContent(t *testing.T) {
	var text string
	srv:= capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		text, _ = body["text"].(string)
		if body["parse_mode"]!= "HTML" {
			t.Fatalf("HTML parsing mode should be used to get %v", body["parse_mode"])
		}
	})
	m:= Message{Items: []Item{{
		Severity: "high",
		// Title and summary from tested target/model output, untrusted content.
		Name: `<script>alert(1)</script>`,
		Summary: "a & b < c",
	}}}
	if _, err:= (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": srv.URL}, m); err!= nil {
		t.Fatalf("Submission Failed: %v", err)
	}
	if strings.Contains(text, "<script>") {
		t.Fatalf("HTML is not escaped, injection exists: %q", text)
	}
	if!strings.Contains(text, "&lt;script&gt;") {
		t.Fatalf("Expected escaped entity to get %q", text)
	}
	if!strings.Contains(text, "a &amp; b") {
		t.Fatalf("& Not escaped, got %q", text)
	}
}

func TestTelegramErrorClassification(t *testing.T) {
	rateLimited:= capturePost(t, `{"ok":false,"error_code":429,"description":"Too Many Requests"}`, nil)
	_, err:= (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": rateLimited.URL}, singleMsg())
	if err == nil || IsPermanent(err) {
		t.Fatalf("429 Should Be Retriable, Got %v", err)
	}

	forbidden:= capturePost(t, `{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`, nil)
	_, err = (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": forbidden.URL}, singleMsg())
	if err == nil ||!IsPermanent(err) {
		t.Fatalf("403 It's a configuration issue, should be permanent failure, got %v", err)
	}
}

func TestWebhookDefaultTemplateProducesValidJSON(t *testing.T) {
	// This is the meaning of the default template: when title contains quotes or newlines, any naive
	// `"title": "{{.Title}}"` All writing methods will produce illegal JSON.{{json .}} won't.
	srv:= capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["title"]!= `[🟠 Retest] Login page "SQLCompleted" Risk` {
			t.Errorf("Title not correctly restored: %v", body["title"])
		}
		items, _:= body["items"].([]any)
		if len(items)!= 1 {
			t.Fatalf("items Quantity should be 1, got %d", len(items))
		}
		it, _:= items[0].(map[string]any)
		if it["summary"]!= "Parameter id\nUnfiltered leading to injection" {
			t.Errorf("Summary not correctly restored: %v", it["summary"])
		}
		// Numeric values must be JSON numbers instead of strings (json:"...,string" Such syntax can lead to this pitfall).
		if _, ok:= it["finding_id"].(float64);!ok {
			t.Errorf("finding_id Should be a number, got %T", it["finding_id"])
		}
	})
	if _, err:= (webhookChannel{}).Send(context.Background(), map[string]any{"url": srv.URL}, singleMsg()); err!= nil {
		t.Fatalf("Submission Failed: %v", err)
	}
}

func TestWebhookCustomTemplateAndHeaders(t *testing.T) {
	srv:= capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, r *http.Request) {
		if r.Header.Get("X-Token")!= "s3cret" {
			t.Errorf("Custom header lost: %v", r.Header)
		}
		if body["msg"]!= "3 Item" {
			t.Errorf("Custom template rendering error.: %v", body["msg"])
		}
		if body["first"]!= "Vulnerability1" {
			t.Errorf("range Extraction error: %v", body["first"])
		}
	})
	cfg:= map[string]any{
		"url": srv.URL,
		"headers": map[string]any{"X-Token": "s3cret"},
		"body_template": `{"msg": {{json (printf "%d Item" .Count)}}, "first": {{json (index .Items 0).Name}}}`,
	}
	if _, err:= (webhookChannel{}).Send(context.Background(), cfg, batchMsg(3)); err!= nil {
		t.Fatalf("Submission Failed: %v", err)
	}
}

func TestWebhookRejectsNonJSONRenderResult(t *testing.T) {
	cfg:= map[string]any{"url": "https://example.com/hook", "body_template": `not json at all`}
	_, err:= (webhookChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil ||!IsPermanent(err) {
		t.Fatalf("If the rendering result is not JSON, it should be permanent failure (template error, retry useless), resulting in %v", err)
	}
}

func TestWebhookValidateCatchesBadConfigEarly(t *testing.T) {
	bad:= []map[string]any{
		{},
		{"url": "file:///etc/passwd"},
		{"url": "https://example.com", "method": "DELETE"},
		{"url": "https://example.com", "body_template": `{{.Items.`},
	}
	for i, cfg:= range bad {
		if err:= (webhookChannel{}).Validate(cfg); err == nil {
			t.Errorf("Intercept %d Group Config Should Be Rejected: %v", i, cfg)
		}
	}
}

func TestEmailMessageIsWellFormed(t *testing.T) {
	msg, err:= buildEmailMessage("artex@example.com", []string{"a@example.com", "b@example.com"}, singleMsg())
	if err!= nil {
		t.Fatalf("Email assembly failed: %v", err)
	}
	if!strings.HasPrefix(msg, "From: artex@example.com\r\n") {
		t.Fatalf("From Header error:\n%s", msg)
	}
	if!strings.Contains(msg, "To: a@example.com, b@example.com\r\n") {
		t.Fatalf("To Header error:\n%s", msg)
	}
	// Chinese theme must be RFC 2047 encoded, otherwise client displays as garbled.
	if!strings.Contains(msg, "Subject: =?utf-8?") {
		t.Fatalf("The subject is not RFC 2047 encoded.:\n%s", msg)
	}
	if dec, err:= new(mime.WordDecoder).DecodeHeader(mustExtractHeader(t, msg, "Subject")); err!= nil {
		t.Fatalf("Theme cannot be decoded.: %v", err)
	} else if!strings.Contains(dec, "SQLCompleted") {
		t.Fatalf("Topic decoding content is incorrect: %q", dec)
	}

	// Body is base64, should decode to valid HTML.
	parts:= strings.SplitN(msg, "\r\n\r\n", 2)
	if len(parts)!= 2 {
		t.Fatal("Email missing header/body separator")
	}
	decoded, err:= base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(parts[1]), "\r\n", ""))
	if err!= nil {
		t.Fatalf("Base64 decoding of the main body failed: %v", err)
	}
	html:= string(decoded)
	if!strings.HasPrefix(html, "<div") {
		t.Fatalf("Content is not HTML: %.80s", html)
	}
	// Title appears as-is in the text position: Double quotes in HTML text content are valid characters and do not need escaping.
	// Here, asserting 'as-is' is to prevent someone from mistakenly adding an extra layer of quote escaping, which would make Chinese quotes
	// Display as &quot;.
	if!strings.Contains(html, `"SQLCompleted"`) {
		t.Fatalf("Preserve quotes in titles as they appear in the text.: %.200s", html)
	}
}

// TestEmailEscapesStructuralInjection Coverage of email body injection truly needs protection:
// Vulnerability titles and summaries come from the target and model output, untrusted content. Text positions must be escaped.
// & < > (Otherwise can inject labels), attribute positions must escape quotes (otherwise can close href).
func TestEmailEscapesStructuralInjection(t *testing.T) {
	m:= Message{
		Items: []Item{{
			Severity: "high",
			Name: `<script>alert(1)</script>`,
			Summary: "a & b > c",
			DetailURL: `https://artex.local/x?a="onmouseover=alert(1)`,
		}},
	}
	html:= htmlBody(m, 0)
	if strings.Contains(html, "<script>") {
		t.Fatalf("Title not escaped, can inject tags.: %s", html)
	}
	if!strings.Contains(html, "&lt;script&gt;") {
		t.Fatalf("Expect the escaped entity: %s", html)
	}
	if!strings.Contains(html, "a &amp; b &gt; c") {
		t.Fatalf("& And > Not escaped: %s", html)
	}
	// Backlink is an administrator-configurable public_base_url, itself more trustworthy, but its attribute position must still be
	// Escape Quotes——Otherwise a quoted address will close href and inject event handlers.
	if strings.Contains(html, `onmouseover=alert(1)">`) {
		t.Fatalf("href Attribute not correctly escaped: %s", html)
	}
	if!strings.Contains(html, "&quot;") {
		t.Fatalf("Quotes in attribute positions should be escaped: %s", html)
	}
}

func mustExtractHeader(t *testing.T, msg, name string) string {
	t.Helper()
	for _, line:= range strings.Split(msg, "\r\n") {
		if strings.HasPrefix(line, name+": ") {
			return strings.TrimPrefix(line, name+": ")
		}
	}
	t.Fatalf("Not Found %s Head", name)
	return ""
}

func TestChannelValidateReportsMissingFields(t *testing.T) {
	// Validation errors will be directly shown to the configurator, must explain what is missing instead of vaguely saying 'configuration invalid'.
	cases:= []struct {
		kind string
		cfg map[string]any
		substr string
	}{
		{KindDingTalk, map[string]any{}, "Webhook"},
		{KindFeishu, map[string]any{}, "Webhook"},
		{KindWeCom, map[string]any{}, "Webhook"},
		{KindTelegram, map[string]any{}, "Bot Token"},
		{KindTelegram, map[string]any{"bot_token": "t"}, "Chat ID"},
		{KindEmail, map[string]any{}, "SMTP"},
		{KindEmail, map[string]any{"host": "h"}, "Port"},
		{KindEmail, map[string]any{"host": "h", "port": 587, "from": "f"}, "Recipient"},
	}
	for _, tc:= range cases {
		ch, ok:= Get(tc.kind)
		if!ok {
			t.Fatalf("Channel %s Unregistered", tc.kind)
		}
		err:= ch.Validate(tc.cfg)
		if err == nil {
			t.Errorf("%s Version %v Should validate failed", tc.kind, tc.cfg)
			continue
		}
		if!strings.Contains(err.Error(), tc.substr) {
			t.Errorf("%s Error messages should mention %q, Got %q", tc.kind, tc.substr, err.Error())
		}
	}
}

// TestEmailSMTPErrorClassification Lock SMTP 4xx/5xx semantics.
// If 4xx is also judged as permanent failure, an email server with a whitelist would make every push fail on the first
// After trying, it falls into failed. —— Graylisting is precisely where automatic retries should be most effective.
func TestEmailSMTPErrorClassification(t *testing.T) {
	cases:= []struct {
		reply string
		permanent bool
	}{
		{"450 4.7.1 Greylisting in action, please come back later", false},
		{"451 4.3.0 Temporary system failure", false},
		{"452 4.2.2 Mailbox full", false},
		{"550 5.1.1 User unknown", true},
		{"553 5.1.3 Bad address syntax", true},
		{"554 5.7.1 Relay access denied", true},
		// When no response code is obtained, handle as 'retryable': prefer one more try rather than risking a transient
		// Fault detection.
		{"unexpected EOF", false},
		{"", false},
	}
	for _, tc:= range cases {
		err:= smtpStageError("Recipient rejected", errors.New(tc.reply))
		if got:= IsPermanent(err); got!= tc.permanent {
			t.Errorf("Response %q: Expect permanent.=%v Got %v", tc.reply, tc.permanent, got)
		}
		// Regardless of classification, the original text must be retained for user troubleshooting.
		if tc.reply!= "" &&!strings.Contains(err.Error(), tc.reply) {
			t.Errorf("Response %q Original text discarded: %v", tc.reply, err)
		}
	}
}

func TestRegistryCoversAllKinds(t *testing.T) {
	// Six channels are essential——Missing one will silently disappear in the UI dropdown.
	want:= []string{KindDingTalk, KindEmail, KindFeishu, KindTelegram, KindWebhook, KindWeCom}
	got:= Kinds()
	if len(got)!= len(want) {
		t.Fatalf("Channel count should be %d, Got %d: %v", len(want), len(got), got)
	}
	for _, k:= range want {
		if!ValidKind(k) {
			t.Errorf("Channel %s Unregistered", k)
		}
		if ch, ok:= Get(k);!ok || ch.Kind()!= k {
			t.Errorf("Channel %s of Kind() Does not match the registration key", k)
		}
	}
	if ValidKind("nope") {
		t.Error("Unregistered types should not pass validation")
	}
}

func TestPermanentErrorUnwrap(t *testing.T) {
	base:= &permanentSentinel{}
	err:= Permanent(base)
	if!IsPermanent(err) {
		t.Fatal("Should be identified as permanent failure")
	}
	if!strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("Error information should be passed through to the underlying layer.: %v", err)
	}
	if Permanent(nil)!= nil {
		t.Fatal("Permanent(nil) Must return nil")
	}
	if IsPermanent(nil) {
		t.Fatal("nil Not a permanent failure.")
	}
}

type permanentSentinel struct{}

func (*permanentSentinel) Error() string { return "sentinel" }
