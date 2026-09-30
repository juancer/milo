package telegram_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/juancer/milo/internal/permissions"
	"github.com/juancer/milo/internal/store"
	"github.com/juancer/milo/internal/telegram/telegramtest"
)

// adminHarness is a running bot with one person who holds the given permissions.
func adminHarness(t *testing.T, perms ...permissions.Permission) (*harness, int64, int64) {
	t.Helper()
	h := newHarness(t, true)
	h.app.Telegram.SetAdminTapGap(time.Millisecond)
	h.enable()
	const tg = 700
	id := h.addUser(tg, "Dad", "PARENT")
	for _, p := range perms {
		if err := h.app.Perms.Set(h.ctx, id, p, true); err != nil {
			t.Fatal(err)
		}
	}
	return h, id, tg
}

func (h *harness) hasButton(chat int64, data string) bool {
	for _, b := range h.fake.Conversation(chat) {
		for _, row := range b.Buttons {
			for _, btn := range row {
				if btn.Data == data {
					return true
				}
			}
		}
	}
	return false
}

func (h *harness) answered(contains string) bool {
	for _, a := range h.fake.Answers() {
		if strings.Contains(a, contains) {
			return true
		}
	}
	return false
}

// press sends a button tap and waits until the bot has handled it (every tap is answered once).
func (h *harness) press(from int64, data string) {
	h.t.Helper()
	before := len(h.fake.Answers())
	h.fake.Press(from, data)
	eventually(h.t, "answer to "+data, func() bool { return len(h.fake.Answers()) > before })
}

func (h *harness) volume() int { return h.app.MockAudio.Status(h.ctx).Volume }

// ---------------------------------------------------------------- menu and permissions

func TestAdminButtonOnlyForPeopleWhoCanAdminister(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	const tg = 700
	h.addUser(tg, "Gran", "FAMILY") // a FAMILY member has no administration permission by default
	h.fake.Send(tg, "/menu")
	eventually(t, "menu", func() bool { return h.botSaid(tg, "Hi Gran") })
	if h.hasButton(tg, "adm:home") {
		t.Fatal("the Administration button is shown to someone without permission")
	}

	h2, _, tg2 := adminHarness(t, permissions.ManageConfiguration)
	h2.fake.Send(tg2, "/menu")
	eventually(t, "menu with admin", func() bool { return h2.hasButton(tg2, "adm:home") })
}

func TestEveryAdminTapRechecksPermission(t *testing.T) {
	h, id, tg := adminHarness(t, permissions.ManageConfiguration)
	h.app.MockAudio.SetVolume(h.ctx, 50)

	h.press(tg, "adm:vu")
	if h.volume() != 60 {
		t.Fatalf("volume = %d after +, want 60", h.volume())
	}

	// The permission is taken away while an old keyboard is still on screen.
	h.app.Perms.Set(h.ctx, id, permissions.ManageConfiguration, false)
	h.press(tg, "adm:vu")
	if h.volume() != 60 {
		t.Fatalf("volume changed to %d after the permission was removed", h.volume())
	}
	if !h.answered("permission") {
		t.Fatalf("the refused tap was not explained: %v", h.fake.Answers())
	}

	// A deactivated person holds nothing at all, and a stranger's forged button does nothing.
	h.app.Perms.Set(h.ctx, id, permissions.ManageConfiguration, true)
	h.app.Store.UpdateUser(h.ctx, id, "Dad", "PARENT", false)
	h.press(tg, "adm:vu")
	h.fake.AddPerson(telegramtest.Person{ID: 901, FirstName: "Stranger"})
	h.press(901, "adm:vu")
	if h.volume() != 60 {
		t.Fatalf("volume = %d: a deactivated person or a stranger changed it", h.volume())
	}
}

func TestAdminSectionsNeedTheirOwnPermission(t *testing.T) {
	// Managing features does not let someone approve people or change the volume.
	h, _, tg := adminHarness(t, permissions.ManageFeatures)
	h.app.MockAudio.SetVolume(h.ctx, 50)
	h.press(tg, "adm:vu")
	h.press(tg, "adm:rq")
	if h.volume() != 50 {
		t.Fatal("volume changed without manage_configuration")
	}
	for _, b := range h.fake.Conversation(tg) {
		if strings.Contains(b.Text, "access request") {
			t.Fatal("the access request list was shown without manage_users")
		}
	}
}

func TestAdminTapsAreRateLimited(t *testing.T) {
	h, _, tg := adminHarness(t, permissions.ManageConfiguration)
	h.app.Telegram.SetAdminTapGap(time.Hour)
	h.app.MockAudio.SetVolume(h.ctx, 50)
	h.press(tg, "adm:vu")
	h.press(tg, "adm:vu") // too soon: ignored
	if h.volume() != 60 {
		t.Fatalf("volume = %d, want 60 (second tap ignored)", h.volume())
	}
}

func TestStaleOrUnknownAdminButtonsAreHarmless(t *testing.T) {
	h, _, tg := adminHarness(t, permissions.ManageConfiguration, permissions.ManageFeatures, permissions.ManageUsers)
	for _, data := range []string{"adm:zzz", "adm:", "adm:ap:notanumber", "adm:ap:99999", "adm:fx:nope", "adm:fx:telegram", "adm:sw:nope"} {
		h.press(tg, data)
	}
	if on, _ := h.app.Features.Enabled(h.ctx, "telegram"); !on {
		t.Fatal("Telegram was switched off from Telegram")
	}
}

// ---------------------------------------------------------------- access requests

func TestAccessRequestArrivesWithButtonsAndCanBeApprovedOnce(t *testing.T) {
	h, _, tg := adminHarness(t, permissions.ManageUsers)
	h.fake.AddPerson(telegramtest.Person{ID: 500, FirstName: "Mami", Username: "mami"})
	h.fake.Send(500, "/start "+h.invite("FAMILY"))

	eventually(t, "request pushed to the administrator", func() bool { return h.botSaid(tg, "Mami") })
	pend, _ := h.app.Store.ListPending(h.ctx)
	if len(pend) != 1 {
		t.Fatalf("pending = %+v", pend)
	}
	approve := "adm:ap:" + itoa(pend[0].ID)
	if !h.hasButton(tg, approve) || !h.hasButton(tg, "adm:rj:"+itoa(pend[0].ID)) {
		t.Fatal("the notification has no Approve / Reject buttons")
	}

	h.press(tg, approve)
	u, err := h.app.Store.GetUserByTelegramID(h.ctx, 500)
	if err != nil || !u.Active || u.DisplayName != "Mami" {
		t.Fatalf("approved user = %+v, %v", u, err)
	}
	eventually(t, "the person is told", func() bool { return h.botSaid(500, "approved") })

	h.press(tg, approve) // a second tap on the same button
	if !h.answered("already decided") {
		t.Fatalf("a stale approval was not explained: %v", h.fake.Answers())
	}
	if users, _ := h.app.Store.ListTelegramUsers(h.ctx); len(users) != 2 {
		t.Fatalf("users = %d, want 2 (no duplicate)", len(users))
	}
}

func TestAccessRequestCanBeRejectedAndOnlyTheRightPeopleAreAsked(t *testing.T) {
	h, _, tg := adminHarness(t, permissions.ManageUsers)
	h.addUser(701, "Gran", "FAMILY") // holds no permission to approve people
	h.fake.AddPerson(telegramtest.Person{ID: 500, FirstName: "Mami", Username: "mami"})
	h.fake.Send(500, "/start "+h.invite("FAMILY"))
	eventually(t, "request pushed", func() bool { return h.botSaid(tg, "Mami") })
	if len(h.fake.Conversation(701)) != 0 {
		t.Fatal("someone without manage_users was sent the request")
	}
	pend, _ := h.app.Store.ListPending(h.ctx)
	h.press(tg, "adm:rj:"+itoa(pend[0].ID))
	if _, err := h.app.Store.GetUserByTelegramID(h.ctx, 500); err == nil {
		t.Fatal("a rejected person became a user")
	}
	eventually(t, "the person is told", func() bool { return h.botSaid(500, "declined") })
}

// ---------------------------------------------------------------- features and schedules

func TestFeatureToggleAndNeverARawError(t *testing.T) {
	h, _, tg := adminHarness(t, permissions.ManageFeatures)

	h.press(tg, "adm:fx:timer")
	if on, _ := h.app.Features.Enabled(h.ctx, "timer"); !on {
		t.Fatal("timer was not turned on")
	}
	h.press(tg, "adm:fx:timer")
	if on, _ := h.app.Features.Enabled(h.ctx, "timer"); on {
		t.Fatal("timer was not turned off")
	}

	// The assistant cannot start without its key: it stays off and the person is told what to do.
	h.press(tg, "adm:fx:assistant")
	if on, _ := h.app.Features.Enabled(h.ctx, "assistant"); on {
		t.Fatal("an unconfigured feature was left switched on")
	}
	if !h.answered("needs to be set up") {
		t.Fatalf("no friendly message: %v", h.fake.Answers())
	}
	for _, a := range h.fake.Answers() {
		if strings.Contains(a, "API key") || strings.Contains(a, "error") {
			t.Fatalf("a raw error reached the person: %q", a)
		}
	}
}

func TestScheduleSwitchKeepsWindowsAndRefusesEmptyHours(t *testing.T) {
	h, _, tg := adminHarness(t, permissions.ManageSchedules)

	h.press(tg, "adm:sw:timer") // no hours defined: switching on would shut the feature for good
	if on, _ := h.app.Store.ScheduleEnabled(h.ctx, "timer"); on {
		t.Fatal("hours were switched on with no window")
	}
	if !h.answered("no usage hours") {
		t.Fatalf("not explained: %v", h.fake.Answers())
	}

	rules := []store.ScheduleRule{{Feature: "timer", DayOfWeek: 1, StartMinute: 8 * 60, EndMinute: 20 * 60, Enabled: true}}
	if err := h.app.Store.SaveSchedule(h.ctx, "timer", false, rules); err != nil {
		t.Fatal(err)
	}
	h.press(tg, "adm:sw:timer")
	if on, _ := h.app.Store.ScheduleEnabled(h.ctx, "timer"); !on {
		t.Fatal("hours were not switched on")
	}
	if got, _ := h.app.Store.ListScheduleRules(h.ctx, "timer"); len(got) != 1 {
		t.Fatalf("windows = %+v, want them kept", got)
	}
}

func TestAdminActionsAreAudited(t *testing.T) {
	h, _, tg := adminHarness(t, permissions.ManageFeatures)
	h.press(tg, "adm:fx:timer")
	entries, _ := h.app.Store.ListAudit(h.ctx, 20)
	for _, e := range entries {
		if e.Action == "feature.enabled" && e.Target == "timer" && e.Actor == "telegram:Dad" {
			return
		}
	}
	t.Fatalf("no audit entry for the feature change: %+v", entries)
}

// ---------------------------------------------------------------- alerts

func TestDeviceAlertsAreOptIn(t *testing.T) {
	h, id, tg := adminHarness(t, permissions.ManageConfiguration)
	h.addUser(701, "Mum", "PARENT")
	h.app.Perms.Set(h.ctx, 2, permissions.ManageConfiguration, true)

	// Off by default: a failing feature tells nobody.
	h.app.Features.SetEnabled(h.ctx, "assistant", true)
	time.Sleep(200 * time.Millisecond)
	if h.botSaid(tg, "could not start") {
		t.Fatal("an alert was sent to someone who did not opt in")
	}
	h.app.Features.SetEnabled(h.ctx, "assistant", false)

	h.press(tg, "adm:al")
	if u, _ := h.app.Store.GetUser(h.ctx, id); !u.DeviceAlerts {
		t.Fatal("alerts were not switched on")
	}
	h.app.Features.SetEnabled(h.ctx, "assistant", true)
	eventually(t, "the alert", func() bool { return h.botSaid(tg, "assistant feature could not start") })
	if len(h.fake.Conversation(701)) != 0 {
		t.Fatal("the alert went to someone who did not opt in")
	}
	for _, b := range h.fake.Conversation(tg) {
		if strings.Contains(b.Text, "API key") {
			t.Fatalf("the alert leaked an error text: %q", b.Text)
		}
	}
}

func TestSwitchingOnAFeatureFromTheMenuDoesNotAlsoAlert(t *testing.T) {
	h, id, tg := adminHarness(t, permissions.ManageConfiguration, permissions.ManageFeatures)
	h.app.Store.SetUserDeviceAlerts(h.ctx, id, true)
	h.press(tg, "adm:fx:assistant")
	time.Sleep(200 * time.Millisecond)
	if h.botSaid(tg, "could not start") {
		t.Fatal("the person who tried to switch it on was also sent a failure alert")
	}
}

// ---------------------------------------------------------------- copies of incoming messages

func TestCopiesAreOptInAndOnlyForPeopleWhoCanManageUsers(t *testing.T) {
	h, id, tg := adminHarness(t, permissions.ManageUsers)
	h.addUser(701, "Gran", "FAMILY")

	// Off by default.
	h.fake.Send(701, "hello from gran")
	eventually(t, "delivery", func() bool { return h.botSaid(701, "Delivered") })
	if h.botSaid(tg, "hello from gran") {
		t.Fatal("a copy was sent without opt-in")
	}

	// The button switches it on, then a text and a voice note are copied.
	h.press(tg, "adm:fw")
	if u, _ := h.app.Store.GetUser(h.ctx, id); !u.ForwardIncoming {
		t.Fatal("copies were not switched on")
	}
	h.fake.Send(701, "second hello")
	eventually(t, "text copy", func() bool { return h.botSaid(tg, "Gran") && h.botSaid(tg, "second hello") })
	h.fake.SendVoice(701, []byte("OggS-voice-bytes"), 3)
	eventually(t, "voice copy", func() bool {
		for _, b := range h.fake.Conversation(tg) {
			if b.Kind == "voice" && strings.Contains(b.Caption, "Gran") && string(b.Audio) == "OggS-voice-bytes" {
				return true
			}
		}
		return false
	})

	// Copies stop at once when the permission is removed, without touching the stored flag.
	h.app.Perms.Set(h.ctx, id, permissions.ManageUsers, false)
	n := len(h.fake.Conversation(tg))
	h.fake.Send(701, "third hello")
	eventually(t, "third delivered", func() bool {
		return h.botSaid(701, "Delivered") && countBot(h.fake.Conversation(701), "Delivered") >= 3
	})
	time.Sleep(200 * time.Millisecond)
	if got := len(h.fake.Conversation(tg)); got != n {
		t.Fatalf("a copy was sent after the permission was removed (%d -> %d bubbles)", n, got)
	}
}

func countBot(bs []telegramtest.Bubble, contains string) int {
	n := 0
	for _, b := range bs {
		if b.From == "bot" && strings.Contains(b.Text, contains) {
			n++
		}
	}
	return n
}

func TestNobodyGetsACopyOfTheirOwnMessage(t *testing.T) {
	h, id, tg := adminHarness(t, permissions.ManageUsers, permissions.SendMessage)
	h.app.Store.SetUserForwardIncoming(h.ctx, id, true)
	h.fake.Send(tg, "note to self")
	eventually(t, "delivery", func() bool { return h.botSaid(tg, "Delivered") })
	time.Sleep(200 * time.Millisecond)
	for _, b := range h.fake.Conversation(tg) {
		if b.From == "bot" && strings.Contains(b.Text, "note to self") {
			t.Fatalf("the sender was sent a copy of their own message: %q", b.Text)
		}
	}
}

func TestAFailingCopyNeverBlocksDelivery(t *testing.T) {
	h, id, tg := adminHarness(t, permissions.ManageUsers)
	h.addUser(701, "Gran", "FAMILY")
	h.app.Store.SetUserForwardIncoming(h.ctx, id, true)
	h.failChat.Store(tg) // every call to the administrator fails

	h.fake.Send(701, "hello despite the failure")
	eventually(t, "delivery receipt to the sender", func() bool { return h.botSaid(701, "Delivered") })
	if total, _, _ := h.app.Store.PendingCounts(h.ctx); total != 1 {
		t.Fatalf("pending messages = %d, want the message stored for the child", total)
	}
}

func TestPeopleCannotTurnOnCopiesWithoutPermissionFromTheMenu(t *testing.T) {
	h, id, tg := adminHarness(t, permissions.ManageConfiguration) // can open "mine", but not manage users
	h.press(tg, "adm:fw")
	if u, _ := h.app.Store.GetUser(h.ctx, id); u.ForwardIncoming {
		t.Fatal("copies were switched on without manage_users")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestStartAlertGoesOnlyToPeopleWhoOptedIn(t *testing.T) {
	h := newHarness(t, true)
	on := h.addUser(700, "Dad", "PARENT")
	h.addUser(701, "Mum", "PARENT")
	h.app.Perms.Set(h.ctx, on, permissions.ManageConfiguration, true)
	h.app.Store.SetUserDeviceAlerts(h.ctx, on, true)
	h.enable()
	eventually(t, "start alert", func() bool { return h.botSaid(700, "Milo has started") })
	time.Sleep(100 * time.Millisecond)
	if len(h.fake.Conversation(701)) != 0 {
		t.Fatal("the start alert went to someone who did not opt in")
	}
}
