package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
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

// BackupSettings returns the current settings (defaults: on, daily, keep 7).
func (a *App) BackupSettings(ctx context.Context) BackupSettings {
	b := BackupSettings{
		Auto:       a.Setting(ctx, settingBackupAuto, "1") != "0",
		Dir:        a.Setting(ctx, settingBackupDir, filepath.Join(a.Cfg.DataDir, "backups")),
		Keep:       a.IntSetting(ctx, settingBackupKeep, 7),
		EveryHours: a.IntSetting(ctx, settingBackupEvery, 24),
		LastError:  a.Setting(ctx, settingBackupError, ""),
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
