package assistant_test

import (
	"strings"
	"testing"

	"github.com/juancer/milo/internal/assistant"
)

// A recognised command is carried out without the model: no request is sent to it, the action runs
// as the assistant, and the conversation keeps a record of it.
func TestVoiceCommandSkipsTheModel(t *testing.T) {
	h := newHarness(t)
	h.app.Audio.SetVolume(h.ctx, 40) //nolint:errcheck
	h.app.MockAudio.SetUtterance("Oye Milo, sube el volumen")
	if err := h.app.Assistant.RunVoice(h.ctx, "test"); err != nil {
		t.Fatal(err)
	}
	if h.llm.calls() != 0 {
		t.Errorf("the model was asked %d times for a plain command", h.llm.calls())
	}
	if got := h.app.Audio.Status(h.ctx).Volume; got != 50 {
		t.Errorf("volume = %d, want 50", got)
	}
}

// What the router does not recognise still goes to the model.
func TestUnknownRequestStillGoesToTheModel(t *testing.T) {
	h := newHarness(t)
	h.llm.replies = []assistant.Message{say("Once upon a time…")}
	h.app.MockAudio.SetUtterance("cuéntame un cuento de dragones")
	if err := h.app.Assistant.RunVoice(h.ctx, "test"); err != nil {
		t.Fatal(err)
	}
	if h.llm.calls() != 1 {
		t.Errorf("the model was asked %d times, want 1", h.llm.calls())
	}
}

// A question with a short answer is worded locally, in the language it was asked in, and spoken.
func TestVoiceQuestionIsAnsweredWithoutTheModel(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct{ asked, want string }{
		{"¿Qué hora es?", "Son las"},
		{"¿Cuánto es 4 por 2?", "4 por 2 es 8."},
		{"What is 3 plus 4", "3 plus 4 is 7."},
		{"¿Qué día es hoy?", "Hoy es"},
	} {
		h.app.MockAudio.SetUtterance(c.asked)
		before := len(h.app.MockAudio.Played())
		if err := h.app.Assistant.RunVoice(h.ctx, "test"); err != nil {
			t.Fatal(err)
		}
		if h.llm.calls() != 0 {
			t.Fatalf("%q reached the model", c.asked)
		}
		spoke := false
		for _, p := range h.app.MockAudio.Played()[before:] {
			if p.Kind == "audio" && strings.Contains(p.Label, c.want) {
				spoke = true
			}
		}
		if !spoke {
			t.Errorf("%q: nothing containing %q was spoken: %+v", c.asked, c.want, h.app.MockAudio.Played()[before:])
		}
	}
}

// After a command or a short answer the conversation ends: the microphone is not opened again for a
// follow-up, even with follow-up listening switched on.
func TestACommandOrAnswerEndsTheExchange(t *testing.T) {
	h := newHarness(t)
	s, _ := h.app.Store.GetGPTSettings(h.ctx)
	s.FollowUp = true
	h.app.Store.UpdateGPTSettings(h.ctx, s)
	for _, said := range []string{"¿Cuánto es 6 por 7?", "sube el volumen", "adiós", "cállate"} {
		h.app.MockAudio.SetUtterance(said)
		before := len(h.app.MockAudio.Played())
		if err := h.app.Assistant.RunVoice(h.ctx, "test"); err != nil {
			t.Fatal(err)
		}
		opened := 0
		for _, p := range h.app.MockAudio.Played()[before:] {
			if p.Label == "record_start" {
				opened++
			}
		}
		if opened != 1 {
			t.Errorf("%q: the microphone was opened %d times, want once", said, opened)
		}
		if h.llm.calls() != 0 {
			t.Errorf("%q reached the model", said)
		}
	}
}

// Goodbye and "be quiet" say nothing and play nothing.
func TestGoodbyeAndSilenceAreSilent(t *testing.T) {
	h := newHarness(t)
	for _, said := range []string{"adiós", "silencio", "gracias"} {
		h.app.MockAudio.SetUtterance(said)
		before := len(h.app.MockAudio.Played())
		if err := h.app.Assistant.RunVoice(h.ctx, "test"); err != nil {
			t.Fatal(err)
		}
		for _, p := range h.app.MockAudio.Played()[before:] {
			if p.Kind == "audio" || p.Label == "ok" || p.Label == "error" {
				t.Errorf("%q made a sound: %+v", said, p)
			}
		}
	}
}
