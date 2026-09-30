package web

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/juancer/nexo/internal/app"
	"github.com/juancer/nexo/internal/backup"
)

// A mistyped restore passphrase must not lock the owner out of signing in, and restores that
// succeed must never count against the restore limit.
func TestRestoreAttemptsHaveTheirOwnBucket(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	file := downloadBackup(t, e, cookie, false)
	for i := 0; i < 5; i++ {
		body, ct := multipartBody([][2]string{{"passphrase", "wrong passphrase " + fmt.Sprint(i)}}, "b.nexo", file)
		if resp := e.upload(t, "/api/backup/restore", body, ct, cookie); resp.StatusCode != 422 {
			t.Fatalf("wrong passphrase = %d", resp.StatusCode)
		}
	}
	if e.loginAs(t, "admin", testPassword) == nil { // fails the test itself unless the sign-in is 200
		t.Fatal("no session")
	}
	// Five wrong guesses used up the restore bucket, but a fresh address is not affected.
	e2 := newEnv(t, true)
	cookie2 := e2.login(t)
	file2 := downloadBackup(t, e2, cookie2, true)
	for i := 0; i < 7; i++ {
		body, ct := multipartBody([][2]string{{"passphrase", backupPass}}, "b.nexo", file2)
		if resp := e2.upload(t, "/api/backup/restore", body, ct, cookie2); resp.StatusCode != 202 {
			t.Fatalf("successful restore %d = %d", i, resp.StatusCode)
		}
	}
}

func TestBackupFolderMustBeInsideTheDataFolder(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	data := e.app.Cfg.DataDir
	put := func(dir string) int {
		body := fmt.Sprintf(`{"auto":true,"dir":%q,"keep":3,"every_hours":24}`, dir)
		return e.do(t, "PUT", "/api/backup", body, cookie, nil).StatusCode
	}
	for name, dir := range map[string]string{
		"outside the data folder": filepath.Join(t.TempDir(), "b"),
		"the data folder itself":  data,
		"restore-pending":         filepath.Join(data, "restore-pending"),
		"inside restore-pending":  filepath.Join(data, "restore-pending", "x"),
	} {
		if got := put(dir); got != 400 {
			t.Errorf("%s = %d, want 400", name, got)
		}
	}
	if got := put(filepath.Join(data, "snapshots")); got != 200 {
		t.Fatalf("a folder inside the data folder = %d, want 200", got)
	}
}

// A database restored with a folder outside the data folder must not make the listing
// disclose that folder.
func TestBackupListingIgnoresAFolderOutsideTheDataFolder(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "nexo-20260101-000000.db"), []byte("x"), 0o600)
	if err := e.store.SetSetting(context.Background(), "backup.dir", outside); err != nil {
		t.Fatal(err)
	}
	got := decode[struct {
		Settings app.BackupSettings `json:"settings"`
		Files    []backup.File      `json:"files"`
	}](t, e.do(t, "GET", "/api/backup", "", cookie, nil))
	for _, f := range got.Files { // the automatic snapshot in the default folder may be there
		if f.Name == "nexo-20260101-000000.db" {
			t.Fatalf("the folder outside the data folder was listed: %+v", got.Files)
		}
	}
	if got.Settings.Dir != filepath.Join(e.app.Cfg.DataDir, "backups") {
		t.Fatalf("listing = %+v", got)
	}
}
