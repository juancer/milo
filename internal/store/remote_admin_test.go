package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// Both remote-administration switches are opt-in: a new person has them off.
func TestRemoteAdminFlagsDefaultOff(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	id, err := s.CreateTelegramUser(ctx, "Dad", "PARENT", 7, "dad")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.GetUser(ctx, id)
	if err != nil || u.ForwardIncoming || u.DeviceAlerts {
		t.Fatalf("new user = %+v, %v; want both switches off", u, err)
	}
	if err := s.SetUserForwardIncoming(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	byTG, _ := s.GetUserByTelegramID(ctx, 7)
	all, _ := s.ListTelegramUsers(ctx)
	if !byTG.ForwardIncoming || byTG.DeviceAlerts || len(all) != 1 || !all[0].ForwardIncoming {
		t.Fatalf("switch not stored: %+v %+v", byTG, all)
	}
	if err := s.SetUserDeviceAlerts(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.GetUser(ctx, id); !u.DeviceAlerts {
		t.Fatal("alerts switch not stored")
	}
	if err := s.SetUserForwardIncoming(ctx, 999, true); err != ErrNotFound {
		t.Fatalf("unknown user = %v, want ErrNotFound", err)
	}
}

// Installs from before remote administration keep their people, with both switches off.
func TestRemoteAdminMigrationKeepsExistingPeople(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before := fstest.MapFS{}
	fs.WalkDir(migrationFiles, ".", func(p string, d fs.DirEntry, _ error) error {
		if !d.IsDir() && !strings.HasPrefix(p, "0013_") {
			b, _ := fs.ReadFile(migrationFiles, p)
			before[p] = &fstest.MapFile{Data: b}
		}
		return nil
	})
	if err := migrate(ctx, db, before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (display_name, role, telegram_id, telegram_username) VALUES ('Mum', 'PARENT', 5, 'mum')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, migrationFiles); err != nil {
		t.Fatal(err)
	}
	var name string
	var fwd, alerts int
	if err := db.QueryRow(`SELECT display_name, forward_incoming, device_alerts FROM users`).Scan(&name, &fwd, &alerts); err != nil {
		t.Fatal(err)
	}
	if name != "Mum" || fwd != 0 || alerts != 0 {
		t.Fatalf("migrated user = %q %d %d", name, fwd, alerts)
	}
	if _, err := db.Exec(`UPDATE users SET forward_incoming = 2`); err == nil {
		t.Fatal("the switch accepts values other than 0 and 1")
	}
}
