package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func tonePCM(d time.Duration, amp float64) []byte {
	n := int(d.Seconds() * SampleRate)
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := int16(math.Sin(2*math.Pi*440*float64(i)/SampleRate) * amp)
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

func TestWAVRoundTrip(t *testing.T) {
	pcm := tonePCM(200*time.Millisecond, 8000)
	wav := EncodeWAV(pcm, SampleRate)
	got, rate, err := DecodeWAV(wav)
	if err != nil || rate != SampleRate || !bytes.Equal(got, pcm) {
		t.Fatalf("round trip failed: rate=%d err=%v equal=%v", rate, err, bytes.Equal(got, pcm))
	}
	if d := Duration(wav); d < 190*time.Millisecond || d > 210*time.Millisecond {
		t.Fatalf("Duration = %v", d)
	}
}

func TestDecodeWAVRejectsBadInput(t *testing.T) {
	if _, _, err := DecodeWAV([]byte("nope")); err == nil {
		t.Fatal("garbage accepted")
	}
	stereo := EncodeWAV(make([]byte, 100), SampleRate)
	binary.LittleEndian.PutUint16(stereo[22:], 2) // channels
	if _, _, err := DecodeWAV(stereo); err == nil {
		t.Fatal("stereo accepted")
	}
}

func TestEveryNotificationIsValidAudio(t *testing.T) {
	all := []Notification{NotifyMessage, NotifyEmergency, NotifyRecordStart, NotifyRecordStop, NotifyOK, NotifyError, NotifyNone, NotifyAlarm}
	for _, n := range all {
		wav := NotificationWAV(n)
		if _, _, err := DecodeWAV(wav); err != nil || Duration(wav) < 50*time.Millisecond {
			t.Errorf("%s: err=%v duration=%v", n, err, Duration(wav))
		}
	}
	// The emergency sound must be clearly longer than a normal message sound.
	if Duration(NotificationWAV(NotifyEmergency)) < 4*Duration(NotificationWAV(NotifyMessage)) {
		t.Fatal("emergency sound not distinguishable from message sound")
	}
}

func TestEmbedText(t *testing.T) {
	wav := EmbedText(EncodeWAV(Silence(50*time.Millisecond), SampleRate), "hello there")
	if ExtractText(wav) != "hello there" {
		t.Fatalf("ExtractText = %q", ExtractText(wav))
	}
	if _, _, err := DecodeWAV(wav); err != nil {
		t.Fatalf("embedded WAV no longer decodes: %v", err)
	}
	if ExtractText(EncodeWAV(nil, SampleRate)) != "" {
		t.Fatal("phantom text")
	}
}

func TestMockRecordsPlaybackAndVolume(t *testing.T) {
	ctx := context.Background()
	m := NewMock()
	m.SetUtterance("play frozen")
	wav, err := m.Record(ctx, RecordOptions{MaxDuration: 20 * time.Millisecond})
	if err != nil || ExtractText(wav) != "play frozen" {
		t.Fatalf("Record = %q, %v", ExtractText(wav), err)
	}
	if again, _ := m.Record(ctx, RecordOptions{MaxDuration: 20 * time.Millisecond}); ExtractText(again) != "" {
		t.Fatal("utterance must be consumed")
	}
	m.PlayNotification(ctx, NotifyMessage, 100)
	m.Play(ctx, wav)
	m.SetVolume(ctx, 250)
	p := m.Played()
	if len(p) != 2 || p[0].Label != "message" || p[1].Label != "play frozen" {
		t.Fatalf("Played = %+v", p)
	}
	if v := m.Status(ctx).Volume; v != 100 {
		t.Fatalf("volume = %d, want clamped 100", v)
	}
}

func TestALSAConfigValidation(t *testing.T) {
	if err := DefaultALSAConfig.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []ALSAConfig{
		{"a; rm -rf /", "x", "x", "x"},
		{"x", "$(id)", "x", "x"},
		{"x", "x", "card name", "x"},
		{"x", "x", "x", "Speaker`id`"},
		{"", "x", "x", "x"},
	}
	for _, c := range bad {
		if c.Validate() == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	if _, err := NewALSA(bad[0]); err == nil {
		t.Fatal("NewALSA accepted bad config")
	}
}

func TestRecordFromStopsOnSilenceAfterSpeech(t *testing.T) {
	speech := tonePCM(500*time.Millisecond, 9000)
	quiet := Silence(3 * time.Second)
	src := io.MultiReader(bytes.NewReader(speech), bytes.NewReader(quiet))
	stopped := false
	opts := RecordOptions{StopOnSilence: true, SilenceFor: 700 * time.Millisecond, MaxDuration: 30 * time.Second}.withDefaults()
	pcm, ok := recordFrom(context.Background(), src, opts, func() { stopped = true })
	if !ok || !stopped {
		t.Fatal("did not stop on silence")
	}
	got := time.Duration(len(pcm)/2) * time.Second / SampleRate
	if got < 1100*time.Millisecond || got > 1400*time.Millisecond {
		t.Fatalf("recorded %v, want about 500ms speech + 700ms silence", got)
	}
}

func TestRecordFromGivesUpWhenNobodySpeaks(t *testing.T) {
	src := bytes.NewReader(Silence(20 * time.Second))
	opts := RecordOptions{StopOnSilence: true, MaxDuration: 60 * time.Second}.withDefaults()
	pcm, ok := recordFrom(context.Background(), src, opts, func() {})
	if !ok || len(pcm)/2/SampleRate > 9 {
		t.Fatalf("recorded %ds, ok=%v", len(pcm)/2/SampleRate, ok)
	}
}

func TestRecordFromHonoursMaxDuration(t *testing.T) {
	src := bytes.NewReader(tonePCM(5*time.Second, 9000))
	opts := RecordOptions{MaxDuration: time.Second}.withDefaults()
	pcm, ok := recordFrom(context.Background(), src, opts, func() {})
	if !ok || len(pcm)/2 != SampleRate {
		t.Fatalf("samples = %d, ok=%v", len(pcm)/2, ok)
	}
}

func TestRecordFromEndsOnContextCancel(t *testing.T) {
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		pw.Write(tonePCM(300*time.Millisecond, 9000))
		cancel()
		pw.Close()
	}()
	pcm, ok := recordFrom(ctx, pr, RecordOptions{}.withDefaults(), func() {})
	if !ok || len(pcm) == 0 {
		t.Fatalf("len=%d ok=%v", len(pcm), ok)
	}
}

func TestScaleAndBlips(t *testing.T) {
	wav := NotificationWAV(NotifyMessage)
	quiet := Scale(wav, 25)
	loud, _, _ := DecodeWAV(wav)
	soft, _, err := DecodeWAV(quiet)
	if err != nil || len(soft) != len(loud) || rms(soft) >= rms(loud)/3 || rms(soft) == 0 {
		t.Fatalf("scaling failed: %v %v %v", err, rms(soft), rms(loud))
	}
	if !bytes.Equal(Scale(wav, 100), wav) {
		t.Fatal("100% must not change the audio")
	}
	if rms(mustPCM(t, Scale(wav, 0))) != 0 {
		t.Fatal("0% must be silent")
	}
	if Duration(Blips(3)) <= Duration(Blips(1)) || Duration(Blips(99)) != Duration(Blips(9)) {
		t.Fatal("Blips length wrong")
	}
}

func mustPCM(t *testing.T, wav []byte) []byte {
	t.Helper()
	pcm, _, err := DecodeWAV(wav)
	if err != nil {
		t.Fatal(err)
	}
	return pcm
}

func TestPassThroughCodec(t *testing.T) {
	var c Transcoder = PassThrough{}
	in := []byte("x")
	out, _ := c.OggToWAV(context.Background(), in)
	back, _ := c.WAVToOgg(context.Background(), out)
	if string(back) != "x" {
		t.Fatal("PassThrough must not alter data")
	}
}

// A fake arecord that streams silence forever, at real-time pace, so the
// microphone arbitration between Listen and Record can be tested.
func fakeArecordPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nwhile true; do head -c 3200 /dev/zero; sleep 0.1; done\n"
	if err := os.WriteFile(filepath.Join(dir, "arecord"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestRecordTakesPriorityOverListenAndListenResumes(t *testing.T) {
	fakeArecordPath(t)
	a, err := NewALSA(DefaultALSAConfig)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var chunks atomic.Int64
	done := make(chan error, 1)
	go func() { done <- a.Listen(ctx, func(pcm []byte) { chunks.Add(1) }) }()

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("listen to deliver audio", func() bool { return chunks.Load() >= 2 })

	before := chunks.Load()
	wav, err := a.Record(context.Background(), RecordOptions{MaxDuration: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if d := Duration(wav); d < 300*time.Millisecond {
		t.Fatalf("recorded only %v: the microphone was not handed over", d)
	}
	during := chunks.Load() - before
	if during > 2 {
		t.Fatalf("Listen kept receiving audio (%d chunks) while a recording ran", during)
	}

	afterRecord := chunks.Load()
	waitFor("listen to resume after the recording", func() bool { return chunks.Load() > afterRecord+2 })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Listen returned %v after cancel", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Listen did not stop when cancelled")
	}
}

func TestListenReportsAMissingMicrophone(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "arecord"), []byte("#!/bin/sh\necho 'no such device' >&2\nexit 1\n"), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	a, _ := NewALSA(DefaultALSAConfig)
	err := a.Listen(context.Background(), func([]byte) {})
	if err == nil || !strings.Contains(err.Error(), "no such device") {
		t.Fatalf("err = %v", err)
	}
}
