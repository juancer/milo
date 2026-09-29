package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
)

const pass = "correct horse battery staple"

// ---------------------------------------------------------------- the encrypted container

func encrypt(t *testing.T, plaintext []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := newEncWriter(&buf, pass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plaintext); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decrypt(data []byte, passphrase string) ([]byte, error) {
	r, err := newDecReader(bytes.NewReader(data), passphrase)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func TestContainerRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 17} {
		plain := randomBytes(n)
		got, err := decrypt(encrypt(t, plain), pass)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("%d bytes: err=%v equal=%v", n, err, bytes.Equal(got, plain))
		}
	}
}

func TestContainerIsRandomisedAndHidesContent(t *testing.T) {
	secret := bytes.Repeat([]byte("SUPER-SECRET-MESSAGE "), 1000)
	a, b := encrypt(t, secret), encrypt(t, secret)
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of the same data are identical (salt/nonce reuse)")
	}
	if bytes.Contains(a, []byte("SUPER-SECRET")) {
		t.Fatal("plaintext visible in the encrypted file")
	}
}

func TestContainerRejectsWrongPassphraseAndTampering(t *testing.T) {
	plain := randomBytes(3*chunkSize + 100)
	good := encrypt(t, plain)

	if _, err := decrypt(good, "not the passphrase"); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
	if _, err := decrypt([]byte("hello, I am not a backup"), pass); !errors.Is(err, ErrNotABackup) {
		t.Errorf("not a backup: %v", err)
	}
	if _, err := decrypt(nil, pass); !errors.Is(err, ErrNotABackup) {
		t.Errorf("empty file: %v", err)
	}

	flip := func(name string, offset int) {
		bad := append([]byte(nil), good...)
		bad[offset] ^= 0x01
		if _, err := decrypt(bad, pass); err == nil {
			t.Errorf("%s: tampering not detected", name)
		}
	}
	flip("first chunk", headerLen+4+10)
	flip("middle chunk", headerLen+4+chunkSize+16+4+50)
	flip("last byte", len(good)-1)
	flip("salt", 10)
	flip("nonce prefix", 40)
	flip("chunk length", headerLen+1)

	if _, err := decrypt(good[:len(good)-5], pass); err == nil {
		t.Error("a file cut short was accepted")
	}
	if _, err := decrypt(append(append([]byte(nil), good...), 0), pass); err == nil {
		t.Error("trailing data was accepted")
	}
}

func TestContainerDetectsDroppedAndReorderedChunks(t *testing.T) {
	good := encrypt(t, randomBytes(4*chunkSize))
	// Split into chunks.
	var chunks [][]byte
	rest := good[headerLen:]
	for len(rest) > 0 {
		n := int(binary.BigEndian.Uint32(rest)) + 4
		chunks = append(chunks, rest[:n])
		rest = rest[n:]
	}
	join := func(order ...int) []byte {
		out := append([]byte(nil), good[:headerLen]...)
		for _, i := range order {
			out = append(out, chunks[i]...)
		}
		return out
	}
	last := len(chunks) - 1
	if last < 3 {
		t.Fatalf("need several chunks, got %d", len(chunks))
	}
	order := make([]int, last+1)
	for i := range order {
		order[i] = i
	}
	dropped := append(append([]int(nil), order[:1]...), order[2:]...) // chunk 1 removed
	if _, err := decrypt(join(dropped...), pass); err == nil {
		t.Error("a dropped chunk was not detected")
	}
	swapped := append([]int(nil), order...)
	swapped[1], swapped[2] = swapped[2], swapped[1]
	if _, err := decrypt(join(swapped...), pass); err == nil {
		t.Error("reordered chunks were not detected")
	}
	if _, err := decrypt(join(order[:last]...), pass); !errors.Is(err, ErrTruncated) {
		t.Errorf("a file missing its final chunk: %v", err)
	}
}

func TestContainerRejectsHostileKeyDerivationSettings(t *testing.T) {
	good := encrypt(t, []byte("x"))
	bad := append([]byte(nil), good...)
	binary.BigEndian.PutUint32(bad[28:], 4*1024*1024) // 4 GiB of memory
	start := time.Now()
	if _, err := decrypt(bad, pass); !errors.Is(err, ErrNotABackup) {
		t.Fatalf("hostile parameters accepted: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("hostile parameters were not rejected up front")
	}
}

// ---------------------------------------------------------------- whole backups

type device struct {
	st      *store.Store
	dataDir string
	keyPath string
	key     []byte
	vault   *secrets.Vault
}

func newDevice(t *testing.T) *device {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if _, err := secrets.GenerateKeyFile(keyPath); err != nil {
		t.Fatal(err)
	}
	key, _ := secrets.LoadKey(keyPath)
	st, err := store.Open(context.Background(), filepath.Join(dir, "nexo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	vault, _ := secrets.New(key, st)
	return &device{st: st, dataDir: dir, keyPath: keyPath, key: key, vault: vault}
}

func (d *device) fill(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	d.st.SetSetting(ctx, "family.motto", "be kind")
	d.st.CreateTelegramUser(ctx, "Dad", "PARENT", 1001, "dad")
	if err := d.vault.Put(ctx, "telegram.bot_token", []byte("123456:FAKE-TOKEN-FOR-BACKUP-TEST-xxxxxx")); err != nil {
		t.Fatal(err)
	}
	d.st.InsertMessage(ctx, store.Message{Direction: "in", Kind: "text", Body: "hello from the past"})
}

func createBackup(t *testing.T, d *device, includeKey bool) []byte {
	t.Helper()
	opts := Options{Passphrase: pass, NexoVersion: "test", DeviceName: "Test Nexo", WorkDir: t.TempDir()}
	if includeKey {
		opts.KeyPath = d.keyPath
	}
	var buf bytes.Buffer
	if err := Create(context.Background(), d.st, opts, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBackupRestoreRoundTripOnANewDevice(t *testing.T) {
	ctx := context.Background()
	old := newDevice(t)
	old.fill(t)
	file := createBackup(t, old, true)

	if bytes.Contains(file, []byte("hello from the past")) || bytes.Contains(file, []byte("FAKE-TOKEN")) {
		t.Fatal("the backup file exposes data in clear")
	}

	// A brand-new device: empty data dir, no key.
	fresh := t.TempDir()
	freshKey := filepath.Join(t.TempDir(), "etc", "master.key")
	c, err := Open(ctx, bytes.NewReader(file), pass, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Manifest.DeviceName != "Test Nexo" || !c.Manifest.IncludesKey || c.Manifest.SchemaVersion < 9 || c.Manifest.NexoVersion != "test" {
		t.Fatalf("manifest = %+v", c.Manifest)
	}
	if _, err := Install(c, fresh, freshKey, false); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(ctx, filepath.Join(fresh, "nexo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if v, _ := st.GetSetting(ctx, "family.motto"); v != "be kind" {
		t.Fatalf("setting = %q", v)
	}
	if u, err := st.GetUserByTelegramID(ctx, 1001); err != nil || u.DisplayName != "Dad" {
		t.Fatalf("user = %+v, %v", u, err)
	}
	if m, err := st.NextPending(ctx, false); err != nil || m.Body != "hello from the past" {
		t.Fatalf("message = %+v, %v", m, err)
	}
	// The key came along, so encrypted secrets still decrypt on the new device.
	key, err := secrets.LoadKey(freshKey)
	if err != nil {
		t.Fatal(err)
	}
	vault, _ := secrets.New(key, st)
	if tok, err := vault.Get(ctx, "telegram.bot_token"); err != nil || !strings.Contains(string(tok), "FAKE-TOKEN") {
		t.Fatalf("secret after restore: %q %v", tok, err)
	}
	if info, _ := os.Stat(freshKey); info.Mode().Perm() != 0o600 {
		t.Fatalf("restored key mode = %o", info.Mode().Perm())
	}
}

func TestBackupOptionsAndPassphrases(t *testing.T) {
	ctx := context.Background()
	d := newDevice(t)
	d.fill(t)
	var buf bytes.Buffer
	if err := Create(ctx, d.st, Options{Passphrase: "short"}, &buf); !errors.Is(err, ErrWeakPassphrase) {
		t.Fatalf("weak passphrase: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatal("bytes were written before the passphrase was rejected")
	}
	if err := Create(ctx, d.st, Options{Passphrase: pass, KeyPath: "/nonexistent/key"}, &buf); err == nil {
		t.Fatal("a backup that was supposed to include an unreadable key succeeded")
	}
	noKey := createBackup(t, d, false)
	c, err := Open(ctx, bytes.NewReader(noKey), pass, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Key != nil || c.Manifest.IncludesKey {
		t.Fatalf("key included although not requested: %+v", c.Manifest)
	}
	if _, err := Open(ctx, bytes.NewReader(noKey), "wrong passphrase!", t.TempDir()); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
}

func TestInstallProtectsAgainstAKeyMismatchAndKeepsTheOldDatabase(t *testing.T) {
	ctx := context.Background()
	source := newDevice(t)
	source.fill(t)
	file := createBackup(t, source, true)

	target := newDevice(t) // a different device with its own master key
	target.st.SetSetting(ctx, "family.motto", "the old data")
	target.st.Close()

	c, err := Open(ctx, bytes.NewReader(file), pass, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := Install(c, target.dataDir, target.keyPath, false); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	if v := readMotto(t, target.dataDir); v != "the old data" {
		t.Fatalf("a refused restore modified the database (%q)", v)
	}
	before, _ := os.ReadFile(target.keyPath)

	prev, err := Install(c, target.dataDir, target.keyPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if v := readMotto(t, target.dataDir); v != "be kind" {
		t.Fatalf("restored motto = %q", v)
	}
	if !strings.Contains(prev, "before-restore") {
		t.Fatalf("previous database not kept: %q", prev)
	}
	kept, err := store.Open(ctx, prev)
	if err != nil {
		t.Fatal(err)
	}
	defer kept.Close()
	if v, _ := kept.GetSetting(ctx, "family.motto"); v != "the old data" {
		t.Fatalf("the saved copy of the old database lost its data (%q)", v)
	}
	after, _ := os.ReadFile(target.keyPath)
	if bytes.Equal(before, after) {
		t.Fatal("--replace-key did not replace the key")
	}
}

func readMotto(t *testing.T, dataDir string) string {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(dataDir, "nexo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, _ := st.GetSetting(context.Background(), "family.motto")
	return v
}

func TestStagedRestoreIsAppliedOnceAtNextStart(t *testing.T) {
	ctx := context.Background()
	d := newDevice(t)
	d.fill(t)
	file := createBackup(t, d, true)
	d.st.SetSetting(ctx, "family.motto", "changed after the backup")
	d.st.Close()

	c, err := Open(ctx, bytes.NewReader(file), pass, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := Stage(c, d.dataDir, d.keyPath); err != nil {
		t.Fatal(err)
	}
	if v := readMotto(t, d.dataDir); v != "changed after the backup" {
		t.Fatal("staging changed the live database before the restart")
	}
	applied, prev, err := ApplyPending(d.dataDir)
	if err != nil || !applied || prev == "" {
		t.Fatalf("apply = %v %q %v", applied, prev, err)
	}
	if v := readMotto(t, d.dataDir); v != "be kind" {
		t.Fatalf("after restore motto = %q", v)
	}
	if applied, _, _ := ApplyPending(d.dataDir); applied {
		t.Fatal("a restore was applied twice")
	}
	if _, err := os.Stat(filepath.Join(d.dataDir, pendingDir)); !os.IsNotExist(err) {
		t.Fatal("staging folder left behind")
	}

	other := newDevice(t)
	if err := Stage(c, other.dataDir, other.keyPath); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("staging a backup with a foreign key: %v", err)
	}
}

func TestOpenRejectsMaliciousAndBrokenArchives(t *testing.T) {
	ctx := context.Background()
	build := func(entries map[string][]byte) []byte {
		var buf bytes.Buffer
		enc, _ := newEncWriter(&buf, pass)
		gz := gzip.NewWriter(enc)
		tw := tar.NewWriter(gz)
		for name, data := range entries {
			tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data))})
			tw.Write(data)
		}
		tw.Close()
		gz.Close()
		enc.Close()
		return buf.Bytes()
	}
	goodMan := []byte(`{"version":1}`)
	d := newDevice(t)
	realDB, _ := os.ReadFile(filepath.Join(d.dataDir, "nexo.db"))
	for name, entries := range map[string]map[string][]byte{
		"path traversal":   {"manifest.json": goodMan, "nexo.db": realDB, "../../etc/cron.d/evil": []byte("* * * * * root id")},
		"absolute path":    {"manifest.json": goodMan, "nexo.db": realDB, "/etc/passwd": []byte("x")},
		"unknown entry":    {"manifest.json": goodMan, "nexo.db": realDB, "extra.sh": []byte("#!/bin/sh")},
		"no database":      {"manifest.json": goodMan},
		"no manifest":      {"nexo.db": realDB},
		"future manifest":  {"manifest.json": []byte(`{"version":99}`), "nexo.db": realDB},
		"garbage database": {"manifest.json": goodMan, "nexo.db": []byte("this is not sqlite at all, not even close, honest")},
	} {
		dir := t.TempDir()
		if _, err := Open(ctx, bytes.NewReader(build(entries)), pass, dir); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if left, _ := os.ReadDir(dir); len(left) != 0 {
			t.Errorf("%s: left files behind: %v", name, left)
		}
	}
	if _, err := os.Stat("/etc/cron.d/evil"); err == nil {
		t.Fatal("a crafted backup wrote outside its folder")
	}
}

func TestOpenRefusesADatabaseFromANewerNexo(t *testing.T) {
	ctx := context.Background()
	d := newDevice(t)
	// Simulate a backup taken by a future version by inserting an unknown migration.
	future := filepath.Join(t.TempDir(), "future.db")
	if err := d.st.Snapshot(ctx, future); err != nil {
		t.Fatal(err)
	}
	fs, _ := store.Open(ctx, future)
	fs.ForceMigrationRow(ctx, 9999) // test helper on the store
	fs.Close()
	raw, _ := os.ReadFile(future)

	var buf bytes.Buffer
	enc, _ := newEncWriter(&buf, pass)
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: 13})
	tw.Write([]byte(`{"version":1}`))
	tw.WriteHeader(&tar.Header{Name: "nexo.db", Mode: 0o600, Size: int64(len(raw))})
	tw.Write(raw)
	tw.Close()
	gz.Close()
	enc.Close()
	if _, err := Open(ctx, &buf, pass, t.TempDir()); err == nil || !strings.Contains(err.Error(), "does not know") {
		t.Fatalf("a newer database was accepted: %v", err)
	}
}

// ---------------------------------------------------------------- local snapshots

func TestSnapshotsAreKeptNewestFirstAndPruned(t *testing.T) {
	ctx := context.Background()
	d := newDevice(t)
	d.fill(t)
	dir := filepath.Join(t.TempDir(), "backups")
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	var last string
	for i := 0; i < 5; i++ {
		p, err := Snapshot(ctx, d.st, dir, 3, base.Add(time.Duration(i)*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		last = p
	}
	files, err := List(dir)
	if err != nil || len(files) != 3 {
		t.Fatalf("files = %+v, %v", files, err)
	}
	if files[0].Name != "nexo-20260305-100000.db" || files[2].Name != "nexo-20260303-100000.db" {
		t.Fatalf("order/pruning wrong: %+v", files)
	}
	if info, _ := os.Stat(last); info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %o", info.Mode().Perm())
	}
	// A snapshot is a complete, valid database and never contains the master key.
	st, err := store.Open(ctx, last)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if v, _ := st.GetSetting(ctx, "family.motto"); v != "be kind" {
		t.Fatal("snapshot lost data")
	}
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)
	if files, _ := List(dir); len(files) != 3 {
		t.Fatal("unrelated files must not be listed (or pruned)")
	}
	if list, err := List(filepath.Join(t.TempDir(), "missing")); err != nil || len(list) != 0 {
		t.Fatalf("missing folder: %v %v", list, err)
	}
}
