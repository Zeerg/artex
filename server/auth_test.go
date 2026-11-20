package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// GetSetting For"Key does not exist"And"Read error"The return value differs by one error: both cases have
// Empty string. Password-related handlers once treat error as"Password not set yet", During databaseduring flapping
// Open initialization entry——authInit It will allow an unauthenticated request to overwrite an existing administrator password,
// authStatus It will then send the frontend directly to /setup to do this.
//
// These two tests shut down the handler's connection pool to create read failures, asserting that both fail closed.
func TestAuthStatusFailsClosedWhenDataSourceUnavailable(t *testing.T) {
	m, err:= NewManager(t.TempDir(), "")
	if err!= nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	// Turn off the pool so that subsequent GetSetting returns an error instead of sql.ErrNoRows.
	if err:= m.pg.Close(); err!= nil {
		t.Fatal(err)
	}

	s:= &Server{m: m}
	w:= httptest.NewRecorder()
	s.authStatus(w, httptest.NewRequest("GET", "/api/auth/status", nil))

	if w.Code!= 503 {
		t.Fatalf("status=%d want 503 (Read failure treated as uninitiated sends user to /setup to reset password); body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Initialized *bool `json:"initialized"`
	}
	if err:= json.Unmarshal(w.Body.Bytes(), &payload); err == nil && payload.Initialized!= nil {
		t.Fatalf("Do not respond with initialized if read fails, get %v", *payload.Initialized)
	}
}

func TestAuthInitFailsClosedWhenDataSourceUnavailable(t *testing.T) {
	m, err:= NewManager(t.TempDir(), "")
	if err!= nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	if err:= m.pg.Close(); err!= nil {
		t.Fatal(err)
	}

	s:= &Server{m: m}
	w:= httptest.NewRecorder()
	body:= strings.NewReader(`{"password":"correct horse battery"}`)
	s.authInit(w, httptest.NewRequest("POST", "/api/auth/init", body))

	if w.Code!= 503 {
		t.Fatalf("status=%d want 503 (Allowing requests to pass when reading fails could allow unauthenticated requests to overwrite existing passwords.); body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "token") {
		t.Fatalf("Do not issue token when reading fails.: %s", w.Body.String())
	}
}

func TestValidatePassword(t *testing.T) {
	for _, tc:= range []struct {
		name, pw string
		wantErr bool
	}{
		{"empty", "", true},
		{"Seven Characters", "1234567", true},
		{"8 Characters", "12345678", false},
		{"Counting eight Chinese characters by character count rather than byte count.", "Password password password password", false},
		{"Three Chinese characters are enough for 9 bytes but only 3 characters.", "\u5bc6\u7801\u5f3a", true},
		{"72 bytes", strings.Repeat("a", 72), false},
		{"73 Bytes exceed bcrypt limit", strings.Repeat("a", 73), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got:= validatePassword(tc.pw); (got!= "")!= tc.wantErr {
				t.Fatalf("validatePassword(%q)=%q, wantErr=%v", tc.pw, got, tc.wantErr)
			}
		})
	}
}
