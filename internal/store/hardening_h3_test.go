package store

import (
	"context"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestChatNameIsUniqueIgnoringCaseInTheDatabase(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.CreateChat(ctx, Chat{Name: "Dad", Kind: "group", TelegramChatID: -1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateChat(ctx, Chat{Name: "dad", Kind: "group", TelegramChatID: -2, Enabled: true}); err == nil {
		t.Fatal("case-variant duplicate was accepted")
	}
}

func TestChatNameMigrationRenamesLaterDuplicates(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.db.ExecContext(ctx, `DROP INDEX chats_name_lower`); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i, n := range []string{"Dad", "dad", "DAD", "Mum"} {
		id, err := s.CreateChat(ctx, Chat{Name: n, Kind: "group", TelegramChatID: int64(-i - 1), Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	body, err := fs.ReadFile(migrationFiles, "0011_chat_name_unique.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("migration on data with duplicates: %v", err)
	}
	want := []string{"Dad", "dad (" + itoa(ids[1]) + ")", "DAD (" + itoa(ids[2]) + ")", "Mum"}
	for i, id := range ids {
		c, err := s.GetChat(ctx, id)
		if err != nil || c.Name != want[i] {
			t.Fatalf("chat %d = %q, %v; want %q", id, c.Name, err, want[i])
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestApprovePendingCapsTheDefaultName(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	long := strings.Repeat("é", 64)
	pid, err := s.CreatePending(ctx, PendingAuthorization{TelegramID: 77, FirstName: long, Role: "FAMILY"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.ApprovePending(ctx, pid, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(u.DisplayName)); n != 40 {
		t.Fatalf("default name has %d runes, want 40", n)
	}
}

func TestQueuedOutCountsPerChatAndTotal(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	a, _ := s.CreateChat(ctx, Chat{Name: "A", Kind: "group", TelegramChatID: -1, Enabled: true})
	b, _ := s.CreateChat(ctx, Chat{Name: "B", Kind: "group", TelegramChatID: -2, Enabled: true})
	for _, c := range []int64{a, a, b} {
		if _, err := s.InsertMessage(ctx, Message{Direction: "out", Kind: "text", ChatID: c, Body: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if fc, total, err := s.QueuedOut(ctx, a); err != nil || fc != 2 || total != 3 {
		t.Fatalf("QueuedOut = %d, %d, %v; want 2, 3", fc, total, err)
	}
}
