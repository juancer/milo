package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nexo.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestOpenConfiguresWALAndForeignKeys(t *testing.T) {
	s, _ := openTemp(t)
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v; want wal", mode, err)
	}
	var sync int
	if err := s.db.QueryRow(`PRAGMA synchronous`).Scan(&sync); err != nil || sync != 1 {
		t.Fatalf("synchronous = %d, %v; want 1 (NORMAL)", sync, err)
	}
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v; want 1", fk, err)
	}
}

func TestMigrationsAreIdempotentAndKeepData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nexo.db")

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := s.SchemaVersion(ctx)
	if err != nil || v1 < 1 {
		t.Fatalf("SchemaVersion = %d, %v", v1, err)
	}
	if err := s.SetSetting(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s.Close()
	v2, _ := s.SchemaVersion(ctx)
	if v2 != v1 {
		t.Fatalf("schema version changed on reopen: %d -> %d", v1, v2)
	}
	if got, err := s.GetSetting(ctx, "k"); err != nil || got != "v" {
		t.Fatalf("setting lost across reopen: %q, %v", got, err)
	}
}

func TestMigrationFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	bad := fstest.MapFS{
		"0100_bad.sql": {Data: []byte(`CREATE TABLE half_done (id INTEGER); THIS IS NOT SQL;`)},
	}
	if err := migrate(ctx, s.db, bad); err == nil {
		t.Fatal("expected migration error")
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'half_done'`).Scan(&n)
	if n != 0 {
		t.Fatal("failed migration left a table behind")
	}
	var applied int
	s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 100`).Scan(&applied)
	if applied != 0 {
		t.Fatal("failed migration was recorded as applied")
	}
}

func TestRefusesDatabaseNewerThanBinary(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.db.Exec(`INSERT INTO schema_migrations (version, name) VALUES (9999, 'future')`); err != nil {
		t.Fatal(err)
	}
	err := migrate(ctx, s.db, migrationFiles)
	if err == nil || !strings.Contains(err.Error(), "9999") {
		t.Fatalf("expected downgrade refusal, got %v", err)
	}
}

func TestBadMigrationNames(t *testing.T) {
	for name, fsys := range map[string]fstest.MapFS{
		"no prefix":  {"init.sql": {Data: []byte("SELECT 1;")}},
		"duplicates": {"0001_a.sql": {Data: []byte("SELECT 1;")}, "0001_b.sql": {Data: []byte("SELECT 1;")}},
	} {
		if _, err := loadMigrations(fsys); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.GetSetting(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing setting err = %v", err)
	}
	s.SetSetting(ctx, "k", "1")
	s.SetSetting(ctx, "k", "2")
	if got, _ := s.GetSetting(ctx, "k"); got != "2" {
		t.Fatalf("got %q, want 2", got)
	}
}

func TestFeatures(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if err := s.SetFeatureEnabled(ctx, "ghost", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown feature err = %v", err)
	}
	s.EnsureFeature(ctx, "b")
	s.EnsureFeature(ctx, "a")
	s.SetFeatureEnabled(ctx, "a", true)
	s.EnsureFeature(ctx, "a") // must not reset the flag
	got, err := s.ListFeatures(ctx)
	if err != nil || len(got) != 2 || got[0].Name != "a" || !got[0].Enabled || got[1].Enabled {
		t.Fatalf("ListFeatures = %+v, %v", got, err)
	}
}

func TestAuditNewestFirst(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	s.InsertAudit(ctx, AuditEntry{Actor: "a", Action: "first"})
	s.InsertAudit(ctx, AuditEntry{Actor: "a", Action: "second", Target: "t", Details: "{}"})
	got, err := s.ListAudit(ctx, 10)
	if err != nil || len(got) != 2 || got[0].Action != "second" || got[0].At.IsZero() {
		t.Fatalf("ListAudit = %+v, %v", got, err)
	}
}

func TestSecretsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.GetSecret(ctx, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	s.PutSecret(ctx, "x", []byte{1, 2, 3})
	if got, _ := s.GetSecret(ctx, "x"); string(got) != "\x01\x02\x03" {
		t.Fatalf("got %v", got)
	}
}

func TestUsersAndPermissions(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.CreateUser(ctx, "x", "SUPERUSER"); err == nil {
		t.Fatal("invalid role must be rejected by the schema")
	}
	id, err := s.CreateUser(ctx, "Parent", "PARENT")
	if err != nil {
		t.Fatal(err)
	}
	if u, err := s.GetUser(ctx, id); err != nil || u.Role != "PARENT" {
		t.Fatalf("GetUser = %+v, %v", u, err)
	}
	if _, err := s.GetUser(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	s.SetPermission(ctx, id, "send_message", false)
	s.SetPermission(ctx, id, "send_message", true)
	if got, _ := s.UserPermissions(ctx, id); !got["send_message"] || len(got) != 1 {
		t.Fatalf("UserPermissions = %v", got)
	}
}

func TestButtonsSeededAndEditable(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	list, err := s.ListButtons(ctx)
	if err != nil || len(list) != 4 {
		t.Fatalf("seeded buttons = %+v, %v", list, err)
	}
	if list[0].Color != "red" || list[0].GPIO != 17 || list[0].Actions["short_press"].Action != "talk" {
		t.Fatalf("red = %+v", list[0])
	}

	// Another installation: RED becomes GPT, pin changes.
	list[0].Actions = map[string]ButtonAction{"short_press": {Action: "gpt"}, "long_press": {Action: "set_volume", Parameters: `{"change":10}`}}
	list[0].GPIO = 5
	if err := s.UpdateButton(ctx, list[0]); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetButton(ctx, list[0].ID)
	if got.GPIO != 5 || got.Actions["short_press"].Action != "gpt" || len(got.Actions) != 2 {
		t.Fatalf("updated = %+v", got)
	}

	if _, err := s.CreateButton(ctx, Button{Name: "Blue", GPIO: 40, Enabled: true}); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if _, err := s.CreateButton(ctx, Button{Name: "X", GPIO: 5, Enabled: true}); err == nil {
		t.Fatal("duplicate gpio accepted")
	}
	if _, err := s.CreateButton(ctx, Button{Name: "X", GPIO: 99, Enabled: true}); err == nil {
		t.Fatal("out-of-range gpio accepted")
	}

	if err := s.DeleteButton(ctx, got.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM button_actions WHERE button_id = ?`, got.ID).Scan(&n)
	if n != 0 {
		t.Fatal("actions not cascaded")
	}
	if err := s.DeleteButton(ctx, got.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestFeatureEnabledLookup(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if on, err := s.FeatureEnabled(ctx, "ghost"); on || err != nil {
		t.Fatalf("unknown = %v, %v", on, err)
	}
	s.EnsureFeature(ctx, "x")
	s.SetFeatureEnabled(ctx, "x", true)
	if on, _ := s.FeatureEnabled(ctx, "x"); !on {
		t.Fatal("enabled feature reported off")
	}
}

func TestInvitationsAreSingleUseExpiringAndRevocable(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	if _, err := s.ConsumeInvitation(ctx, "unknown", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token = %v", err)
	}
	s.CreateInvitation(ctx, "h-valid", "FAMILY", "grandma", now.Add(time.Hour))
	inv, err := s.ConsumeInvitation(ctx, "h-valid", now)
	if err != nil || inv.Role != "FAMILY" || inv.Note != "grandma" {
		t.Fatalf("consume = %+v, %v", inv, err)
	}
	if _, err := s.ConsumeInvitation(ctx, "h-valid", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("invitation redeemed twice")
	}

	s.CreateInvitation(ctx, "h-old", "PARENT", "", now.Add(time.Hour))
	if _, err := s.ConsumeInvitation(ctx, "h-old", now.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired invitation accepted (boundary)")
	}
	if _, err := s.ConsumeInvitation(ctx, "h-old", now.Add(48*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired invitation accepted")
	}

	id, _ := s.CreateInvitation(ctx, "h-revoked", "CHILD", "", now.Add(time.Hour))
	if err := s.RevokeInvitation(ctx, id, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeInvitation(ctx, "h-revoked", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked invitation accepted")
	}
	if err := s.RevokeInvitation(ctx, id, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoking twice must report not found")
	}
	if _, err := s.CreateInvitation(ctx, "h-owner", "OWNER", "", now); err == nil {
		t.Fatal("an invitation must never be able to grant OWNER")
	}

	list, _ := s.ListInvitations(ctx, 10)
	statuses := map[string]string{}
	for _, i := range list {
		statuses[i.Note+i.Role] = i.Status(now.Add(2 * time.Hour))
	}
	if statuses["grandmaFAMILY"] != "used" || statuses["CHILD"] != "revoked" || statuses["PARENT"] != "expired" {
		t.Fatalf("statuses = %v", statuses)
	}
}

func TestConcurrentConsumeOnlyOneWins(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := time.Now()
	s.CreateInvitation(ctx, "race", "FAMILY", "", now.Add(time.Hour))
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { _, err := s.ConsumeInvitation(ctx, "race", now); results <- err }()
	}
	wins := 0
	for i := 0; i < 8; i++ {
		if err := <-results; err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d goroutines redeemed a single-use invitation", wins)
	}
}

func TestPendingAuthorizationApproveAndReject(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := time.Now()
	invID, _ := s.CreateInvitation(ctx, "h", "PARENT", "", now.Add(time.Hour))
	pid, err := s.CreatePending(ctx, PendingAuthorization{TelegramID: 111, Username: "mum", FirstName: "Mami", Role: "PARENT", InvitationID: invID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePending(ctx, PendingAuthorization{TelegramID: 111, Role: "PARENT"}); err == nil {
		t.Fatal("a person can have only one pending request")
	}
	if ok, _ := s.HasPending(ctx, 111); !ok {
		t.Fatal("HasPending false")
	}
	if u, err := s.GetUserByTelegramID(ctx, 111); err == nil {
		t.Fatalf("user exists before approval: %+v", u)
	}

	user, err := s.ApprovePending(ctx, pid, "", now)
	if err != nil || user.DisplayName != "Mami" || user.Role != "PARENT" || !user.Active {
		t.Fatalf("approve = %+v, %v", user, err)
	}
	if _, err := s.ApprovePending(ctx, pid, "", now); !errors.Is(err, ErrNotFound) {
		t.Fatal("approved twice")
	}
	got, err := s.GetUserByTelegramID(ctx, 111)
	if err != nil || got.ID != user.ID {
		t.Fatalf("lookup = %+v, %v", got, err)
	}

	pid2, _ := s.CreatePending(ctx, PendingAuthorization{TelegramID: 222, Role: "FAMILY"})
	tg, err := s.RejectPending(ctx, pid2, now)
	if err != nil || tg != 222 {
		t.Fatalf("reject = %d, %v", tg, err)
	}
	if _, err := s.GetUserByTelegramID(ctx, 222); !errors.Is(err, ErrNotFound) {
		t.Fatal("rejected person became a user")
	}
	if list, _ := s.ListPending(ctx); len(list) != 0 {
		t.Fatalf("pending = %+v", list)
	}
}

func TestChatsCRUD(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	uid, _ := s.CreateTelegramUser(ctx, "Dad", "PARENT", 5, "dad")
	id, err := s.CreateChat(ctx, Chat{Name: "Dad", Kind: "user", UserID: uid, TelegramChatID: 5, Favorite: true, Position: 2, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	s.CreateChat(ctx, Chat{Name: "Family", Kind: "group", TelegramChatID: -100, Favorite: true, Position: 1, Enabled: true})
	list, _ := s.ListChats(ctx)
	if len(list) != 2 || list[0].Name != "Family" {
		t.Fatalf("order = %+v", list)
	}
	if _, err := s.CreateChat(ctx, Chat{Name: "Dad", Kind: "group", TelegramChatID: 1}); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if _, err := s.CreateChat(ctx, Chat{Name: "X", Kind: "channel", TelegramChatID: 1}); err == nil {
		t.Fatal("bad kind accepted")
	}
	c, _ := s.GetChat(ctx, id)
	c.Name = "Daddy"
	if err := s.UpdateChat(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetChat(ctx, id); got.Name != "Daddy" {
		t.Fatalf("got %+v", got)
	}
	if err := s.DeleteChat(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetChat(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("chat not deleted")
	}
}

func TestMessagesPendingOrderOutboxAndPrune(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := time.Now()

	if _, err := s.NextPending(ctx, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("empty inbox must report not found")
	}
	a, _ := s.InsertMessage(ctx, Message{Direction: "in", Kind: "text", Body: "first"})
	b, _ := s.InsertMessage(ctx, Message{Direction: "in", Kind: "text", Body: "second"})
	e, _ := s.InsertMessage(ctx, Message{Direction: "in", Kind: "text", Body: "help", Type: "emergency", Priority: "critical"})

	if m, _ := s.NextPending(ctx, false); m.ID != e {
		t.Fatalf("emergency must come first, got %d", m.ID)
	}
	if m, _ := s.NextPending(ctx, true); m.ID != e {
		t.Fatal("emergencyOnly returned wrong message")
	}
	s.MarkPlayed(ctx, e, now)
	if _, err := s.NextPending(ctx, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("emergencyOnly returned a non-emergency message")
	}
	if m, _ := s.NextPending(ctx, false); m.ID != a {
		t.Fatalf("oldest first, got %d", m.ID)
	}
	if total, emg, _ := s.PendingCounts(ctx); total != 2 || emg != 0 {
		t.Fatalf("counts = %d, %d", total, emg)
	}
	if un, _ := s.UnnotifiedPending(ctx); len(un) != 2 {
		t.Fatalf("unnotified = %d", len(un))
	}
	s.MarkNotified(ctx, a, now)
	if un, _ := s.UnnotifiedPending(ctx); len(un) != 1 || un[0].ID != b {
		t.Fatalf("unnotified = %+v", un)
	}

	// Outbox with retry.
	q, _ := s.InsertMessage(ctx, Message{Direction: "out", Kind: "text", Body: "hi"})
	if m, err := s.NextQueued(ctx, now); err != nil || m.ID != q || m.Status != "queued" {
		t.Fatalf("queued = %+v, %v", m, err)
	}
	retry := now.Add(time.Minute)
	s.MarkAttemptFailed(ctx, q, "network down", &retry)
	if _, err := s.NextQueued(ctx, now); !errors.Is(err, ErrNotFound) {
		t.Fatal("message retried before its backoff elapsed")
	}
	if m, err := s.NextQueued(ctx, now.Add(2*time.Minute)); err != nil || m.Attempts != 1 || m.LastError != "network down" {
		t.Fatalf("after backoff = %+v, %v", m, err)
	}
	s.MarkSent(ctx, q)
	if _, err := s.NextQueued(ctx, now.Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatal("sent message still queued")
	}

	// Prune keeps unfinished messages.
	n, _ := s.PruneMessages(ctx, now.Add(time.Hour))
	if n != 2 { // the played emergency and the sent outgoing one
		t.Fatalf("pruned %d, want 2", n)
	}
	if total, _, _ := s.PendingCounts(ctx); total != 2 {
		t.Fatal("pending messages were pruned")
	}
	if list, _ := s.RecentMessages(ctx, 10); len(list) != 2 {
		t.Fatalf("recent = %d", len(list))
	}
}
