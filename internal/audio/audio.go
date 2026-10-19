// Package audio hides the sound hardware behind one interface. The real
// implementation drives ALSA through fixed arecord/aplay/amixer invocations
// (keeping the binary CGO-free); a mock is used in tests and development.
//
// Audio exchanged through this package is 16-bit mono PCM in WAV containers.
package audio

import (
	"context"
	"io"
	"time"
)

// SampleRate is the capture and synthesis rate used everywhere.
const SampleRate = 16000

// Notification identifies a built-in sound. Sounds are synthesised, so no
// audio files ship with the binary.
type Notification string

const (
	NotifyMessage     Notification = "message"      // short "piii"
	NotifyEmergency   Notification = "emergency"    // PIII - PIII - PIII
	NotifyWake        Notification = "wake"         // loud rising chirp: the wake word was heard
	NotifyRecordStart Notification = "record_start" // rising blip
	NotifyRecordStop  Notification = "record_stop"  // falling blip
	NotifyOK          Notification = "ok"
	NotifyError       Notification = "error"
	NotifyNone        Notification = "none" // "nothing to play"
	NotifyAlarm       Notification = "alarm"
)

// Stream is raw signed 16-bit little-endian PCM to be played as it is read.
type Stream struct {
	Reader     io.Reader
	SampleRate int
	Channels   int
	Label      string // shown in logs and the simulator
}

// RecordOptions control a recording.
type RecordOptions struct {
	MaxDuration   time.Duration // hard limit; 0 means 60s
	StopOnSilence bool          // end after SilenceFor of quiet following speech
	SilenceFor    time.Duration // default 1500ms
	Threshold     int           // RMS level counted as speech in a quiet room; default 600
	NoSpeechFor   time.Duration // give up when nobody speaks for this long; default 8 s
}

// Status describes the audio subsystem.
type Status struct {
	Backend   string `json:"backend"` // "alsa" or "mock"
	Available bool   `json:"available"`
	Volume    int    `json:"volume"` // 0-100
	Detail    string `json:"detail,omitempty"`
}

// Audio records and plays sound.
type Audio interface {
	// Record captures from the microphone until ctx is cancelled, the maximum
	// duration passes or (optionally) the speaker goes quiet. The result is a
	// WAV file.
	Record(ctx context.Context, opts RecordOptions) ([]byte, error)
	// Play plays a WAV file and blocks until it finishes, ctx is cancelled or
	// Stop is called.
	Play(ctx context.Context, wav []byte) error
	// PlayStream plays raw 16-bit PCM from a reader (music). It blocks until the
	// stream ends or is interrupted. Any other playback (a notification, speech)
	// or Stop interrupts it, so alerts are never drowned out by music.
	PlayStream(ctx context.Context, s Stream) error
	// Busy reports whether a sound (other than a stream) is playing or waiting.
	Busy() bool
	// PlayNotification plays a built-in sound at volume percent (100 = full).
	PlayNotification(ctx context.Context, n Notification, volume int) error
	// Stop interrupts current playback, if any.
	Stop()
	// Listen streams microphone audio (16 kHz mono 16-bit PCM in ~100 ms chunks)
	// to handler until ctx is cancelled. It is for local processing such as
	// wake-word detection. Record always takes priority: Listen pauses while a
	// recording runs and resumes afterwards.
	Listen(ctx context.Context, handler func(pcm []byte)) error
	SetVolume(ctx context.Context, percent int) error
	Status(ctx context.Context) Status
}

func (o RecordOptions) withDefaults() RecordOptions {
	if o.MaxDuration <= 0 {
		o.MaxDuration = 60 * time.Second
	}
	if o.SilenceFor <= 0 {
		o.SilenceFor = 1500 * time.Millisecond
	}
	if o.Threshold <= 0 {
		o.Threshold = 600
	}
	return o
}
