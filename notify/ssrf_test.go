package notify

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// This file covers two related reinforcements:
// ① 'Delivery address must not use the server as a jumpboard for internal network / cloud metadata (SSRF)',
// ② Error message from address validation should not include credentials.
//
// Regarding the test environment: This package uses many cases with 127.0.0.1 The httptest fake receiver, the guard will default to blocking
// They. So TestMain uniformly opens AllowLocalTargetsEnv, while each SSRF test case below uses
// Explicitly clear it to assert**Default rejection**Its Behavior.

func TestMain(m *testing.M) {
	// Allow regular use cases to connect to a local fake receiver; SSRF use cases will temporarily clear themselves.
	_ = os.Setenv(AllowLocalTargetsEnv, "1")
	os.Exit(m.Run())
}

// TestDialGuardRejectsLoopbackByDefault This is the core assertion for SSRF protection:
// Under default configuration, deliveries to the loopback address must be**Connection layer**Deny.
func TestDialGuardRejectsLoopbackByDefault(t *testing.T) {
	var hit bool
	srv:= httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "") // Close escape hatch = Default behavior
	_, err:= (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("Default should not allow delivery to loopback addresses.")
	}
	if hit {
		t.Fatal("The request has already reached the local service——Guard not effective")
	}
	// Error messages should guide users on how to relax (legitimate SMTP relay configuration on this machine).
	if!strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("Refusal message should explain how to explicitly relax: %v", err)
	}
}

// TestDialGuardAllowsLoopbackWhenOptedIn Reverse case: Must be usable after explicit opening.
// Otherwise, legitimate deployments such as this machine's postfix / internal relay would be cut off.
func TestDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	srv:= httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err:= (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}}); err!= nil {
		t.Fatalf("After explicit release, it should be deliverable.: %v", err)
	}
}

func TestIsBlockedDialIP(t *testing.T) {
	blocked:= []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"169.254.169.254", // Cloud metadata endpoint.——The main reason this function exists.
		"169.254.1.1", "fe80::1",
		"0.0.0.0", "::",
		"224.0.0.1", "ff02::1",
		"::ffff:127.0.0.1", // IPv4-mapped The form must be restored before judgment, otherwise it's a bypass mouth',
		"",
	}
	for _, s:= range blocked {
		if!isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s should be rejected", s)
		}
	}
	// RFC1918 Private Network**intentionallylet through**: Internal self-built Mattermost / SMTP relay is a common legal use case.
	// This assertion fixes this choice permanently.——If someone adds private network judgment later, this will fail,
	// Thus forcing a conscious decision (instead of silently discarding a batch of deployments).
	allowed:= []string{"10.0.0.5", "172.16.3.4", "192.168.1.10", "8.8.8.8", "2606:4700::1111"}
	for _, s:= range allowed {
		if isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s This situation should be allowed (private networks are common legitimate delivery targets).", s)
		}
	}
}

// TestValidateHTTPURLRejectsLiteralPrivateTargets Cover pre-prompt in configuration phase:
// Literal IP addresses should be rejected when saved, not waiting until the first delivery fails.
func TestValidateHTTPURLRejectsLiteralPrivateTargets(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "")
	for _, raw:= range []string{
		"http://127.0.0.1:8080/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]:8080/hook",
	} {
		if err:= validateHTTPURL(raw); err == nil {
			t.Errorf("%s Should be rejected in the configuration stage", raw)
		}
	}
	// Public network addresses and private network addresses pass through normally (private networks left for dial-up stage, where they are not blocked).
	for _, raw:= range []string{"https://oapi.dingtalk.com/robot/send", "http://10.0.0.9/hook"} {
		if err:= validateHTTPURL(raw); err!= nil {
			t.Errorf("%s Should be validated through: %v", raw, err)
		}
	}
}

// TestValidateHTTPURLErrorNeverLeaksCredentials This is a branch I missed in the audit last round.
//
// url.Parse **Success**Will return *url.Error, Its Error() Contains complete original address. In the previous round, I only
// Desensitized http.Client.Do The return error was missing here; and the 'permanent failure path' test case added at the time
// (file://, gopher://, ftp://)Actually, both can be handled by url.Parse If parsing is successful, it follows the scheme branch.
// So, all green does not prove the safety of this path.——It's a false guarantee.
func TestValidateHTTPURLErrorNeverLeaksCredentials(t *testing.T) {
	cases:= []string{
		"http://127.0.0.1/%zz?access_token=" + leakProbeToken, // Illegal percentage escape
		"https://a.example.com:port/x?access_token=" + leakProbeToken, // Port is not a number
		"http://[::1?access_token=" + leakProbeToken, // Parentheses do not match
	}
	for _, raw:= range cases {
		// Confirm this input first**Sure**Let url.Parse Failed. If this step is not done, the test case may be interrupted
		// Walk into another branch without being noticed (the false guarantee from the previous round was like this).
		if _, err:= url.Parse(raw); err == nil {
			t.Errorf("%q Should have parsed as failure, otherwise this test case did not cover the target branch.", raw)
			continue
		}
		err:= validateHTTPURL(raw)
		if err == nil {
			t.Errorf("%q Should validate failed", raw)
			continue
		}
		assertNoSecret(t, err.Error(), leakProbeToken)
	}
	// Confirm that the channel layer's wrapping did not take the address out.
	t.Setenv(AllowLocalTargetsEnv, "")
	err:= (dingTalkChannel{}).Validate(map[string]any{"webhook": cases[0]})
	if err == nil {
		t.Fatal("An illegal address should fail validation.")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestEmailDialGuardRejectsLoopbackByDefault Override SMTP channel's dial guard',
//
// Email channels used to use plain net..Dialer, The only gap in the entire SSRF protection: host filled with
// 169.254.169.254 Or 127.0.0.1 Can connect directly, while smtp.NewClient Handshake Failure Will Pass
// The other side returns that line wrapped in an error, through last_error by delivery history interface to reflect——It's another channel
// Closed semi-blind read primitive; 'connection refused vs timeout' timing differences can still be used to probe ports.
//
// The TestMain function of this package globally opens AllowLocalTargetsEnv (many test cases use 127.0.0.1 On the
// Assuming the receiving end), so this test case must clean it up itself.——Otherwise, whether the guard is there or not will pass,
// This is also why the gap was never discovered by any test initially.
func TestEmailDialGuardRejectsLoopbackByDefault(t *testing.T) {
	f:= newFakeSMTP(t)
	cfg:= emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "") // Close escape hatch = Default behavior
	_, err:= (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Fatal("Default should not allow emails to be delivered to loopback addresses.")
	}
	// Should not establish a connection: it is blocked by the guard in the Control hook, EHLO can never be sent out.
	if f.sawCommand("EHLO") || f.sawCommand("HELO") {
		t.Fatal("SMTP Session already established——Guard not effective")
	}
	// Error messages should guide users on how to relax (this machine postfix relay is a legal configuration).
	if!strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("Refusal message should explain how to explicitly relax: %v", err)
	}
}

// TestEmailDialGuardAllowsLoopbackWhenOptedIn The reverse case is an explicit open case:
// It must be able to deliver normally. Internal network self-built SMTP / local relay is a very common deployment; the guard should nottake a blanket approach.
func TestEmailDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	f:= newFakeSMTP(t)
	cfg:= emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err:= (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err!= nil {
		t.Fatalf("Explicitly opening it after this, the local SMTP should be able to send emails: %v", err)
	}
	if!f.sawCommand("EHLO") {
		t.Fatal("Did not see EHLO——Session did not truly establish")
	}
}
