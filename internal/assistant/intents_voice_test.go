package assistant_test

import (
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
