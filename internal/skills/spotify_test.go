package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/audio"
)

// fakeLibrespot stands in for go-librespot's local REST API.
type fakeLibrespot struct {
	mu        sync.Mutex
	signedIn  bool
	stopped   bool
	paused    bool
	track     string
	pairing   *SpotifyPairing
	playlists []spotifyPlaylist
	calls     []string
	srv       *httptest.Server
}

func newFakeLibrespot(t *testing.T) *fakeLibrespot {
	f := &fakeLibrespot{signedIn: true, stopped: true}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.signedIn {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		st := map[string]any{"username": "family", "device_name": "Milo", "stopped": f.stopped, "paused": f.paused}
		if f.track != "" {
			st["track"] = map[string]any{"name": f.track, "artist_names": []string{"Somebody"}}
		}
		json.NewEncoder(w).Encode(st)
	})
	mux.HandleFunc("GET /auth/code", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.pairing == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		json.NewEncoder(w).Encode(f.pairing)
	})
	mux.HandleFunc("GET /library/playlists", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"items": f.playlists})
	})
	act := func(name string, apply func(body map[string]any)) {
		mux.HandleFunc("POST /player/"+name, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck // some calls have no body
			f.mu.Lock()
			f.calls = append(f.calls, name)
			if name == "play" {
				f.calls[len(f.calls)-1] = "play " + body["uri"].(string)
			}
			apply(body)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		})
	}
	act("pause", func(map[string]any) { f.paused = true })
	act("resume", func(map[string]any) { f.paused, f.stopped = false, false })
	act("next", func(map[string]any) {})
	act("prev", func(map[string]any) {})
	act("play", func(map[string]any) { f.paused, f.stopped = false, false })
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLibrespot) set(fn func()) {
	f.mu.Lock()
	fn()
	f.mu.Unlock()
}

func (f *fakeLibrespot) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeLibrespot) count(name string) int {
	n := 0
	for _, c := range f.called() {
		if c == name {
			n++
		}
	}
	return n
}

type spotifyRig struct {
	*rig
	sp    *Spotify
	fake  *fakeLibrespot
	mock  *audio.Mock
	bin   string
	dir   string
	plays int32
}

// fakeBinary is a shell script that records how it was started and then idles like a receiver.
func fakeBinary(t *testing.T, dir string, exitNow bool) string {
	t.Helper()
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"" + dir + "/args\"\necho $$ > \"" + dir + "/pid\"\n"
	if exitNow {
		body += "exit 3\n"
	} else {
		body += "exec sleep 60\n"
	}
	p := filepath.Join(dir, "go-librespot")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func newSpotifyRig(t *testing.T, exitNow bool) *spotifyRig {
	t.Helper()
	r := newRig(t)
	dir := t.TempDir()
	sr := &spotifyRig{rig: r, fake: newFakeLibrespot(t), mock: audio.NewMock(), dir: dir}
	sr.mock.StreamSpeed = 50
	sr.bin = fakeBinary(t, dir, exitNow)
	if err := r.store.SetSetting(context.Background(), SettingSpotifyBinary, sr.bin); err != nil {
		t.Fatal(err)
	}
	spotifyBackoff = 20 * time.Millisecond
	t.Cleanup(func() { spotifyBackoff = 2 * time.Second })
	sr.sp = &Spotify{Common: r.common, Audio: sr.mock, DataDir: dir, APIBase: sr.fake.srv.URL,
		PollEvery: 10 * time.Millisecond, QuietFor: 30 * time.Millisecond,
		OnPlay: func() { sr.plays++ }}
	sr.sp.RegisterActions(r.eng)
	return sr
}

func (sr *spotifyRig) start(t *testing.T) {
	t.Helper()
	if err := sr.sp.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sr.sp.Stop(context.Background()) }) //nolint:errcheck
}

// feed writes silence into the receiver's pipe, as go-librespot would while playing.
func (sr *spotifyRig) feed(t *testing.T) (stop func()) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(sr.dir, "spotify", "audio.pipe"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		chunk := make([]byte, 17640)
		for {
			select {
			case <-done:
				return
			default:
			}
			f.SetWriteDeadline(time.Now().Add(50 * time.Millisecond)) //nolint:errcheck
			f.Write(chunk)                                            //nolint:errcheck // a full pipe just times out
		}
	}()
	return func() { close(done); wg.Wait(); f.Close() }
}

func (sr *spotifyRig) run(actor actions.ActorKind, name string, args map[string]any) (actions.Result, error) {
	return sr.eng.Execute(context.Background(), actions.Actor{Kind: actor}, name, args)
}

func musicStreams(m *audio.Mock) int {
	n := 0
	for _, p := range m.Played() {
		if p.Kind == "music" {
			n++
		}
	}
	return n
}

func TestSpotifyMissingBinaryIsAFriendlyError(t *testing.T) {
	r := newRig(t)
	r.store.SetSetting(context.Background(), SettingSpotifyBinary, filepath.Join(t.TempDir(), "nope")) //nolint:errcheck
	sp := &Spotify{Common: r.common, Audio: audio.NewMock(), DataDir: t.TempDir()}
	err := sp.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "go-librespot is not installed") || !strings.Contains(err.Error(), "docs/spotify.md") {
		t.Fatalf("error = %v", err)
	}
	if info := sp.Info(context.Background()); info.BinaryFound || info.Running {
		t.Fatalf("info = %+v", info)
	}
}

func TestSpotifyStartsTheReceiverWithAPrivateConfigAndStopsIt(t *testing.T) {
	sr := newSpotifyRig(t, false)
	sr.start(t)
	cfgDir := filepath.Join(sr.dir, "spotify")
	waitFor(t, "the receiver to start", func() bool { _, err := os.Stat(filepath.Join(sr.dir, "pid")); return err == nil })

	args, _ := os.ReadFile(filepath.Join(sr.dir, "args"))
	if !strings.Contains(string(args), "--config_dir\n"+cfgDir) {
		t.Fatalf("args = %q", args)
	}
	if fi, err := os.Stat(cfgDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("config dir = %v %v, want mode 0700", fi, err)
	}
	cfg, _ := os.ReadFile(filepath.Join(cfgDir, "config.yml"))
	for _, want := range []string{"audio_backend: pipe", "audio_output_pipe_format: s16le", "type: device_auth", "device_name: \"Milo\"", "external_volume: true"} {
		if !strings.Contains(string(cfg), want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
	pidText, _ := os.ReadFile(filepath.Join(sr.dir, "pid"))
	pid := 0
	for _, c := range strings.TrimSpace(string(pidText)) {
		pid = pid*10 + int(c-'0')
	}
	if err := sr.sp.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the receiver to be gone", func() bool { return syscall.Kill(pid, 0) != nil })
}

func TestSpotifyRestartsAReceiverThatExits(t *testing.T) {
	sr := newSpotifyRig(t, true)
	sr.start(t)
	waitFor(t, "a second start", func() bool {
		b, _ := os.ReadFile(filepath.Join(sr.dir, "args"))
		return strings.Count(string(b), "--config_dir") >= 2
	})
	waitFor(t, "the reason to be kept", func() bool { return strings.Contains(sr.sp.Info(context.Background()).Error, "go-librespot") })
}

func TestSpotifyPlaysThePipeAndStopsLocalMusic(t *testing.T) {
	sr := newSpotifyRig(t, false)
	sr.start(t)
	stop := sr.feed(t)
	defer stop()
	sr.fake.set(func() { sr.fake.stopped, sr.fake.paused, sr.fake.track = false, false, "Twinkle" })

	waitFor(t, "the stream to play", func() bool { return musicStreams(sr.mock) >= 1 })
	if got := playedMusic(sr.mock); got[0] != "Twinkle" {
		t.Fatalf("label = %v", got)
	}
	waitFor(t, "OnPlay", func() bool { return sr.plays >= 1 })
	if info := sr.sp.Info(context.Background()); !info.SignedIn || !info.Playing || !strings.Contains(info.Track, "Twinkle") {
		t.Fatalf("info = %+v", info)
	}
}

func TestSpotifyPausesForANotificationAndResumesWhenQuiet(t *testing.T) {
	sr := newSpotifyRig(t, false)
	sr.start(t)
	stop := sr.feed(t)
	defer stop()
	sr.fake.set(func() { sr.fake.stopped, sr.fake.paused, sr.fake.track = false, false, "Twinkle" })
	waitFor(t, "the stream to play", func() bool { return musicStreams(sr.mock) >= 1 })

	sr.mock.PlayNotification(context.Background(), audio.NotifyMessage, 100) //nolint:errcheck // interrupts the stream

	waitFor(t, "Spotify to be paused", func() bool { return sr.fake.count("pause") >= 1 })
	waitFor(t, "Spotify to resume", func() bool { return sr.fake.count("resume") >= 1 })
	waitFor(t, "the stream to start again", func() bool { return musicStreams(sr.mock) >= 2 })
}

func TestSpotifyPausedFromThePhoneIsNotResumed(t *testing.T) {
	sr := newSpotifyRig(t, false)
	sr.start(t)
	stop := sr.feed(t)
	defer stop()
	sr.fake.set(func() { sr.fake.stopped, sr.fake.paused, sr.fake.track = false, false, "Twinkle" })
	waitFor(t, "the stream to play", func() bool { return musicStreams(sr.mock) >= 1 })

	sr.fake.set(func() { sr.fake.paused = true })
	time.Sleep(300 * time.Millisecond)
	if n := sr.fake.count("resume"); n != 0 {
		t.Fatalf("resumed %d times after the family paused it", n)
	}
	if n := sr.fake.count("pause"); n != 0 {
		t.Fatalf("Milo sent %d pauses for a pause it did not cause", n)
	}
	if sr.sp.Active() {
		t.Fatal("still active after a pause")
	}
}

func TestSpotifyVoiceControlIsOffByDefaultForTheAssistant(t *testing.T) {
	sr := newSpotifyRig(t, false)
	sr.start(t)
	waitFor(t, "the receiver to answer", func() bool { return sr.sp.Info(context.Background()).SignedIn })

	res, err := sr.run(actions.ActorAssistant, "spotify_next", nil)
	if err != nil || !strings.Contains(res.Message, "voice control is switched off") || sr.fake.count("next") != 0 {
		t.Fatalf("assistant next = %q %v calls=%v", res.Message, err, sr.fake.called())
	}
	// Physical buttons are not voice control.
	if _, err := sr.run(actions.ActorDevice, "spotify_next", nil); err != nil || sr.fake.count("next") != 1 {
		t.Fatalf("device next: %v calls=%v", err, sr.fake.called())
	}
	sr.store.SetSetting(context.Background(), SettingSpotifyVoice, "1") //nolint:errcheck
	for _, name := range []string{"spotify_next", "spotify_previous", "spotify_resume"} {
		if res, err := sr.run(actions.ActorAssistant, name, nil); err != nil || strings.Contains(res.Message, "switched off") {
			t.Fatalf("%s with voice control on: %q %v", name, res.Message, err)
		}
	}
	if sr.fake.count("next") != 2 || sr.fake.count("prev") != 1 || sr.fake.count("resume") != 1 {
		t.Fatalf("calls = %v", sr.fake.called())
	}
}

func TestSpotifyPauseIsAlwaysAllowed(t *testing.T) {
	sr := newSpotifyRig(t, false)
	sr.start(t)
	waitFor(t, "the receiver to answer", func() bool { return sr.sp.Info(context.Background()).SignedIn })
	sr.fake.set(func() { sr.fake.stopped, sr.fake.paused = false, false })
	waitFor(t, "playing", sr.sp.Active)

	res, err := sr.run(actions.ActorAssistant, "spotify_pause", nil)
	if err != nil || res.Message != "Paused." || sr.fake.count("pause") != 1 {
		t.Fatalf("pause = %q %v calls=%v", res.Message, err, sr.fake.called())
	}
	if sr.sp.Active() {
		t.Fatal("still active after pausing")
	}
}

func TestSpotifyPlaysThePlaylistThatBestMatchesTheWords(t *testing.T) {
	sr := newSpotifyRig(t, false)
	sr.start(t)
	sr.store.SetSetting(context.Background(), SettingSpotifyVoice, "1") //nolint:errcheck
	sr.fake.set(func() {
		sr.fake.playlists = []spotifyPlaylist{{"spotify:playlist:a", "Bedtime lullabies"}, {"spotify:playlist:b", "Dance Party Mix"}}
	})
	waitFor(t, "the receiver to answer", func() bool { return sr.sp.Info(context.Background()).SignedIn })

	res, err := sr.run(actions.ActorAssistant, "spotify_play_playlist", map[string]any{"name": "play the dance party"})
	if err != nil || res.Message != "Playing Dance Party Mix." || sr.fake.count("play spotify:playlist:b") != 1 {
		t.Fatalf("play = %q %v calls=%v", res.Message, err, sr.fake.called())
	}
	if res, _ := sr.run(actions.ActorAssistant, "spotify_play_playlist", map[string]any{"name": "heavy metal"}); !strings.Contains(res.Message, "could not find") {
		t.Fatalf("no match answer = %q", res.Message)
	}
	if sr.plays < 1 {
		t.Fatal("OnPlay not called: local music would keep playing")
	}
}

func TestBestWordMatch(t *testing.T) {
	names := []string{"Bedtime lullabies", "Dance Party Mix", "Car songs"}
	for query, want := range map[string]int{"dance party": 1, "play my lullabies": 0, "car": 2} {
		if i, ok := bestWordMatch(names, query); !ok || i != want {
			t.Errorf("%q -> %d %v, want %d", query, i, ok, want)
		}
	}
	for _, q := range []string{"", "play", "heavy metal"} {
		if _, ok := bestWordMatch(names, q); ok {
			t.Errorf("%q matched", q)
		}
	}
}

func TestSpotifyInfoShowsThePairingCodeUntilSignedIn(t *testing.T) {
	sr := newSpotifyRig(t, false)
	sr.fake.set(func() {
		sr.fake.signedIn = false
		sr.fake.pairing = &SpotifyPairing{URL: "https://spotify.com/pair", Code: "ABC-123", ExpiresAt: time.Now().Add(time.Minute)}
	})
	sr.start(t)
	waitFor(t, "the receiver to answer", func() bool { return sr.sp.Info(context.Background()).Running })
	info := sr.sp.Info(context.Background())
	if info.SignedIn || info.Pairing == nil || info.Pairing.Code != "ABC-123" {
		t.Fatalf("info = %+v", info)
	}
	sr.fake.set(func() { sr.fake.signedIn, sr.fake.pairing = true, nil })
	waitFor(t, "sign-in", func() bool { return sr.sp.Info(context.Background()).SignedIn })
	if info := sr.sp.Info(context.Background()); info.Pairing != nil || info.Username != "family" {
		t.Fatalf("info after sign-in = %+v", info)
	}
}

func TestSpotifySignOutDeletesTheSavedLogin(t *testing.T) {
	sr := newSpotifyRig(t, false)
	sr.start(t)
	state := filepath.Join(sr.dir, "spotify", "state.json")
	os.WriteFile(state, []byte(`{"credentials":"x"}`), 0o600) //nolint:errcheck
	if err := sr.sp.SignOut(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("state.json still there: %v", err)
	}
	if err := sr.sp.SignOut(context.Background()); err != nil { // nothing left to remove is fine
		t.Fatal(err)
	}
}

func TestMusicPauseStopAndSkipReachSpotifyWhenNoLocalSongPlays(t *testing.T) {
	sr := newSpotifyRig(t, false)
	sr.start(t)
	m, _, r := newMusic(t)
	m.Other = sr.sp
	_ = r
	sr.fake.set(func() { sr.fake.stopped, sr.fake.paused = false, false })
	waitFor(t, "playing", sr.sp.Active)

	var eng = sr.eng
	m.RegisterActions(eng)
	if res, err := eng.Execute(context.Background(), actions.Actor{Kind: actions.ActorDevice}, "pause_music", nil); err != nil || res.Message != "Paused." || sr.fake.count("pause") != 1 {
		t.Fatalf("pause_music = %q %v calls=%v", res.Message, err, sr.fake.called())
	}
}

func TestStartingLocalMusicPausesSpotify(t *testing.T) {
	sr := newSpotifyRig(t, false)
	sr.start(t)
	m, _, _ := newMusic(t)
	m.BeforePlay = sr.sp.YieldToLocal
	sr.fake.set(func() { sr.fake.stopped, sr.fake.paused = false, false })
	waitFor(t, "playing", sr.sp.Active)
	if _, err := m.Play(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Spotify to be paused", func() bool { return sr.fake.count("pause") >= 1 })
}
