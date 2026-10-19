package telegram_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/juancer/milo/internal/assistant"
	"github.com/juancer/milo/internal/audio"
	"github.com/juancer/milo/internal/speech"
	"github.com/juancer/milo/internal/store"
)

// listenerBackends gives the assistant a working microphone path (the simulator's speech-to-text and
// text-to-speech) and no language model: nothing in the announcement flow may need one.
type listenerBackends struct{}

func (listenerBackends) LLM(context.Context, store.GPTSettings) (assistant.AIProvider, error) {
	return nil, assistant.ErrNotConfigured
}
func (listenerBackends) STT(context.Context, store.GPTSettings) (speech.STT, error) {
	return speech.EmbeddedTextSTT{}, nil
}
func (listenerBackends) TTS(context.Context, store.GPTSettings) (speech.TTS, error) {
	return speech.MockTTS{}, nil
}

const (
	annDad    = "Hay un mensaje nuevo de Dad. ¿Quieres que lo lea?"
	annBtwOne = "Por cierto, tienes 1 mensaje por escuchar. ¿Quieres que lo lea?"
)

// voiceHarness is a harness whose device can speak and listen, in Spanish, with usage hours open.
func voiceHarness(t *testing.T, before ...func(*harness, *fakeSchedule)) (*harness, *fakeSchedule) {
	t.Helper()
	h := newHarness(t, true)
	h.app.Assistant.SetBackends(listenerBackends{})
	g, err := h.app.Store.GetGPTSettings(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	g.Language, g.TTSLanguage = "es", "es"
	if err := h.app.Store.UpdateGPTSettings(h.ctx, g); err != nil {
		t.Fatal(err)
	}
	sched := &fakeSchedule{open: true}
	h.app.Telegram.SetSchedules(sched)
	for _, f := range before {
		f(h, sched)
	}
	h.enable()
	h.addUser(10, "Dad", "PARENT")
	return h, sched
}

func (h *harness) pendingMessages() int {
	total, _, _ := h.app.Store.PendingCounts(h.ctx)
	return total
}

// quiet fails when the speaker played anything containing the text.
func (h *harness) neverPlayed(t *testing.T, label string) {
	t.Helper()
	if h.played(label) {
		t.Fatalf("played %q; speaker log: %+v", label, h.app.MockAudio.Played())
	}
}

func TestNewTextMessageIsAnnouncedAndReadOnYes(t *testing.T) {
	h, _ := voiceHarness(t)
	h.app.MockAudio.SetUtterance("sí, por favor")
	h.fake.Send(10, "dinner at 7")

	eventually(t, "the announcement", func() bool { return h.played(annDad) })
	eventually(t, "the message read aloud", func() bool { return h.played("dinner at 7") })
	eventually(t, "marked delivered", func() bool { return h.pendingMessages() == 0 })
	if h.played("message") { // the spoken question replaces the notification sound
		t.Fatal("the notification sound was played too")
	}
}

func TestNewVoiceNoteIsAnnouncedAndPlayedOnYes(t *testing.T) {
	h, _ := voiceHarness(t)
	h.app.MockAudio.SetUtterance("yes")
	h.fake.SendVoice(10, audio.EmbedText(audio.EncodeWAV(audio.Silence(time.Second), 16000), "good night"), 1)

	eventually(t, "the announcement", func() bool { return h.played(annDad) })
	eventually(t, "the voice note played", func() bool { return h.played("good night") })
	eventually(t, "marked delivered", func() bool { return h.pendingMessages() == 0 })
}

func TestNoLeavesTheMessageQueuedAndSaysNothingMore(t *testing.T) {
	h, _ := voiceHarness(t)
	h.app.MockAudio.SetUtterance("ahora no")
	h.fake.Send(10, "dinner at 7")

	eventually(t, "the announcement", func() bool { return h.played(annDad) })
	time.Sleep(400 * time.Millisecond)
	h.neverPlayed(t, "dinner at 7")
	if h.pendingMessages() != 1 {
		t.Fatalf("pending = %d, want the message to stay queued", h.pendingMessages())
	}
}

func TestNoAnswerLeavesTheMessageQueuedSilently(t *testing.T) {
	h, _ := voiceHarness(t) // nobody speaks
	h.fake.Send(10, "dinner at 7")

	eventually(t, "the announcement", func() bool { return h.played(annDad) })
	time.Sleep(400 * time.Millisecond)
	h.neverPlayed(t, "dinner at 7")
	if h.pendingMessages() != 1 {
		t.Fatalf("pending = %d, want the message to stay queued", h.pendingMessages())
	}
	if n := h.playedCount(annDad); n != 1 {
		t.Fatalf("announced %d times, want once", n)
	}
}

func TestNothingIsAnnouncedOutsideUsageHours(t *testing.T) {
	h, sched := voiceHarness(t, func(h *harness, s *fakeSchedule) {
		s.set(false)
		h.app.Telegram.SetTick(20 * time.Millisecond)
	})
	h.app.MockAudio.SetUtterance("sí")
	h.fake.Send(10, "dinner at 7")

	eventually(t, "stored", func() bool { return h.pendingMessages() == 1 })
	time.Sleep(400 * time.Millisecond)
	if got := h.app.MockAudio.Played(); len(got) != 0 {
		t.Fatalf("something played while hours were closed: %+v", got)
	}
	// Hours open: the waiting message is announced.
	sched.set(true)
	eventually(t, "the announcement once hours open", func() bool { return h.played(annDad) })
}

func TestSeveralMessagesBecomeOneAnnouncement(t *testing.T) {
	h, _ := voiceHarness(t)
	h.app.Telegram.SetAnnounceTiming(400*time.Millisecond, 10*time.Millisecond)
	h.addUser(11, "Mamá", "PARENT")
	h.app.MockAudio.SetUtterance("claro")
	h.fake.Send(10, "one")
	h.fake.Send(11, "two")
	h.fake.Send(10, "three")

	want := "Hay 3 mensajes nuevos, de Dad y Mamá. ¿Quieres que los lea?"
	eventually(t, "one announcement for the burst", func() bool { return h.played(want) })
	eventually(t, "all read", func() bool { return h.pendingMessages() == 0 })
	n := 0
	for _, p := range h.app.MockAudio.Played() {
		if strings.HasPrefix(p.Label, "Hay ") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d announcements, want 1: %+v", n, h.app.MockAudio.Played())
	}
}

func TestAnnouncementWaitsUntilTheAssistantIsFree(t *testing.T) {
	h, _ := voiceHarness(t)
	release := h.app.Assistant.Hold() // a conversation is going on
	h.app.MockAudio.SetUtterance("sí")
	h.fake.Send(10, "dinner at 7")

	eventually(t, "stored", func() bool { return h.pendingMessages() == 1 })
	time.Sleep(300 * time.Millisecond)
	if h.played(annDad) {
		t.Fatal("announced in the middle of a conversation")
	}
	release()
	eventually(t, "the announcement once it is free", func() bool { return h.played(annDad) })
}

func TestUnheardMessageIsOfferedAfterTheNextAnswer(t *testing.T) {
	h, _ := voiceHarness(t)
	h.fake.Send(10, "dinner at 7")
	eventually(t, "the announcement", func() bool { return h.played(annDad) }) // nobody answers
	time.Sleep(300 * time.Millisecond)
	if h.pendingMessages() != 1 {
		t.Fatal("the message should still be queued")
	}

	// Somebody says the wake word and asks for something local.
	h.app.Audio.SetVolume(h.ctx, 40) //nolint:errcheck
	h.app.MockAudio.SetUtterance("Oye Milo, sube el volumen")
	done := make(chan error, 1)
	go func() { done <- h.app.Assistant.RunVoice(h.ctx, "test") }()
	eventually(t, "the request carried out", func() bool { return h.app.Audio.Status(h.ctx).Volume == 50 })
	h.app.MockAudio.SetUtterance("sí") // the answer to "por cierto"

	eventually(t, "the por cierto", func() bool { return h.played(annBtwOne) })
	eventually(t, "the message read", func() bool { return h.played("dinner at 7") })
	eventually(t, "marked delivered", func() bool { return h.pendingMessages() == 0 })
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the voice session never ended")
	}
}

func TestSilentCommandsDoNotOfferTheMessage(t *testing.T) {
	h, _ := voiceHarness(t)
	h.fake.Send(10, "dinner at 7")
	eventually(t, "the announcement", func() bool { return h.played(annDad) })
	time.Sleep(300 * time.Millisecond)

	h.app.MockAudio.SetUtterance("Oye Milo, adiós")
	if err := h.app.Assistant.RunVoice(h.ctx, "test"); err != nil {
		t.Fatal(err)
	}
	h.neverPlayed(t, annBtwOne)
	if h.pendingMessages() != 1 {
		t.Fatal("the message should still be queued")
	}
}

func TestWithoutSpeechRecognitionTheOldSoundIsUsed(t *testing.T) {
	h := newHarness(t, true) // no key, so the device cannot listen for an answer
	h.enable()
	h.addUser(10, "Dad", "PARENT")
	h.fake.Send(10, "dinner at 7")
	eventually(t, "the notification sound", func() bool { return h.played("message") })
	if h.pendingMessages() != 1 {
		t.Fatal("the message should wait to be listened to")
	}
}
