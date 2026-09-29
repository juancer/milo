// Package secrets encrypts credentials (bot tokens, API keys, Wi-Fi passwords)
// before they reach SQLite.
//
// Threat model: this protects against someone who obtains only the database
// file (backup, SD-card copy). It does NOT protect against an attacker with
// root on the running device, because the service must be able to read the
// master key. The key lives outside the data directory (see docs/security.md).
package secrets

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/juancer/nexo/internal/store"
)

// ErrNotFound is returned when a secret does not exist.
var ErrNotFound = errors.New("secrets: not found")

const canaryName = "system.key_canary"

// GenerateKeyFile creates a new random master key at path with mode 0600.
// It never overwrites an existing key; created reports whether one was written.
func GenerateKeyFile(path string) (created bool, err error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("secrets: stat key: %w", err)
	}
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(key); err != nil {
		return false, fmt.Errorf("secrets: generate key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("secrets: create key dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, fmt.Errorf("secrets: create key file: %w", err)
	}
	if _, err := f.WriteString(hex.EncodeToString(key) + "\n"); err != nil {
		f.Close()
		return false, fmt.Errorf("secrets: write key: %w", err)
	}
	return true, f.Close()
}

// LoadKey reads the master key. It refuses keys readable by group or others.
func LoadKey(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("secrets: master key %s: %w (run `nexo keygen`)", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secrets: master key %s must not be accessible by group or others (mode %o)", path, info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secrets: read master key: %w", err)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != chacha20poly1305.KeySize {
		return nil, fmt.Errorf("secrets: master key %s is not a valid %d-byte hex key", path, chacha20poly1305.KeySize)
	}
	return key, nil
}

// Vault encrypts and decrypts named secrets stored in the database.
type Vault struct {
	aead  cipher.AEAD
	store *store.Store
}

// New creates a Vault using XChaCha20-Poly1305 with the given 32-byte key.
func New(key []byte, s *store.Store) (*Vault, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: init cipher: %w", err)
	}
	return &Vault{aead: aead, store: s}, nil
}

// Put encrypts plaintext and stores it under name. The name is bound to the
// ciphertext as associated data, so rows cannot be swapped between names.
func (v *Vault) Put(ctx context.Context, name string, plaintext []byte) error {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("secrets: nonce: %w", err)
	}
	sealed := v.aead.Seal(nonce, nonce, plaintext, []byte(name))
	return v.store.PutSecret(ctx, name, sealed)
}

// Get returns the decrypted secret, or ErrNotFound.
func (v *Vault) Get(ctx context.Context, name string) ([]byte, error) {
	sealed, err := v.store.GetSecret(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	n := v.aead.NonceSize()
	if len(sealed) < n {
		return nil, fmt.Errorf("secrets: %q is corrupt", name)
	}
	plain, err := v.aead.Open(nil, sealed[:n], sealed[n:], []byte(name))
	if err != nil {
		return nil, fmt.Errorf("secrets: cannot decrypt %q (wrong master key or corrupt data)", name)
	}
	return plain, nil
}

// CheckKey verifies the master key matches this database. On first use it
// stores a canary; afterwards a mismatch fails fast at startup instead of
// breaking features later.
func (v *Vault) CheckKey(ctx context.Context) error {
	_, err := v.Get(ctx, canaryName)
	if errors.Is(err, ErrNotFound) {
		return v.Put(ctx, canaryName, []byte("ok"))
	}
	return err
}
