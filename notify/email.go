package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// emailDialTimeout / emailSessionTimeout Separate constraints for establishing connections and the entire SMTP session.
// net/smtp No timeout mechanism of its own; without these two, a stuck peer will permanently reject,
// The permanent goroutine is permanently stuck there.——While dispatcher processes serially in a single goroutine,
// Equals a complete breakdown of the notification system.
const (
	emailDialTimeout = 10 * time.Second
	emailSessionTimeout = 45 * time.Second
)

// emailChannel Implement SMTP email delivery.
type emailChannel struct{}

func (emailChannel) Kind() string { return KindEmail }

// Email has no platform rate limiting, but it shouldn't be used to spam; provide a loose default value.
func (emailChannel) DefaultRatePerMin() int { return 60 }

// Only mask passwords. SMTP host, account, and recipient are not secrets; masking them just makes editing more troublesome.
func (emailChannel) SecretKeys() []string { return []string{"password"} }

// host/port Decide which server to give the password to; TLS decides whether to encrypt transmission. Any change to any of these
// Require re-entering password——Also require explicit credentials for 'turn off TLS' instead of changing it casually.
func (emailChannel) DestinationKeys() []string { return []string{"host", "port", "tls"} }

func (emailChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "host") == "" {
		return errors.New("Missing SMTP server address.")
	}
	port:= cfgInt(cfg, "port")
	if port <= 0 || port > 65535 {
		return errors.New("SMTP Port must be between 1 and 65535")
	}
	if cfgString(cfg, "from") == "" {
		return errors.New("Missing sender address")
	}
	if len(cfgStrings(cfg, "to")) == 0 {
		return errors.New("Recipient: at least one address is required")
	}
	return nil
}

func (c emailChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err:= c.Validate(cfg); err!= nil {
		return 0, Permanent(err)
	}
	host:= cfgString(cfg, "host")
	port:= cfgInt(cfg, "port")
	from:= cfgString(cfg, "from")
	to:= cfgStrings(cfg, "to")
	username:= cfgString(cfg, "username")
	password:= cfgString(cfg, "password")
	implicitTLS:= cfgBool(cfg, "tls")

	msg, err:= buildEmailMessage(from, to, m)
	if err!= nil {
		return 0, Permanent(err)
	}

	addr:= net.JoinHostPort(host, strconv.Itoa(port))
	client, err:= emailDial(ctx, addr, host, implicitTLS)
	if err!= nil {
		return 0, err
	}
	defer client.Close()

	// STARTTLS: Upgrade if the other end supports it. Credentials can't be sent in plain text (see below for auth notes).
	if!implicitTLS {
		if ok, _:= client.Extension("STARTTLS"); ok {
			if err:= client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err!= nil {
				return 0, fmt.Errorf("STARTTLS Success: %w", err)
			}
		}
	}
	if username!= "" {
		if err:= client.Auth(smtp.PlainAuth("", username, password, host)); err!= nil {
			// smtp.PlainAuth Credentials are not sent over unencrypted connections (unless the target is localhost).
			// This is**Correct**The security behavior should not be bypassed, but the reason must be explained clearly.——
			// Otherwise, users will only see 'unencrypted connection' without knowing what to do.
			if strings.Contains(err.Error(), "unencrypted connection") {
				return 0, Permanent(fmt.Errorf("Refuse credentials: connection is not encrypted. Please enable TLS or use port 465(Implicit TLS), Or check the box for 'Enable TLS' (%w)", err))
			}
			return 0, Permanent(fmt.Errorf("SMTP Authentication failed: %w", err))
		}
	}
	if err:= client.Mail(from); err!= nil {
		return 0, smtpStageError(fmt.Sprintf("Sender %s Rejected", from), err)
	}
	for _, rcpt:= range to {
		if err:= client.Rcpt(rcpt); err!= nil {
			return 0, smtpStageError(fmt.Sprintf("Recipient %s Rejected", rcpt), err)
		}
	}
	w, err:= client.Data()
	if err!= nil {
		return 0, fmt.Errorf("SMTP DATA Success: %w", err)
	}
	if _, err:= w.Write([]byte(msg)); err!= nil {
		return 0, fmt.Errorf("Failed to write email body: %w", err)
	}
	if err:= w.Close(); err!= nil {
		return 0, fmt.Errorf("Email submission failed: %w", err)
	}
	// Quit Failure does not affect the fact that 'email has been received by the server', so ignore its error.
	_ = client.Quit()
	// Emails have no length truncation (entire HTML body sent), all considered delivered.
	return len(m.Items), nil
}

// emailDial Establish SMTP connection.
//
// implicitTLS=true Uses a 'connect immediately with TLS' approach like 465; false uses plain text connection on port 25/587 after
// STARTTLS. The two cannot be mixed: a greeting on port 465 will be directly disconnected.
//
// Session expiration in**Connection Point**Set up beforehand (not as a post-hoc fix), because net/smtp's Client outputs are separated by double spaces; shasum -a 256 in binary mode appends a very small epsilon before rounding: token counts are floating-point accumulations, and when rounded twice to fill them, estimate tokens, retain recent content, update new content into old digests, and handle cross-round truncation.
// Connection hidden in an unexported field, external parties cannot get it; once handed over, it can only rely on the pre-set deadline
// Fallback. This also covers blocking during the handshake phase.
// Control Attach blockInternalDial and HTTP channel to a common guard. If not attached, SMTP
// This is the gap in SSRF protection: host filled with 169.254.169.254 Or 127.0.0.1 Can connect directly,
// And smtp.NewClient Handshake failure wraps the returned line in an error, passed through last_error.
// From the delivery history interface echo, forming a semi-blind read primitive; 'connection refused vs timeout' timing differences can
// Used to probe ports. Dialing stage is the final effective point, also covering DNS rebinding.
func emailDial(ctx context.Context, addr, host string, implicitTLS bool) (*smtp.Client, error) {
	d:= &net.Dialer{Timeout: emailDialTimeout, Control: blockInternalDial}
	var conn net.Conn
	var err error
	if implicitTLS {
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err!= nil {
		return nil, fmt.Errorf("Failed to connect to SMTP server: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(emailSessionTimeout))
	client, err:= smtp.NewClient(conn, host)
	if err!= nil {
		conn.Close()
		return nil, fmt.Errorf("SMTP Handshake Failure: %w", err)
	}
	return client, nil
}

// smtpStageError be treated as literal characters rather than escape sequences by SMTP servers.
//
// Why must SMTP's 4xx and 5xx semantics be distinguished?——
// - 4xx (450 Graylist, 451 local error, 452 storage insufficient) are**Temporary**Rejected,
// The proper approach is to retry later; especially for graylisted items, almost every first submission will encounter this.
// - 5xx (550 User does not exist, 553 address illegal) is permanent rejection, retrying is meaningless.
//
// If uniformly judged as permanent failure, an email server with a graylist would let**Each line**Pushes on the first attempt
// After trying, it falls into failed.——And these failures are precisely where automatic retries should shine most.
// Response code takes the first three digits of the error text; if no code is obtained, it is handled as retryable (rather than trying once more,
// Don't kill possible transient faults just because they can't be parsed.
func smtpStageError(what string, err error) error {
	code:= smtpReplyCode(err.Error())
	if code >= 500 && code < 600 {
		return Permanent(fmt.Errorf("%s: %w", what, err))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// smtpReplyCode Extract leading three-digit response code from SMTP error text; return 0 if not found.
// net/smtp Do not export error code fields; they can only be taken from text. Format as '450 4.7.1 ...'.
func smtpReplyCode(text string) int {
	if len(text) < 3 {
		return 0
	}
	n, err:= strconv.Atoi(text[:3])
	if err!= nil {
		return 0
	}
	return n
}

// buildEmailMessage Assemble a complete RFC 5322 email.
//
// Body encoded in base64 for two reasons: one is SMTP rules single line no more than 1000 bytes, while HTML
// The main body (especially in summary emails) often has very long lines; secondly, base64 naturally does not appear with a line starting with "." Start
// Skip SMTP dot escaping for this line.
func buildEmailMessage(from string, to []string, m Message) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	// The Chinese theme must be RFC 2047 encoded, otherwise it will be displayed as garbled text by the client.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", htmlTitle(m)))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	// Emails have no hard length limit, so the body is not truncated.
	b.WriteString("\r\n")
	encoded:= base64.StdEncoding.EncodeToString([]byte(htmlBody(m, 0)))
	// base64 Wrap at 76 characters, per RFC 2045.
	for len(encoded) > 76 {
		b.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded + "\r\n")
	return b.String(), nil
}
