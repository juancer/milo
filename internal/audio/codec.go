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

func run(ctx context.Context, name string, in []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(in)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		if _, lookErr := exec.LookPath(name); lookErr != nil {
			return nil, fmt.Errorf("audio: %s not found (sudo apt install opus-tools)", name)
		}
		return nil, fmt.Errorf("audio: %s failed: %s", name, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
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
