package skills

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/juancer/milo/internal/secrets"
)

// Searching for an artist needs a token for Spotify's Web API. The receiver gives one for free, but it is
// shared with everyone who uses go-librespot and Spotify rate-limits it (HTTP 429), so searches fail at
// random. The owner can instead give Milo the Client ID and Secret of a free Spotify developer app: those
// have their own limit, and Milo asks for a token with them ("client credentials": no sign-in, and the
// token can only search the public catalogue).
const (
	SecretSpotifyClientID     = "spotify.client_id"
	SecretSpotifyClientSecret = "spotify.client_secret"
)

var credentialRe = regexp.MustCompile(`^[A-Za-z0-9]{20,64}$`)

// ErrSpotifyCredentials means Spotify did not accept the Client ID and Secret.
var ErrSpotifyCredentials = errors.New("spotify refused those credentials")

func (s *Spotify) accountsBase() string {
	if s.AccountsBase != "" {
		return strings.TrimRight(s.AccountsBase, "/")
	}
	return "https://accounts.spotify.com"
}

func (s *Spotify) credentials(ctx context.Context) (id, secret string, ok bool) {
	if s.Vault == nil {
		return "", "", false
	}
	i, err1 := s.Vault.Get(ctx, SecretSpotifyClientID)
	k, err2 := s.Vault.Get(ctx, SecretSpotifyClientSecret)
	if err1 != nil || err2 != nil || len(i) == 0 || len(k) == 0 {
		return "", "", false
	}
	return string(i), string(k), true
}

// SearchConfigured reports whether a developer app's credentials are stored.
func (s *Spotify) SearchConfigured(ctx context.Context) bool {
	_, _, ok := s.credentials(ctx)
	return ok
}

// SetSearchCredentials checks the Client ID and Secret with Spotify and stores them (encrypted).
func (s *Spotify) SetSearchCredentials(ctx context.Context, id, secret string) error {
	id, secret = strings.TrimSpace(id), strings.TrimSpace(secret)
	if s.Vault == nil || !credentialRe.MatchString(id) || !credentialRe.MatchString(secret) {
		return ErrSpotifyCredentials
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, _, err := s.clientToken(cctx, id, secret); err != nil {
		return err
	}
	if err := s.Vault.Put(ctx, SecretSpotifyClientID, []byte(id)); err != nil {
		return err
	}
	if err := s.Vault.Put(ctx, SecretSpotifyClientSecret, []byte(secret)); err != nil {
		return err
	}
	s.mu.Lock()
	s.webToken, s.webTokenExp = "", time.Time{}
	s.mu.Unlock()
	return nil
}

// ClearSearchCredentials forgets the developer app's credentials; searches use the receiver's token again.
func (s *Spotify) ClearSearchCredentials(ctx context.Context) error {
	if s.Vault == nil || s.Store == nil {
		return nil
	}
	for _, name := range []string{SecretSpotifyClientID, SecretSpotifyClientSecret} {
		if err := s.Store.DeleteSecret(ctx, name); err != nil && !errors.Is(err, secrets.ErrNotFound) {
			return err
		}
	}
	s.mu.Lock()
	s.webToken, s.webTokenExp = "", time.Time{}
	s.mu.Unlock()
	return nil
}

// clientToken asks Spotify for an app-only token with the client credentials flow.
func (s *Spotify) clientToken(ctx context.Context, id, secret string) (token string, ttl time.Duration, err error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.accountsBase()+"/api/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(id+":"+secret)))
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, errSpotifySearchBusy
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		return "", 0, ErrSpotifyCredentials
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, errSpotifySearchBusy
	}
	var out struct {
		Token   string `json:"access_token"`
		Expires int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil || out.Token == "" {
		return "", 0, errSpotifySearchBusy
	}
	return out.Token, time.Duration(out.Expires) * time.Second, nil
}

// searchToken returns a token for searching: the developer app's when one is configured (kept until
// shortly before it expires), else the receiver's.
func (s *Spotify) searchToken(ctx context.Context, c *spotifyClient) (string, error) {
	if id, secret, ok := s.credentials(ctx); ok {
		s.mu.Lock()
		tok, exp := s.webToken, s.webTokenExp
		s.mu.Unlock()
		if tok != "" && time.Now().Before(exp) {
			return tok, nil
		}
		tok, ttl, err := s.clientToken(ctx, id, secret)
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		s.webToken, s.webTokenExp = tok, time.Now().Add(ttl-time.Minute)
		s.mu.Unlock()
		return tok, nil
	}
	return c.librespotToken(ctx)
}
