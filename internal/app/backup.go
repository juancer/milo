package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/juancer/nexo/internal/backup"
)

// Setting keys for local backups.
const (
	settingBackupAuto  = "backup.auto"
	settingBackupDir   = "backup.dir"
	settingBackupKeep  = "backup.keep"
	settingBackupEvery = "backup.every_hours"
	settingBackupLast  = "backup.last_at"
	settingBackupError = "backup.last_error"
)

// BackupSettings are the local-snapshot settings.
type BackupSettings struct {
	Auto       bool      `json:"auto"`
	Dir        string    `json:"dir"`
	Keep       int       `json:"keep"`
	EveryHours int       `json:"every_hours"`
	LastAt     time.Time `json:"last_at,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

// BackupDirAllowed reports whether dir may hold local snapshots: a folder below the data
// folder, but not the data folder itself and not the folders a restore works in.
func (a *App) BackupDirAllowed(dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(a.Cfg.DataDir), filepath.Clean(dir))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return !strings.HasPrefix(strings.SplitN(rel, string(filepath.Separator), 2)[0], "restore-")
}

// BackupSettings returns the current settings (defaults: on, daily, keep 7). Values read
// back from the database are clamped to what the settings page accepts: a restored
// database is not trusted to have valid ones.
func (a *App) BackupSettings(ctx context.Context) BackupSettings {
	b := BackupSettings{
		Auto:       a.Setting(ctx, settingBackupAuto, "1") != "0",
		Dir:        a.Setting(ctx, settingBackupDir, filepath.Join(a.Cfg.DataDir, "backups")),
		Keep:       max(a.IntSetting(ctx, settingBackupKeep, 7), 1),
		EveryHours: min(max(a.IntSetting(ctx, settingBackupEvery, 24), 1), 168),
		LastError:  a.Setting(ctx, settingBackupError, ""),
	}
	if !a.BackupDirAllowed(b.Dir) {
		b.Dir = filepath.Join(a.Cfg.DataDir, "backups")
	}
	if t, err := time.Parse(time.RFC3339, a.Setting(ctx, settingBackupLast, "")); err == nil {
		b.LastAt = t
	}
	return b
}

// SaveBackupSettings stores the settings after the caller validated them.
func (a *App) SaveBackupSettings(ctx context.Context, b BackupSettings) error {
	auto := "0"
	if b.Auto {
		auto = "1"
	}
	for k, v := range map[string]string{
		settingBackupAuto: auto, settingBackupDir: b.Dir,
		settingBackupKeep: strconv.Itoa(b.Keep), settingBackupEvery: strconv.Itoa(b.EveryHours),
	} {
		if err := a.Store.SetSetting(ctx, k, v); err != nil {
			return err
		}
	}
	return nil
}

// RunLocalBackup writes a database snapshot into the backup folder and prunes old ones.
func (a *App) RunLocalBackup(ctx context.Context) (string, error) {
	s := a.BackupSettings(ctx)
	path, err := backup.Snapshot(ctx, a.Store, s.Dir, s.Keep, time.Now())
	if err != nil {
		a.Store.SetSetting(ctx, settingBackupError, err.Error()) //nolint:errcheck
		return "", fmt.Errorf("backup failed: %w", err)
	}
	a.Store.SetSetting(ctx, settingBackupLast, time.Now().UTC().Format(time.RFC3339)) //nolint:errcheck
	a.Store.SetSetting(ctx, settingBackupError, "")                                   //nolint:errcheck
	return path, nil
}

// backupIfDue runs the automatic backup when it is switched on and overdue.
func (a *App) backupIfDue(ctx context.Context) {
	s := a.BackupSettings(ctx)
	if !s.Auto || time.Since(s.LastAt) < time.Duration(s.EveryHours)*time.Hour {
		return
	}
	if path, err := a.RunLocalBackup(ctx); err != nil {
		a.Log.Warn("automatic backup failed", "error", err)
	} else {
		a.Log.Info("automatic backup written", "file", filepath.Base(path))
	}
}
