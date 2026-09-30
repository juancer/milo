package audit

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juancer/nexo/internal/store"
	_ "modernc.org/sqlite"
)

func TestRecordRedactsSecretsAndPersists(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	l := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))

	err = l.Record(ctx, "admin", "feature.enabled", "telegram", map[string]string{
		"ip":        "192.168.1.5",
		"bot_token": "SUPERSECRET",
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := st.ListAudit(ctx, 5)
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	e := entries[0]
	if e.Actor != "admin" || e.Action != "feature.enabled" || e.Target != "telegram" {
		t.Fatalf("entry = %+v", e)
	}
	if strings.Contains(e.Details, "SUPERSECRET") || !strings.Contains(e.Details, "192.168.1.5") {
		t.Fatalf("details = %s", e.Details)
	}
}

var dbPaths = map[*testing.T]string{}

func dbPath(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "n.db")
	dbPaths[t] = p
	return p
}

func openTest(t *testing.T) (*store.Store, *Logger) {
	t.Helper()
	st, err := store.Open(context.Background(), dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestFloodDoesNotEvictOtherActors(t *testing.T) {
	ctx := context.Background()
	st, l := openTest(t)
	for i := 0; i < 3; i++ {
		l.Record(ctx, "owner", "feature.enabled", fmt.Sprintf("f%d", i), nil)
	}
	for i := 0; i < 5000; i++ {
		l.Record(ctx, "mallory", "feature.enabled", "telegram", map[string]string{"ip": "10.0.0.9"})
	}
	if err := l.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	entries, _ := st.ListAudit(ctx, 100)
	owner, mallory, repeats := 0, 0, 0
	for _, e := range entries {
		switch e.Actor {
		case "owner":
			owner++
		case "mallory":
			mallory++
			repeats += e.Repeats
		}
	}
	if owner != 3 {
		t.Fatalf("owner rows = %d, want 3", owner)
	}
	if mallory != 1 || repeats != 5000 {
		t.Fatalf("mallory rows = %d repeats = %d, want 1 row counting 5000", mallory, repeats)
	}
}

func TestCapEvictsHeaviestActorFirst(t *testing.T) {
	ctx := context.Background()
	st, _ := openTest(t)
	for i := 0; i < 2; i++ {
		st.InsertAudit(ctx, store.AuditEntry{Actor: "owner", Action: "a", Target: fmt.Sprint(i)})
	}
	for i := 0; i < 20; i++ {
		st.InsertAudit(ctx, store.AuditEntry{Actor: "mallory", Action: "a", Target: fmt.Sprint(i)})
	}
	if err := st.PruneAudit(ctx, time.Hour, 10); err != nil {
		t.Fatal(err)
	}
	entries, _ := st.ListAudit(ctx, 100)
	owner := 0
	for _, e := range entries {
		if e.Actor == "owner" {
			owner++
		}
	}
	if len(entries) != 10 || owner != 2 {
		t.Fatalf("rows = %d owner = %d, want 10 and 2", len(entries), owner)
	}
	if entries[0].Target != "19" || entries[7].Target != "12" {
		t.Fatalf("expected newest mallory rows kept: %+v", entries)
	}
}

func TestPruneByAgeRemovesOnlyOldRows(t *testing.T) {
	ctx := context.Background()
	st, _ := openTest(t)
	st.InsertAudit(ctx, store.AuditEntry{Actor: "a", Action: "old"})
	st.InsertAudit(ctx, store.AuditEntry{Actor: "b", Action: "new"})
	raw, err := sql.Open("sqlite", dbPaths[t])
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx,
		`UPDATE audit_log SET at = strftime('%Y-%m-%dT%H:%M:%fZ','now','-100 days') WHERE action = 'old'`); err != nil {
		t.Fatal(err)
	}
	if err := st.PruneAudit(ctx, retention, maxEntries); err != nil {
		t.Fatal(err)
	}
	entries, _ := st.ListAudit(ctx, 10)
	if len(entries) != 1 || entries[0].Action != "new" {
		t.Fatalf("entries = %+v", entries)
	}
}
