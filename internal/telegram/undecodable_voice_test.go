package telegram_test

import (
	"context"
	"testing"
	"time"

	"github.com/juancer/nexo/internal/audio"
)

// stereoDecoder applies the same check as audio.Opus.OggToWAV after opusdec runs: only
// 16-bit mono PCM is accepted.
type stereoDecoder struct{ audio.PassThrough }

func (stereoDecoder) OggToWAV(_ context.Context, b []byte) ([]byte, error) {
	if _, _, err := audio.DecodeWAV(b); err != nil {
		return nil, err
	}
	return b, nil
}

// One voice note that cannot be decoded must not keep the child from hearing the messages
// that arrived after it.
func TestUndecodableVoiceNoteDoesNotBlockLaterMessages(t *testing.T) {
	h := newHarness(t, true)
	h.enable()
	h.app.Telegram.Codec = stereoDecoder{}
	h.addUser(10, "Cousin", "FAMILY")
	h.addUser(11, "Dad", "PARENT")

	stereo := audio.EncodeWAV(audio.Silence(time.Second), 16000)
	stereo[22] = 2 // two channels, as opusdec would write for a two-channel stream
	h.fake.SendVoice(10, stereo, 1)
	eventually(t, "voice stored", func() bool { n, _, _ := h.app.Store.PendingCounts(h.ctx); return n == 1 })
	h.fake.Send(11, "dinner at 7")
	eventually(t, "text stored", func() bool { n, _, _ := h.app.Store.PendingCounts(h.ctx); return n == 2 })

	heard := false
	for i := 0; i < 5 && !heard; i++ {
		_, err := h.app.Actions.Execute(h.ctx, device(), "listen", nil)
		heard = err == nil
	}
	if !heard {
		t.Fatal("the message behind the undecodable voice note never played")
	}
	if n, _, _ := h.app.Store.PendingCounts(h.ctx); n != 0 {
		t.Fatalf("%d messages still pending", n)
	}
	// The bad recording is finished (failed), so retention can remove it.
	if pruned, _ := h.app.Store.PruneMessages(h.ctx, time.Now().AddDate(10, 0, 0)); pruned < 1 {
		t.Fatalf("pruned = %d, want the failed recording to be removable", pruned)
	}
}
