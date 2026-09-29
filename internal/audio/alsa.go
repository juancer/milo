package audio

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ALSAConfig selects the sound devices. Defaults suit the Waveshare WM8960 HAT
// (card "wm8960soundcard", mixer control "Speaker").
type ALSAConfig struct {
	CaptureDevice  string // arecord -D, e.g. "plughw:wm8960soundcard"
	PlaybackDevice string // aplay -D
	MixerCard      string // amixer -c
	MixerControl   string // amixer control, e.g. "Speaker"
}

// DefaultALSAConfig is the WM8960 HAT default.
var DefaultALSAConfig = ALSAConfig{
	CaptureDevice:  "plughw:wm8960soundcard",
	PlaybackDevice: "plughw:wm8960soundcard",
	MixerCard:      "wm8960soundcard",
	MixerControl:   "Speaker",
}

var (
	deviceRe  = regexp.MustCompile(`^[A-Za-z0-9_:,=.\-]{1,64}$`)
	cardRe    = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,64}$`)
	controlRe = regexp.MustCompile(`^[A-Za-z0-9 _\-]{1,64}$`)
)

// Validate rejects values that could confuse the fixed alsa-utils invocations.
// Arguments are always passed as separate argv entries, never through a shell.
func (c ALSAConfig) Validate() error {
	switch {
	case !deviceRe.MatchString(c.CaptureDevice):
		return fmt.Errorf("audio: invalid capture device %q", c.CaptureDevice)
	case !deviceRe.MatchString(c.PlaybackDevice):
		return fmt.Errorf("audio: invalid playback device %q", c.PlaybackDevice)
	case !cardRe.MatchString(c.MixerCard):
		return fmt.Errorf("audio: invalid mixer card %q", c.MixerCard)
	case !controlRe.MatchString(c.MixerControl):
		return fmt.Errorf("audio: invalid mixer control %q", c.MixerControl)
	}
	return nil
}

// ALSA is the real Audio implementation.
type ALSA struct {
	cfg ALSAConfig

	micMu         sync.Mutex // the microphone: one user at a time
	recordWaiters atomic.Int32
	playMu        sync.Mutex // one playback at a time
	cancelMu      sync.Mutex
	cancel        context.CancelFunc
	streamMu      sync.Mutex // guards streamCancel
	streamCancel  context.CancelFunc
	pending       atomic.Int32 // non-stream playbacks running or waiting

	volMu  sync.Mutex
	volume int
}

// NewALSA validates cfg and returns an ALSA audio backend.
func NewALSA(cfg ALSAConfig) (*ALSA, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &ALSA{cfg: cfg, volume: -1}, nil
}

// Record implements Audio using `arecord` streaming raw PCM on stdout.
func (a *ALSA) Record(ctx context.Context, opts RecordOptions) ([]byte, error) {
	opts = opts.withDefaults()
	a.recordWaiters.Add(1) // asks Listen to let go of the microphone
	a.micMu.Lock()
	a.recordWaiters.Add(-1)
	defer a.micMu.Unlock()

	rctx, cancel := context.WithTimeout(ctx, opts.MaxDuration)
	defer cancel()
	cmd := exec.CommandContext(rctx, "arecord", "-q", "-D", a.cfg.CaptureDevice,
		"-t", "raw", "-f", "S16_LE", "-r", fmt.Sprint(SampleRate), "-c", "1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("audio: start arecord: %w", err)
	}
	pcm, stopped := recordFrom(rctx, stdout, opts, cancel)
	cancel()
	waitErr := cmd.Wait()
	if len(pcm) == 0 && !stopped && waitErr != nil {
		return nil, fmt.Errorf("audio: arecord failed: %s", strings.TrimSpace(stderr.String()))
	}
	return EncodeWAV(pcm, SampleRate), nil
}

const chunkDuration = 100 * time.Millisecond

// recordFrom reads 16 kHz mono PCM from r. It returns when r ends, ctx ends, the
// maximum length is reached or (if enabled) silence follows speech. stop is
// called to terminate the source. stopped reports whether we ended it ourselves.
func recordFrom(ctx context.Context, r io.Reader, opts RecordOptions, stop func()) (pcm []byte, stopped bool) {
	chunkBytes := int(chunkDuration.Seconds()*SampleRate) * 2
	maxBytes := int(opts.MaxDuration.Seconds()*SampleRate) * 2
	silentChunks := int(opts.SilenceFor / chunkDuration)
	const noSpeechGiveUp = 80 // 8 s of nothing but quiet

	buf := make([]byte, chunkBytes)
	var quiet int
	var heard bool
	for ctx.Err() == nil {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			pcm = append(pcm, buf[:n]...)
			if opts.StopOnSilence && n == chunkBytes {
				if rms(buf[:n]) >= float64(opts.Threshold) {
					heard, quiet = true, 0
				} else {
					quiet++
				}
				if (heard && quiet >= silentChunks) || (!heard && quiet >= noSpeechGiveUp) {
					stop()
					return pcm, true
				}
			}
			if len(pcm) >= maxBytes {
				stop()
				return pcm, true
			}
		}
		if err != nil {
			break
		}
	}
	return pcm, ctx.Err() != nil
}

// Listen implements Audio by streaming `arecord` output in 100 ms chunks.
func (a *ALSA) Listen(ctx context.Context, handler func(pcm []byte)) error {
	for ctx.Err() == nil {
		for a.recordWaiters.Load() > 0 && ctx.Err() == nil { // let a recording go first
			time.Sleep(20 * time.Millisecond)
		}
		if err := a.listenOnce(ctx, handler); err != nil {
			return err
		}
	}
	return nil
}

func (a *ALSA) listenOnce(ctx context.Context, handler func([]byte)) error {
	a.micMu.Lock()
	defer a.micMu.Unlock()
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(lctx, "arecord", "-q", "-D", a.cfg.CaptureDevice,
		"-t", "raw", "-f", "S16_LE", "-r", fmt.Sprint(SampleRate), "-c", "1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("audio: start arecord: %w", err)
	}
	buf := make([]byte, int(chunkDuration.Seconds()*SampleRate)*2)
	got := false
	for a.recordWaiters.Load() == 0 && ctx.Err() == nil {
		n, err := io.ReadFull(stdout, buf)
		if n > 0 {
			got = true
			handler(buf[:n])
		}
		if err != nil {
			break
		}
	}
	cancel()
	cmd.Wait() //nolint:errcheck
	if !got && ctx.Err() == nil && a.recordWaiters.Load() == 0 {
		return fmt.Errorf("audio: microphone unavailable: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Play implements Audio using `aplay` reading the WAV from stdin.
func (a *ALSA) Play(ctx context.Context, wav []byte) error {
	a.pending.Add(1)
	defer a.pending.Add(-1)
	a.interruptStream() // sounds and speech always win over music
	a.playMu.Lock()
	defer a.playMu.Unlock()

	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.cancelMu.Lock()
	a.cancel = cancel
	a.cancelMu.Unlock()

	cmd := exec.CommandContext(pctx, "aplay", "-q", "-D", a.cfg.PlaybackDevice, "-t", "wav", "-")
	cmd.Stdin = bytes.NewReader(wav)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if pctx.Err() != nil {
			return nil // stopped on purpose
		}
		return fmt.Errorf("audio: aplay failed: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (a *ALSA) interruptStream() {
	a.streamMu.Lock()
	if a.streamCancel != nil {
		a.streamCancel()
	}
	a.streamMu.Unlock()
}

// Busy implements Audio.
func (a *ALSA) Busy() bool { return a.pending.Load() > 0 }

// PlayStream implements Audio by feeding raw PCM to `aplay`.
func (a *ALSA) PlayStream(ctx context.Context, s Stream) error {
	if s.SampleRate < 8000 || s.SampleRate > 192000 || (s.Channels != 1 && s.Channels != 2) {
		return fmt.Errorf("audio: unsupported stream format (%d Hz, %d channels)", s.SampleRate, s.Channels)
	}
	a.playMu.Lock()
	defer a.playMu.Unlock()
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.streamMu.Lock()
	a.streamCancel = cancel
	a.streamMu.Unlock()
	defer func() {
		a.streamMu.Lock()
		a.streamCancel = nil
		a.streamMu.Unlock()
	}()
	a.cancelMu.Lock()
	a.cancel = cancel // Stop() ends music too
	a.cancelMu.Unlock()

	cmd := exec.CommandContext(sctx, "aplay", "-q", "-D", a.cfg.PlaybackDevice, "-t", "raw", "-f", "S16_LE",
		"-r", fmt.Sprint(s.SampleRate), "-c", fmt.Sprint(s.Channels), "-")
	cmd.Stdin = s.Reader
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if sctx.Err() != nil {
			return nil // interrupted on purpose
		}
		return fmt.Errorf("audio: aplay failed: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// PlayNotification implements Audio.
func (a *ALSA) PlayNotification(ctx context.Context, n Notification, volume int) error {
	return a.Play(ctx, Scale(NotificationWAV(n), volume))
}

// Stop implements Audio.
func (a *ALSA) Stop() {
	a.cancelMu.Lock()
	defer a.cancelMu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}

// SetVolume implements Audio using `amixer sset`.
func (a *ALSA) SetVolume(ctx context.Context, percent int) error {
	percent = max(0, min(100, percent))
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "amixer", "-q", "-c", a.cfg.MixerCard,
		"sset", a.cfg.MixerControl, fmt.Sprintf("%d%%", percent)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("audio: amixer failed: %s", strings.TrimSpace(string(out)))
	}
	a.volMu.Lock()
	a.volume = percent
	a.volMu.Unlock()
	return nil
}

// Status implements Audio.
func (a *ALSA) Status(ctx context.Context) Status {
	a.volMu.Lock()
	vol := a.volume
	a.volMu.Unlock()
	st := Status{Backend: "alsa", Volume: max(vol, 0)}
	for _, tool := range []string{"aplay", "arecord", "amixer"} {
		if _, err := exec.LookPath(tool); err != nil {
			st.Detail = tool + " not found (install alsa-utils)"
			return st
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "aplay", "-l").Output()
	switch {
	case err != nil:
		st.Detail = "cannot list sound cards"
	case !strings.Contains(string(out), "card"):
		st.Detail = "no sound card detected"
	case !strings.Contains(string(out), strings.TrimPrefix(a.cfg.PlaybackDevice, "plughw:")) &&
		strings.Contains(a.cfg.PlaybackDevice, "wm8960"):
		st.Detail = "WM8960 card not found; check the device tree overlay (docs/hardware.md)"
	default:
		st.Available = true
	}
	return st
}
