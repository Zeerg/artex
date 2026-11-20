package server

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	jwtKeyFilename = "jwt.key"
	authPassKey = "auth.password_hash"
	jwtTTL = 7 * 24 * time.Hour
	keyChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	// The lower limit should match the frontend validation on the setup page.——Validation only on frontend is useless, directly hitting API can bypass it.
	// Bypass. The limit is the hard restriction of bcrypt: GenerateFromPassword returns
	// ErrPasswordTooLong, It's better to block good users from receiving an unclear 'password encryption failed' message.
	minPasswordRunes = 8
	maxPasswordBytes = 72
)

// errDataSourceUnavailable Unified response for password-related read operation failures. These handlers must never
// put"Can't read"treat as"Not set": authInit Previously, it allowed unauthenticated requests to overwrite when there was a database error.
// Drop existing admin password.
const errDataSourceUnavailable = "The data source is temporarily unavailable. Please retry later."

// validatePassword Return an empty string to indicate success; otherwise, return a Chinese reason that can be directly displayed to the user.
func validatePassword(pw string) string {
	if utf8.RuneCountInString(pw) < minPasswordRunes {
		return fmt.Sprintf("Password length must be at least %d At", minPasswordRunes)
	}
	if len(pw) > maxPasswordBytes {
		return fmt.Sprintf("Password length cannot exceed %d bytes", maxPasswordBytes)
	}
	return ""
}

// loadOrCreateJWTKey reads the 32-byte signing key from keyDir/jwt.key. keyDir is
// the project base dir (next to the executable), NOT the browsable workspace root
// (dataDir) — the signing key must never be listable/downloadable via the file
// manager. Legacy installs kept it at dataDir/jwt.key; if present there and not yet
// at the new location, it is migrated (key preserved, so sessions stay valid) and
// the old file removed so it disappears from the workspace. On first run a random
// key is generated and persisted.
func loadOrCreateJWTKey(keyDir, dataDir string) ([]byte, error) {
	path:= filepath.Join(keyDir, jwtKeyFilename)
	// one-time migration out of the old in-workspace location.
	if legacy:= filepath.Join(dataDir, jwtKeyFilename); legacy!= path {
		if _, err:= os.Stat(path); os.IsNotExist(err) {
			if data, rerr:= os.ReadFile(legacy); rerr == nil {
				if werr:= os.WriteFile(path, data, 0o600); werr == nil {
					_ = os.Remove(legacy)
					log.Printf("[auth] JWT key Already removed from %s Migrate to %s (Remove browseable workspace)", legacy, path)
				}
			}
		}
	}
	if data, err:= os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		return []byte(strings.TrimSpace(string(data))), nil
	}
	buf:= make([]byte, 32)
	for i:= range buf {
		n, err:= rand.Int(rand.Reader, big.NewInt(int64(len(keyChars))))
		if err!= nil {
			return nil, fmt.Errorf("generate jwt key: %w", err)
		}
		buf[i] = keyChars[n.Int64()]
	}
	if err:= os.WriteFile(path, buf, 0600); err!= nil {
		return nil, fmt.Errorf("write jwt key: %w", err)
	}
	log.Printf("[auth] New JWT key written %s", path)
	return buf, nil
}

// signJWT issues a 7-day HS256 token for user ARTEX.
func signJWT(key []byte) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject: "ARTEX",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(jwtTTL)),
		IssuedAt: jwt.NewNumericDate(time.Now()),
	}).SignedString(key)
}

// verifyJWT returns true when tokenStr is a valid, non-expired HS256 token.
func verifyJWT(tokenStr string, key []byte) bool {
	t, err:= jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok:= t.Method.(*jwt.SigningMethodHMAC);!ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return key, nil
	})
	return err == nil && t.Valid
}

// extractToken reads the JWT from Authorization: Bearer header,
// artex_token cookie, or?token= query param (for SSE connections).
func extractToken(r *http.Request) string {
	if h:= r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err:= r.Cookie("artex_token"); err == nil && c.Value!= "" {
		return c.Value
	}
	return r.URL.Query().Get("token")
}

// requireAuth wraps h with JWT validation.
// /api/auth/* and /api/health are exempt.
func (s *Server) requireAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p:= r.URL.Path
		if strings.HasPrefix(p, "/api/auth/") || p == "/api/health" {
			h.ServeHTTP(w, r)
			return
		}
		tok:= extractToken(r)
		if tok == "" {
			writeErr(w, 401, "Unauthorized")
			return
		}
		if!verifyJWT(tok, s.jwtKey) {
			writeErr(w, 401, "token Invalid or expired")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// GET /api/auth/status — reports whether the admin password has been initialised.
// Read failure must return 503 instead of initialized:false: Frontend is initialized:false It will affect the user
// Send to /setup to set the password (login/page.tsx), Wrap database failures as 200 equals pushing users to
// Pushing over an existing password.
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	pg:= s.pg(w)
	if pg == nil {
		return
	}
	hash, _, err:= pg.GetSetting(authPassKey)
	if err!= nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	writeJSON(w, 200, map[string]any{"initialized": hash!= ""})
}

// POST /api/auth/init — sets the password for the first time; rejected if already set.
func (s *Server) authInit(w http.ResponseWriter, r *http.Request) {
	pg:= s.pg(w)
	if pg == nil {
		return
	}
	existing, _, err:= pg.GetSetting(authPassKey)
	if err!= nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if existing!= "" {
		writeErr(w, 403, "Password set")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err:= decode(r, &req); err!= nil || req.Password == "" {
		writeErr(w, 400, "Password cannot be empty.")
		return
	}
	if msg:= validatePassword(req.Password); msg!= "" {
		writeErr(w, 400, msg)
		return
	}
	hash, err:= bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err!= nil {
		writeErr(w, 500, "Password encryption failed")
		return
	}
	// Use INSERT ... ON CONFLICT DO NOTHING Instead of upsert: The previous GetSetting was just
	// Fast failure path, truly"Only set for the first time"guarantee falls on primary key constraints. bcrypt takes several milliseconds,
	// During this time, other requests might have already set the password, and the read check itself may also fail due to a fault.
	inserted, err:= pg.InsertSettingIfAbsent(authPassKey, string(hash))
	if err!= nil {
		writeErr(w, 500, "Save failed.: "+err.Error())
		return
	}
	if!inserted {
		writeErr(w, 403, "Password set")
		return
	}
	tok, err:= signJWT(s.jwtKey)
	if err!= nil {
		writeErr(w, 500, "token generation failed")
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}

// POST /api/auth/change-password — changes the admin password. Requires a valid
// token (this route is under /api/auth/* which requireAuth exempts, so the token
// is validated here) AND the current password.
func (s *Server) authChangePassword(w http.ResponseWriter, r *http.Request) {
	pg:= s.pg(w)
	if pg == nil {
		return
	}
	if!verifyJWT(extractToken(r), s.jwtKey) {
		writeErr(w, 401, "Unauthorized")
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err:= decode(r, &req); err!= nil {
		writeErr(w, 400, "Request format error")
		return
	}
	if req.NewPassword == "" {
		writeErr(w, 400, "New password cannot be empty")
		return
	}
	if msg:= validatePassword(req.NewPassword); msg!= "" {
		writeErr(w, 400, msg)
		return
	}
	hash, ok, err:= pg.GetSetting(authPassKey)
	if err!= nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if!ok || hash == "" {
		writeErr(w, 403, "Password not initialized, please set a password first.")
		return
	}
	if err:= bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.OldPassword)); err!= nil {
		writeErr(w, 401, "Current password is incorrect")
		return
	}
	newHash, err:= bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err!= nil {
		writeErr(w, 500, "Password encryption failed")
		return
	}
	if err:= pg.SetSetting(authPassKey, string(newHash)); err!= nil {
		writeErr(w, 500, "Save failed.: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// POST /api/auth/login — validates username/password and returns a JWT.
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	pg:= s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err:= decode(r, &req); err!= nil {
		writeErr(w, 400, "Request format error")
		return
	}
	if req.Username!= "ARTEX" {
		writeErr(w, 401, "Username or password error")
		return
	}
	hash, ok, err:= pg.GetSetting(authPassKey)
	if err!= nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if!ok || hash == "" {
		writeErr(w, 403, "Password not initialized, please set a password first.")
		return
	}
	if err:= bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err!= nil {
		writeErr(w, 401, "Username or password error")
		return
	}
	tok, err:= signJWT(s.jwtKey)
	if err!= nil {
		writeErr(w, 500, "token generation failed")
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok})
}
