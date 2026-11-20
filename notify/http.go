package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// allowLocalTargets Decide whether to allow delivery of messages to loopback/link-local addresses.
//
// Default rejection. These addresses are not where IM robots or public email servers would appear, and they can
// What you hit is very sensitive: another service's management port on the same machine, and cloud environment metadata endpoints.
// (169.254.169.254, Readable instance credentials). The delivery address is set by the admin, but one that's been XSS/CSRF
// A borrowed management session or someone sharing the same JWT can read back the response content by changing the configuration
// ——doJSON It writes the first 200 bytes of 4xx/5xx response bodies into last_error, while the delivery history interface
// It will be echoed back; this is a semi-blind read primitive.
//
// But 'this machine SMTP relay' (127.0.0.1:25 The postfix (e.g., on the email) is a common configuration for self-hosted emails,
// A one-size-fits-all approach can get you stuck. So leave an explicit escape hatch instead of hardcoding passage:
// Set ARTEX_NOTIFY_ALLOW_LOCAL=1 allowed.
//
// Export as AllowLocalTargetsEnv to allow tests to clearly open it.——This package and the server package's
// Many use cases use 127.0.0.1 The httptest fake receiver on top, if not opened, will be blocked by all guards.
const AllowLocalTargetsEnv = "ARTEX_NOTIFY_ALLOW_LOCAL"

func allowLocalTargets() bool {
	v:= strings.TrimSpace(os.Getenv(AllowLocalTargetsEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// isBlockedDialIP Check if target IP belongs to the 'default disallowed delivery' address range.
//
// Only reject loopback and link-local addresses (including cloud metadata 169.254.x.x)..254.169.254), Not specified with multicast.
// **Don't reject** RFC1918 Private network: Building Mattermost / SMTP relays in an internal network is a common legitimate use case,
// Blocking them would make the feature unusable in a real environment. This trade-off is intentional.——
// Protection must block truly sensitive targets while not disabling normal deployments.
func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6 (::ffff:127.0.0.1)Restore to IPv4 before judging, otherwise bypass the check.
	if v4:= ip.To4(); v4!= nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// blockInternalDial Is http.Transport The Control hook of the dialer, where**Connection established when**
// Check target address.
//
// Why set it during dialing phase instead of only validating on saving configuration: this is the final effective point.
// It covers two scenarios of bypassing configuration validation simultaneously.——DNS Rebind (when parsed as public IP during validation,
// When the real connection parses to the internal network) with redirection (although we have rejected cross-host jumps, but same-host jumps
// It may still point to somewhere else.
func blockInternalDial(_, address string, _ syscall.RawConn) error {
	if allowLocalTargets() {
		return nil
	}
	host, _, err:= net.SplitHostPort(address)
	if err!= nil {
		return err
	}
	ip:= net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("Unable to resolve target address %q", host)
	}
	if isBlockedDialIP(ip) {
		return fmt.Errorf("Refuse delivery to this machine/local network address. %s (If delivery to local services is required, set %s=1)", ip, AllowLocalTargetsEnv)
	}
	return nil
}

// notifyTransport Add only one dial guard based on the default Transport.
// Clones to retain all default optimizations (connection pool, HTTP/2, timeout, proxy, etc.) of the default configuration,
// Avoid changing other behaviors just to add a check.
var notifyTransport = func() *http.Transport {
	t, ok:= http.DefaultTransport.(*http.Transport)
	if!ok {
		return &http.Transport{}
	}
	clone:= t.Clone()
	clone.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: blockInternalDial}).DialContext
	return clone
}()

// httpClient This client is shared by all channels for delivery.
//
// Intentional**not**Reuse the project's global exit proxy (the server-side GlobalProxy): that proxy is for penetration
// Target traffic often uses unstable tunnels, and notification availability shouldn't be bound by fluctuations in the target network.
// IM Push directly. Timeout set to 15 seconds.——The other end is actually already in a fault state.
//
// Reject cross-host redirects: The delivery addresses for this feature are all in the form of 'a fixed endpoint', which normally won't
// Redirect to another host; and these companies' credentials (DingTalk's access_token, WeCom's key, Telegram's
// bot token)**Right in the URL**, Following cross-host redirection equals handing over credentials to the redirect target. For the same host
// Redirects (like appending a slash at the end) are still allowed.
var httpClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: notifyTransport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("Too many redirects")
		}
		if len(via) > 0 && req.URL.Host!= via[0].URL.Host {
			return fmt.Errorf("Reject cross-host redirection (%s → %s)", via[0].URL.Host, req.URL.Host)
		}
		return nil
	},
}

// respBodyLimit Limit the size of the response body read. When the other end has an exception, it may return extremely large content, while we only need
// Error code and a short error description for display in delivery history.
const respBodyLimit = 8 << 10

// doJSON Send one request and return the response body (already limited in length).
//
// payload Send an empty body when nil (used in GET requests or when the platform does not require a body).
// headers The key-value pairs are appended as-is for use in custom headers for generic Webhooks.
//
// Error classification is the core responsibility of this function: network layer failures and 5xx/408/429 are classified as 'retryable',
// Other 4xx errors are classified as 'permanent failure'.——Retrying a 403 just logs the same error three times.
func doJSON(ctx context.Context, method, url string, headers map[string]string, payload any) ([]byte, error) {
	var body io.Reader
	if payload!= nil {
		raw, err:= json.Marshal(payload)
		if err!= nil {
			// Serialization failure is a local bug (incorrect configuration field type), retries won't help.
			return nil, Permanent(fmt.Errorf("Failed to Construct Request Body: %w", err))
		}
		body = bytes.NewReader(raw)
	}
	req, err:= http.NewRequestWithContext(ctx, method, url, body)
	if err!= nil {
		// URL Illegal——This is likely due to a user entering an incorrect address, which is considered a permanent failure.
		// Here, err cannot be passed through: url.Parse The error text should contain the full address.
		return nil, Permanent(fmt.Errorf("Invalid request address: %s", redactRequestTarget(url)))
	}
	if payload!= nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v:= range headers {
		req.Header.Set(k, v)
	}
	resp, err:= httpClient.Do(req)
	if err!= nil {
		// Connection refused, DNS failure, timeout.——OftenTransient failures are left to backoff retry.
		//
		// Error text must be redacted before being transmitted. Reason: http.Client.Do Returns *url.Error,
		// Its Error() is `Op "Complete URL": Underlying Error`, And for these few credentials in this feature,**Right in the URL**
		// (Dingtalk access_token, Qianwai key, Feishu hook id, Telegram /bot<token>/).
		// If not redacted, credentials will flow to four places along this error stream: notification_deliveries
		// The last_error (plain text stored in the database) and the response from the delivery history interface (**Bypass channel configuration mask**),
		// Server logs and 502 text returned to the frontend from the test send interface.
		return nil, fmt.Errorf("Request Failed: %s", redactTransportError(err))
	}
	defer resp.Body.Close()
	raw, readErr:= io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if readErr!= nil {
		return nil, fmt.Errorf("Failed to read response.: %w", readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	// 429 (Rate limiting) and 408 (timeout) are worth retrying; all other 4xx are configuration or permission issues, retries are meaningless.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("The other party has rate limiting or timeout (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("Other party service exception (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	return nil, Permanent(fmt.Errorf("Other party rejected request (HTTP %d): %s", resp.StatusCode, snippet(raw)))
}

// snippet Compress the response body into a single short text for error messages. The response may contain line breaks and large amounts of whitespace,
// 'Directly inserting into last_error will cause the delivery history page to layout break.',
func snippet(raw []byte) string {
	return OneLine(string(raw), 200)
}

// redactRequestTarget Compress the submission address into 'scheme'://host/...', Used for error information.
//
// This is the unique address desensitization criterion for this package, deliberately made**Brutal enough**: Except for scheme and host,
// All others are discarded. The reason is there is no 'general and safe' way to determine which segment of a URL is a credential:
//
//	DingTalk credentials in query/robot/send?access_token=xxx
//	Qianwai credentials in query /cgi-bin/webhook/send?key=xxx
//	Feishu credentials at**path end** /open-apis/bot/v2/hook/<hook_id>
//	Telegram Credentials in**Segment of the path** /bot<token>/sendMessage
//
// Want to 'keep only useful parts' means patching by channel, missing any one is a credential leak.
// Retaining host is already enough for troubleshooting (it can locate issues even if DNS resolution fails, connection cannot be established, or the certificate is incorrect),
// Which specific robot is recognized by the channel configuration's mask tail number.
//
// Return fixed placeholder on parsing failure——Never echo the original string.
func redactRequestTarget(raw string) string {
	u, err:= url.Parse(raw)
	if err!= nil || u.Host == "" {
		return "(Address cannot be resolved)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// redactTransportError Peel off addresses from transmission layer errors, retaining only the underlying reason.
//
// *url.Error Its Structure is {Op, URL, Err}, Error() It will print the URL together.
// Explicitly take the Err field here, bypassing its Error() ——More reliable than string replacement after the fact,
// Because replacements must correctly handle various variants of URL encoding/escaping, it's easy to miss.
func redactTransportError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		host:= ""
		if u, parseErr:= url.Parse(uerr.URL); parseErr == nil {
			host = u.Host
		}
		if uerr.Err!= nil {
			return fmt.Sprintf("%s %s: %s", uerr.Op, host, uerr.Err)
		}
		return fmt.Sprintf("%s %s: Unknown error", uerr.Op, host)
	}
	// non *url.Error (Errors returned by redirection policies may also contain addresses, which should be redacted uniformly.
	return redactURLsInText(err.Error())
}

// redactURLsInText Replace http in a segment of text with(s) Replace addresses with redacted form.
//
// Used as a fallback for errors that cannot retrieve structured fields (redirection policy errors, custom errors from third-party libraries).
// Only recognizes http/https prefixes, splits by whitespace and quotes.——The address will not contain these two types of characters.
func redactURLsInText(s string) string {
	var b strings.Builder
	for i:= 0; i < len(s); {
		rest:= s[i:]
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
			end:= len(rest)
			if j:= strings.IndexAny(rest, " \t\n\"'"); j >= 0 {
				end = j
			}
			b.WriteString(redactRequestTarget(rest[:end]))
			i += end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
