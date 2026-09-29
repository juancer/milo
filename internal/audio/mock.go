package audio

import (
	"bytes"
	"context"
	"sync"
	"time"
)

// Played is one item the mock "played".
type Played struct {
	At       time.Time     `json:"at"`
	Kind     string        `json:"kind"`  // "notification", "audio"
	Label    string        `json:"label"` // notification name or free text
	Duration time.Duration `json:"duration_ms"`
}

// Mock is an in-memory Audio used in tests and the development simulator.
// Nothing is actually played; playback is logged and returns immediately.
type Mock struct {
	mu        sync.Mutex
	volume    int
	played    []Played
	utterance string // what the "child" says on the next recording
	recDur    time.Duration
}

// NewMock returns a Mock at 70% volume.
func NewMock() *Mock { return &Mock{volume: 70, recDur: 300 * time.Millisecond} }

// SetUtterance sets the text the next recording "contains". The mock embeds it
// in the WAV so the development STT stub can transcribe it.
func (m *Mock) SetUtterance(text string) {
	m.mu.Lock()
	m.utterance = text
	m.mu.Unlock()
}

// Played returns a copy of the playback log (oldest first).
func (m *Mock) Played() []Played {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Played(nil), m.played...)
}

func (m *Mock) log(p Played) {
	p.At = time.Now()
	m.mu.Lock()
	m.played = append(m.played, p)
	if len(m.played) > 200 {
		m.played = m.played[len(m.played)-200:]
	}
	m.mu.Unlock()
}

// Record implements Audio. It "listens" until ctx ends or the mock duration passes.
func (m *Mock) Record(ctx context.Context, opts RecordOptions) ([]byte, error) {
	opts = opts.withDefaults()
	wait := m.recDur
	if opts.StopOnSilence {
		wait = 100 * time.Millisecond
	}
	select {
	case <-ctx.Done():
	case <-time.After(min(wait, opts.MaxDuration)):
	}
	m.mu.Lock()
	text := m.utterance
	m.utterance = ""
	m.mu.Unlock()
	wav := EncodeWAV(Silence(300*time.Millisecond), SampleRate)
	if text != "" {
		wav = EmbedText(wav, text)
	}
	return wav, nil
}

// Play implements Audio.
func (m *Mock) Play(_ context.Context, wav []byte) error {
	label := "audio"
	if t := ExtractText(wav); t != "" {
		label = t
	}
	m.log(Played{Kind: "audio", Label: label, Duration: Duration(wav)})
	return nil
}

// PlayNotification implements Audio.
func (m *Mock) PlayNotification(_ context.Context, n Notification) error {
	m.log(Played{Kind: "notification", Label: string(n), Duration: Duration(NotificationWAV(n))})
	return nil
}

// Stop implements Audio.
func (m *Mock) Stop() {}

// SetVolume implements Audio.
func (m *Mock) SetVolume(_ context.Context, percent int) error {
	m.mu.Lock()
	m.volume = max(0, min(100, percent))
	m.mu.Unlock()
	return nil
}

// Status implements Audio.
func (m *Mock) Status(context.Context) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{Backend: "mock", Available: true, Volume: m.volume, Detail: "no real sound is produced"}
}

const textChunkID = "nxTX"

// EmbedText adds a private "nxTX" chunk carrying text to a WAV. It exists only
// so the development speech-to-text stub can "transcribe" mock recordings.
// Real recordings never contain it.
func EmbedText(wav []byte, text string) []byte {
	var b bytes.Buffer
	b.Write(wav)
	b.WriteString(textChunkID)
	b.Write([]byte{byte(len(text)), byte(len(text) >> 8), byte(len(text) >> 16), byte(len(text) >> 24)})
	b.WriteString(text)
	if len(text)%2 == 1 {
		b.WriteByte(0)
	}
	return b.Bytes()
}

// ExtractText returns text added by EmbedText, or "".
func ExtractText(wav []byte) string {
	i := bytes.LastIndex(wav, []byte(textChunkID))
	if i < 0 || i+8 > len(wav) {
		return ""
	}
	n := int(wav[i+4]) | int(wav[i+5])<<8 | int(wav[i+6])<<16 | int(wav[i+7])<<24
	if i+8+n > len(wav) {
		return ""
	}
	return string(wav[i+8 : i+8+n])
}
