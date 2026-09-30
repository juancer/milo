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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

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
	maxDBBytes   = 4 << 30
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
			b, e := io.ReadAll(io.LimitReader(tr, 1024))
			if e != nil {
				return nil, unwrap(e)
			}
			out.Key = bytes.TrimSpace(b)
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
	return st.IntegrityCheck(ctx)
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

// swapDatabase moves the current database aside and puts src in its place.
func swapDatabase(src, dataDir string) (previous string, err error) {
	live := filepath.Join(dataDir, dbName)
	if _, statErr := os.Stat(live); statErr == nil {
		previous = live + ".before-restore-" + time.Now().UTC().Format("20060102-150405")
		if err := os.Rename(live, previous); err != nil {
			return "", err
		}
	}
	for _, ext := range []string{"-wal", "-shm"} { // stale side files would corrupt the restored copy
		os.Remove(live + ext)
	}
	if err := copyFile(src, live); err != nil {
		if previous != "" {
			os.Rename(previous, live) //nolint:errcheck // put the old one back
		}
		return "", err
	}
	// Keep the files usable by the service user when a root shell restored them.
	if info, err := os.Stat(dataDir); err == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			// Through the open file, not the path: the service user owns the folder and could
			// swap the name for a symlink between the copy and this call.
			if f, err := os.OpenFile(live, os.O_RDONLY|syscall.O_NOFOLLOW, 0); err == nil {
				f.Chown(int(st.Uid), int(st.Gid)) //nolint:errcheck
				f.Close()
			}
		}
	}
	return previous, nil
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
	dir := filepath.Join(dataDir, pendingDir)
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return copyFile(c.DBPath, filepath.Join(dir, dbName))
}

// ApplyPending installs a staged restore, if there is one. Call it before the
// database is opened.
func ApplyPending(dataDir string) (applied bool, previous string, err error) {
	staged := filepath.Join(dataDir, pendingDir, dbName)
	if _, statErr := os.Stat(staged); statErr != nil {
		return false, "", nil
	}
	previous, err = swapDatabase(staged, dataDir)
	if err != nil {
		return false, "", err
	}
	os.RemoveAll(filepath.Join(dataDir, pendingDir))
	return true, previous, nil
}

// ---------------------------------------------------------------- local snapshots

// Snapshot writes an unencrypted copy of the database into dir (0600) and keeps
// only the newest `keep` copies. These files are for recovery from SD-card
// damage and hold what the live database holds, but not the master key.
func Snapshot(ctx context.Context, src Source, dir string, keep int, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "nexo-"+now.UTC().Format("20060102-150405")+".db")
	if err := src.Snapshot(ctx, path); err != nil {
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
