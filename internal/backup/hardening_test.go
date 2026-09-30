package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// packWithKey builds a backup file around a genuine database with the given key entry.
func packWithKey(t *testing.T, db, key []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc, _ := newEncWriter(&buf, pass)
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: 13})
	tw.Write([]byte(`{"version":1}`))
	tw.WriteHeader(&tar.Header{Name: "milo.db", Mode: 0o600, Size: int64(len(db))})
	tw.Write(db)
	tw.WriteHeader(&tar.Header{Name: "master.key", Mode: 0o600, Size: int64(len(key))})
	tw.Write(key)
	tw.Close()
	gz.Close()
	enc.Close()
	return buf.Bytes()
}

func TestOpenValidatesTheKeyEntry(t *testing.T) {
	ctx := context.Background()
	d := newDevice(t)
	snap := filepath.Join(t.TempDir(), "s.db")
	if err := d.st.Snapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	db, _ := os.ReadFile(snap)
	good := hex.EncodeToString(d.key)
	for name, key := range map[string]string{
		"not hex":    strings.Repeat("zz", 32),
		"too short":  good[:62],
		"too long":   good + "00",
		"over 1 KiB": strings.Repeat("a", 2000),
		"empty":      "",
	} {
		if c, err := Open(ctx, bytes.NewReader(packWithKey(t, db, []byte(key))), pass, t.TempDir()); err == nil {
			c.Close()
			t.Errorf("%s: a bad key entry was accepted", name)
		}
	}
	c, err := Open(ctx, bytes.NewReader(packWithKey(t, db, []byte(good+"\n"))), pass, t.TempDir())
	if err != nil {
		t.Fatalf("a valid key entry was refused: %v", err)
	}
	c.Close()
}

func TestReplaceKeyChecksTheCanaryAndKeepsTheOldKey(t *testing.T) {
	ctx := context.Background()
	source := newDevice(t)
	source.fill(t)
	source.vault.CheckKey(ctx) // the canary a running device always has
	target := newDevice(t)
	target.st.Close()
	oldKey, _ := os.ReadFile(target.keyPath)

	c, err := Open(ctx, bytes.NewReader(createBackup(t, source, true)), pass, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	realKey := c.Key

	// A well-formed key that does not open the restored database.
	c.Key = []byte(hex.EncodeToString(randomBytes(32)))
	if _, err := Install(c, target.dataDir, target.keyPath, true); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("a key that does not decrypt the canary: %v", err)
	}
	// Not a key at all.
	c.Key = []byte("not a key")
	if _, err := Install(c, target.dataDir, target.keyPath, true); err == nil {
		t.Fatal("an invalid key was accepted")
	}
	if now, _ := os.ReadFile(target.keyPath); !bytes.Equal(now, oldKey) {
		t.Fatal("a refused restore overwrote the device key")
	}
	if left, _ := filepath.Glob(filepath.Join(target.dataDir, "milo.db.before-restore-*")); len(left) != 0 {
		t.Fatalf("a refused restore swapped the database: %v", left)
	}

	c.Key = realKey
	if _, err := Install(c, target.dataDir, target.keyPath, true); err != nil {
		t.Fatal(err)
	}
	kept, _ := filepath.Glob(target.keyPath + ".before-restore-*")
	if len(kept) != 1 {
		t.Fatalf("old key copies: %v", kept)
	}
	if got, _ := os.ReadFile(kept[0]); !bytes.Equal(got, oldKey) {
		t.Fatal("the saved copy is not the old key")
	}
	if info, _ := os.Stat(kept[0]); info.Mode().Perm() != 0o600 {
		t.Fatalf("saved key mode = %o", info.Mode().Perm())
	}
	if now, _ := os.ReadFile(target.keyPath); bytes.Equal(now, oldKey) {
		t.Fatal("the key was not replaced")
	}
}

func writeFiles(t *testing.T, files map[string]string) {
	t.Helper()
	for p, v := range files {
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSwapKeepsTheWriteAheadLogWithThePreviousDatabase(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "milo.db")
	src := filepath.Join(dir, "new.db")
	writeFiles(t, map[string]string{live: "db", live + "-wal": "wal frames", live + "-shm": "shm", src: "new"})
	previous, err := swapDatabase(src, dir)
	if err != nil {
		t.Fatal(err)
	}
	for ext, want := range map[string]string{"-wal": "wal frames", "-shm": "shm"} {
		if got, _ := os.ReadFile(previous + ext); string(got) != want {
			t.Errorf("previous%s = %q, want %q", ext, got, want)
		}
		if _, err := os.Stat(live + ext); err == nil {
			t.Errorf("a stale %s is still next to the restored database", ext)
		}
	}
}

func TestFailedSwapPutsTheWriteAheadLogBack(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "milo.db")
	writeFiles(t, map[string]string{live: "db", live + "-wal": "wal frames", live + "-shm": "shm"})
	if _, err := swapDatabase(filepath.Join(dir, "missing.db"), dir); err == nil {
		t.Fatal("copy of a missing file succeeded")
	}
	for ext, want := range map[string]string{"": "db", "-wal": "wal frames", "-shm": "shm"} {
		if got, _ := os.ReadFile(live + ext); string(got) != want {
			t.Errorf("after the rollback milo.db%s = %q, want %q", ext, got, want)
		}
	}
}

func TestPruningCountsDatabasesNotTheirSideFiles(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "milo.db")
	for _, n := range []string{"20240101-000000", "20240102-000000", "20240103-000000", "20240104-000000"} {
		writeFiles(t, map[string]string{live + ".before-restore-" + n: "old", live + ".before-restore-" + n + "-wal": "w", live + ".before-restore-" + n + "-shm": "s"})
	}
	pruneBeforeRestore(live, 3)
	dbs, _ := filepath.Glob(live + ".before-restore-*")
	if len(dbs) != 9 {
		t.Fatalf("kept %v", dbs)
	}
	for _, ext := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(live + ".before-restore-20240101-000000" + ext); err == nil {
			t.Errorf("the oldest copy%s was kept", ext)
		}
	}
}

// A FIFO swapped in for the restored database must not hang the root process at the chown.
func TestChownDoesNotBlockOnAFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "milo.db")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { chownToDirOwner(fifo, dir); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		f, _ := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0) // release the blocked open
		if f != nil {
			f.Close()
		}
		t.Fatal("chownToDirOwner blocked on a FIFO")
	}
}

func TestQuarantineSetsAStagedRestoreAside(t *testing.T) {
	dir := t.TempDir()
	if dest, err := Quarantine(dir); dest != "" || err != nil {
		t.Fatalf("nothing staged: %q %v", dest, err)
	}
	os.MkdirAll(filepath.Join(dir, "restore-failed-20200101-000000"), 0o700)
	pending := filepath.Join(dir, pendingDir)
	os.MkdirAll(pending, 0o700)
	os.WriteFile(filepath.Join(pending, "milo.db"), []byte("x"), 0o600)
	dest, err := Quarantine(dir)
	if err != nil || !strings.HasPrefix(filepath.Base(dest), "restore-failed-") {
		t.Fatalf("quarantine = %q %v", dest, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "milo.db")); err != nil {
		t.Fatal("the staged database was not moved")
	}
	if _, err := os.Stat(pending); err == nil {
		t.Fatal("the pending restore is still in place")
	}
	if _, err := os.Stat(filepath.Join(dir, "restore-failed-20200101-000000")); err == nil {
		t.Fatal("an older quarantined copy was kept")
	}
	if applied, _, err := ApplyPending(dir); applied || err != nil {
		t.Fatalf("apply after quarantine = %v %v", applied, err)
	}
}

func TestSweepTempRemovesOnlyOldLeftovers(t *testing.T) {
	dir := t.TempDir()
	oldDir := filepath.Join(dir, "milo-restore-123")
	oldBackup := filepath.Join(dir, "milo-backup-456")
	oldDownload := filepath.Join(dir, "download-789.milo")
	fresh := filepath.Join(dir, "download-fresh.milo")
	keep := filepath.Join(dir, "milo.db")
	for _, d := range []string{oldDir, oldBackup} {
		os.MkdirAll(d, 0o700)
		os.WriteFile(filepath.Join(d, "milo.db"), []byte("x"), 0o600)
	}
	writeFiles(t, map[string]string{oldDownload: "x", fresh: "x", keep: "x"})
	past := time.Now().Add(-time.Hour)
	for _, p := range []string{oldDir, oldBackup, oldDownload} {
		os.Chtimes(p, past, past)
	}
	SweepTemp(dir, time.Minute)
	for _, p := range []string{oldDir, oldBackup, oldDownload} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was not swept", filepath.Base(p))
		}
	}
	for _, p := range []string{fresh, keep} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed", filepath.Base(p))
		}
	}
}

type modeSource struct {
	mode os.FileMode
	size int64
}

func (m *modeSource) Snapshot(_ context.Context, dest string) error {
	info, err := os.Stat(dest)
	if err != nil {
		return err
	}
	m.mode, m.size = info.Mode().Perm(), info.Size()
	return os.WriteFile(dest, []byte("snapshot"), 0o644)
}
func (m *modeSource) SchemaVersion(context.Context) (int, error) { return 1, nil }

func TestSnapshotFileExistsPrivateBeforeTheDatabaseIsWritten(t *testing.T) {
	src := &modeSource{}
	dir := filepath.Join(t.TempDir(), "b")
	if _, err := Snapshot(context.Background(), src, dir, 3, time.Now()); err != nil {
		t.Fatal(err)
	}
	if src.mode != 0o600 || src.size != 0 {
		t.Fatalf("at VACUUM INTO time the file was mode %o, %d bytes; want an empty 0600 file", src.mode, src.size)
	}
}

func TestSnapshotWithANonPositiveKeepKeepsTheNewestCopy(t *testing.T) {
	d := newDevice(t)
	dir := filepath.Join(t.TempDir(), "b")
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if _, err := Snapshot(context.Background(), d.st, dir, -1, base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := List(dir)
	if len(files) != 1 || files[0].Name != "milo-20260301-120000.db" {
		t.Fatalf("files = %+v", files)
	}
}
