package secrets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/juancer/milo/internal/store"
)

func newVault(t *testing.T) (*Vault, *store.Store, []byte) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, 32)
	key[0] = 1
	v, err := New(key, st)
	if err != nil {
		t.Fatal(err)
	}
	return v, st, key
}

func TestRoundTripAndCiphertextIsNotPlaintext(t *testing.T) {
	ctx := context.Background()
	v, st, _ := newVault(t)
	const secret = "123456:FAKE-TOKEN-FOR-TESTS"
	if err := v.Put(ctx, "telegram.token", []byte(secret)); err != nil {
		t.Fatal(err)
	}
	raw, _ := st.GetSecret(ctx, "telegram.token")
	if string(raw) == secret || len(raw) <= len(secret) {
		t.Fatal("stored value is not encrypted")
	}
	got, err := v.Get(ctx, "telegram.token")
	if err != nil || string(got) != secret {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

func TestMissingSecret(t *testing.T) {
	v, _, _ := newVault(t)
	if _, err := v.Get(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestWrongKeyFails(t *testing.T) {
	ctx := context.Background()
	v, st, _ := newVault(t)
	v.Put(ctx, "a", []byte("x"))
	other, _ := New(make([]byte, 32), st)
	if _, err := other.Get(ctx, "a"); err == nil {
		t.Fatal("decrypting with the wrong key must fail")
	}
}

func TestCiphertextBoundToName(t *testing.T) {
	ctx := context.Background()
	v, st, _ := newVault(t)
	v.Put(ctx, "a", []byte("x"))
	raw, _ := st.GetSecret(ctx, "a")
	st.PutSecret(ctx, "b", raw)
	if _, err := v.Get(ctx, "b"); err == nil {
		t.Fatal("ciphertext copied to another name must not decrypt")
	}
}

func TestCorruptCiphertext(t *testing.T) {
	ctx := context.Background()
	v, st, _ := newVault(t)
	st.PutSecret(ctx, "short", []byte{1, 2})
	if _, err := v.Get(ctx, "short"); err == nil {
		t.Fatal("expected error")
	}
}

func TestCheckKeyDetectsMismatch(t *testing.T) {
	ctx := context.Background()
	v, st, _ := newVault(t)
	if err := v.CheckKey(ctx); err != nil {
		t.Fatal(err)
	}
	if err := v.CheckKey(ctx); err != nil {
		t.Fatalf("second check: %v", err)
	}
	other, _ := New(make([]byte, 32), st)
	if err := other.CheckKey(ctx); err == nil {
		t.Fatal("a different master key must be detected")
	}
}

func TestVerifyCanaryNeverWritesAndNeedsTheRightKey(t *testing.T) {
	ctx := context.Background()
	v, st, _ := newVault(t)
	if err := v.VerifyCanary(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing canary = %v, want ErrNotFound", err)
	}
	if _, err := st.GetSecret(ctx, canaryName); err == nil {
		t.Fatal("VerifyCanary wrote the canary")
	}
	v.CheckKey(ctx)
	if err := v.VerifyCanary(ctx); err != nil {
		t.Fatal(err)
	}
	other, _ := New(make([]byte, 32), st)
	if err := other.VerifyCanary(ctx); err == nil {
		t.Fatal("a different master key passed")
	}
}

func TestKeyFileLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "master.key")
	created, err := GenerateKeyFile(path)
	if err != nil || !created {
		t.Fatalf("GenerateKeyFile = %v, %v", created, err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	first, err := LoadKey(path)
	if err != nil || len(first) != 32 {
		t.Fatalf("LoadKey = %d bytes, %v", len(first), err)
	}
	created, err = GenerateKeyFile(path)
	if err != nil || created {
		t.Fatalf("second GenerateKeyFile must not overwrite: %v, %v", created, err)
	}
	second, _ := LoadKey(path)
	if string(first) != string(second) {
		t.Fatal("existing key was replaced")
	}
}

func TestLoadKeyRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadKey(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing key must fail")
	}
	open := filepath.Join(dir, "open.key")
	os.WriteFile(open, []byte("00"), 0o644)
	if _, err := LoadKey(open); err == nil {
		t.Fatal("group/world readable key must be rejected")
	}
	bad := filepath.Join(dir, "bad.key")
	os.WriteFile(bad, []byte("not hex"), 0o600)
	if _, err := LoadKey(bad); err == nil {
		t.Fatal("malformed key must be rejected")
	}
}
