package audio

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os/exec"
	"regexp"
	"sort"
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
	deviceRe  = regexp.MustCompile(`^[A-Za-z0-9_:,=.][A-Za-z0-9_:,=.\-]{0,63}$`)
	cardRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_\-]{0,63}$`)
	controlRe = regexp.MustCompile(`^[A-Za-z0-9 _][A-Za-z0-9 _\-]{0,63}$`)
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

	volBackend atomic.Value // name of the volume backend that worked last
	volMu      sync.Mutex
	volume     int
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

// speechTracker decides, chunk by chunk, whether someone is speaking and when they have finished.
// A fixed level does not work in a real room: with a noise floor of 350 and bursts of 1000 from the
// street or a fan, "quiet" (below 600) never lasted 1.5 s, and every recording ran to its 20 s limit.
// So the threshold follows the room (about twice the quiet fifth of what has been heard, between 250
// and one and a half times the configured level), speech needs a few loud chunks close together, and
// the end is a mostly quiet stretch that a stray burst does not restart.
type speechTracker struct {
	base      float64   // configured threshold
	levels    []float64 // RMS of every chunk so far
	flags     []bool    // speech-like flags of the last few chunks
	window    []bool    // speech-like flags of the last silentChunks chunks
	silentFor int       // chunks of mostly-quiet that end a phrase
	heard     bool
}

const (
	speechRun  = 6 // chunks looked at to decide that speech began...
	speechNeed = 3 // ...of which this many must be loud
)

func newSpeechTracker(base, silentChunks int) *speechTracker {
	return &speechTracker{base: float64(base), silentFor: max(silentChunks, 1)}
}

// threshold is the level a chunk must reach to count as speech.
func (t *speechTracker) threshold() float64 {
	if len(t.levels) < 10 {
		return t.base
	}
	sorted := append([]float64(nil), t.levels...)
	sort.Float64s(sorted)
	floor := sorted[int(float64(len(sorted)-1)*0.2)]
	return math.Max(250, math.Min(floor*2.2, t.base*1.5))
}

// add takes the level of the next chunk and reports whether the phrase has ended.
func (t *speechTracker) add(level float64) (ended bool) {
	loud := level >= t.threshold()
	t.levels = append(t.levels, level)
	t.flags = append(t.flags, loud)
	if len(t.flags) > speechRun {
		t.flags = t.flags[1:]
	}
	if !t.heard {
		n := 0
		for _, f := range t.flags {
			if f {
				n++
			}
		}
		t.heard = n >= speechNeed
	}
	t.window = append(t.window, loud)
	if len(t.window) > t.silentFor {
		t.window = t.window[1:]
	}
	if !t.heard || len(t.window) < t.silentFor || loud {
		return false
	}
	n := 0
	for _, f := range t.window {
		if f {
			n++
		}
	}
	return n <= t.silentFor/6
}

// speechIn reports whether PCM holds speech by the same rules, for recordings that are already made.
func speechIn(pcm []byte, base int) bool {
	t := newSpeechTracker(base, 1<<30)
	chunk := int(chunkDuration.Seconds()*SampleRate) * 2
	for i := 0; i+chunk <= len(pcm); i += chunk {
		t.add(rms(pcm[i : i+chunk]))
	}
	return t.heard
}

// HasSpeech reports whether a recording (a WAV) holds speech and not only the noise of the room.
func (a *ALSA) HasSpeech(wav []byte) bool {
	pcm, _, err := DecodeWAV(wav)
	return err != nil || speechIn(pcm, RecordOptions{}.withDefaults().Threshold)
}

// recordFrom reads 16 kHz mono PCM from r. It returns when r ends, ctx ends, the
// maximum length is reached or (if enabled) silence follows speech. stop is
// called to terminate the source. stopped reports whether we ended it ourselves.
func recordFrom(ctx context.Context, r io.Reader, opts RecordOptions, stop func()) (pcm []byte, stopped bool) {
	chunkBytes := int(chunkDuration.Seconds()*SampleRate) * 2
	maxBytes := int(opts.MaxDuration.Seconds()*SampleRate) * 2
	tr := newSpeechTracker(opts.Threshold, int(opts.SilenceFor/chunkDuration))
	noSpeechGiveUp := int(opts.NoSpeechFor / chunkDuration)
	if noSpeechGiveUp <= 0 {
		noSpeechGiveUp = 80 // 8 s of nothing but quiet
	}

	buf := make([]byte, chunkBytes)
	chunks := 0
	for ctx.Err() == nil {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			pcm = append(pcm, buf[:n]...)
			if opts.StopOnSilence && n == chunkBytes {
				chunks++
				if tr.add(rms(buf[:n])) || (!tr.heard && chunks >= noSpeechGiveUp) {
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

// SetVolume implements Audio. See volume.go for the backends it falls back to.
func (a *ALSA) SetVolume(ctx context.Context, percent int) error {
	percent = max(0, min(100, percent))
	if _, err := a.setVolume(ctx, percent); err != nil {
		return err
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
