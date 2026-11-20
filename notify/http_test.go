package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// This file overrides HTTP error classification for doJSON.
//
// Why test separately: each channel adapter only handles platform-specific business error codes (DingTalk errcode, etc.,
// Feishu code, Telegram ok field), while**HTTP Layer**The classification is done uniformly by doJSON,
// The two are independent lines of defense. Without this one, a 503 redirect gateway would be considered permanent failure,
// Directly give up retrying; a 403 will be treated as retryable and wasted on three rounds of backoff.

func replyServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv:= httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDoJSONClassifiesHTTPStatus(t *testing.T) {
	cases:= []struct {
		name string
		status int
		permanent bool
	}{
		{"200 Success is not an error.", 200, false},
		{"429 Rate limit retryable", 429, false},
		{"408 Request timeouts can be retried", 408, false},
		{"500 Server errors can be retried", 500, false},
		{"502 Gateway Error Can Retry", 502, false},
		{"503 Service unavailable, retryable", 503, false},
		{"400 Parameter error leads to permanent failure", 400, true},
		{"401 Authentication failure permanent failure", 401, true},
		{"403 Permanent access failure prohibited", 403, true},
		{"404 Address does not exist permanent failure.", 404, true},
	}
	for _, tc:= range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv:= replyServer(t, tc.status, `{"detail":"upstream says no"}`)
			_, err:= doJSON(context.Background(), "GET", srv.URL, nil, nil)
			if tc.status < 300 {
				if err!= nil {
					t.Fatalf("2xx should not error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Non-2xx should report error")
			}
			if got:= IsPermanent(err); got!= tc.permanent {
				t.Fatalf("HTTP %d The permanent determination error: Expected %v Got %v (%v)",
					tc.status, tc.permanent, got, err)
			}
			// The status code must appear in the error, otherwise the user has no way of knowing if they configured it wrong or if the other end went down.
			// Assert number instead of Go's English StatusText: The text in this package is Chinese
			// (Consistent with other parts of the project), numbers are language-independent and can stably assert themselves.
			if!strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
				t.Errorf("Error messages should include the HTTP status code. %d, Got %v", tc.status, err)
			}
		})
	}
}

// TestDoJSONIncludesResponseSnippet Override snippet: The error message returned by the other end should be brought back,
// Otherwise, users only know 'failed' without knowing why the other end rejected it.
func TestDoJSONIncludesResponseSnippet(t *testing.T) {
	srv:= replyServer(t, 400, `{"error":"invalid webhook token"}`)
	_, err:= doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("Should report an error")
	}
	if!strings.Contains(err.Error(), "invalid webhook token") {
		t.Errorf("Error messages should return the explanation from the opposite end, resulting in %v", err)
	}
}

// TestDoJSONSnippetIsSingleLineAndBounded Constraint snippet form:
// It will be sent to the last_error column and frontend table as is. Multi-line/long content can break formatting and payload.
func TestDoJSONSnippetIsSingleLineAndBounded(t *testing.T) {
	// Response with newline, tab, and 5000-character long content.
	long:= strings.Repeat("x", 5000)
	srv:= replyServer(t, 500, "line1\nline2\r\n\tline3 "+long)
	_, err:= doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("Should report an error")
	}
	msg:= err.Error()
	if strings.ContainsAny(msg, "\r\n\t") {
		t.Errorf("Error messages should be compressed to a single line, resulting in %q", msg)
	}
	// snippet Limit 200 characters + Fixed prefix, total must be far less than the original response.
	if len(msg) > 400 {
		t.Errorf("Error message too long (%d bytes), should be truncated by snippet: %q", len(msg), msg)
	}
}

// TestDoJSONRejectsOversizedResponse Confirm reading has an upper limit: when the other end returns excessively large content
// Can't read the entire response into memory (every entry in delivery history will store one last_error).
func TestDoJSONRejectsOversizedResponse(t *testing.T) {
	huge:= strings.Repeat("A", 1<<20) // 1 MiB
	srv:= replyServer(t, 400, huge)
	_, err:= doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("Should report an error")
	}
	if len(err.Error()) > 400 {
		t.Errorf("Large responses should be truncated after limited reading, with different lengths of error messages %d", len(err.Error()))
	}
}
