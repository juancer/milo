package audit

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juancer/nexo/internal/store"
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
