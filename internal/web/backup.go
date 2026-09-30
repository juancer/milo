package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/juancer/milo/internal/app"
	"github.com/juancer/milo/internal/backup"
)

const maxUpload = 1 << 30 // 1 GiB

// restoreBusy allows one restore upload at a time: each one holds a decrypted copy of the
// database on the SD card and up to 64 MiB of Argon2 memory.
var restoreBusy sync.Mutex

func (s *server) backupRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/backup", s.requireAuth(s.getBackup))
	mux.Handle("PUT /api/backup", s.requireAuth(s.putBackup))
	mux.Handle("POST /api/backup/run", s.requireAuth(s.runBackup))
	// Only the owner may export or replace the data: a backup carries the admin accounts and,
	// with the key, every stored secret, and a restore swaps the account table wholesale.
	mux.Handle("POST /api/backup/download", s.requireOwner(s.downloadBackup))
	mux.Handle("POST /api/backup/restore", s.requireOwner(s.restoreBackup))
}

func (s *server) getBackup(w http.ResponseWriter, r *http.Request) {
	st := s.app.BackupSettings(r.Context())
	files, _ := backup.List(st.Dir)
	if files == nil {
		files = []backup.File{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": st, "files": files, "min_passphrase": backup.MinPassphrase})
}

func (s *server) putBackup(w http.ResponseWriter, r *http.Request) {
	var b app.BackupSettings
	if !decodeJSON(w, r, &b) {
		return
	}
	b.Dir = strings.TrimSpace(b.Dir)
	switch {
	case !dirRe.MatchString(b.Dir) || strings.Contains(b.Dir, ".."):
		writeError(w, http.StatusBadRequest, "web.err.enter_an_absolute_folder_path_such_as_var_li")
		return
	case !s.app.BackupDirAllowed(b.Dir):
		writeError(w, http.StatusBadRequest, "web.err.folder_inside_data_dir", "example", filepath.Join(s.app.Cfg.DataDir, "backups"))
		return
	case b.Keep < 1 || b.Keep > 60 || b.EveryHours < 1 || b.EveryHours > 168:
		writeError(w, http.StatusBadRequest, "web.err.keep_1_60_copies_every_1_168_hours")
		return
	}
	if err := os.MkdirAll(b.Dir, 0o700); err != nil {
		writeError(w, http.StatusBadRequest, "web.err.that_folder_cannot_be_created_or_written_by")
		return
	}
	if err := s.app.SaveBackupSettings(r.Context(), b); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.audit(r, "backup.settings_updated", "", nil)
	s.getBackup(w, r)
}

func (s *server) runBackup(w http.ResponseWriter, r *http.Request) {
	if _, err := s.app.RunLocalBackup(r.Context()); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.audit(r, "backup.run", "", nil)
	s.getBackup(w, r)
}

// downloadBackup streams an encrypted backup the parents can keep anywhere.
func (s *server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Passphrase string `json:"passphrase"`
		IncludeKey bool   `json:"include_key"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.Passphrase) < backup.MinPassphrase || len(body.Passphrase) > 200 {
		writeError(w, http.StatusBadRequest, backup.ErrWeakPassphrase.Error())
		return
	}
	name, _ := s.app.Store.DeviceName(r.Context())
	opts := backup.Options{Passphrase: body.Passphrase, MiloVersion: s.version, DeviceName: name, WorkDir: s.app.Cfg.DataDir}
	if body.IncludeKey {
		opts.KeyPath = s.app.Cfg.KeyPath
	}
	// Build the file on disk first so a failure becomes a clean error, not a broken download.
	tmp, err := os.CreateTemp(s.app.Cfg.DataDir, "download-*.milo")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.cannot_prepare_the_backup")
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := backup.Create(r.Context(), s.app.Store, opts, tmp); err != nil {
		s.app.Log.Error("backup download", "error", err)
		writeError(w, http.StatusInternalServerError, "web.err.the_backup_could_not_be_created")
		return
	}
	info, _ := tmp.Stat()
	tmp.Seek(0, io.SeekStart)                                                                           //nolint:errcheck
	s.audit(r, "backup.downloaded", "", map[string]string{"includes_key": fmt.Sprint(body.IncludeKey)}) // never the passphrase
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="milo-backup-`+time.Now().Format("2006-01-02")+`.milo"`)
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	w.Header().Set("Cache-Control", "no-store")
	io.Copy(w, tmp) //nolint:errcheck
}

// restoreBackup verifies an uploaded backup and stages it; it takes effect
// when Milo restarts. Form fields: "passphrase" first, then the file.
func (s *server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	// Their own bucket: a mistyped restore passphrase must not count against signing in.
	if !s.restoreLimiter.Try(ip) {
		writeError(w, http.StatusTooManyRequests, "web.err.too_many_failed_attempts_try_again_later")
		return
	}
	wrongPass := false
	defer func() {
		if !wrongPass {
			s.restoreLimiter.Refund(ip) // only wrong passphrases stay on record
		}
	}()
	if !restoreBusy.TryLock() {
		writeError(w, http.StatusConflict, "web.err.another_restore_is_being_checked_try_again_i")
		return
	}
	defer restoreBusy.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "web.err.send_the_backup_as_a_form_upload")
		return
	}
	var pass string
	for {
		part, err := mr.NextPart()
		if err != nil {
			writeError(w, http.StatusBadRequest, "web.err.the_passphrase_and_the_backup_file_are_both")
			return
		}
		if part.FormName() == "passphrase" {
			b, _ := io.ReadAll(io.LimitReader(part, 512))
			pass = string(b)
			continue
		}
		if part.FormName() != "file" || pass == "" {
			writeError(w, http.StatusBadRequest, "web.err.send_the_passphrase_before_the_file")
			return
		}
		wrongPass = s.stageRestore(w, r, part, pass)
		return
	}
}

// stageRestore reports whether the backup could not be opened because the passphrase was wrong.
func (s *server) stageRestore(w http.ResponseWriter, r *http.Request, file io.Reader, pass string) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	c, err := backup.Open(ctx, file, pass, s.app.Cfg.DataDir)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return errors.Is(err, backup.ErrWrongPassphrase)
	}
	defer c.Close()
	if err := backup.Stage(c, s.app.Cfg.DataDir, s.app.Cfg.KeyPath); err != nil {
		if errors.Is(err, backup.ErrKeyMismatch) && c.Manifest.IncludesKey {
			writeError(w, http.StatusUnprocessableEntity, "web.err.restore_key_mismatch", "reason", err.Error())
		} else {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		}
		return false
	}
	s.audit(r, "backup.restore_staged", c.Manifest.DeviceName, nil)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"staged": true, "device_name": c.Manifest.DeviceName, "created_at": c.Manifest.CreatedAt,
		"schema_version": c.Manifest.SchemaVersion, "restart_required": true,
	})
	return false
}
