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

var sounds = map[Notification][]beep{
	NotifyMessage: {{880, 180 * time.Millisecond}},
	NotifyEmergency: {
		{1200, 450 * time.Millisecond}, {0, 200 * time.Millisecond},
		{1200, 450 * time.Millisecond}, {0, 200 * time.Millisecond},
		{1200, 450 * time.Millisecond},
	},
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
		pcm = append(pcm, synth(b.freq, b.dur)...)
	}
	return EncodeWAV(pcm, SampleRate)
}

// synth renders a sine tone with short fades so it does not click.
func synth(freq float64, d time.Duration) []byte {
	n := int(d.Seconds() * SampleRate)
	out := make([]byte, n*2)
	if freq == 0 {
		return out
	}
	fade := SampleRate / 200 // 5 ms
	for i := 0; i < n; i++ {
		amp := 0.45
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
