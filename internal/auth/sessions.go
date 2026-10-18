package auth

import (
	"context"
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

// SessionDB keeps sessions across restarts. It only ever sees the SHA-256 of a token.
type SessionDB interface {
	PutSession(ctx context.Context, tokenHash string, userID int64, fingerprint string, expires time.Time) error
	GetSession(ctx context.Context, tokenHash string, now time.Time) (userID int64, fingerprint string, ok bool, err error)
	DeleteSession(ctx context.Context, tokenHash string) error
	PurgeSessions(ctx context.Context, now time.Time) error
}

// Sessions issues and checks sign-in tokens. Without a SessionDB they live in memory and a
// restart of Milo signs everyone out; with one they survive restarts until they expire.
type Sessions struct {
	mu  sync.Mutex
	m   map[string]session
	db  SessionDB
	ttl time.Duration
	now func() time.Time
}

// NewSessions returns an in-memory store whose sessions expire after ttl.
func NewSessions(ttl time.Duration) *Sessions {
	return &Sessions{m: map[string]session{}, ttl: ttl, now: time.Now}
}

// NewPersistentSessions returns a store that keeps sessions in db.
func NewPersistentSessions(db SessionDB, ttl time.Duration) *Sessions {
	s := NewSessions(ttl)
	s.db = db
	return s
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
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
	if s.db != nil {
		ctx := context.Background()
		now := s.now()
		s.db.PurgeSessions(ctx, now) //nolint:errcheck // housekeeping; expired rows are refused on lookup anyway
		if err := s.db.PutSession(ctx, hashToken(token), userID, fingerprint, now.Add(s.ttl)); err != nil {
			return "", err
		}
		return token, nil
	}
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
	if s.db != nil {
		userID, fingerprint, ok, err := s.db.GetSession(context.Background(), hashToken(token), s.now())
		if err != nil || !ok {
			return 0, "", false
		}
		return userID, fingerprint, true
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
	if s.db != nil {
		s.db.DeleteSession(context.Background(), hashToken(token)) //nolint:errcheck // the cookie is cleared either way
		return
	}
	s.mu.Lock()
	delete(s.m, token)
	s.mu.Unlock()
}

// TTL is the session lifetime.
func (s *Sessions) TTL() time.Duration { return s.ttl }
