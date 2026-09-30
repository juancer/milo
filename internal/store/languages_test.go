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

func TestLanguageDefaultsOnNewInstall(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	g, err := s.GetGPTSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g.Language != "en" || g.TTSLanguage != "" || g.TTSSpeed != 1 || g.TTSVolume != 100 {
		t.Fatalf("new install defaults = %q %q %v %d", g.Language, g.TTSLanguage, g.TTSSpeed, g.TTSVolume)
	}
	if got := g.SpeechLanguage(); got != "en" {
		t.Fatalf("SpeechLanguage = %q", got)
	}
	g.Language, g.TTSLanguage = "auto", ""
	if g.SpeechLanguage() != "" {
		t.Fatal("auto has no fixed speech language")
	}
	g.Language, g.TTSLanguage, g.TTSSpeed, g.TTSVolume = "es", "fr", 1.5, 40
	if err := s.UpdateGPTSettings(ctx, g); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetGPTSettings(ctx)
	if got.SpeechLanguage() != "fr" || got.TTSSpeed != 1.5 || got.TTSVolume != 40 {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestUserLanguage(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	id, _ := s.CreateTelegramUser(ctx, "Mum", "PARENT", 5, "mum")
	if u, _ := s.GetUser(ctx, id); u.Language != "" {
		t.Fatalf("default language = %q, want none (follow the notification language)", u.Language)
	}
	if err := s.SetUserLanguage(ctx, id, "es"); err != nil {
		t.Fatal(err)
	}
	u, _ := s.GetUserByTelegramID(ctx, 5)
	if u.Language != "es" {
		t.Fatalf("language = %q", u.Language)
	}
	list, _ := s.ListTelegramUsers(ctx)
	if len(list) != 1 || list[0].Language != "es" {
		t.Fatalf("list = %+v", list)
	}
	if err := s.SetUserLanguage(ctx, 999, "es"); err != ErrNotFound {
		t.Fatalf("unknown user: %v", err)
	}
}

// Installs from before languages existed keep working: free text that names a supported language
// is converted, anything else follows the child ("auto").
func TestLanguageMigrationConvertsOldFreeText(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ old, want string }{
		{"", "auto"}, {"Spanish", "es"}, {" español ", "es"}, {"English", "en"}, {"Klingon", "auto"},
	} {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
		if err != nil {
			t.Fatal(err)
		}
		before := fstest.MapFS{}
		fs.WalkDir(migrationFiles, ".", func(p string, d fs.DirEntry, _ error) error {
			if !d.IsDir() && !strings.HasPrefix(p, "0012_") {
				b, _ := fs.ReadFile(migrationFiles, p)
				before[p] = &fstest.MapFile{Data: b}
			}
			return nil
		})
		if err := migrate(ctx, db, before); err != nil {
			t.Fatal(err)
		}
		// An existing install already has an administrator.
		db.Exec(`INSERT INTO admin_users (username, password_hash, is_owner) VALUES ('admin', 'x', 1)`)
		db.Exec(`UPDATE gpt_settings SET language = ?`, c.old)
		if err := migrate(ctx, db, migrationFiles); err != nil {
			t.Fatal(err)
		}
		var got string
		db.QueryRow(`SELECT language FROM gpt_settings`).Scan(&got)
		if got != c.want {
			t.Errorf("language %q migrated to %q, want %q", c.old, got, c.want)
		}
		db.Close()
	}
}
