package assistant_test

import (
	"strings"
	"testing"
	"time"

	"github.com/juancer/milo/internal/assistant"
	"github.com/juancer/milo/internal/store"
)

func composeHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.app.Features.SetEnabled(h.ctx, "telegram", true)
	uid, err := h.app.Store.CreateTelegramUser(h.ctx, "Papi", "PARENT", 77, "papi")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Store.CreateChat(h.ctx, store.Chat{Name: "Papi", Kind: "user", UserID: uid, TelegramChatID: 77, Favorite: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) spoke(before int, want string) bool {
	for _, p := range h.app.MockAudio.Played()[before:] {
		if p.Kind == "audio" && strings.Contains(p.Label, want) {
			return true
		}
	}
	return false
}

// "Send a message to Papi" with no message: Milo asks text or voice, and a text message is dictated and queued.
func TestComposeTextMessageByVoice(t *testing.T) {
	h := composeHarness(t)
	h.app.MockAudio.SetUtterance("Mándale un mensaje a Papi")
	h.app.MockAudio.QueueUtterances("de texto", "ya he terminado los deberes")
	before := len(h.app.MockAudio.Played())
	if err := h.app.Assistant.RunVoice(h.ctx, "test"); err != nil {
		t.Fatal(err)
	}
	if h.llm.calls() != 0 {
		t.Errorf("the model was asked %d times", h.llm.calls())
	}
	if !h.spoke(before, "texto o de voz") {
		t.Errorf("Milo did not ask text or voice: %+v", h.app.MockAudio.Played()[before:])
	}
	m, err := h.app.Store.NextQueued(h.ctx, time.Now().Add(time.Hour))
	if err != nil || m.Kind != "text" || m.Body != "ya he terminado los deberes" {
		t.Fatalf("queued = %+v, %v", m, err)
	}
	if !h.spoke(before, "mensaje enviado a Papi") {
		t.Errorf("no confirmation: %+v", h.app.MockAudio.Played()[before:])
	}
}

// Asking for a voice note records one and queues it, with no model involved.
func TestComposeVoiceMessageByVoice(t *testing.T) {
	h := composeHarness(t)
	h.app.MockAudio.SetUtterance("envía un mensaje a papi")
	h.app.MockAudio.QueueUtterances("de voz")
	before := len(h.app.MockAudio.Played())
	if err := h.app.Assistant.RunVoice(h.ctx, "test"); err != nil {
		t.Fatal(err)
	}
	m, err := h.app.Store.NextQueued(h.ctx, time.Now().Add(time.Hour))
	if err != nil || m.Kind != "voice" || len(m.Audio) == 0 {
		t.Fatalf("queued = %+v, %v", m, err)
	}
	if !h.spoke(before, "nota de voz enviada a Papi") {
		t.Errorf("no confirmation: %+v", h.app.MockAudio.Played()[before:])
	}
}

// The kind can already be in the request, and nothing is asked then.
func TestComposeKindInTheRequestIsNotAskedAgain(t *testing.T) {
	h := composeHarness(t)
	h.app.MockAudio.SetUtterance("manda un mensaje de texto a papi")
	h.app.MockAudio.QueueUtterances("buenas noches")
	before := len(h.app.MockAudio.Played())
	if err := h.app.Assistant.RunVoice(h.ctx, "test"); err != nil {
		t.Fatal(err)
	}
	if h.spoke(before, "texto o de voz") {
		t.Error("asked something that was already said")
	}
	if m, err := h.app.Store.NextQueued(h.ctx, time.Now().Add(time.Hour)); err != nil || m.Body != "buenas noches" {
		t.Fatalf("queued = %+v, %v", m, err)
	}
}

// A request that already has its message goes to the model, which sends the text itself.
func TestRequestWithTheMessageGoesToTheModel(t *testing.T) {
	h := composeHarness(t)
	h.llm.replies = []assistant.Message{say("Listo")}
	h.app.MockAudio.SetUtterance("manda un mensaje a papi que llego tarde")
	if err := h.app.Assistant.RunVoice(h.ctx, "test"); err != nil {
		t.Fatal(err)
	}
	if h.llm.calls() != 1 {
		t.Errorf("the model was asked %d times, want 1", h.llm.calls())
	}
}

// Nobody answers the question: nothing is sent.
func TestComposeNothingSentWhenNobodyAnswers(t *testing.T) {
	h := composeHarness(t)
	h.app.MockAudio.SetUtterance("manda un mensaje a papi")
	if err := h.app.Assistant.RunVoice(h.ctx, "test"); err != nil {
		t.Fatal(err)
	}
	if m, err := h.app.Store.NextQueued(h.ctx, time.Now().Add(time.Hour)); err == nil {
		t.Fatalf("a message was queued: %+v", m)
	}
}
