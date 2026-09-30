package app

import (
	"context"
	"path/filepath"
	"testing"
)

// Values read back from a restored database are clamped to what the settings page accepts.
func TestBackupSettingsAreClampedWhenRead(t *testing.T) {
	ctx := context.Background()
	a := newApp(t)
	for _, c := range []struct {
		keep, every       string
		wantKeep, wantHrs int
	}{
		{"-1", "0", 1, 1},
		{"0", "-5", 1, 1},
		{"3", "99999999999999", 3, 168},
		{"3", "24", 3, 24},
	} {
		a.Store.SetSetting(ctx, settingBackupKeep, c.keep)
		a.Store.SetSetting(ctx, settingBackupEvery, c.every)
		if got := a.BackupSettings(ctx); got.Keep != c.wantKeep || got.EveryHours != c.wantHrs {
			t.Errorf("keep %s every %s = %+v, want %d/%d", c.keep, c.every, got, c.wantKeep, c.wantHrs)
		}
	}
}

func TestBackupDirIsRestrictedToTheDataFolder(t *testing.T) {
	ctx := context.Background()
	a := newApp(t)
	data := a.Cfg.DataDir
	for dir, want := range map[string]bool{
		filepath.Join(data, "backups"):                        true,
		filepath.Join(data, "a", "b"):                         true,
		data:                                                  false,
		filepath.Join(data, "restore-pending"):                false,
		filepath.Join(data, "restore-pending", "x"):           false,
		filepath.Join(data, "restore-pending.applying"):       false,
		filepath.Join(data, "restore-failed-20260101-000000"): false,
		filepath.Join(data, "..", "elsewhere"):                false,
		"/etc":                                                false,
		filepath.Dir(data):                                    false,
	} {
		if got := a.BackupDirAllowed(dir); got != want {
			t.Errorf("BackupDirAllowed(%s) = %v, want %v", dir, got, want)
		}
	}
	a.Store.SetSetting(ctx, settingBackupDir, "/etc")
	if got := a.BackupSettings(ctx).Dir; got != filepath.Join(data, "backups") {
		t.Fatalf("a stored folder outside the data folder was used: %s", got)
	}
}
