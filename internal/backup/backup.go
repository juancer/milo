// Package backup creates and restores encrypted backups of a Nexo device.
//
// A backup file is a single .nexo file: a consistent snapshot of the database
// plus, if requested, the master key, packed into a tar.gz and encrypted with a
// passphrase the parents choose (Argon2id + XChaCha20-Poly1305). With the key
// included, one file and one passphrase are enough to rebuild the device on a
// new SD card, and the file is safe to keep on a USB stick or in the cloud
// because nothing in it is readable without the passphrase.
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
)

// Manifest describes a backup.
type Manifest struct {
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	NexoVersion   string    `json:"nexo_version"`
	SchemaVersion int       `json:"schema_version"`
	DeviceName    string    `json:"device_name"`
	IncludesKey   bool      `json:"includes_key"`
}

const (
	manifestName = "manifest.json"
	dbName       = "nexo.db"
	keyName      = "master.key"
	maxDBBytes   = 1 << 30 // decompressed: the upload cap only covers the ciphertext
	maxKeyBytes  = 1024
	// MinPassphrase is the shortest accepted backup passphrase.
	MinPassphrase = 12
)

// Errors.
var (
	ErrWeakPassphrase = errors.New("the backup passphrase must be at least 12 characters")
	ErrKeyMismatch    = errors.New("this backup was made with a different master key than this device uses")
)

// Source is what a backup reads from (a *store.Store).
type Source interface {
	Snapshot(ctx context.Context, dest string) error
	SchemaVersion(ctx context.Context) (int, error)
}

// Options control Create.
type Options struct {
	Passphrase  string
	KeyPath     string // master key to include; empty to leave it out
	NexoVersion string
	DeviceName  string
	WorkDir     string // where the temporary snapshot is written (default: system temp)
}

// Create writes an encrypted backup to w.
func Create(ctx context.Context, src Source, opts Options, w io.Writer) error {
	if len(opts.Passphrase) < MinPassphrase {
		return ErrWeakPassphrase
	}
	dir, err := os.MkdirTemp(opts.WorkDir, "nexo-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	snap := filepath.Join(dir, dbName)
	if err := src.Snapshot(ctx, snap); err != nil {
		return err
	}
	version, err := src.SchemaVersion(ctx)
	if err != nil {
		return err
	}

	var key []byte
	if opts.KeyPath != "" {
		if key, err = os.ReadFile(opts.KeyPath); err != nil {
			return fmt.Errorf("cannot read the master key to include it: %w", err)
		}
	}
	man := Manifest{Version: 1, CreatedAt: time.Now().UTC(), NexoVersion: opts.NexoVersion,
		SchemaVersion: version, DeviceName: opts.DeviceName, IncludesKey: key != nil}

	enc, err := newEncWriter(w, opts.Passphrase)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)

	manJSON, _ := json.MarshalIndent(man, "", "  ")
	if err := addBytes(tw, manifestName, manJSON); err != nil {
		return err
	}
	f, err := os.Open(snap)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: dbName, Mode: 0o600, Size: info.Size(), ModTime: man.CreatedAt}); err != nil {
		return err
	}
	if _, err := io.Copy(tw, f); err != nil {
		return err
	}
	if key != nil {
		if err := addBytes(tw, keyName, key); err != nil {
			return err
		}
	}
	for _, c := range []io.Closer{tw, gz, enc} {
		if err := c.Close(); err != nil {
			return err
		}
	}
	return nil
}

func addBytes(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// Contents is an opened, verified backup extracted to a private temporary folder.
type Contents struct {
	Manifest Manifest
	DBPath   string // the verified database copy
	Key      []byte // nil if the backup does not include the key
	dir      string
}

// Close deletes the temporary files.
func (c *Contents) Close() { os.RemoveAll(c.dir) }

// Open decrypts and verifies a backup. Nothing on the device is touched: the
// contents are extracted below workDir and the database is checked for damage
// (and upgraded to this version's schema) before anyone can install it.
func Open(ctx context.Context, r io.Reader, passphrase, workDir string) (c *Contents, err error) {
	dec, err := newDecReader(r, passphrase)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		return nil, unwrap(err)
	}
	dir, err := os.MkdirTemp(workDir, "nexo-restore-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	out := &Contents{dir: dir, DBPath: filepath.Join(dir, dbName)}
	var haveMan, haveDB bool
	tr := tar.NewReader(gz)
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, unwrap(e)
		}
		switch h.Name { // exact names only: nothing in the file can pick a path
		case manifestName:
			b, e := io.ReadAll(io.LimitReader(tr, 64<<10))
			if e != nil {
				return nil, unwrap(e)
			}
			if e := json.Unmarshal(b, &out.Manifest); e != nil || out.Manifest.Version != 1 {
				return nil, errors.New("backup: unsupported or unreadable manifest")
			}
			haveMan = true
		case dbName:
			if h.Size > maxDBBytes {
				return nil, errors.New("backup: database is unreasonably large")
			}
			f, e := os.OpenFile(out.DBPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if e != nil {
				return nil, e
			}
			_, e = io.Copy(f, io.LimitReader(tr, maxDBBytes))
			f.Close()
			if e != nil {
				return nil, unwrap(e)
			}
			haveDB = true
		case keyName:
			b, e := io.ReadAll(io.LimitReader(tr, maxKeyBytes+1))
			if e != nil {
				return nil, unwrap(e)
			}
			if len(b) > maxKeyBytes {
				return nil, errors.New("backup: the master key entry is too long")
			}
			out.Key = bytes.TrimSpace(b)
			if e := validateKey(out.Key); e != nil {
				return nil, e
			}
		default:
			return nil, fmt.Errorf("backup: unexpected entry %q", h.Name)
		}
	}
	if !haveMan || !haveDB {
		return nil, errors.New("backup: the file is missing its manifest or database")
	}
	if e := verifyDB(ctx, out.DBPath); e != nil {
		return nil, e
	}
	return out, nil
}

// validateKey accepts only what secrets.LoadKey would: a 32-byte key written as hex.
func validateKey(key []byte) error {
	if raw, err := hex.DecodeString(string(key)); err != nil || len(raw) != chacha20poly1305.KeySize {
		return fmt.Errorf("backup: the master key inside is not a valid %d-byte hex key", chacha20poly1305.KeySize)
	}
	return nil
}

func unwrap(err error) error {
	if errors.Is(err, ErrWrongPassphrase) || errors.Is(err, ErrTruncated) || errors.Is(err, ErrNotABackup) {
		return err
	}
	return fmt.Errorf("%w (%v)", ErrWrongPassphrase, err)
}

// verifyDB opens the extracted database (running any newer migrations, and
// refusing one from a newer Nexo) and checks it for corruption.
func verifyDB(ctx context.Context, path string) error {
	st, err := store.Open(ctx, path)
	if err != nil {
		return fmt.Errorf("backup: the database inside cannot be used: %w", err)
	}
	defer st.Close()
	if err := st.IntegrityCheck(ctx); err != nil {
		return err
	}
	if err := st.ForeignKeyCheck(ctx); err != nil {
		return err
	}
	// schema_migrations only says what the database claims: compare the real schema with a
	// freshly migrated one, so a crafted copy cannot lack tables or constraints.
	ref, err := store.Open(ctx, filepath.Join(filepath.Dir(path), "reference.db"))
	if err != nil {
		return err
	}
	defer ref.Close()
	want, err := ref.SchemaSignature(ctx)
	if err != nil {
		return err
	}
	got, err := st.SchemaSignature(ctx)
	if err != nil {
		return err
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		return errors.New("backup: the database inside does not have the schema Nexo expects")
	}
	return nil
}

// checkCanary makes sure the restored database can be read with the key it will run with:
// the canary secret must decrypt. That key is the backup's own when replaceKey is set and
// the device's otherwise (a database without any canary passes only in the first case).
// Without this the service would crash-loop at the next start. A device with no key file
// yet cannot be checked.
func checkCanary(ctx context.Context, c *Contents, keyPath string, replaceKey bool) error {
	var key []byte
	if c.Key != nil {
		if !replaceKey {
			return nil
		}
		if err := validateKey(c.Key); err != nil {
			return err
		}
		key, _ = hex.DecodeString(string(c.Key))
	} else {
		var err error
		key, err = secrets.LoadKey(keyPath)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
	}
	st, err := store.Open(ctx, c.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	v, err := secrets.New(key, st)
	if err != nil {
		return err
	}
	err = v.VerifyCanary(ctx)
	if err != nil && c.Key != nil && errors.Is(err, secrets.ErrNotFound) {
		// A database that never ran has no canary yet; the backup's own key creates it at the
		// first start. Only a canary that is there and does not decrypt proves a wrong key.
		return nil
	}
	if err != nil {
		return ErrKeyMismatch
	}
	return nil
}

// checkKey compares the backup's key with the device's.
func checkKey(c *Contents, keyPath string, replaceKey bool) (writeKey bool, err error) {
	if c.Key == nil {
		return false, nil
	}
	existing, rerr := os.ReadFile(keyPath)
	switch {
	case rerr != nil: // this device has no key yet
		return true, nil
	case bytes.Equal(bytes.TrimSpace(existing), c.Key):
		return false, nil
	case replaceKey:
		return true, nil
	}
	return false, ErrKeyMismatch
}

// Install replaces the device's database with the backup's. The previous
// database is kept next to it as nexo.db.before-restore-<time>. Stop Nexo
// first. With replaceKey the master key file is replaced by the backup's;
// otherwise a different key is refused, because secrets in the restored
// database could then not be decrypted.
func Install(c *Contents, dataDir, keyPath string, replaceKey bool) (previous string, err error) {
	writeKey, err := checkKey(c, keyPath, replaceKey)
	if err != nil {
		return "", err
	}
	if writeKey {
		if err := validateKey(c.Key); err != nil {
			return "", err
		}
	}
	if err := checkCanary(context.Background(), c, keyPath, writeKey); err != nil {
		return "", err
	}
	if writeKey { // the database kept aside still holds secrets sealed with the old key
		if err := keepOldKey(keyPath, time.Now().UTC().Format("20060102-150405")); err != nil {
			return "", err
		}
	}
	previous, err = swapDatabase(c.DBPath, dataDir)
	if err != nil {
		return "", err
	}
	if writeKey {
		if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
			return previous, err
		}
		if err := os.WriteFile(keyPath, append(c.Key, '\n'), 0o600); err != nil {
			return previous, fmt.Errorf("cannot write the master key: %w", err)
		}
	}
	return previous, nil
}

// keepOldKey saves the device's current master key as <key>.before-restore-<stamp> before
// --replace-key overwrites it: the database kept aside still holds secrets sealed with it.
func keepOldKey(keyPath, stamp string) error {
	old, err := os.ReadFile(keyPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("cannot read the current master key to keep a copy: %w", err)
	}
	f, err := os.OpenFile(keyPath+".before-restore-"+stamp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cannot keep a copy of the current master key: %w", err)
	}
	if _, err := f.Write(old); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// sideFiles are SQLite's write-ahead log and shared-memory index, which belong to one database file.
var sideFiles = []string{"-wal", "-shm"}

// swapDatabase moves the current database aside and puts src in its place.
func swapDatabase(src, dataDir string) (previous string, err error) {
	live := filepath.Join(dataDir, dbName)
	if _, statErr := os.Stat(live); statErr == nil {
		previous = live + ".before-restore-" + time.Now().UTC().Format("20060102-150405")
		if err := os.Rename(live, previous); err != nil {
			return "", err
		}
	}
	// The side files travel with the database they belong to: committed frames that were never
	// checkpointed live only there, and a failed copy must be able to put everything back.
	var moved []string
	for _, ext := range sideFiles {
		if previous == "" {
			os.Remove(live + ext) // no database to keep them with: stale, they would corrupt the restored copy
		} else if os.Rename(live+ext, previous+ext) == nil {
			moved = append(moved, ext)
		}
	}
	if err := copyFile(src, live); err != nil {
		if previous != "" {
			os.Rename(previous, live) //nolint:errcheck // put the old one back
			for _, ext := range moved {
				os.Rename(previous+ext, live+ext) //nolint:errcheck
			}
		}
		return "", err
	}
	pruneBeforeRestore(live, 3)
	chownToDirOwner(live, dataDir)
	return previous, nil
}

// chownToDirOwner keeps the file usable by the service user when a root shell restored it.
func chownToDirOwner(path, dataDir string) {
	info, err := os.Stat(dataDir)
	if err != nil {
		return
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		// Through the open file, not the path: the service user owns the folder and could
		// swap the name for a symlink between the copy and this call. O_NONBLOCK keeps a
		// FIFO swapped in for the file from blocking the open forever.
		if f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0); err == nil {
			f.Chown(int(st.Uid), int(st.Gid)) //nolint:errcheck
			f.Close()
		}
	}
}

// pruneBeforeRestore keeps the newest `keep` saved pre-restore databases (they hold admin
// hashes and messages) and deletes the older ones.
func pruneBeforeRestore(live string, keep int) {
	all, _ := filepath.Glob(live + ".before-restore-*")
	var old []string
	for _, p := range all {
		if !strings.HasSuffix(p, "-wal") && !strings.HasSuffix(p, "-shm") { // those go with their database
			old = append(old, p)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(old))) // the timestamps sort chronologically
	for i := keep; i < len(old); i++ {
		os.Remove(old[i])
		for _, ext := range sideFiles {
			os.Remove(old[i] + ext)
		}
	}
}

func copyFile(src, dst string) error {
	// A symlink in the source position (the service user can rename entries in the data
	// folder) must not make a root restore read some other file.
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ---------------------------------------------------------------- restore from the web UI

const pendingDir = "restore-pending"

// Stage prepares a restore for the next start. The running service cannot swap
// its own database, so the verified copy waits in <dataDir>/restore-pending
// and ApplyPending installs it at the beginning of the next run. The master
// key cannot be replaced this way (it belongs to the operating system), so a
// backup made with another key is refused here.
func Stage(c *Contents, dataDir, keyPath string) error {
	if writeKey, err := checkKey(c, keyPath, false); err != nil {
		return err
	} else if writeKey {
		// The device has no key file at all: the backup's key cannot be installed from here.
		return ErrKeyMismatch
	}
	if err := checkCanary(context.Background(), c, keyPath, false); err != nil {
		return err
	}
	dir := filepath.Join(dataDir, pendingDir)
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Move, not copy: the extracted database can be large and c is spent afterwards.
	staged := filepath.Join(dir, dbName)
	if os.Rename(c.DBPath, staged) == nil {
		return nil
	}
	return copyFile(c.DBPath, staged)
}

// ApplyPending installs a staged restore, if there is one. Call it before the
// database is opened.
func ApplyPending(dataDir string) (applied bool, previous string, err error) {
	pending := filepath.Join(dataDir, pendingDir)
	applying := pending + ".applying"
	// A copy left by a crash after the swap must never be applied again over newer data.
	os.RemoveAll(applying)
	if _, statErr := os.Stat(filepath.Join(pending, dbName)); statErr != nil {
		return false, "", nil
	}
	// Take it out of the pending name first: whatever happens next, it is applied at most once.
	if err := os.Rename(pending, applying); err != nil {
		return false, "", err
	}
	previous, err = swapDatabase(filepath.Join(applying, dbName), dataDir)
	if err != nil {
		os.Rename(applying, pending) //nolint:errcheck // nothing was installed: try again at the next start
		return false, "", err
	}
	os.RemoveAll(applying)
	return true, previous, nil
}

const failedPrefix = "restore-failed-"

// Quarantine sets a staged restore aside as restore-failed-<time> so that one which keeps
// failing does not stop the service from starting on the current database. Only the newest
// quarantined copy is kept: each one is as large as the database.
func Quarantine(dataDir string) (string, error) {
	pending := filepath.Join(dataDir, pendingDir)
	if _, err := os.Lstat(pending); err != nil {
		return "", nil
	}
	old, _ := filepath.Glob(filepath.Join(dataDir, failedPrefix+"*"))
	dest := filepath.Join(dataDir, failedPrefix+time.Now().UTC().Format("20060102-150405"))
	if err := os.Rename(pending, dest); err != nil {
		return "", err
	}
	for _, p := range old {
		if p != dest {
			os.RemoveAll(p)
		}
	}
	return dest, nil
}

// SweepTemp deletes the temporary files a crash or kill left in dataDir (nexo-restore-*,
// nexo-backup-* and download-*.nexo) once they are older than minAge.
func SweepTemp(dataDir string, minAge time.Duration) {
	for _, pat := range []string{"nexo-restore-*", "nexo-backup-*", "download-*.nexo"} {
		matches, _ := filepath.Glob(filepath.Join(dataDir, pat))
		for _, p := range matches {
			if info, err := os.Lstat(p); err == nil && time.Since(info.ModTime()) >= minAge {
				os.RemoveAll(p)
			}
		}
	}
}

// ---------------------------------------------------------------- local snapshots

// Snapshot writes an unencrypted copy of the database into dir (0600) and keeps
// only the newest `keep` copies. These files are for recovery from SD-card
// damage and hold what the live database holds, but not the master key.
func Snapshot(ctx context.Context, src Source, dir string, keep int, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if keep < 1 {
		keep = 1 // a bad setting must neither delete every copy nor panic below
	}
	path := filepath.Join(dir, "nexo-"+now.UTC().Format("20060102-150405")+".db")
	// SQLite's VACUUM INTO accepts an empty file: create it 0600 so the snapshot is never
	// readable by others, not even briefly.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	f.Close()
	if err := src.Snapshot(ctx, path); err != nil {
		os.Remove(path)
		return "", err
	}
	os.Chmod(path, 0o600)
	files, _ := List(dir)
	for i := keep; i < len(files); i++ { // List is newest first
		os.Remove(filepath.Join(dir, files[i].Name))
	}
	return path, nil
}

// File is a local snapshot.
type File struct {
	Name string    `json:"name"`
	Size int64     `json:"size"`
	At   time.Time `json:"at"`
}

// List returns the snapshots in dir, newest first.
func List(dir string) ([]File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || len(name) < 8 || name[:5] != "nexo-" || filepath.Ext(name) != ".db" {
			continue
		}
		if info, err := e.Info(); err == nil {
			out = append(out, File{name, info.Size(), info.ModTime()})
		}
	}
	// File names sort chronologically.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Name > out[i].Name {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}
