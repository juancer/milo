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
	userID      int64
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

// Create issues a new session token for a user.
func (s *Sessions) Create(userID int64, fingerprint string) (string, error) {
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
	s.m[token] = session{expires: now.Add(s.ttl), userID: userID, fingerprint: fingerprint}
	return token, nil
}

// Lookup returns the user and password fingerprint a live token belongs to.
// The caller must compare the fingerprint with the user's current password so
// that changing a password signs that user out everywhere.
func (s *Sessions) Lookup(token string) (userID int64, fingerprint string, ok bool) {
	if token == "" {
		return 0, "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, found := s.m[token]
	if !found {
		return 0, "", false
	}
	if s.now().After(sess.expires) {
		delete(s.m, token)
		return 0, "", false
	}
	return sess.userID, sess.fingerprint, true
}

// Matches reports whether fp equals the fingerprint of the given hash, in constant time.
func Matches(fp, passwordHash string) bool {
	return subtle.ConstantTimeCompare([]byte(fp), []byte(Fingerprint(passwordHash))) == 1
}

// Delete ends a session.
func (s *Sessions) Delete(token string) {
	s.mu.Lock()
	delete(s.m, token)
	s.mu.Unlock()
}

// TTL is the session lifetime.
func (s *Sessions) TTL() time.Duration { return s.ttl }
