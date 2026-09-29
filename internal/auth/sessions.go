package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"sync"
	"time"
)

type session struct {
	expires     time.Time
	fingerprint string
}

// Sessions is an in-memory session store. Restarting Nexo signs everyone out,
// which is acceptable for a single-admin local device.
type Sessions struct {
	mu  sync.Mutex
	m   map[string]session
	ttl time.Duration
	now func() time.Time
}

// NewSessions returns a store whose sessions expire after ttl.
func NewSessions(ttl time.Duration) *Sessions {
	return &Sessions{m: map[string]session{}, ttl: ttl, now: time.Now}
}

// Fingerprint derives a value from the stored password hash. A session is only
// valid while it matches, so changing the password signs everyone out.
func Fingerprint(passwordHash string) string {
	sum := sha256.Sum256([]byte(passwordHash))
	return hex.EncodeToString(sum[:])
}

// Create issues a new session token.
func (s *Sessions) Create(fingerprint string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for t, sess := range s.m {
		if now.After(sess.expires) {
			delete(s.m, t)
		}
	}
	s.m[token] = session{expires: now.Add(s.ttl), fingerprint: fingerprint}
	return token, nil
}

// Valid reports whether token is a live session for the given fingerprint.
func (s *Sessions) Valid(token, fingerprint string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[token]
	if !ok {
		return false
	}
	if s.now().After(sess.expires) || subtle.ConstantTimeCompare([]byte(sess.fingerprint), []byte(fingerprint)) != 1 {
		delete(s.m, token)
		return false
	}
	return true
}

// Delete ends a session.
func (s *Sessions) Delete(token string) {
	s.mu.Lock()
	delete(s.m, token)
	s.mu.Unlock()
}

// TTL is the session lifetime.
func (s *Sessions) TTL() time.Duration { return s.ttl }
