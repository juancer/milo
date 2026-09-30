package app

import (
	"context"
	"testing"
)

// Timers and reminders speak through the assistant, so they stay silent (the tone
// still sounds) while the assistant feature is switched off.
func TestAnnounceDoesNotSpeakWhileTheAssistantIsOff(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	spoken := func() int {
		n := 0
		for _, p := range a.MockAudio.Played() {
			if p.Kind == "audio" {
				n++
			}
		}
		return n
	}
	if err := a.Features.SetEnabled(ctx, "assistant", false); err != nil {
		t.Fatal(err)
	}
	a.Announce(ctx, "Reminder: brush your teeth", false)
	if spoken() != 0 {
		t.Fatalf("spoke while the assistant was off: %+v", a.MockAudio.Played())
	}
	if err := a.Features.SetEnabled(ctx, "assistant", true); err != nil {
		t.Fatal(err)
	}
	a.Announce(ctx, "Reminder: brush your teeth", false)
	if spoken() != 1 {
		t.Fatalf("did not speak while the assistant was on: %+v", a.MockAudio.Played())
	}
}
