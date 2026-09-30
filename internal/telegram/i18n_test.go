package telegram_test

import (
	"testing"

	"github.com/juancer/milo/internal/i18n"
	"github.com/juancer/milo/internal/telegram/telegramtest"
)

// Strangers are answered in the notification language; nothing is hardcoded in English.
func TestStrangerIsAnsweredInTheNotificationLanguage(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	if err := h.app.Store.SetSetting(h.ctx, i18n.KeyNotification, "es"); err != nil {
		t.Fatal(err)
	}
	h.fake.AddPerson(telegramtest.Person{ID: 900, FirstName: "Stranger"})
	h.fake.Send(900, "hello")
	eventually(t, "Spanish refusal", func() bool {
		return h.botSaid(900, "No tienes autorización para utilizar este dispositivo.")
	})
}

// Each person is written to in their own language, falling back to the notification language.
func TestPeopleAreAnsweredInTheirOwnLanguage(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	dad := h.addUser(10, "Dad", "PARENT")
	mum := h.addUser(11, "Mum", "PARENT")
	h.addUser(12, "Gran", "PARENT")
	if err := h.app.Store.SetUserLanguage(h.ctx, dad, "en"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.Store.SetUserLanguage(h.ctx, mum, "es"); err != nil {
		t.Fatal(err)
	}
	h.app.Store.SetSetting(h.ctx, i18n.KeyNotification, "es") // Gran has no language: follows this

	h.tapEmergency(10)
	h.tapEmergency(11)
	h.tapEmergency(12)
	eventually(t, "English confirmation", func() bool { return h.botSaid(10, "Send emergency alert?") })
	eventually(t, "Spanish confirmation", func() bool { return h.botSaid(11, "¿Enviar alerta de emergencia?") })
	eventually(t, "default-language confirmation", func() bool { return h.botSaid(12, "¿Enviar alerta de emergencia?") })
}
