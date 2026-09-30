package audio

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Transcoder converts between Telegram's OGG/Opus voice notes and the WAV
// audio used everywhere else.
type Transcoder interface {
	OggToWAV(ctx context.Context, ogg []byte) ([]byte, error)
	WAVToOgg(ctx context.Context, wav []byte) ([]byte, error)
}

// Opus uses opus-tools (`opusdec`, `opusenc`) with fixed arguments and data on
// stdin/stdout. Install with: sudo apt install opus-tools
type Opus struct{}

// maxOutput caps what a converter may write. Two minutes of 16 kHz mono speech is under 4 MiB, so
// this leaves room while stopping a crafted file from filling the device's memory.
const maxOutput = 16 << 20

var errOutputTooLarge = fmt.Errorf("output larger than %d MiB", maxOutput>>20)

// limitedBuffer collects output and stops the process once it has produced too much.
type limitedBuffer struct {
	buf  bytes.Buffer // a named field: embedding it would promote ReadFrom and skip the check
	stop func()
	over bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > maxOutput {
		b.over = true
		b.stop()
		return 0, errOutputTooLarge
	}
	return b.buf.Write(p)
}

// maxStderr caps the diagnostics kept from a converter's stderr.
const maxStderr = 1 << 20

// CappedBuffer keeps the first Max bytes written to it and silently drops the
// rest, so a chatty child process can neither fill memory nor block on a full pipe.
type CappedBuffer struct {
	Max int
	buf bytes.Buffer
}

// Write implements io.Writer. It always reports the whole write as done.
func (b *CappedBuffer) Write(p []byte) (int, error) {
	if room := b.Max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// String returns what was kept.
func (b *CappedBuffer) String() string { return b.buf.String() }

// NewStderrBuffer returns a CappedBuffer sized for a child process's diagnostics (1 MiB).
func NewStderrBuffer() *CappedBuffer { return &CappedBuffer{Max: maxStderr} }

func run(ctx context.Context, name string, in []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(in)
	out := &limitedBuffer{stop: cancel}
	stderr := NewStderrBuffer()
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		if out.over {
			return nil, fmt.Errorf("audio: %s: %w", name, errOutputTooLarge)
		}
		if _, lookErr := exec.LookPath(name); lookErr != nil {
			return nil, fmt.Errorf("audio: %s not found (sudo apt install opus-tools)", name)
		}
		return nil, fmt.Errorf("audio: %s failed: %s", name, strings.TrimSpace(stderr.String()))
	}
	return out.buf.Bytes(), nil
}

// OggToWAV implements Transcoder.
func (Opus) OggToWAV(ctx context.Context, ogg []byte) ([]byte, error) {
	wav, err := run(ctx, "opusdec", ogg, "--quiet", "--rate", fmt.Sprint(SampleRate), "--force-wav", "-", "-")
	if err != nil {
		return nil, err
	}
	if _, _, err := DecodeWAV(wav); err != nil {
		return nil, err
	}
	return wav, nil
}

// WAVToOgg implements Transcoder.
func (Opus) WAVToOgg(ctx context.Context, wav []byte) ([]byte, error) {
	return run(ctx, "opusenc", wav, "--quiet", "--bitrate", "24", "--downmix-mono", "-", "-")
}

// PassThrough leaves audio untouched. Development and tests use it together
// with the fake Telegram server, whose "voice notes" are plain WAV files.
type PassThrough struct{}

// OggToWAV implements Transcoder.
func (PassThrough) OggToWAV(_ context.Context, b []byte) ([]byte, error) { return b, nil }

// WAVToOgg implements Transcoder.
func (PassThrough) WAVToOgg(_ context.Context, b []byte) ([]byte, error) { return b, nil }
