package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/audio"
)

// Spotify settings (system_settings keys).
const (
	SettingSpotifyBinary = "spotify.binary"
	SettingSpotifyVoice  = "spotify.voice_control" // "1" lets the assistant control Spotify; off by default
	// SettingMusicSource says where "play some music" comes from: "spotify" (the default, used whenever
	// Spotify is connected) or "local" (only the family's music folder).
	SettingMusicSource = "music.source"
	// SettingSpotifyDefaultPlaylist holds words from the name of the playlist "play some music" starts
	// when nothing is paused; empty means the account's first playlist.
	SettingSpotifyDefaultPlaylist = "spotify.default_playlist"

	// DefaultSpotifyBinary is where `milo install` puts the go-librespot release binary.
	DefaultSpotifyBinary = "/usr/local/bin/go-librespot"

	// go-librespot's pipe backend writes 44.1 kHz stereo signed 16-bit little-endian PCM.
	spotifyRate     = 44100
	spotifyChannels = 2
)

// Spotify is the "spotify" feature: it runs go-librespot (a Spotify Connect receiver the family
// installs themselves; Milo never ships it) as a child process, so "Milo" shows up as a speaker in
// the Spotify app. The receiver's audio goes through a named pipe into Milo's own player, so Milo
// keeps the sound card and Spotify gives way to notifications, speech and conversations exactly
// like the local music does. Spotify Premium is required by Spotify itself.
type Spotify struct {
	Common
	Audio   audio.Audio
	DataDir string // the receiver keeps its files in <DataDir>/spotify
	// Idle and QuietFor work as for Music: a paused stream resumes once the device was quiet.
	Idle     func() bool
	QuietFor time.Duration
	// OnPlay runs when Spotify starts playing, so the local music can stop.
	OnPlay func()
	// DeviceNameOf gives the device name the Spotify app shows (default "Milo").
	DeviceNameOf func(ctx context.Context) string
	// APIBase replaces the receiver's local address and PollEvery the status polling (tests).
	APIBase   string
	PollEvery time.Duration
	// HungAfter is how long the receiver may run without answering its status before Milo restarts it
	// (default 30 s). go-librespot can wedge with its process alive and its port open.
	HungAfter time.Duration
	// WebAPIBase is Spotify's Web API address used to search for artists (tests replace it).
	WebAPIBase string

	mu       sync.Mutex
	killProc context.CancelFunc // stops the receiver process; procLoop then starts a new one
	procRun  runner
	pollRun  runner
	playRun  runner
	client   *spotifyClient
	fifo     *os.File
	snap     spotifySnap
	pump     context.CancelFunc
	wake     chan struct{}
	lastErr  string
}

type spotifySnap struct {
	up       bool // the receiver answers
	signedIn bool
	st       SpotifyStatus
}

// Name implements features.Feature.
func (s *Spotify) Name() string { return "spotify" }

func (s *Spotify) setting(ctx context.Context, key, def string) string {
	if v, err := s.Store.GetSetting(ctx, key); err == nil && v != "" {
		return v
	}
	return def
}

func (s *Spotify) binary(ctx context.Context) string {
	return s.setting(ctx, SettingSpotifyBinary, DefaultSpotifyBinary)
}

func (s *Spotify) voiceControl(ctx context.Context) bool {
	return s.setting(ctx, SettingSpotifyVoice, "0") == "1"
}

func (s *Spotify) dir() string { return filepath.Join(s.DataDir, "spotify") }

// binaryOK reports whether path is an executable regular file.
func binaryOK(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// Start implements features.Feature.
func (s *Spotify) Start(ctx context.Context) error {
	bin := s.binary(ctx)
	if !binaryOK(bin) {
		return fmt.Errorf("go-librespot is not installed at %s: run `sudo milo install` to fetch it (see docs/spotify.md)", bin)
	}
	dir := s.dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	os.Chmod(dir, 0o700) //nolint:errcheck // best effort on a directory we may not own
	pipe := filepath.Join(dir, "audio.pipe")
	os.Remove(pipe) //nolint:errcheck
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		return fmt.Errorf("cannot create the audio pipe: %w", err)
	}
	// Opened for reading and writing so it never blocks and never reports the end of the stream.
	f, err := os.OpenFile(pipe, os.O_RDWR, 0)
	if err != nil {
		return err
	}

	base := s.APIBase
	if base == "" {
		port, err := freeLocalPort()
		if err != nil {
			f.Close()
			return err
		}
		base = "http://127.0.0.1:" + strconv.Itoa(port)
		if err := writeSpotifyConfig(dir, s.deviceName(ctx), pipe, port); err != nil {
			f.Close()
			return err
		}
	} else if err := writeSpotifyConfig(dir, s.deviceName(ctx), pipe, 0); err != nil {
		f.Close()
		return err
	}

	s.mu.Lock()
	s.client, s.fifo, s.snap, s.lastErr = newSpotifyClient(base), f, spotifySnap{}, ""
	s.wake = make(chan struct{}, 1)
	s.mu.Unlock()

	s.procRun.start(func(c context.Context) { s.procLoop(c, bin, dir) })
	s.pollRun.start(s.pollLoop)
	s.playRun.start(s.playLoop)
	return nil
}

// Stop implements features.Feature.
func (s *Spotify) Stop(ctx context.Context) error {
	s.cancelPump()
	s.playRun.stop(ctx)
	s.pollRun.stop(ctx)
	s.procRun.stop(ctx)
	s.mu.Lock()
	if s.fifo != nil {
		s.fifo.Close()
		s.fifo = nil
	}
	s.client, s.snap = nil, spotifySnap{}
	s.mu.Unlock()
	return nil
}

func (s *Spotify) deviceName(ctx context.Context) string {
	name := "Milo"
	if s.DeviceNameOf != nil {
		if n := s.DeviceNameOf(ctx); n != "" {
			name = n
		}
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, name)
}

func freeLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// writeSpotifyConfig writes go-librespot's config.yml. Sign-in is by pairing code (device_auth):
// the person types a short code at spotify.com/pair, so the device needs no browser and Milo never
// sees a Spotify password. The volume is left to Milo's own volume (external_volume).
func writeSpotifyConfig(dir, name, pipe string, port int) error {
	var b strings.Builder
	fmt.Fprintf(&b, "device_name: \"%s\"\n", name)
	b.WriteString("device_type: speaker\n")
	b.WriteString("audio_backend: pipe\n")
	fmt.Fprintf(&b, "audio_output_pipe: \"%s\"\n", pipe)
	b.WriteString("audio_output_pipe_format: s16le\n")
	b.WriteString("external_volume: true\n")
	b.WriteString("zeroconf_enabled: false\n")
	b.WriteString("log_level: warn\n")
	b.WriteString("credentials:\n  type: device_auth\n")
	if port > 0 {
		fmt.Fprintf(&b, "server:\n  enabled: true\n  address: 127.0.0.1\n  port: %d\n", port)
	}
	return os.WriteFile(filepath.Join(dir, "config.yml"), []byte(b.String()), 0o600)
}

// ---------------------------------------------------------------- the receiver process

// spotifyBackoff is the first wait before a receiver that stopped is started again (tests shorten it).
var spotifyBackoff = 2 * time.Second

func (s *Spotify) procLoop(ctx context.Context, bin, dir string) {
	backoff := spotifyBackoff
	for ctx.Err() == nil {
		started := time.Now()
		err := s.runProcess(ctx, bin, dir)
		if ctx.Err() != nil {
			return
		}
		msg := "go-librespot exited"
		if err != nil {
			msg = "go-librespot stopped: " + err.Error()
		}
		s.mu.Lock()
		s.lastErr = msg
		s.mu.Unlock()
		s.Log.Warn("spotify: receiver stopped, restarting", "reason", msg, "in", backoff.String())
		if time.Since(started) > time.Minute {
			backoff = spotifyBackoff
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (s *Spotify) runProcess(ctx context.Context, bin, dir string) error {
	ctx, kill := context.WithCancel(ctx)
	defer kill()
	s.mu.Lock()
	s.killProc = kill
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.killProc = nil
		s.mu.Unlock()
	}()
	cmd := exec.CommandContext(ctx, bin, "--config_dir", dir) // GNU-style: "-config_dir" is parsed as -c onfig_dir
	cmd.Dir = dir
	// A clean environment: the receiver needs nothing from Milo's (no keys, no paths).
	cmd.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin"}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	w := &lineLogger{log: s.Log}
	cmd.Stdout, cmd.Stderr = w, w
	return cmd.Run()
}

// lineLogger passes the receiver's output to Milo's debug log, line by line.
type lineLogger struct {
	log *slog.Logger
	buf []byte
}

func (l *lineLogger) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.log.Debug("go-librespot", "line", strings.TrimSpace(string(l.buf[:i])))
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > 8<<10 {
		l.buf = nil
	}
	return len(p), nil
}

// ---------------------------------------------------------------- status polling

func (s *Spotify) pollLoop(ctx context.Context) {
	every := s.PollEvery
	if every <= 0 {
		every = time.Second
	}
	hung := s.HungAfter
	if hung <= 0 {
		hung = 30 * time.Second
	}
	var failingSince time.Time
	for ctx.Err() == nil {
		s.mu.Lock()
		c := s.client
		s.mu.Unlock()
		if c != nil {
			rctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			st, err := c.Status(rctx)
			cancel()
			s.apply(st, err)
			// "Not signed in" is an answer; a timeout or an error status is a receiver that is stuck.
			if err != nil && !errors.Is(err, errSpotifyNoSession) {
				if failingSince.IsZero() {
					failingSince = time.Now()
				} else if time.Since(failingSince) > hung {
					s.restartReceiver()
					failingSince = time.Time{}
				}
			} else {
				failingSince = time.Time{}
			}
		}
		if !sleepCtx(ctx, every) {
			return
		}
	}
}

// restartReceiver stops a receiver that has stopped answering; procLoop starts a fresh one.
func (s *Spotify) restartReceiver() {
	s.mu.Lock()
	kill := s.killProc
	s.mu.Unlock()
	if kill != nil {
		s.Log.Warn("spotify: the receiver is not answering, restarting it")
		kill()
	}
}

// apply records a status answer and reacts to the receiver starting or stopping playback.
func (s *Spotify) apply(st SpotifyStatus, err error) {
	s.mu.Lock()
	wasPlaying := s.snap.signedIn && s.snap.st.playing()
	s.snap = spotifySnap{up: err == nil || errors.Is(err, errSpotifyNoSession), signedIn: err == nil, st: st}
	playing := s.snap.signedIn && st.playing()
	var cancel context.CancelFunc
	if !playing {
		cancel = s.pump // paused or stopped from the phone: end the stream
		s.pump = nil
	}
	wake := s.wake
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if playing && !wasPlaying {
		if s.OnPlay != nil {
			s.OnPlay()
		}
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (s *Spotify) isPlaying() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap.signedIn && s.snap.st.playing()
}

// Active reports whether Spotify is playing now.
func (s *Spotify) Active() bool { return s.isPlaying() }

func (s *Spotify) cancelPump() {
	s.mu.Lock()
	cancel := s.pump
	s.pump = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ---------------------------------------------------------------- playing the receiver's audio

func (s *Spotify) waitPlaying(ctx context.Context) bool {
	for {
		if s.isPlaying() {
			return true
		}
		s.mu.Lock()
		wake := s.wake
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-wake:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *Spotify) playLoop(ctx context.Context) {
	for ctx.Err() == nil {
		if !s.waitPlaying(ctx) {
			return
		}
		s.mu.Lock()
		f := s.fifo
		label := "Spotify"
		if t := s.snap.st.Track; t != nil && t.Name != "" {
			label = t.Name
		}
		pctx, cancel := context.WithCancel(ctx)
		s.pump = cancel
		s.mu.Unlock()
		if f == nil {
			cancel()
			return
		}
		drainPipe(f) // anything left from before a pause is stale
		err := s.Audio.PlayStream(pctx, audio.Stream{Reader: &pipeReader{f: f, ctx: pctx}, SampleRate: spotifyRate, Channels: spotifyChannels, Label: label})
		cancel()
		if ctx.Err() != nil {
			return
		}
		switch {
		case !s.isPlaying():
			// paused or stopped by someone: wait for the next time it plays
		case err != nil:
			s.Log.Warn("spotify: playback failed", "error", err)
			if !sleepCtx(ctx, 2*time.Second) {
				return
			}
		default:
			s.yield(ctx) // something more important took the speaker
		}
		sleepCtx(ctx, 100*time.Millisecond)
	}
}

// yield pauses Spotify while a notification, spoken answer or conversation has the speaker and
// resumes it afterwards, like the local music does. Spotify is paused rather than mixed under the
// other sound.
func (s *Spotify) yield(ctx context.Context) {
	s.mu.Lock()
	c := s.client
	s.mu.Unlock()
	if c == nil {
		return
	}
	pc, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := c.Pause(pc)
	cancel()
	if err != nil {
		s.Log.Debug("spotify: could not pause for a notification", "error", err)
		return
	}
	if !waitUntilQuiet(ctx, s.idle(), s.QuietFor) {
		return // stays paused; the family can press play in the Spotify app
	}
	rc, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Resume(rc); err != nil {
		s.Log.Debug("spotify: could not resume", "error", err)
	}
}

func (s *Spotify) idle() func() bool {
	if s.Idle != nil {
		return s.Idle
	}
	return func() bool { return s.Audio == nil || !s.Audio.Busy() }
}

// pipeReader reads the receiver's PCM from the pipe and gives up when its stream is cancelled, so
// a cancelled stream never keeps a read pending that would swallow the next stream's audio.
type pipeReader struct {
	f   *os.File
	ctx context.Context
}

func (r *pipeReader) Read(p []byte) (int, error) {
	for {
		if r.ctx.Err() != nil {
			return 0, io.EOF
		}
		r.f.SetReadDeadline(time.Now().Add(200 * time.Millisecond)) //nolint:errcheck // a pipe supports deadlines
		n, err := r.f.Read(p)
		if n > 0 {
			return n, nil
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			continue
		}
		if err != nil {
			return 0, err
		}
	}
}

// drainPipe throws away what is waiting in the pipe (at most the pipe's 64 KiB: a receiver that is
// playing refills it at once, so draining until empty would never end).
func drainPipe(f *os.File) {
	buf := make([]byte, 16<<10)
	for range 4 {
		f.SetReadDeadline(time.Now().Add(15 * time.Millisecond)) //nolint:errcheck
		if n, err := f.Read(buf); n == 0 || err != nil {
			return
		}
	}
}

// ---------------------------------------------------------------- control

func (s *Spotify) api() (*spotifyClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil || !s.snap.signedIn {
		return nil, errors.New("Spotify is not connected yet: a parent can pair it in the admin")
	}
	return s.client, nil
}

// Pause pauses Spotify for good (until someone plays it again). It is always allowed.
func (s *Spotify) Pause(ctx context.Context) error {
	s.cancelPump()
	s.mu.Lock()
	s.snap.st.Paused = true // so nothing resumes it while the answer is on its way
	c := s.client
	s.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.Pause(ctx)
}

// blocked returns the sentence to say when the assistant may not control Spotify.
func (s *Spotify) blocked(ctx context.Context, actor actions.Actor) string {
	if actor.Kind == actions.ActorAssistant && !s.voiceControl(ctx) {
		return "Spotify voice control is switched off. A parent can turn it on in the admin."
	}
	return ""
}

// Skip skips to the next Spotify track, for the music actions when Spotify is what is playing.
func (s *Spotify) Skip(ctx context.Context, actor actions.Actor) (string, error) {
	if msg := s.blocked(ctx, actor); msg != "" {
		return msg, nil
	}
	c, err := s.api()
	if err != nil {
		return err.Error(), nil
	}
	if err := c.Next(ctx); err != nil {
		return "", err
	}
	return "Next song.", nil
}

// PlayAnything is "play some music" when Spotify is the music source. It returns handled=false when
// Spotify is not the right place to play (switched to local, not running, not paired), so the local
// music takes over. Otherwise it plays: a playlist whose name matches the words, else whatever was
// paused, else the default or first playlist. When it cannot, it says why instead of failing.
func (s *Spotify) PlayAnything(ctx context.Context, actor actions.Actor, query string) (actions.Result, bool, error) {
	if s.setting(ctx, SettingMusicSource, "spotify") == "local" {
		return actions.Result{}, false, nil
	}
	s.mu.Lock()
	up, signedIn, loaded := s.snap.up, s.snap.signedIn, s.snap.st.Track != nil
	s.mu.Unlock()
	if !up || !signedIn {
		return actions.Result{}, false, nil
	}
	if msg := s.blocked(ctx, actor); msg != "" {
		return actions.Result{Message: msg, Refused: true}, true, nil
	}
	c, err := s.api()
	if err != nil {
		return actions.Result{Message: err.Error(), Refused: true}, true, nil
	}
	play := func(uri string) error {
		if s.OnPlay != nil {
			s.OnPlay() // the local music gives way
		}
		return c.Play(ctx, uri)
	}
	lists, err := c.Playlists(ctx)
	if err != nil {
		return actions.Result{}, true, err
	}
	names := make([]string, len(lists))
	for i, l := range lists {
		names[i] = l.Name
	}
	if i, ok := bestWordMatch(names, query); ok {
		if err := play(lists[i].URI); err != nil {
			return actions.Result{}, true, err
		}
		return actions.Result{Message: "Playing " + lists[i].Name + ".", Data: map[string]any{"playlist": lists[i].Name}}, true, nil
	}
	// Words that are not a playlist are an artist ("música de Belinda"): search for them.
	if len(words(query)) > 0 {
		base := s.WebAPIBase
		if base == "" {
			base = "https://api.spotify.com"
		}
		sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		name, uri, err := c.searchArtist(sctx, base, strings.Join(words(query), " "))
		cancel()
		switch {
		case err == nil:
			if err := play(uri); err != nil {
				return actions.Result{}, true, err
			}
			return actions.Result{Message: "Playing music by " + name + ".", Data: map[string]any{"artist": name}}, true, nil
		case errors.Is(err, errSpotifyNoArtist):
			return actions.Result{Message: fmt.Sprintf("I could not find %q on Spotify.", query), Refused: true}, true, nil
		default:
			s.Log.Info("spotify: artist search failed", "error", err)
			return actions.Result{Message: fmt.Sprintf("I could not look up %q on Spotify right now; asking for one of the family's playlists by name works.", query), Refused: true}, true, nil
		}
	}
	if loaded {
		if s.OnPlay != nil {
			s.OnPlay()
		}
		if err := c.Resume(ctx); err == nil {
			return actions.Result{Message: "Playing."}, true, nil
		}
	}
	if len(lists) == 0 {
		return actions.Result{Message: "I could not find any playlist on the paired Spotify account. Make one in the Spotify app first.", Refused: true}, true, nil
	}
	pick := 0
	if i, ok := bestWordMatch(names, s.setting(ctx, SettingSpotifyDefaultPlaylist, "")); ok {
		pick = i
	}
	if err := play(lists[pick].URI); err != nil {
		return actions.Result{}, true, err
	}
	return actions.Result{Message: "Playing " + lists[pick].Name + ".", Data: map[string]any{"playlist": lists[pick].Name}}, true, nil
}

// YieldToLocal pauses Spotify because the local music is about to play.
func (s *Spotify) YieldToLocal() {
	if !s.isPlaying() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.Pause(ctx) //nolint:errcheck // best effort: the music starts either way
}

// SignOut forgets the paired account: the receiver is stopped and its saved sign-in deleted. The
// caller restarts the feature if it is on, which shows a new pairing code.
func (s *Spotify) SignOut(ctx context.Context) error {
	if err := s.Stop(ctx); err != nil {
		return err
	}
	for _, name := range []string{"state.json", "credentials.json"} {
		if err := os.Remove(filepath.Join(s.dir(), name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// SpotifyInfo describes the receiver for the admin.
type SpotifyInfo struct {
	Binary       string          `json:"binary"`
	BinaryFound  bool            `json:"binary_found"`
	DeviceName   string          `json:"device_name"`
	Running      bool            `json:"running"`
	SignedIn     bool            `json:"signed_in"`
	Username     string          `json:"username,omitempty"`
	Playing      bool            `json:"playing"`
	Track        string          `json:"track,omitempty"`
	Pairing      *SpotifyPairing `json:"pairing,omitempty"`
	VoiceControl bool            `json:"voice_control"`
	Error        string          `json:"error,omitempty"`
}

// Info reports the receiver's state.
func (s *Spotify) Info(ctx context.Context) SpotifyInfo {
	bin := s.binary(ctx)
	info := SpotifyInfo{Binary: bin, BinaryFound: binaryOK(bin), DeviceName: s.deviceName(ctx), VoiceControl: s.voiceControl(ctx)}
	s.mu.Lock()
	snap, c, lastErr := s.snap, s.client, s.lastErr
	s.mu.Unlock()
	info.Running, info.SignedIn, info.Error = snap.up, snap.signedIn, lastErr
	if snap.signedIn {
		info.Username, info.Playing = snap.st.Username, snap.st.playing()
		if t := snap.st.Track; t != nil {
			info.Track = strings.Join(append([]string{t.Name}, t.ArtistNames...), " · ")
		}
	} else if c != nil && snap.up {
		pc, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if p, err := c.AuthCode(pc); err == nil {
			info.Pairing = &p
		}
	}
	return info
}

// RegisterActions registers the Spotify actions. Pausing is always allowed; the other controls
// work for the assistant only when a parent switched voice control on.
func (s *Spotify) RegisterActions(e *actions.Engine) {
	both := actions.ExposeDevice | actions.ExposeAssistant
	control := func(name, desc string, do func(ctx context.Context, c *spotifyClient) error, say string) {
		e.MustRegister(actions.Action{
			Name: name, Description: desc, Feature: "spotify", Exposure: both,
			Handler: func(ctx context.Context, call actions.Call) (actions.Result, error) {
				if msg := s.blocked(ctx, call.Actor); msg != "" {
					return actions.Result{Message: msg, Refused: true}, nil
				}
				c, err := s.api()
				if err != nil {
					return actions.Result{Message: err.Error(), Refused: true}, nil
				}
				if err := do(ctx, c); err != nil {
					return actions.Result{}, err
				}
				return actions.Result{Message: say}, nil
			},
		})
	}
	e.MustRegister(actions.Action{
		Name: "spotify_pause", Description: "Pause Spotify.", Feature: "spotify", Exposure: both,
		BypassSchedule: true, // the child can always stop it
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			if !s.Active() {
				return actions.Result{Message: "Spotify is not playing."}, nil
			}
			if err := s.Pause(ctx); err != nil {
				return actions.Result{}, err
			}
			return actions.Result{Message: "Paused."}, nil
		},
	})
	control("spotify_resume", "Resume Spotify after a pause.", func(ctx context.Context, c *spotifyClient) error { return c.Resume(ctx) }, "Playing.")
	control("spotify_next", "Skip to the next Spotify song.", func(ctx context.Context, c *spotifyClient) error { return c.Next(ctx) }, "Next song.")
	control("spotify_previous", "Go back to the previous Spotify song.", func(ctx context.Context, c *spotifyClient) error { return c.Prev(ctx) }, "Previous song.")
	e.MustRegister(actions.Action{
		Name:        "spotify_play_playlist",
		Description: "Play one specific Spotify playlist by name, when the person asks for that playlist. For a plain request to play music use play_music instead. Only the family's own playlists can be played: searching for any song or artist on Spotify is not possible.",
		Feature:     "spotify", Exposure: both,
		Params: []actions.Param{{Name: "name", Description: "Words from the playlist's name", Type: actions.String, MaxLen: 100, Required: true}},
		Handler: func(ctx context.Context, call actions.Call) (actions.Result, error) {
			if msg := s.blocked(ctx, call.Actor); msg != "" {
				return actions.Result{Message: msg, Refused: true}, nil
			}
			c, err := s.api()
			if err != nil {
				return actions.Result{Message: err.Error(), Refused: true}, nil
			}
			lists, err := c.Playlists(ctx)
			if err != nil {
				return actions.Result{}, err
			}
			names := make([]string, len(lists))
			for i, l := range lists {
				names[i] = l.Name
			}
			i, ok := bestWordMatch(names, call.Args.String("name"))
			if !ok {
				return actions.Result{Message: fmt.Sprintf("I could not find a playlist called %q.", call.Args.String("name")), Refused: true}, nil
			}
			if s.OnPlay != nil {
				s.OnPlay() // the local music gives way
			}
			if err := c.Play(ctx, lists[i].URI); err != nil {
				return actions.Result{}, err
			}
			return actions.Result{Message: "Playing " + lists[i].Name + ".", Data: map[string]any{"playlist": lists[i].Name}}, nil
		},
	})
}

// bestWordMatch picks the text that contains the most of the query's words (filler such as
// "play" is ignored); at least half of the words must match, as for the local music.
func bestWordMatch(texts []string, query string) (int, bool) {
	qw := words(query)
	if len(qw) == 0 {
		return 0, false
	}
	best, bestScore := -1, 0
	for i, t := range texts {
		h, score := strings.ToLower(t), 0
		for _, w := range qw {
			if strings.Contains(h, w) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 || bestScore*2 < len(qw) {
		return 0, false
	}
	return best, true
}
