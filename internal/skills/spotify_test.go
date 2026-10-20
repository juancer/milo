package skills

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
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
	hung      bool // /status answers 503, like a wedged receiver
	stopped   bool
	paused    bool
	track     string
	pairing   *SpotifyPairing
	ctxURI    string // what the receiver reports as playing from
	resumeBad int    // how many /player/resume calls fail with a 500 (a restarting receiver)
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
		if f.hung {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if !f.signedIn {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		st := map[string]any{"username": "family", "device_name": "Milo", "stopped": f.stopped, "paused": f.paused, "context_uri": f.ctxURI}
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
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": "tok-123"})
	})
	act := func(name string, apply func(body map[string]any)) {
		mux.HandleFunc("POST /player/"+name, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck // some calls have no body
			f.mu.Lock()
			f.calls = append(f.calls, name)
			if name == "resume" && f.resumeBad > 0 {
				f.resumeBad--
				f.mu.Unlock()
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
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
	act("shuffle_context", func(map[string]any) {})
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

func TestSpotifyCarriesOnAfterAnAnswerEvenWhenTheFirstResumesFail(t *testing.T) {
	resumeRetryEvery = 10 * time.Millisecond
	t.Cleanup(func() { resumeRetryEvery = 2 * time.Second })
	sr := newSpotifyRig(t, false)
	sr.start(t)
	stop := sr.feed(t)
	defer stop()
	sr.fake.set(func() {
		sr.fake.stopped, sr.fake.paused, sr.fake.track = false, false, "Twinkle"
		sr.fake.ctxURI, sr.fake.resumeBad = "spotify:playlist:abc", 2
	})
	waitFor(t, "the stream to play", func() bool { return musicStreams(sr.mock) >= 1 })

	sr.mock.PlayNotification(context.Background(), audio.NotifyMessage, 100) //nolint:errcheck // interrupts the stream

	waitFor(t, "the playlist to start again", func() bool { return sr.fake.count("play spotify:playlist:abc") >= 1 })
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

func TestPlaySomeMusicStartsSomethingNewForYouAndSaysWhyWhenItCannot(t *testing.T) {
	sr := newSpotifyRig(t, false)
	ctx := context.Background()
	m := &Music{Common: sr.common, Audio: sr.mock, Dir: t.TempDir(), Other: sr.sp} // an empty music folder
	m.RegisterActions(sr.eng)
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Halt)
	sr.start(t)
	sr.store.SetSetting(ctx, SettingSpotifyVoice, "1") //nolint:errcheck
	sr.fake.set(func() {
		sr.fake.playlists = []spotifyPlaylist{{"spotify:playlist:a", "Bedtime lullabies"}, {"spotify:playlist:b", "Dance Party Mix"}, {"spotify:playlist:c", "Daily Mix 1"}, {"spotify:playlist:d", "Descubrimiento semanal"}}
	})
	waitFor(t, "the receiver to answer", func() bool { return sr.sp.Info(ctx).SignedIn })
	// Something is paused on Spotify: "play some music" must still start something new, not carry on.
	sr.fake.set(func() { sr.fake.paused, sr.fake.stopped, sr.fake.track = true, false, "Old song" })
	waitFor(t, "the paused song to be seen", func() bool { return sr.sp.Info(ctx).Track != "" })
	pickRandom = func(n int) int { return n - 1 }
	defer func() { pickRandom = rand.Intn }()

	// For you: one of the playlists Spotify makes from your taste, shuffled.
	res, err := sr.run(actions.ActorAssistant, "play_music", map[string]any{})
	if err != nil || res.Message != "Playing Descubrimiento semanal." || sr.fake.count("play spotify:playlist:d") != 1 || sr.fake.count("shuffle_context") < 1 || sr.fake.count("resume") != 0 {
		t.Fatalf("play = %q %v calls=%v", res.Message, err, sr.fake.called())
	}
	// "sigue" carries on what was paused instead.
	if res, _ := sr.run(actions.ActorAssistant, "play_music", map[string]any{"resume": true}); res.Message != "Playing." || sr.fake.count("resume") != 1 {
		t.Fatalf("resume = %+v calls=%v", res, sr.fake.called())
	}
	// Words that match a playlist start that one.
	if res, _ := sr.run(actions.ActorAssistant, "play_music", map[string]any{"query": "dance party"}); res.Message != "Playing Dance Party Mix." {
		t.Fatalf("matching playlist = %q", res.Message)
	}
	// Without taste playlists, one of the family's at random.
	sr.fake.set(func() {
		sr.fake.playlists = []spotifyPlaylist{{"spotify:playlist:a", "Bedtime lullabies"}, {"spotify:playlist:b", "Dance Party Mix"}}
	})
	if res, _ := sr.run(actions.ActorAssistant, "play_music", map[string]any{}); res.Message != "Playing Dance Party Mix." {
		t.Fatalf("no taste playlists = %q", res.Message)
	}
	// Voice control off: the child is told, and nothing local plays instead.
	sr.store.SetSetting(ctx, SettingSpotifyVoice, "0") //nolint:errcheck
	if res, _ := sr.run(actions.ActorAssistant, "play_music", map[string]any{}); !strings.Contains(res.Message, "voice control is switched off") {
		t.Fatalf("blocked = %q", res.Message)
	}
	// Switched to local: Spotify is not asked at all.
	sr.store.SetSetting(ctx, SettingSpotifyVoice, "1")    //nolint:errcheck
	sr.store.SetSetting(ctx, SettingMusicSource, "local") //nolint:errcheck
	before := len(sr.fake.called())
	if res, err := sr.run(actions.ActorAssistant, "play_music", map[string]any{}); !strings.Contains(res.Message, "no music on the device") {
		t.Fatalf("local with an empty folder = %q (err %v)", res.Message, err)
	}
	if len(sr.fake.called()) != before {
		t.Error("Spotify was used although the source is local")
	}
}

func TestArtistMatches(t *testing.T) {
	for _, c := range []struct {
		query, artist string
		want          bool
	}{
		{"camilo", "Camilo", true},
		{"música de belinda", "Belinda", true},
		{"belinda", "Belinda Carlisle", true},
		{"queen", "Queen", true},
		{"los payasos", "Los Payasos de la Tele", true},
		{"camilo", "Camila Cabello", false},
		{"maluma", "Maluma Baby", true},
		{"shakira", "Karol G", false},
		{"frozen", "Elsa y Anna", false},
		{"el kanka", "El Kanka", true},
		{"", "Camilo", false},
	} {
		if got := artistMatches(c.query, c.artist); got != c.want {
			t.Errorf("artistMatches(%q, %q) = %v, want %v", c.query, c.artist, got, c.want)
		}
	}
}

func TestPickForYouPrefersTheTastePlaylists(t *testing.T) {
	pickRandom = func(n int) int { return 0 }
	defer func() { pickRandom = rand.Intn }()
	lists := []spotifyPlaylist{{"u:1", "Fiesta"}, {"u:2", "Radar de novedades"}, {"u:3", "Mi lista"}}
	if got := pickForYou(lists); got.URI != "u:2" {
		t.Errorf("picked %q, want the taste playlist", got.Name)
	}
	if got := pickForYou(lists[:1]); got.URI != "u:1" {
		t.Errorf("picked %q, want the only playlist", got.Name)
	}
}

func TestAReceiverThatStopsAnsweringIsRestarted(t *testing.T) {
	sr := newSpotifyRig(t, false)
	sr.sp.HungAfter = 150 * time.Millisecond
	sr.start(t)
	waitFor(t, "the receiver to answer", func() bool { return sr.sp.Info(context.Background()).SignedIn })
	starts := func() int {
		b, _ := os.ReadFile(filepath.Join(sr.dir, "args"))
		return strings.Count(string(b), "--config_dir")
	}
	// SignedIn comes from the fake API, which answers before a slow machine has run the fake receiver's
	// script: wait for the first start instead of assuming it already happened.
	waitFor(t, "the receiver to be started once", func() bool { return starts() == 1 })
	sr.fake.set(func() { sr.fake.hung = true })
	waitFor(t, "the wedged receiver to be restarted", func() bool { return starts() >= 2 })
}

func TestPlaySomeMusicByAnArtistSearchesSpotifyOnceAndRemembersThem(t *testing.T) {
	sr := newSpotifyRig(t, false)
	ctx := context.Background()
	var auth, query string
	searches := 0
	status := http.StatusOK
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		searches++
		auth, query = r.Header.Get("Authorization"), r.URL.Query().Get("q")
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "1")
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			if query == "nadie" {
				w.Write([]byte(`{"artists":{"items":[]}}`))
				return
			}
			name := strings.ToUpper(query[:1]) + query[1:]
			w.Write([]byte(`{"artists":{"items":[{"name":"` + name + `","uri":"spotify:artist:` + query + `"}]}}`))
		}
	}))
	defer web.Close()
	sr.sp.WebAPIBase = web.URL
	sr.sp.Store = sr.store
	sr.start(t)
	sr.store.SetSetting(ctx, SettingSpotifyVoice, "1") //nolint:errcheck
	sr.fake.set(func() { sr.fake.playlists = []spotifyPlaylist{{"spotify:playlist:a", "Bedtime lullabies"}} })
	waitFor(t, "the receiver to answer", func() bool { return sr.sp.Info(ctx).SignedIn })
	m := &Music{Common: sr.common, Audio: sr.mock, Dir: t.TempDir(), Other: sr.sp}
	m.RegisterActions(sr.eng)
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Halt)

	res, err := sr.run(actions.ActorAssistant, "play_music", map[string]any{"query": "belinda"})
	if err != nil || res.Message != "Playing music by Belinda." || res.Refused || sr.fake.count("play spotify:artist:belinda") != 1 {
		t.Fatalf("artist = %q refused=%v err=%v calls=%v", res.Message, res.Refused, err, sr.fake.called())
	}
	if auth != "Bearer tok-123" || query != "belinda" || searches != 1 {
		t.Errorf("the search was sent as auth=%q q=%q (%d searches)", auth, query, searches)
	}
	// The second time the artist is known: no search at all, and a looser spelling still finds them.
	if res, _ := sr.run(actions.ActorAssistant, "play_music", map[string]any{"query": "música de Belinda"}); res.Message != "Playing music by Belinda." || searches != 1 {
		t.Fatalf("remembered artist = %q after %d searches", res.Message, searches)
	}
	// An artist Spotify does not know is said as it is.
	res, _ = sr.run(actions.ActorAssistant, "play_music", map[string]any{"query": "nadie"})
	if !res.Refused || res.Data["reason"] != "artist_not_found" {
		t.Errorf("unknown artist = %+v", res)
	}
	// Rate-limited: the person is told it will take a moment, and the artist starts by itself when Spotify allows.
	searchMinWait, searchExtra = 20*time.Millisecond, 20*time.Millisecond
	defer func() { searchMinWait, searchExtra = 2*time.Second, time.Second }()
	status = http.StatusTooManyRequests
	before := sr.fake.count("play spotify:artist:maluma")
	res, _ = sr.run(actions.ActorAssistant, "play_music", map[string]any{"query": "maluma"})
	if res.Refused || res.Data["pending"] != "artist_search" || before != 0 {
		t.Fatalf("rate-limited = %+v", res)
	}
	if res, _ := sr.run(actions.ActorAssistant, "play_music", map[string]any{"query": "maluma"}); res.Data["pending"] != "artist_search" {
		t.Errorf("a second ask while waiting = %+v", res)
	}
	status = http.StatusOK
	waitFor(t, "the waiting search to play the artist", func() bool { return sr.fake.count("play spotify:artist:maluma") == 1 })
	if _, _, ok := sr.sp.knownArtist(ctx, "maluma"); !ok {
		t.Error("the artist found later was not remembered")
	}
}

func TestArtistSearchUsesTheDeveloperAppWhenOneIsConfigured(t *testing.T) {
	sr := newSpotifyRig(t, false)
	ctx := context.Background()
	var searchAuth, tokenAuth string
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenAuth = r.Header.Get("Authorization")
		if user, pass, ok := r.BasicAuth(); !ok || user != "abcdefghijklmnopqrstuvwxyz012345" || pass != "secretsecretsecretsecret012345" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"access_token":"cc-token","expires_in":3600}`))
	}))
	defer accounts.Close()
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		searchAuth = r.Header.Get("Authorization")
		q := r.URL.Query().Get("q")
		w.Write([]byte(`{"artists":{"items":[{"name":"` + strings.ToUpper(q[:1]) + q[1:] + `","uri":"spotify:artist:` + q + `"}]}}`))
	}))
	defer web.Close()
	sr.sp.WebAPIBase, sr.sp.AccountsBase, sr.sp.Vault, sr.sp.Store = web.URL, accounts.URL, sr.vault, sr.store
	sr.start(t)
	sr.store.SetSetting(ctx, SettingSpotifyVoice, "1") //nolint:errcheck
	waitFor(t, "the receiver to answer", func() bool { return sr.sp.Info(ctx).SignedIn })
	m := &Music{Common: sr.common, Audio: sr.mock, Dir: t.TempDir(), Other: sr.sp}
	m.RegisterActions(sr.eng)
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Halt)

	// Credentials Spotify refuses are not stored.
	if err := sr.sp.SetSearchCredentials(ctx, "abcdefghijklmnopqrstuvwxyz012345", "wrongwrongwrongwrongwrong0000"); !errors.Is(err, ErrSpotifyCredentials) {
		t.Fatalf("wrong secret: %v", err)
	}
	if err := sr.sp.SetSearchCredentials(ctx, "short", "x"); !errors.Is(err, ErrSpotifyCredentials) || sr.sp.SearchConfigured(ctx) {
		t.Fatalf("malformed credentials accepted: %v", err)
	}
	// Good ones are checked, stored, and used: the search carries the app's token, not the receiver's.
	if err := sr.sp.SetSearchCredentials(ctx, "abcdefghijklmnopqrstuvwxyz012345", "secretsecretsecretsecret012345"); err != nil {
		t.Fatal(err)
	}
	if !sr.sp.SearchConfigured(ctx) || !sr.sp.Info(ctx).SearchConfigured || !strings.HasPrefix(tokenAuth, "Basic ") {
		t.Fatalf("configured=%v auth=%q", sr.sp.SearchConfigured(ctx), tokenAuth)
	}
	res, err := sr.run(actions.ActorAssistant, "play_music", map[string]any{"query": "camilo"})
	if err != nil || res.Message != "Playing music by Camilo." || searchAuth != "Bearer cc-token" {
		t.Fatalf("res=%q err=%v searchAuth=%q", res.Message, err, searchAuth)
	}
	// Removed, the receiver's token is used again.
	if err := sr.sp.ClearSearchCredentials(ctx); err != nil || sr.sp.SearchConfigured(ctx) {
		t.Fatalf("clear: %v", err)
	}
	if res, _ := sr.run(actions.ActorAssistant, "play_music", map[string]any{"query": "maluma"}); res.Message != "Playing music by Maluma." || searchAuth != "Bearer tok-123" {
		t.Fatalf("after clearing: res=%q searchAuth=%q", res.Message, searchAuth)
	}
}

func TestListenTrackerCountsASongOnceAfterItHasBeenHeardForAWhile(t *testing.T) {
	var tr listenTracker
	st := SpotifyStatus{Track: &struct {
		URI         string   `json:"uri"`
		Name        string   `json:"name"`
		ArtistNames []string `json:"artist_names"`
		ArtistURIs  []string `json:"artist_uris"`
		AlbumName   string   `json:"album_name"`
	}{URI: "spotify:track:1", Name: "Song"}}
	now := time.Unix(1000, 0)
	counted := 0
	for i := 0; i < 20; i++ { // 40 s of listening in 2 s steps
		now = now.Add(2 * time.Second)
		if tr.observe(st, true, now) {
			counted++
		}
	}
	if counted != 1 {
		t.Fatalf("counted %d times, want once", counted)
	}
	// A different song starts again from nothing; pausing does not add time; a long stall adds little.
	st.Track.URI = "spotify:track:2"
	now = now.Add(2 * time.Second)
	if tr.observe(st, true, now) {
		t.Error("a new song was counted at once")
	}
	if tr.observe(st, false, now.Add(time.Hour)) || tr.observe(st, true, now.Add(2*time.Hour)) {
		t.Error("time while paused or stalled counted as listening")
	}
}

func TestListeningMemoryDrivesFavoritesAndTheDefaultMusic(t *testing.T) {
	sr := newSpotifyRig(t, false)
	ctx := context.Background()
	sr.sp.Store = sr.store
	sr.start(t)
	sr.store.SetSetting(ctx, SettingSpotifyVoice, "1") //nolint:errcheck
	sr.fake.set(func() {
		sr.fake.playlists = []spotifyPlaylist{{"spotify:playlist:a", "Bedtime lullabies"}, {"spotify:playlist:d", "Daily Mix 1"}}
	})
	waitFor(t, "the receiver to answer", func() bool { return sr.sp.Info(ctx).SignedIn })
	m := &Music{Common: sr.common, Audio: sr.mock, Dir: t.TempDir(), Other: sr.sp}
	m.RegisterActions(sr.eng)
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Halt)
	pickRandom = func(n int) int { return 0 }
	defer func() { pickRandom = rand.Intn }()

	// No history yet: the favorites say so, and the default music is Spotify's own pick.
	if res, _ := sr.run(actions.ActorAssistant, "play_music", map[string]any{"favorites": true}); !res.Refused || res.Data["reason"] != "no_history" {
		t.Fatalf("favorites without history = %+v", res)
	}
	if res, _ := sr.run(actions.ActorAssistant, "play_music", map[string]any{}); res.Message != "Playing Daily Mix 1." {
		t.Fatalf("default without history = %q", res.Message)
	}
	// History: three listens of an artist and two of a playlist.
	now := time.Now()
	for i := 0; i < 3; i++ {
		sr.store.CountArtistPlay(ctx, "spotify:artist:cam", "Camilo", now) //nolint:errcheck
	}
	sr.store.CountContextPlay(ctx, "spotify:playlist:fav", "Mis favoritas", now) //nolint:errcheck
	sr.store.CountContextPlay(ctx, "spotify:playlist:fav", "Mis favoritas", now) //nolint:errcheck
	sr.store.CountArtistPlay(ctx, "spotify:artist:once", "Una vez", now)         //nolint:errcheck // a single listen is not a favorite

	// With pickRandom at 0 the most listened comes first: Camilo.
	if res, _ := sr.run(actions.ActorAssistant, "play_music", map[string]any{"favorites": true}); res.Message != "Playing Camilo." || sr.fake.count("play spotify:artist:cam") != 1 {
		t.Fatalf("favorites = %q calls=%v", res.Message, sr.fake.called())
	}
	// "Pon algo de música" now draws on the history (pickRandom(3) == 0 would pick Spotify's own instead).
	pickRandom = func(n int) int { return n - 1 }
	if res, _ := sr.run(actions.ActorAssistant, "play_music", map[string]any{}); !strings.HasPrefix(res.Message, "Playing ") || res.Data["favorite"] == nil {
		t.Fatalf("default with history = %+v", res)
	}
	// The artist heard is known by name without searching.
	if name, uri, ok := sr.sp.knownArtist(ctx, "camilo"); !ok || uri != "spotify:artist:cam" || name != "Camilo" {
		t.Errorf("knownArtist = %q %q %v", name, uri, ok)
	}
}

func TestMusicRequestWhileReceiverRestartsSaysTryAgain(t *testing.T) {
	sr := newSpotifyRig(t, false)
	ctx := context.Background()
	res, handled, _ := sr.sp.PlayAnything(ctx, actions.Actor{Kind: actions.ActorDevice}, "")
	if handled {
		t.Fatalf("never signed in: Spotify is simply not connected, got %+v", res)
	}
	sr.sp.mu.Lock()
	sr.sp.signedAt = time.Now().Add(-time.Minute) // it worked a minute ago, and is down now
	sr.sp.mu.Unlock()
	res, handled, _ = sr.sp.PlayAnything(ctx, actions.Actor{Kind: actions.ActorDevice}, "")
	if !handled || !res.Refused || res.Data["reason"] != "spotify_restarting" {
		t.Fatalf("want a restarting refusal, got handled=%v %+v", handled, res)
	}
	sr.sp.mu.Lock()
	sr.sp.signedAt = time.Now().Add(-time.Hour)
	sr.sp.mu.Unlock()
	if _, handled, _ = sr.sp.PlayAnything(ctx, actions.Actor{Kind: actions.ActorDevice}, ""); handled {
		t.Fatal("down for an hour is not a restart")
	}
}
