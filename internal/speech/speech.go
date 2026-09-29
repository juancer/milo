// Package speech holds the speech-to-text and text-to-speech providers. The
// assistant depends only on the STT and TTS interfaces, so either can be
// replaced (cloud STT today, maybe whisper.cpp later; local Piper TTS).
package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/juancer/nexo/internal/audio"
)

// STT converts speech to text.
type STT interface {
	Transcribe(ctx context.Context, wav []byte) (string, error)
}

// TTS converts text to speech (a WAV file).
type TTS interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

// ---------------------------------------------------------------- STT (OpenAI-compatible)

// OpenAICompatSTT calls an OpenAI-compatible /audio/transcriptions endpoint.
// Groq (whisper-large-v3-turbo) and OpenAI both speak it. Only the recording
// is sent, never any other data about the device or the family.
type OpenAICompatSTT struct {
	base, key, model string
	http             *http.Client
}

// NewOpenAICompatSTT returns a client.
func NewOpenAICompatSTT(baseURL, apiKey, model string) *OpenAICompatSTT {
	return &OpenAICompatSTT{base: strings.TrimRight(baseURL, "/"), key: apiKey, model: model, http: &http.Client{Timeout: 45 * time.Second}}
}

// Transcribe implements STT.
func (s *OpenAICompatSTT) Transcribe(ctx context.Context, wav []byte) (string, error) {
	if s.key == "" {
		return "", errors.New("speech-to-text is not configured (no API key)")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("model", s.model)
	mw.WriteField("response_format", "json")
	mw.WriteField("temperature", "0")
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="speech.wav"`)
	h.Set("Content-Type", "audio/wav")
	part, err := mw.CreatePart(h)
	if err != nil {
		return "", err
	}
	part.Write(wav)
	mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/audio/transcriptions", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := s.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drops the URL
		}
		return "", fmt.Errorf("speech-to-text unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("speech-to-text failed (status %d)", resp.StatusCode)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", errors.New("speech-to-text returned an unreadable response")
	}
	return strings.TrimSpace(out.Text), nil
}

// ---------------------------------------------------------------- TTS

var (
	modelPathRe  = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,200}\.onnx$`)
	binaryPathRe = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,200}$`)
)

// ValidModelPath reports whether p is an acceptable Piper voice path.
func ValidModelPath(p string) bool { return modelPathRe.MatchString(p) && !strings.Contains(p, "..") }

// Piper runs the local Piper text-to-speech engine. The command line is fixed;
// only the (validated) voice model path varies and the text goes on stdin, so
// nothing the child or the model says can reach a shell.
type Piper struct {
	binary, model string
}

// NewPiper validates the paths. binary is usually just "piper".
func NewPiper(binary, model string) (*Piper, error) {
	if binary == "" {
		binary = "piper"
	}
	if !binaryPathRe.MatchString(binary) || strings.Contains(binary, "..") {
		return nil, fmt.Errorf("speech: invalid piper binary %q", binary)
	}
	if !ValidModelPath(model) {
		return nil, fmt.Errorf("speech: invalid Piper voice model %q (expected a path to a .onnx file)", model)
	}
	return &Piper{binary: binary, model: model}, nil
}

// Synthesize implements TTS.
func (p *Piper) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("nothing to say")
	}
	if len(text) > 4000 {
		text = text[:4000]
	}
	dir, err := os.MkdirTemp("", "nexo-tts-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "speech.wav")

	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, p.binary, "--model", p.model, "--output_file", out)
	cmd.Stdin = strings.NewReader(text + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if _, lookErr := exec.LookPath(p.binary); lookErr != nil {
			return nil, fmt.Errorf("piper not found: install Piper or set the text-to-speech engine to none")
		}
		return nil, fmt.Errorf("piper failed: %s", strings.TrimSpace(stderr.String()))
	}
	wav, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("piper produced no audio: %w", err)
	}
	if _, _, err := audio.DecodeWAV(wav); err != nil {
		return nil, fmt.Errorf("piper output is not usable audio: %w", err)
	}
	return wav, nil
}

// MockTTS "speaks" by producing a short tone burst that carries the text, so
// the development speaker log shows what would have been said.
type MockTTS struct{}

// Synthesize implements TTS.
func (MockTTS) Synthesize(_ context.Context, text string) ([]byte, error) {
	d := time.Duration(max(1, len(strings.Fields(text)))) * 300 * time.Millisecond
	return audio.EmbedText(audio.EncodeWAV(audio.Silence(min(d, 20*time.Second)), audio.SampleRate), text), nil
}

// EmbeddedTextSTT reads the text that the development simulator hides inside
// its recordings. It exists so the whole voice pipeline can run offline in the
// simulator; a real microphone recording never contains any text.
type EmbeddedTextSTT struct{}

// Transcribe implements STT.
func (EmbeddedTextSTT) Transcribe(_ context.Context, wav []byte) (string, error) {
	return audio.ExtractText(wav), nil
}
