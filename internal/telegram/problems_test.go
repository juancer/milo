package telegram_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/juancer/milo/internal/permissions"
	"github.com/juancer/milo/internal/telegram"
)

// A made-up token tail, assembled at run time so no secret scanner mistakes the file for a leak.
var fakeTokenTail = "AAH" + strings.Repeat("x", 32)

func problemHarness(t *testing.T, cfg telegram.ErrorAlertSettings) (*harness, int64) {
	t.Helper()
	h, id, tg := adminHarness(t, permissions.ManageConfiguration)
	if err := h.app.Store.SetUserDeviceAlerts(h.ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Telegram.SaveErrorAlertSettings(h.ctx, cfg); err != nil {
		t.Fatal(err)
	}
	return h, tg
}

func (h *harness) quietFor(chat int64, text string) bool {
	time.Sleep(250 * time.Millisecond)
	return !h.botSaid(chat, text)
}

var sampleLine = `12:00:00 ERROR spotify: cannot reach the receiver url=https://example.org/a?token=abc error=Get "https://api.telegram.org/bot123456789:` + fakeTokenTail + `/getUpdates": dial tcp 10.0.0.1:443: i/o timeout key=123456789:` + fakeTokenTail

func TestProblemsAreOffByDefault(t *testing.T) {
	h, id, tg := adminHarness(t, permissions.ManageConfiguration)
	h.app.Store.SetUserDeviceAlerts(h.ctx, id, true)
	h.app.Telegram.AlertProblem(sampleLine)
	if !h.quietFor(tg, "spotify") {
		t.Fatal("a problem was sent although the panel never switched it on")
	}
}

func TestProblemDetailFollowsThePanel(t *testing.T) {
	for _, c := range []struct {
		detail            string
		want, mustNotHave []string
	}{
		{telegram.ErrorDetailNone, []string{"ERROR: spotify: cannot reach the receiver"}, []string{"example.org", "i/o timeout", "123456789"}},
		{telegram.ErrorDetailSafe, []string{"[url]", "[censored]"}, []string{"example.org", fakeTokenTail, "api.telegram.org"}},
		{telegram.ErrorDetailFull, []string{"example.org", "123456789:" + fakeTokenTail, "i/o timeout"}, nil},
	} {
		h, tg := problemHarness(t, telegram.ErrorAlertSettings{Enabled: true, Level: telegram.ErrorLevelError, Detail: c.detail})
		h.app.Telegram.AlertProblem(sampleLine)
		eventually(t, "the problem ("+c.detail+")", func() bool { return h.botSaid(tg, "spotify") })
		var got string
		for _, b := range h.fake.Conversation(tg) {
			if b.From == "bot" && strings.Contains(b.Text, "spotify") {
				got = b.Text
			}
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q does not contain %q", c.detail, got, w)
			}
		}
		for _, w := range c.mustNotHave {
			if strings.Contains(got, w) {
				t.Errorf("%s: %q leaks %q", c.detail, got, w)
			}
		}
	}
}

func TestProblemLevelAndOwnTrouble(t *testing.T) {
	h, tg := problemHarness(t, telegram.ErrorAlertSettings{Enabled: true, Level: telegram.ErrorLevelError, Detail: telegram.ErrorDetailNone})
	h.app.Telegram.AlertProblem("12:00:00 WARN  warning-only thing happened")
	if !h.quietFor(tg, "warning-only") {
		t.Fatal("a warning was sent with the level set to errors only")
	}
	h.app.Telegram.AlertProblem("12:00:00 ERROR telegram: cannot send the alert")
	if !h.quietFor(tg, "cannot send the alert") {
		t.Fatal("Telegram's own trouble was sent through Telegram")
	}
	h.app.Telegram.SaveErrorAlertSettings(h.ctx, telegram.ErrorAlertSettings{Enabled: true, Level: telegram.ErrorLevelWarn, Detail: telegram.ErrorDetailNone})
	h.app.Telegram.AlertProblem("12:00:00 WARN  warning-only thing happened")
	eventually(t, "the warning", func() bool { return h.botSaid(tg, "warning-only") })
}

func TestProblemsAreNotRepeatedAndAreCapped(t *testing.T) {
	h, tg := problemHarness(t, telegram.ErrorAlertSettings{Enabled: true, Level: telegram.ErrorLevelError, Detail: telegram.ErrorDetailNone})
	for i := 0; i < 3; i++ {
		h.app.Telegram.AlertProblem("12:00:00 ERROR the same message")
	}
	eventually(t, "the first", func() bool { return h.botSaid(tg, "the same message") })
	time.Sleep(250 * time.Millisecond)
	n := 0
	for _, b := range h.fake.Conversation(tg) {
		if b.From == "bot" && strings.Contains(b.Text, "the same message") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the same message was sent %d times", n)
	}
	for i := 0; i < 10; i++ {
		h.app.Telegram.AlertProblem(fmt.Sprintf("12:00:00 ERROR distinct problem number %d", i))
	}
	time.Sleep(500 * time.Millisecond)
	total := 0
	for _, b := range h.fake.Conversation(tg) {
		if b.From == "bot" && strings.Contains(b.Text, "ERROR:") {
			total++
		}
	}
	if total > 6 {
		t.Fatalf("%d problems went out in one window, the cap is 6", total)
	}
}

func TestProblemsOnlyGoToPeopleWhoOptedIn(t *testing.T) {
	h, tg := problemHarness(t, telegram.ErrorAlertSettings{Enabled: true, Level: telegram.ErrorLevelError, Detail: telegram.ErrorDetailNone})
	h.addUser(701, "Mum", "PARENT")
	h.app.Perms.Set(h.ctx, 2, permissions.ManageConfiguration, true)
	h.app.Telegram.AlertProblem("12:00:00 ERROR only for the opted in")
	eventually(t, "the problem", func() bool { return h.botSaid(tg, "only for the opted in") })
	if len(h.fake.Conversation(701)) != 0 {
		t.Fatal("a problem went to someone who did not opt in")
	}
}
