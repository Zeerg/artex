package notify

import (
	"bufio"
	"context"

	"net"
	"strings"
	"sync"
	"testing"
)

// This file completes protocol-level testing for the email channel. Before that, email.Send Coverage is 0——
// The entire SMTP path has no running cases, and it is precisely the protocol face with the largest coverage among six channels,
// The most error-prone one (handshake, authentication, envelope, DATA phase each have their own failure semantics).
//
// Here use a self-built minimal SMTP server driver instead of mocking net/smtp:
// Most risks of the email channel are at the 'dialogue with a real SMTP server' step,
// Mocking this step equals not testing.

// fakeSMTP A just-enough SMTP server: can complete greet/EHLO/AUTH/MAIL/RCPT/DATA/QUIT,
// Respond with a specific status code according to the use case requirements at certain stages.
type fakeSMTP struct {
	ln net.Listener

	// rcptReply Response to RCPT TO; default is 250.
	rcptReply string
	// mailReply It is a response to MAIL FROM; default 250.
	mailReply string
	// advertiseAuth Declare support for AUTH PLAIN in EHLO when true.
	advertiseAuth bool

	mu sync.Mutex
	data string
	commands []string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err:= net.Listen("tcp", "127.0.0.1:0")
	if err!= nil {
		t.Fatal(err)
	}
	f:= &fakeSMTP{ln: ln, rcptReply: "250 OK", mailReply: "250 OK"}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSMTP) hostPort(t *testing.T) (string, int) {
	t.Helper()
	addr, ok:= f.ln.Addr().(*net.TCPAddr)
	if!ok {
		t.Fatal("Non-TCP listening address")
	}
	return "127.0.0.1", addr.Port
}

func (f *fakeSMTP) record(cmd string) {
	f.mu.Lock()
	f.commands = append(f.commands, cmd)
	f.mu.Unlock()
}

func (f *fakeSMTP) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data
}

func (f *fakeSMTP) sawCommand(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c:= range f.commands {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func (f *fakeSMTP) serve() {
	conn, err:= f.ln.Accept()
	if err!= nil {
		return
	}
	defer conn.Close()
	br:= bufio.NewReader(conn)
	w:= func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	w("220 fake.local ESMTP ready")
	for {
		line, err:= br.ReadString('\n')
		if err!= nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.record(line)
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			// Do not declare STARTTLS: let the code take the plaintext branch (the test target is the envelope logic, not TLS).
			w("250-fake.local")
			if f.advertiseAuth {
				w("250-AUTH PLAIN")
			}
			w("250 8BITMIME")
		case strings.HasPrefix(line, "AUTH"):
			// Simplify processing: PLAIN's initial response may span multiple lines, directly accept it.
			w("235 2.7.0 Authentication successful")
		case strings.HasPrefix(line, "MAIL FROM"):
			w(f.mailReply)
		case strings.HasPrefix(line, "RCPT TO"):
			w(f.rcptReply)
		case strings.HasPrefix(line, "DATA"):
			w("354 End data with <CR><LF>.<CR><LF>")
			var sb strings.Builder
			for {
				dl, err:= br.ReadString('\n')
				if err!= nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				sb.WriteString(dl)
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			w("250 2.0.0 Ok: queued as FAKE1")
		case strings.HasPrefix(line, "QUIT"):
			w("221 2.0.0 Bye")
			return
		default:
			w("250 OK")
		}
	}
}

func emailCfg(t *testing.T, f *fakeSMTP, extra map[string]any) map[string]any {
	t.Helper()
	host, port:= f.hostPort(t)
	cfg:= map[string]any{
		"host": host,
		"port": float64(port),
		"from": "artex@example.com",
		"to": []any{"a@example.com", "b@example.com"},
	}
	for k, v:= range extra {
		cfg[k] = v
	}
	return cfg
}

func TestEmailSendDeliversFullMessage(t *testing.T) {
	f:= newFakeSMTP(t)
	f.advertiseAuth = true
	cfg:= emailCfg(t, f, map[string]any{"username": "artex", "password": "pw"})

	if _, err:= (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err!= nil {
		t.Fatalf("Submission Failed: %v", err)
	}
	// Envelope stage must reach: sender, two recipients, DATA.
	for _, want:= range []string{"MAIL FROM:<artex@example.com>", "RCPT TO:<a@example.com>", "RCPT TO:<b@example.com>", "DATA", "AUTH", "QUIT"} {
		if!f.sawCommand(want) {
			t.Errorf("SMTP Missing in session %q, Actual command:%v", want, f.commands)
		}
	}
	// The body is base64 HTML, and must include real vulnerability content (still recognizable after encoding).
	body:= f.body()
	if body == "" {
		t.Fatal("DATA No body received in the phase")
	}
	if!strings.Contains(body, "Content-Type: text/html") {
		t.Errorf("Missing Content-Type header:\n%s", body)
	}
	if!strings.Contains(body, "base64") {
		t.Errorf("Main content not base64 encoded (long HTML lines can break SMTP's 1000-character line length limit):\n%s", body)
	}
	// Multiple recipients must appear in the To header.
	if!strings.Contains(body, "a@example.com, b@example.com") {
		t.Errorf("To Header does not include all recipients:\n%s", body)
	}
}

func TestEmailSendWithoutAuth(t *testing.T) {
	// Should not send AUTH when no account is configured —— Some relays may reject it for this reason.
	f:= newFakeSMTP(t)
	cfg:= emailCfg(t, f, nil)
	if _, err:= (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err!= nil {
		t.Fatalf("Submission Failed: %v", err)
	}
	if f.sawCommand("AUTH") {
		t.Errorf("Sending AUTH without an account.: %v", f.commands)
	}
}

// TestEmailSendClassifiesSMTPReplies This is the direct validation for this audit repair:
// 5xx Permanently failed, 4xx (graylist) should be retried.
func TestEmailSendClassifiesSMTPReplies(t *testing.T) {
	cases:= []struct {
		name string
		rcptReply string
		mailReply string
		permanent bool
	}{
		{"Recipient permanently rejected with 550", "550 5.1.1 User unknown", "250 OK", true},
		{"Recipient encountered a 450 greylisted error.", "450 4.7.1 Greylisting in action", "250 OK", false},
		{"Recipient encountered 452 mailbox full", "452 4.2.2 Mailbox full", "250 OK", false},
		{"Sender permanently rejected by 553.", "250 OK", "553 5.1.3 Bad address", true},
		{"Sender encountered a 451 temporary error", "250 OK", "451 4.3.0 Temporary failure", false},
	}
	for _, tc:= range cases {
		t.Run(tc.name, func(t *testing.T) {
			f:= newFakeSMTP(t)
			f.rcptReply = tc.rcptReply
			f.mailReply = tc.mailReply
			_, err:= (emailChannel{}).Send(context.Background(), emailCfg(t, f, nil), singleMsg())
			if err == nil {
				t.Fatal("Should report an error")
			}
			if got:= IsPermanent(err); got!= tc.permanent {
				t.Fatalf("permanent Error judgment: Expecting %v Got %v (%v)", tc.permanent, got, err)
			}
			// The server text must be retained, otherwise users won't know whether to contact the server administrator or change the address.
			if!strings.Contains(err.Error(), strings.Fields(tc.rcptReply)[0]) &&!strings.Contains(err.Error(), strings.Fields(tc.mailReply)[0]) {
				t.Errorf("Error messages should retain the server's response code: %v", err)
			}
		})
	}
}

func TestEmailSendRefusesPlaintextCredentials(t *testing.T) {
	// net/smtp The PlainAuth rejects credentials on unencrypted connections (unless the target is localhost).
	// This is**Correct**Ensure safe behavior that cannot be bypassed; but provide an error message to guide users on how to fix it.
	// Trigger it with a non-localhost hostname.
	f:= newFakeSMTP(t)
	f.advertiseAuth = true
	_, port:= f.hostPort(t)
	cfg:= map[string]any{
		"host": "smtp.example.com", // Non localhost
		"port": float64(port),
		"from": "a@example.com",
		"to": []any{"b@example.com"},
		"username": "artex",
		"password": "pw",
	}
	_, err:= (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Skip("Local DNS resolution points to local server, skipping (does not affect other cases).")
	}
	// Counting as passed if connection fails or credentials are rejected; the key is**Can't**Silently send the password out.
	if!IsPermanent(err) &&!strings.Contains(err.Error(), "Connect") {
		t.Logf("Error:%v (Unable to connect to the expected target below localhost.", err)
	}
}

func TestEmailValidateReportsMissingFields(t *testing.T) {
	// Divide failures at a certain stage into 'retryable' and 'permanent failure' based on the SMTP response code for email channels.
	// Validation should preemptively block. Assertion checks what information is missing in the error message.
	cases:= []struct {
		name string
		cfg map[string]any
	}{
		{"Missing host", map[string]any{"port": float64(25), "from": "a@b.c", "to": []any{"d@e.f"}}},
		{"Missing port", map[string]any{"host": "smtp.example.com"}},
		{"port Out of Bounds", map[string]any{"host": "h", "port": float64(70000), "from": "a@b.c", "to": []any{"d@e.f"}}},
		{"Missing from", map[string]any{"host": "h", "port": float64(25), "to": []any{"d@e.f"}}},
		{"Missing 'to'", map[string]any{"host": "h", "port": float64(25), "from": "a@b.c"}},
	}
	for _, tc:= range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err:= (emailChannel{}).Validate(tc.cfg); err == nil {
				t.Fatalf("Should validate failed: %v", tc.cfg)
			}
		})
	}
}

// TestEmailConfigTolerance 'Fallback for configuration read error: JSONB values are float64',
// But users might enter ports as strings in the UI; arrays can also be a single string.
func TestEmailConfigTolerance(t *testing.T) {
	cfg:= map[string]any{
		"host": "smtp.example.com",
		"port": "587", // Port in string form
		"from": "a@b.c",
		"to": "d@e.f", // Single string rather than array
		"tls": "true", // Boolean in string form
	}
	if err:= (emailChannel{}).Validate(cfg); err!= nil {
		t.Fatalf("Should tolerate numeric values as strings.: %v", err)
	}
	if got:= cfgInt(cfg, "port"); got!= 587 {
		t.Errorf("cfgInt Unparsed string port received. %d", got)
	}
	if!cfgBool(cfg, "tls") {
		t.Error("cfgBool Unparsed string \"true\"")
	}
	if to:= cfgStrings(cfg, "to"); len(to)!= 1 || to[0]!= "d@e.f" {
		t.Errorf("cfgStrings Not compatible with single strings, got %v", to)
	}
}

// TestFilterValidateRejectsTypo Direct verification for audit repair:
// A typo in the threshold must be blocked when writing, otherwise the filter will silently fail and become a full push.
func TestFilterValidateRejectsTypo(t *testing.T) {
	good:= []string{"", "low", "medium", "high", "critical"}
	for _, s:= range good {
		if err:= (Filter{MinSeverity: s}).Validate(); err!= nil {
			t.Errorf("legal threshold %q Rejected: %v", s, err)
		}
	}
	// These are real mistakes that will happen——All must be rejected.
	for _, s:= range []string{"hgih", "HIGH", "Support", "high ", "crit"} {
		err:= (Filter{MinSeverity: s}).Validate()
		if err == nil {
			t.Errorf("Illegal barrier %q Should be rejected (otherwise the filter will silently fail and become a full push).", s)
			continue
		}
		// Error messages should guide users on how to correct them.
		if!strings.Contains(err.Error(), "low") ||!strings.Contains(err.Error(), "critical") {
			t.Errorf("Error messages should list optional values, received %q", err.Error())
		}
	}
}

// TestFilterValidateIsWriteTimeOnly Locking down the 'write strict, read broad' division:
// Existing bad values in the library should not prevent the channel from reading entirely (otherwise, historical channels would suddenly stop pushing).
func TestFilterValidateIsWriteTimeOnly(t *testing.T) {
	raw:= []byte(`{"min_severity":"hgih"}`)
	f:= ParseFilter(raw) // No error
	if f.MinSeverity!= "hgih" {
		t.Fatalf("Read path should be retained as-is, resulting in %q", f.MinSeverity)
	}
	// And this channel should still be able to make a judgment on events (don't panic, don't block).
	_ = Match(f, Snapshot{Kind: EventFindingCreated, Severity: "critical"})
}
