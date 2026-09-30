// Package auth provides admin password hashing, sessions and login throttling.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters follow the OWASP minimum (19 MiB, 2 passes), which is
// affordable on a Raspberry Pi 3B.
const (
	argonMemoryKiB  = 19 * 1024
	argonIterations = 2
	argonThreads    = 1
	argonSaltLen    = 16
	argonKeyLen     = 32

	// Upper bounds accepted when verifying a stored hash.
	maxArgonMemoryKiB  = 64 * 1024
	maxArgonIterations = 10
	maxArgonThreads    = 4
	maxArgonKeyLen     = 64

	MinPasswordLength = 12
	maxPasswordBytes  = 128
)

// kdfSlots bounds how many Argon2 derivations run at once. Each one holds about 19 MiB,
// so a burst of parallel sign-in attempts cannot exhaust a small device's memory.
var kdfSlots = make(chan struct{}, 2)

// kdfWait is how long a request waits for a free slot before giving up.
var kdfWait = 10 * time.Second

// ErrBusy is returned when no Argon2 slot became free in time (or the caller went away).
var ErrBusy = errors.New("auth: too many password checks in progress, try again shortly")

func deriveKey(ctx context.Context, pw, salt []byte, iter, mem uint32, threads uint8, keyLen uint32) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ErrBusy
	}
	timer := time.NewTimer(kdfWait)
	defer timer.Stop()
	select {
	case kdfSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ErrBusy
	case <-timer.C:
		return nil, ErrBusy
	}
	defer func() { <-kdfSlots }()
	return argon2.IDKey(pw, salt, iter, mem, threads, keyLen), nil
}

// ValidatePassword enforces the admin password policy.
func ValidatePassword(pw string) error {
	if len(pw) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	if len(pw) > maxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes", maxPasswordBytes)
	}
	return nil
}

// HashPassword returns a PHC-formatted Argon2id hash.
func HashPassword(pw string) (string, error) {
	return HashPasswordContext(context.Background(), pw)
}

// HashPasswordContext is HashPassword that gives up with ErrBusy if ctx ends or no slot frees in time.
func HashPasswordContext(ctx context.Context, pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum, err := deriveKey(ctx, []byte(pw), salt, argonIterations, argonMemoryKiB, argonThreads, argonKeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonIterations, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(sum)), nil
}

// VerifyPassword checks pw against a hash produced by HashPassword.
func VerifyPassword(pw, encoded string) (bool, error) {
	return VerifyPasswordContext(context.Background(), pw, encoded)
}

// VerifyPasswordContext is VerifyPassword that gives up with ErrBusy if ctx ends or no slot frees in time.
func VerifyPasswordContext(ctx context.Context, pw, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("auth: unsupported password hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("auth: unsupported argon2 version")
	}
	var mem, iter uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iter, &threads); err != nil {
		return false, errors.New("auth: malformed argon2 parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errors.New("auth: malformed salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errors.New("auth: malformed hash")
	}
	// The parameters come from the database, which a restore can replace: keep them within
	// what this device could have written, so a crafted hash cannot panic or exhaust memory.
	if mem < 1 || mem > maxArgonMemoryKiB || iter < 1 || iter > maxArgonIterations ||
		threads < 1 || threads > maxArgonThreads || len(want) > maxArgonKeyLen {
		return false, errors.New("auth: argon2 parameters out of range")
	}
	got, err := deriveKey(ctx, []byte(pw), salt, iter, mem, threads, uint32(len(want)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
