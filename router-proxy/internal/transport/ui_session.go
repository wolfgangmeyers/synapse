package transport

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	uiSessionCookieName = "mecha_ui_session"
	uiSessionLifetime   = 8 * time.Hour
	uiSessionCapacity   = 64
)

type SessionRegistryOptions struct {
	Clock    deterministicClock
	Random   io.Reader
	Capacity int
}
type deterministicClock interface{ Now() time.Time }

type uiSession struct {
	tokenHash, csrfHash, salt []byte
	expiresAt                 time.Time
}
type UISession struct {
	IdentityHash string
	ExpiresAt    time.Time
	csrfHash     []byte
}

type SessionRegistry struct {
	mu       sync.Mutex
	clock    deterministicClock
	random   io.Reader
	capacity int
	sessions map[string]uiSession
}

func NewSessionRegistry(options SessionRegistryOptions) *SessionRegistry {
	clock := options.Clock
	if clock == nil {
		clock = wallClockAdapter{}
	}
	random := options.Random
	if random == nil {
		random = rand.Reader
	}
	capacity := options.Capacity
	if capacity == 0 {
		capacity = uiSessionCapacity
	}
	return &SessionRegistry{clock: clock, random: random, capacity: capacity, sessions: map[string]uiSession{}}
}

type wallClockAdapter struct{}

func (wallClockAdapter) Now() time.Time { return time.Now() }
func (s *SessionRegistry) Issue() (string, string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeExpiredLocked()
	if len(s.sessions) >= s.capacity {
		return "", "", time.Time{}, errors.New("UI session capacity reached")
	}
	token, err := randomToken(s.random)
	if err != nil {
		return "", "", time.Time{}, err
	}
	csrf, err := randomToken(s.random)
	if err != nil {
		return "", "", time.Time{}, err
	}
	salt, err := randomToken(s.random)
	if err != nil {
		return "", "", time.Time{}, err
	}
	expires := s.clock.Now().UTC().Add(uiSessionLifetime)
	session := uiSession{tokenHash: hashToken(salt, token), csrfHash: hashToken(salt, csrf), salt: salt, expiresAt: expires}
	s.sessions[base64.RawURLEncoding.EncodeToString(session.tokenHash)] = session
	return base64.RawURLEncoding.EncodeToString(token), base64.RawURLEncoding.EncodeToString(csrf), expires, nil
}
func randomToken(reader io.Reader) ([]byte, error) {
	value := make([]byte, 32)
	_, err := io.ReadFull(reader, value)
	return value, err
}
func hashToken(salt, token []byte) []byte {
	sum := sha256.New()
	_, _ = sum.Write(salt)
	_, _ = sum.Write(token)
	return sum.Sum(nil)
}
func (s *SessionRegistry) Authenticate(token string) (UISession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" {
		return UISession{}, errors.New("UI session required")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return UISession{}, errors.New("UI session required")
	}
	for _, candidate := range s.sessions {
		if candidate.expiresAt.Before(s.clock.Now()) {
			continue
		}
		if subtle.ConstantTimeCompare(candidate.tokenHash, hashToken(candidate.salt, raw)) == 1 {
			return UISession{IdentityHash: base64.RawURLEncoding.EncodeToString(candidate.tokenHash), ExpiresAt: candidate.expiresAt, csrfHash: append([]byte(nil), candidate.csrfHash...)}, nil
		}
	}
	s.removeExpiredLocked()
	return UISession{}, errors.New("UI session expired")
}
func (s *SessionRegistry) ValidateCSRF(session UISession, value string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return false
	}
	// The session registry keeps only the salted CSRF hash. A constant-time
	// comparison is required after deriving the candidate with the same
	// per-session salt, so locate the live record by its identity hash.
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, candidate := range s.sessions {
		if base64.RawURLEncoding.EncodeToString(candidate.tokenHash) == session.IdentityHash {
			return subtle.ConstantTimeCompare(candidate.csrfHash, hashToken(candidate.salt, raw)) == 1
		}
	}
	return false
}
func (s *SessionRegistry) Invalidate(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return
	}
	for key, candidate := range s.sessions {
		if subtle.ConstantTimeCompare(candidate.tokenHash, hashToken(candidate.salt, raw)) == 1 {
			delete(s.sessions, key)
		}
	}
}
func (s *SessionRegistry) InvalidateAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = map[string]uiSession{}
}
func (s *SessionRegistry) removeExpiredLocked() {
	now := s.clock.Now()
	for key, candidate := range s.sessions {
		if !candidate.expiresAt.After(now) {
			delete(s.sessions, key)
		}
	}
}
func (s *SessionRegistry) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeExpiredLocked()
	return len(s.sessions)
}

func setUISessionCookie(w http.ResponseWriter, req *http.Request, token string, maxAge int) {
	cookie := &http.Cookie{Name: uiSessionCookieName, Value: token, Path: "/v1", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge}
	if req.TLS != nil {
		cookie.Secure = true
	}
	http.SetCookie(w, cookie)
}
func clearUISessionCookie(w http.ResponseWriter, req *http.Request) {
	// net/http renders MaxAge < 0 as the required Max-Age=0 deletion marker.
	setUISessionCookie(w, req, "", -1)
}
func uiSessionToken(req *http.Request) string {
	cookie, err := req.Cookie(uiSessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}
