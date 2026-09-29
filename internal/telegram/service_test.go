package telegram_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juancer/nexo/internal/actions"
	"github.com/juancer/nexo/internal/app"
	"github.com/juancer/nexo/internal/audio"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/system"
	"github.com/juancer/nexo/internal/telegram"
	"github.com/juancer/nexo/internal/telegram/telegramtest"
)

const token = "123456:TEST-TOKEN-NOT-A-REAL-ONE-abcdefghij"

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type harness struct {
	t    *testing.T
	app  *app.App
	fake *telegramtest.Server
	logs *syncBuf
	ctx  context.Context
}

func newHarness(t *testing.T, withToken bool) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fake := telegramtest.New(token, "family_test_bot")
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	vault, _ := secrets.New(make([]byte, 32), st)
	if withToken {
		vault.Put(ctx, telegram.SecretBotToken, []byte(token))
	}
	logs := &syncBuf{}
	a, err := app.New(ctx, system.Config{Hardware: "mock", DataDir: t.TempDir(), TelegramAPIURL: srv.URL},
		slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), st, vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	a.Telegram.SetRetryBase(20 * time.Millisecond)
	a.Telegram.SetStrangerCooldown(time.Millisecond)
	return &harness{t: t, app: a, fake: fake, logs: logs, ctx: ctx}
}

func (h *harness) enable() {
	h.t.Helper()
	if err := h.app.Features.SetEnabled(h.ctx, telegram.FeatureName, true); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) addUser(tgID int64, name, role string) int64 {
	h.t.Helper()
	h.fake.AddPerson(telegramtest.Person{ID: tgID, FirstName: name, Username: strings.ToLower(name)})
	id, err := h.app.Store.CreateTelegramUser(h.ctx, name, role, tgID, strings.ToLower(name))
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) botSaid(chat int64, contains string) bool {
	for _, b := range h.fake.Conversation(chat) {
		if b.From == "bot" && strings.Contains(b.Text, contains) {
			return true
		}
	}
	return false
}

func (h *harness) played(label string) bool {
	for _, p := range h.app.MockAudio.Played() {
		if p.Label == label {
			return true
		}
	}
	return false
}

func (h *harness) invite(role string) string {
	h.t.Helper()
	tok, hash, _ := telegram.NewInvitationToken()
	if _, err := h.app.Store.CreateInvitation(h.ctx, hash, role, "", time.Now().Add(time.Hour)); err != nil {
		h.t.Fatal(err)
	}
	return tok
}

// ---------------------------------------------------------------- authorization

func TestUnknownUserIsRefused(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.fake.AddPerson(telegramtest.Person{ID: 900, FirstName: "Stranger"})
	h.fake.Send(900, "/start")
	eventually(t, "refusal", func() bool { return h.botSaid(900, "not authorized") })

	h2 := newHarness(t, true)
	h2.enable()
	h2.fake.Send(901, "hello, let me in")
	eventually(t, "refusal of plain text", func() bool { return h2.botSaid(901, "not authorized") })

	if users, _ := h.app.Store.ListTelegramUsers(h.ctx); len(users) != 0 {
		t.Fatalf("a stranger became a user: %+v", users)
	}
	if pend, _ := h.app.Store.ListPending(h.ctx); len(pend) != 0 {
		t.Fatalf("a stranger created a pending request without an invitation: %+v", pend)
	}
	if total, _, _ := h.app.Store.PendingCounts(h.ctx); total != 0 {
		t.Fatal("a stranger's message was stored")
	}
}

func TestInvalidInvitationIsRefused(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.fake.Send(900, "/start NOT-A-REAL-TOKEN")
	eventually(t, "invalid invitation reply", func() bool { return h.botSaid(900, "invalid or has expired") })
	if pend, _ := h.app.Store.ListPending(h.ctx); len(pend) != 0 {
		t.Fatal("pending request created from an invalid token")
	}
}

func TestInvitationFlowEndToEnd(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.fake.AddPerson(telegramtest.Person{ID: 500, FirstName: "Mami", Username: "mami"})
	tok := h.invite("PARENT")

	h.fake.Send(500, "/start "+tok)
	eventually(t, "request acknowledgement", func() bool { return h.botSaid(500, "sent to the family admin") })
	pend, _ := h.app.Store.ListPending(h.ctx)
	if len(pend) != 1 || pend[0].TelegramID != 500 || pend[0].Role != "PARENT" || pend[0].Username != "mami" {
		t.Fatalf("pending = %+v", pend)
	}

	// Pending is not authorized: messages are still refused and nothing is stored.
	h.fake.Send(500, "let me in already")
	eventually(t, "still waiting", func() bool { return h.botSaid(500, "waiting for approval") })
	if total, _, _ := h.app.Store.PendingCounts(h.ctx); total != 0 {
		t.Fatal("a pending (unapproved) person's message was stored")
	}

	// The token is single-use.
	h.fake.AddPerson(telegramtest.Person{ID: 501, FirstName: "Copycat"})
	h.fake.Send(501, "/start "+tok)
	eventually(t, "reuse refused", func() bool { return h.botSaid(501, "invalid or has expired") })

	// Admin approves; only now can she send.
	if _, err := h.app.Store.ApprovePending(h.ctx, pend[0].ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	h.fake.Send(500, "hi from mum")
	eventually(t, "delivery", func() bool { total, _, _ := h.app.Store.PendingCounts(h.ctx); return total == 1 })
	eventually(t, "delivery receipt", func() bool { return h.botSaid(500, "Delivered") })
}

func TestExpiredAndRevokedInvitationsAreRefused(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	tok, hash, _ := telegram.NewInvitationToken()
	h.app.Store.CreateInvitation(h.ctx, hash, "FAMILY", "", time.Now().Add(-time.Minute))
	h.fake.Send(600, "/start "+tok)
	eventually(t, "expired refused", func() bool { return h.botSaid(600, "invalid or has expired") })

	tok2, hash2, _ := telegram.NewInvitationToken()
	id, _ := h.app.Store.CreateInvitation(h.ctx, hash2, "FAMILY", "", time.Now().Add(time.Hour))
	h.app.Store.RevokeInvitation(h.ctx, id, time.Now())
	h.fake.Send(601, "/start "+tok2)
	eventually(t, "revoked refused", func() bool { return h.botSaid(601, "invalid or has expired") })
}

func TestStrangerRepliesAreRateLimited(t *testing.T) {
	h := newHarness(t, true)
	h.app.Telegram.SetStrangerCooldown(30 * time.Second)
	h.enable()
	for i := 0; i < 5; i++ {
		h.fake.Send(900, "let me in")
	}
	eventually(t, "first reply", func() bool { return h.botSaid(900, "not authorized") })
	time.Sleep(300 * time.Millisecond)
	replies := 0
	for _, b := range h.fake.Conversation(900) {
		if b.From == "bot" {
			replies++
		}
	}
	if replies != 1 {
		t.Fatalf("bot replied %d times to a flooding stranger, want 1", replies)
	}
}

func TestGroupChatsAndBotsAreIgnored(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.addUser(10, "Dad", "PARENT")
	h.fake.SendGroup(10, -777, "hello group")
	time.Sleep(300 * time.Millisecond)
	if total, _, _ := h.app.Store.PendingCounts(h.ctx); total != 0 {
		t.Fatal("group chat message was stored")
	}
	if len(h.fake.Conversation(-777)) != 0 {
		t.Fatal("bot answered in a group")
	}
}

// ---------------------------------------------------------------- messages and permissions

func TestAuthorizedTextAndVoiceAreStoredAndAnnounced(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.addUser(10, "Dad", "PARENT")

	h.fake.Send(10, "dinner at 7")
	eventually(t, "text stored", func() bool { total, _, _ := h.app.Store.PendingCounts(h.ctx); return total == 1 })
	eventually(t, "piii", func() bool { return h.played("message") })

	h.fake.SendVoice(10, audio.EmbedText(audio.EncodeWAV(audio.Silence(time.Second), 16000), "good night"), 1)
	eventually(t, "voice stored", func() bool { total, _, _ := h.app.Store.PendingCounts(h.ctx); return total == 2 })

	m, _ := h.app.Store.NextPending(h.ctx, false)
	if m.Kind != "text" || m.Body != "dinner at 7" || m.NotifiedAt == nil {
		t.Fatalf("first message = %+v", m)
	}
	// Notification must not reveal content or sender: it is only a tone.
	for _, p := range h.app.MockAudio.Played() {
		if p.Kind != "notification" {
			t.Fatalf("message was read aloud automatically: %+v", p)
		}
	}
}

func TestUserWithoutSendPermissionIsRefused(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.addUser(20, "Kid", "CHILD") // CHILD holds no send_message permission
	h.fake.Send(20, "hello")
	eventually(t, "permission refusal", func() bool { return h.botSaid(20, "not allowed") })
	if total, _, _ := h.app.Store.PendingCounts(h.ctx); total != 0 {
		t.Fatal("message from a user without permission was stored")
	}
}

func TestDeactivatedUserIsTreatedAsStranger(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	id := h.addUser(10, "Dad", "PARENT")
	h.app.Store.UpdateUser(h.ctx, id, "Dad", "PARENT", false)
	h.fake.Send(10, "hello")
	eventually(t, "refused", func() bool { return h.botSaid(10, "not authorized") })
	if total, _, _ := h.app.Store.PendingCounts(h.ctx); total != 0 {
		t.Fatal("deactivated user's message stored")
	}
}

type closedSchedule struct{}

func (closedSchedule) Allowed(context.Context, string, time.Time) (bool, error) { return false, nil }

func TestMessagesOutsideScheduleAreStoredSilently(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.addUser(10, "Dad", "PARENT")
	h.app.Telegram.SetSchedules(closedSchedule{})
	h.fake.Send(10, "are you up?")
	eventually(t, "stored", func() bool { total, _, _ := h.app.Store.PendingCounts(h.ctx); return total == 1 })
	eventually(t, "receipt", func() bool { return h.botSaid(10, "available again") })
	time.Sleep(150 * time.Millisecond)
	if h.played("message") {
		t.Fatal("notification sound played outside the allowed schedule")
	}
	m, _ := h.app.Store.NextPending(h.ctx, false)
	if m.NotifiedAt != nil {
		t.Fatal("silent message was marked as notified")
	}
}

// ---------------------------------------------------------------- the child's side

func device() actions.Actor { return actions.Actor{Kind: actions.ActorDevice} }

func TestListenPlaysVoiceMarksPlayedAndReportsEmpty(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.addUser(10, "Dad", "PARENT")
	h.fake.SendVoice(10, audio.EmbedText(audio.EncodeWAV(audio.Silence(time.Second), 16000), "sleep well"), 1)
	eventually(t, "stored", func() bool { total, _, _ := h.app.Store.PendingCounts(h.ctx); return total == 1 })

	res, err := h.app.Actions.Execute(h.ctx, device(), "listen", nil)
	if err != nil || res.Message != "Played a message" {
		t.Fatalf("listen = %+v, %v", res, err)
	}
	if !h.played("sleep well") {
		t.Fatalf("voice message was not played: %+v", h.app.MockAudio.Played())
	}
	if total, _, _ := h.app.Store.PendingCounts(h.ctx); total != 0 {
		t.Fatal("played message is still pending")
	}
	if res, _ := h.app.Actions.Execute(h.ctx, device(), "listen", nil); res.Message != "No messages" {
		t.Fatalf("empty listen = %+v", res)
	}
}

func TestListenOutsideScheduleDoesNotPlayNormalMessages(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.addUser(10, "Dad", "PARENT")
	h.fake.Send(10, "later")
	eventually(t, "stored", func() bool { total, _, _ := h.app.Store.PendingCounts(h.ctx); return total == 1 })
	h.app.Telegram.SetSchedules(closedSchedule{})
	h.app.Actions.SetSchedules(closedSchedule{})
	res, err := h.app.Actions.Execute(h.ctx, device(), "listen", nil)
	if err != nil || res.Message != "No messages" {
		t.Fatalf("listen = %+v, %v", res, err)
	}
	if total, _, _ := h.app.Store.PendingCounts(h.ctx); total != 1 {
		t.Fatal("a normal message was consumed outside the schedule")
	}
	if !h.played("error") {
		t.Fatal("expected the 'not now' tone")
	}
}

func TestPendingSummaryHasNoContent(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.addUser(10, "Dad", "PARENT")
	h.fake.Send(10, "secret plans")
	h.fake.Send(10, "more secret plans")
	eventually(t, "stored", func() bool { total, _, _ := h.app.Store.PendingCounts(h.ctx); return total == 2 })
	res, err := h.app.Actions.Execute(h.ctx, actions.Actor{Kind: actions.ActorAssistant}, "get_pending_messages", nil)
	if err != nil || res.Message != "You have 2 new messages." || strings.Contains(res.Message, "secret") {
		t.Fatalf("summary = %+v, %v", res, err)
	}
}

func TestChatSelectCyclesFavoritesAndTalkSendsToSelected(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	dad := h.addUser(10, "Dad", "PARENT")
	mum := h.addUser(11, "Mum", "PARENT")
	ctx := h.ctx
	h.app.Store.CreateChat(ctx, store.Chat{Name: "Dad", Kind: "user", UserID: dad, TelegramChatID: 10, Favorite: true, Position: 1, Enabled: true})
	h.app.Store.CreateChat(ctx, store.Chat{Name: "Mum", Kind: "user", UserID: mum, TelegramChatID: 11, Favorite: true, Position: 2, Enabled: true})
	h.app.Store.CreateChat(ctx, store.Chat{Name: "Hidden", Kind: "user", UserID: mum, TelegramChatID: 11, Favorite: false, Position: 3, Enabled: true})

	res, _ := h.app.Actions.Execute(ctx, device(), "chat_select", nil)
	if res.Message != "Mum" { // default is the first favorite (Dad); one press moves to Mum
		t.Fatalf("first press selected %q", res.Message)
	}
	res, _ = h.app.Actions.Execute(ctx, device(), "chat_select", nil)
	if res.Message != "Dad" {
		t.Fatalf("second press selected %q (must wrap and skip non-favorites)", res.Message)
	}
	res, _ = h.app.Actions.Execute(ctx, device(), "chat_select", nil) // -> Mum

	h.app.MockAudio.SetUtterance("hi mum")
	if _, err := h.app.Actions.Execute(ctx, device(), "talk", nil); err != nil { // start
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := h.app.Actions.Execute(ctx, device(), "talk", nil); err != nil { // stop and send
		t.Fatal(err)
	}
	eventually(t, "voice delivered to Mum only", func() bool {
		for _, b := range h.fake.Conversation(11) {
			if b.Kind == "voice" && audio.ExtractText(b.Audio) == "hi mum" {
				return true
			}
		}
		return false
	})
	for _, b := range h.fake.Conversation(10) {
		if b.Kind == "voice" {
			t.Fatal("voice message also went to Dad")
		}
	}
	eventually(t, "marked sent", func() bool {
		list, _ := h.app.Store.RecentMessages(ctx, 5)
		return len(list) > 0 && list[0].Direction == "out" && list[0].Status == "sent"
	})
}

func TestTalkWithoutChatsReportsAnError(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	if _, err := h.app.Actions.Execute(h.ctx, device(), "talk", nil); err == nil {
		t.Fatal("talk without a favorite chat must fail")
	}
	if !h.played("error") {
		t.Fatal("expected an error tone")
	}
}

func TestSendMessageActionResolvesByNameAndRejectsUnknown(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	dad := h.addUser(10, "Dad", "PARENT")
	h.app.Store.CreateChat(h.ctx, store.Chat{Name: "Dad", Kind: "user", UserID: dad, TelegramChatID: 10, Favorite: true, Enabled: true})
	assistant := actions.Actor{Kind: actions.ActorAssistant}

	if _, err := h.app.Actions.Execute(h.ctx, assistant, "send_message", map[string]any{"chat": " dad ", "text": "I finished my homework"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "text delivered", func() bool { return h.botSaid(10, "I finished my homework") })

	_, err := h.app.Actions.Execute(h.ctx, assistant, "send_message", map[string]any{"chat": "Stranger Danger", "text": "hi"})
	if err == nil || !strings.Contains(err.Error(), "available chats: Dad") {
		t.Fatalf("unknown chat err = %v", err)
	}
}

func TestOutgoingToDeactivatedRecipientIsNeverSent(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	dad := h.addUser(10, "Dad", "PARENT")
	h.app.Store.CreateChat(h.ctx, store.Chat{Name: "Dad", Kind: "user", UserID: dad, TelegramChatID: 10, Favorite: true, Enabled: true})
	h.app.Store.UpdateUser(h.ctx, dad, "Dad", "PARENT", false)
	h.app.Actions.Execute(h.ctx, actions.Actor{Kind: actions.ActorAssistant}, "send_message", map[string]any{"chat": "Dad", "text": "hello"})
	eventually(t, "marked failed", func() bool {
		list, _ := h.app.Store.RecentMessages(h.ctx, 5)
		return len(list) > 0 && list[0].Status == "failed"
	})
	for _, b := range h.fake.Conversation(10) {
		if b.Text == "hello" {
			t.Fatal("message sent to a deactivated user")
		}
	}
}

// ---------------------------------------------------------------- resilience and secrets

func TestOutboxRetriesAfterOutage(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	dad := h.addUser(10, "Dad", "PARENT")
	h.app.Store.CreateChat(h.ctx, store.Chat{Name: "Dad", Kind: "user", UserID: dad, TelegramChatID: 10, Favorite: true, Enabled: true})

	h.fake.SetFailing(true)
	h.app.Actions.Execute(h.ctx, actions.Actor{Kind: actions.ActorAssistant}, "send_message", map[string]any{"chat": "Dad", "text": "are you there"})
	eventually(t, "a failed attempt", func() bool {
		list, _ := h.app.Store.RecentMessages(h.ctx, 5)
		return len(list) > 0 && list[0].Attempts >= 1 && list[0].Status == "queued"
	})
	h.fake.SetFailing(false)
	eventually(t, "delivery after recovery", func() bool { return h.botSaid(10, "are you there") })
	eventually(t, "sent", func() bool {
		list, _ := h.app.Store.RecentMessages(h.ctx, 5)
		return list[0].Status == "sent"
	})
}

func TestStartRequiresValidTokenButToleratesOffline(t *testing.T) {
	h := newHarness(t, false)
	if err := h.app.Features.SetEnabled(h.ctx, telegram.FeatureName, true); err != nil {
		t.Fatal(err)
	}
	list, _ := h.app.Features.List(h.ctx)
	if list[0].Running || !strings.Contains(list[0].LastError, "no Telegram bot token") {
		t.Fatalf("without a token: %+v", list)
	}

	h.app.Vault.Put(h.ctx, telegram.SecretBotToken, []byte("999:wrong-token-wrong-token-wrong-token"))
	h.app.Features.Restart(h.ctx, telegram.FeatureName)
	list, _ = h.app.Features.List(h.ctx)
	if list[0].Running || !strings.Contains(list[0].LastError, "rejected the bot token") {
		t.Fatalf("with a wrong token: %+v", list)
	}

	h.fake.SetFailing(true) // Telegram unreachable at boot must not stop the device
	h.app.Vault.Put(h.ctx, telegram.SecretBotToken, []byte(token))
	h.app.Features.Restart(h.ctx, telegram.FeatureName)
	list, _ = h.app.Features.List(h.ctx)
	if !list[0].Running {
		t.Fatalf("offline start must still run: %+v", list)
	}
	h.fake.SetFailing(false)
	h.addUser(10, "Dad", "PARENT")
	h.fake.Send(10, "back online")
	eventually(t, "recovered after outage", func() bool { total, _, _ := h.app.Store.PendingCounts(h.ctx); return total == 1 })
}

func TestTokenNeverAppearsInLogsOrDatabase(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.fake.Send(900, "/start")
	eventually(t, "activity", func() bool { return h.botSaid(900, "not authorized") })
	h.fake.SetFailing(true)
	h.fake.Send(900, "again")
	time.Sleep(1500 * time.Millisecond) // provoke poll errors
	h.app.Features.SetEnabled(h.ctx, telegram.FeatureName, false)

	if strings.Contains(h.logs.String(), "TEST-TOKEN") {
		t.Fatalf("bot token leaked into the logs:\n%s", h.logs.String())
	}
	raw, _ := h.app.Store.GetSecret(h.ctx, telegram.SecretBotToken)
	if bytes.Contains(raw, []byte("TEST-TOKEN")) {
		t.Fatal("bot token stored in plaintext")
	}
	entries, _ := h.app.Store.ListAudit(h.ctx, 100)
	for _, e := range entries {
		if strings.Contains(e.Details, "TEST-TOKEN") {
			t.Fatalf("token in audit trail: %+v", e)
		}
	}
}

func TestDisabledFeatureDoesNotContactTelegram(t *testing.T) {
	h := newHarness(t, true)
	h.fake.Send(10, "hello")
	time.Sleep(300 * time.Millisecond)
	if st := h.app.Telegram.Status(h.ctx); st.Running {
		t.Fatal("telegram is running while disabled")
	}
	if len(h.fake.Conversation(10)) != 1 { // only the user's own bubble: the bot never answered
		t.Fatalf("bot answered while the feature was disabled: %+v", h.fake.Conversation(10))
	}
}

func TestValidInvitationWorksRightAfterStrangerChatter(t *testing.T) {
	h := newHarness(t, true)
	h.app.Telegram.SetStrangerCooldown(time.Hour) // replies to chatter are throttled...
	h.enable()
	h.fake.AddPerson(telegramtest.Person{ID: 700, FirstName: "Sam"})
	h.fake.Send(700, "/start")
	eventually(t, "first refusal", func() bool { return h.botSaid(700, "not authorized") })
	// ...but a real invitation must still be honoured immediately.
	h.fake.Send(700, "/start "+h.invite("FAMILY"))
	eventually(t, "request accepted", func() bool { return h.botSaid(700, "sent to the family admin") })
	if pend, _ := h.app.Store.ListPending(h.ctx); len(pend) != 1 {
		t.Fatalf("pending = %+v", pend)
	}
}
