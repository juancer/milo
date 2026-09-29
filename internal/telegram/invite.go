package telegram

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
)

// NewInvitationToken returns a random token (safe for a Telegram deep-link
// parameter: A-Z a-z 0-9 _ -) and the hash to store. The raw token is shown to
// the admin once; only the hash is kept.
func NewInvitationToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken returns the hex SHA-256 of a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// DeepLink is the link a parent opens to start the bot with an invitation.
// botUsername comes from this installation's own bot, never a global constant.
func DeepLink(botUsername, token string) string {
	return "https://t.me/" + url.PathEscape(botUsername) + "?start=" + url.QueryEscape(token)
}
