package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// This file is an invariant test:**Any**Error text from channel implementations must not carry credentials.
//
// Why pull a file separately: The initial channel use case only covered successful paths and platform business errors,
// Completely ignore transport layer failures. Yet, it is precisely transport layer errors (connection refused/DNS failure/timeout) that are most dangerous——
// http.Client.Do Returned *url.Error Will put**Full URL** Outputting error text, while this feature
// The credentials are right there in the URL. Credentials flow out through four exits:
//
//	notification_deliveries.last_error → Plain Text Storage,
//	GET /api/notify/deliveries Multi-select → Bypass channel configuration mask, echo to browser',
//	Server logs → Commonly forwarded and retained externally
//	Test sending interface 502 response → Directly displayed on the frontend
//
// So here we don't just test one function; instead, we send a request that will inevitably fail for each channel and assert the error text.
// Could not find that credential.

// credentialCases Cover all 'credential in URL' channel forms:
// DingTalk/WeCom in query, Feishu at the end of the path, Telegram in the middle of the path.
var credentialCases = []struct {
	name string
	ch Channel
	cfg map[string]any
	secret string
}{
	{
		name: "DingTalk access_token in query",
		ch: dingTalkChannel{},
		cfg: map[string]any{"webhook": "http://127.0.0.1:1/robot/send?access_token=" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name: "The enterprise WeChat key is in the query.",
		ch: weComChannel{},
		cfg: map[string]any{"webhook": "http://127.0.0.1:1/cgi-bin/webhook/send?key=" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name: "The Feishu hook ID is at the end of the path.",
		ch: feishuChannel{},
		cfg: map[string]any{"webhook": "http://127.0.0.1:1/open-apis/bot/v2/hook/" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name: "Telegram bot token In the middle of the path",
		ch: telegramChannel{},
		cfg: map[string]any{"bot_token": leakProbeToken, "chat_id": "1", "base_url": "http://127.0.0.1:1"},
		secret: leakProbeToken,
	},
	{
		name: "DingTalk signature key",
		ch: dingTalkChannel{},
		cfg: map[string]any{"webhook": "http://127.0.0.1:1/robot/send", "secret": leakProbeToken},
		secret: leakProbeToken,
	},
}

// leakProbeToken A sentinel value that is absolutely impossible to be a real credential, used to search for it in error text.
const leakProbeToken = "LEAKPROBE0123456789abcdef"

// TestChannelErrorsNeverLeakCredentials Is a core invariant.
func TestChannelErrorsNeverLeakCredentials(t *testing.T) {
	for _, tc:= range credentialCases {
		t.Run(tc.name, func(t *testing.T) {
			// Aguaranteed-fail peer: 127.0.0.1:1 Not monitored, following the path of connection refused.
			_, err:= tc.ch.Send(context.Background(), tc.cfg, Message{
				Items: []Item{{FindingID: 1, Severity: "high", Name: "Exfiltration probe"}},
			})
			if err == nil {
				t.Fatal("An error should be reported for unreachable addresses.")
			}
			assertNoSecret(t, err.Error(), tc.secret)
		})
	}
}

// TestChannelErrorsNeverLeakCredentialsInPermanentPath Cover permanent failure branch:
// URL Validation failures and platform business errors also transmit error text without credentials.
func TestChannelErrorsNeverLeakCredentialsInPermanentPath(t *testing.T) {
	cases:= []struct {
		name string
		ch Channel
		cfg map[string]any
	}{
		// Address contains credentials but format is illegal → Trigger validateHTTPURL / url.Parse Branch.
		{"DingTalk address illegal", dingTalkChannel{}, map[string]any{"webhook": "file:///" + leakProbeToken}},
		{"Enterprise WeChat address illegal", weComChannel{}, map[string]any{"webhook": "gopher://" + leakProbeToken}},
		{"Invalid Feishu address", feishuChannel{}, map[string]any{"webhook": "ftp://" + leakProbeToken + "/hook"}},
		{"Telegram API address illegal", telegramChannel{}, map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": "file://" + leakProbeToken}},
		{"General Webhook address illegal", webhookChannel{}, map[string]any{"url": "javascript:" + leakProbeToken}},
	}
	for _, tc:= range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err:= tc.ch.Send(context.Background(), tc.cfg, Message{Items: []Item{{Severity: "high"}}})
			if err == nil {
				t.Fatal("Illegal configurations should report errors")
			}
			assertNoSecret(t, err.Error(), leakProbeToken)
		})
	}
}

func assertNoSecret(t *testing.T, text, secret string) {
	t.Helper()
	if strings.Contains(text, secret) {
		t.Fatalf("Error text leaked credentials %q:\n %s", secret, text)
	}
}

func TestRedactRequestTargetKeepsOnlySchemeAndHost(t *testing.T) {
	cases:= map[string]string{
		"https://oapi.dingtalk.com/robot/send?access_token=S1": "https://oapi.dingtalk.com/…",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=S2": "https://qyapi.weixin.qq.com/…",
		"https://open.feishu.cn/open-apis/bot/v2/hook/S3": "https://open.feishu.cn/…",
		"https://api.telegram.org/botS4/sendMessage": "https://api.telegram.org/…",
		"http://10.0.0.5:8080/hook": "http://10.0.0.5:8080/…",
	}
	for in, want:= range cases {
		got:= redactRequestTarget(in)
		if got!= want {
			t.Errorf("redactRequestTarget(%q) = %q, Expectation %q", in, got, want)
		}
		// The desensitization result itself should not contain any path/query fragments from the original address.
		if parts:= strings.SplitN(in, "://", 2); len(parts) == 2 {
			if hostAndRest:= strings.SplitN(parts[1], "/", 2); len(hostAndRest) == 2 && hostAndRest[1]!= "" {
				if strings.Contains(got, hostAndRest[1]) {
					t.Errorf("After de-sensitization, it still contains path/query fragments. %q: %q", hostAndRest[1], got)
				}
			}
		}
	}
	// Unparseable input must never echo the original string.
	for _, bad:= range []string{"", "://", "not a url", "http://"} {
		if got:= redactRequestTarget(bad); strings.Contains(got, bad) && bad!= "" {
			t.Errorf("Input not resolvable %q Will be echoed as %q", bad, got)
		}
	}
}

// TestRedactTransportErrorStripsURL Directly Focus on *url.Error This specific type:
// It is http.Client.Do Also the first scene of data leakage.
func TestRedactTransportErrorStripsURL(t *testing.T) {
	inner:= errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	uerr:= &url.Error{
		Op: "Post",
		URL: "https://api.telegram.org/bot" + leakProbeToken + "/sendMessage",
		Err: inner,
	}
	got:= redactTransportError(uerr)
	assertNoSecret(t, got, leakProbeToken)
	if!strings.Contains(got, "api.telegram.org") {
		t.Errorf("Retain host for troubleshooting, get %q", got)
	}
	if!strings.Contains(got, "connection refused") {
		t.Errorf("Should retain underlying reasons for troubleshooting, resulting in: %q", got)
	}
	// Op Also retain it (POST vs GET is meaningful for troubleshooting).
	if!strings.Contains(got, "Post") {
		t.Errorf("Should retain operation name, resulting in %q", got)
	}
}

// TestRedactURLsInTextHandlesFallback Fallback path: no *url.Error Custom error of
// (Addresses in errors returned by redirection policies must also be removed.
func TestRedactURLsInTextHandlesFallback(t *testing.T) {
	in:= fmt.Sprintf("Reject cross-host redirection (a).example → http://b.example/bot%s/send)", leakProbeToken)
	got:= redactURLsInText(in)
	assertNoSecret(t, got, leakProbeToken)
	if!strings.Contains(got, "http://b.example/…") {
		t.Errorf("The address should be replaced with a desensitized form, resulting in %q", got)
	}
	// Text without addresses should be retained as is.
	if plain:= "dial tcp: connection refused"; redactURLsInText(plain)!= plain {
		t.Error("Text without an address should not be altered.")
	}
}

// TestCrossHostRedirectRefused Cover 'credentials in URL' + Follow Cross-Host Redirects = Hand over credentials'.
// httptest Two services listening on 127.0.0.1 Different ports mean different Hosts,
// Exactly forming cross-host jump.
func TestCrossHostRedirectRefused(t *testing.T) {
	var hit bool
	target:= httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer target.Close()

	redirector:= httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/robot/send?access_token="+leakProbeToken, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	_, err:= (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": redirector.URL + "/robot/send?access_token=" + leakProbeToken},
		Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("Cross-host redirects should be rejected.")
	}
	if hit {
		t.Fatal("The target of the redirect is accessed——Credentials leaked with the redirect.")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestSameHostRedirectAllowed Reverse case: A cross-host redirect (like appending a slash) must still be usable,
// Otherwise, it will block the normal workflow.
func TestSameHostRedirectAllowed(t *testing.T) {
	srv:= httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robot/send" {
			// Jump to the same host and port.
			http.Redirect(w, r, "/robot/send/", http.StatusTemporaryRedirect)
			return
		}
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	if _, err:= (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"},
		Message{Items: []Item{{Severity: "high"}}}); err!= nil {
		t.Fatalf("Redirects to the same host should not be rejected.: %v", err)
	}
}
