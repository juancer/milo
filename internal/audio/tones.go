package audio

import (
	"encoding/binary"
	"math"
	"time"
)

type beep struct {
	freq float64 // Hz; 0 = silence
	dur  time.Duration
}

// defaultAmplitude is the level of a tone as a fraction of full scale; loudAmplitude lists the
// sounds that must cut through a small speaker (and a noisy room) at nearly full scale.
const defaultAmplitude = 0.45

var loudAmplitude = map[Notification]float64{NotifyWake: 0.95}

var sounds = map[Notification][]beep{
	NotifyMessage: {{880, 180 * time.Millisecond}},
	NotifyEmergency: {
		{1200, 450 * time.Millisecond}, {0, 200 * time.Millisecond},
		{1200, 450 * time.Millisecond}, {0, 200 * time.Millisecond},
		{1200, 450 * time.Millisecond},
	},
	// The wake word was heard: higher, because small speakers are weak below 500 Hz.
	NotifyWake:        {{1400, 90 * time.Millisecond}, {2000, 90 * time.Millisecond}, {2600, 160 * time.Millisecond}},
	NotifyRecordStart: {{660, 80 * time.Millisecond}, {880, 120 * time.Millisecond}},
	NotifyRecordStop:  {{880, 80 * time.Millisecond}, {660, 120 * time.Millisecond}},
	NotifyOK:          {{784, 90 * time.Millisecond}, {0, 40 * time.Millisecond}, {988, 130 * time.Millisecond}},
	NotifyError:       {{300, 250 * time.Millisecond}, {0, 60 * time.Millisecond}, {220, 300 * time.Millisecond}},
	NotifyNone:        {{440, 90 * time.Millisecond}},
	NotifyAlarm: {
		{1000, 200 * time.Millisecond}, {0, 100 * time.Millisecond},
		{1000, 200 * time.Millisecond}, {0, 100 * time.Millisecond},
		{1000, 200 * time.Millisecond}, {0, 100 * time.Millisecond},
		{1000, 200 * time.Millisecond},
	},
}

// NotificationWAV synthesises a built-in sound. Unknown kinds yield a short blip.
func NotificationWAV(n Notification) []byte {
	seq, ok := sounds[n]
	if !ok {
		seq = sounds[NotifyNone]
	}
	var pcm []byte
	for _, b := range seq {
		pcm = append(pcm, synth(b.freq, b.dur, loudAmplitude[n])...)
	}
	return EncodeWAV(pcm, SampleRate)
}

// Tone returns 16-bit mono PCM for a sine tone of the given frequency (0 = silence).
func Tone(freq float64, d time.Duration) []byte { return synth(freq, d, 0) }

// synth renders a sine tone with short fades so it does not click.
func synth(freq float64, d time.Duration, amplitude float64) []byte {
	n := int(d.Seconds() * SampleRate)
	out := make([]byte, n*2)
	if amplitude == 0 {
		amplitude = defaultAmplitude
	}
	if freq == 0 {
		return out
	}
	fade := SampleRate / 200 // 5 ms
	for i := 0; i < n; i++ {
		amp := amplitude
		if i < fade {
			amp *= float64(i) / float64(fade)
		} else if n-i < fade {
			amp *= float64(n-i) / float64(fade)
		}
		v := int16(math.Sin(2*math.Pi*freq*float64(i)/SampleRate) * amp * math.MaxInt16)
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

// Scale returns a copy of a 16-bit WAV with its amplitude multiplied by percent/100.
// It is how notification volume is applied without touching the mixer.
func Scale(wav []byte, percent int) []byte {
	if percent >= 100 {
		return wav
	}
	percent = max(percent, 0)
	pcm, rate, err := DecodeWAV(wav)
	if err != nil {
		return wav
	}
	out := make([]byte, len(pcm))
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int16(binary.LittleEndian.Uint16(pcm[i:]))
		binary.LittleEndian.PutUint16(out[i:], uint16(int16(int(v)*percent/100)))
	}
	return EncodeWAV(out, rate)
}

// Blips returns n short beeps (1-9): a spoken-word-free way to say "chat number n".
func Blips(n int) []byte {
	n = max(1, min(n, 9))
	var pcm []byte
	for i := 0; i < n; i++ {
		pcm = append(pcm, synth(660, 110*time.Millisecond, 0)...)
		pcm = append(pcm, synth(0, 110*time.Millisecond, 0)...)
	}
	return EncodeWAV(pcm, SampleRate)
}
