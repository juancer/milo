package skills

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juancer/nexo/internal/actions"
	"github.com/juancer/nexo/internal/audio"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
)

type allOn struct{}

func (allOn) Enabled(context.Context, string) (bool, error) { return true, nil }

type announcement struct {
	Text  string
	Alarm bool
}

type recorder struct {
	mu   sync.Mutex
	list []announcement
}

func (r *recorder) Announce(_ context.Context, text string, alarm bool) {
	r.mu.Lock()
	r.list = append(r.list, announcement{text, alarm})
	r.mu.Unlock()
}

func (r *recorder) all() []announcement {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]announcement(nil), r.list...)
}

type rig struct {
	common Common
	eng    *actions.Engine
	ann    *recorder
	now    time.Time
	store  *store.Store
	vault  *secrets.Vault
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	vault, _ := secrets.New(make([]byte, 32), st)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := &rig{ann: &recorder{}, now: time.Date(2026, 3, 2, 15, 0, 0, 0, time.UTC), store: st, vault: vault} // a Monday
	r.eng = actions.NewEngine(allOn{}, nil, nil, log)
	r.common = Common{Store: st, Announce: r.ann, Log: log, AllowHTTP: true,
		Now: func() time.Time { return r.now }, Location: func(context.Context) *time.Location { return time.UTC },
		Check: func(_ context.Context, text string) error {
			if strings.Contains(strings.ToLower(text), "badword") {
				return errors.New("blocked")
			}
			return nil
		}}
	return r
}

func (r *rig) run(name string, args map[string]any) (actions.Result, error) {
	return r.eng.Execute(context.Background(), actions.Actor{Kind: actions.ActorAssistant}, name, args)
}

// ---------------------------------------------------------------- timers

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Second: "45 seconds", time.Second: "1 second", 5 * time.Minute: "5 minutes",
		90 * time.Second: "1 minute 30 seconds", time.Hour + 5*time.Minute: "1 hour 5 minutes", 2 * time.Hour: "2 hours", 0: "0 seconds",
	} {
		if got := HumanDuration(d); got != want {
			t.Errorf("HumanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestTimersLifecycle(t *testing.T) {
	r := newRig(t)
	tm := &Timers{Common: r.common}
	tm.RegisterActions(r.eng)
	ctx := context.Background()

	res, err := r.run("set_timer", map[string]any{"seconds": float64(300), "label": "pasta"})
	if err != nil || res.Message != "Timer set for 5 minutes." {
		t.Fatalf("set_timer = %+v, %v", res, err)
	}
	if res, _ := r.run("list_timers", nil); !strings.Contains(res.Message, "pasta timer has 5 minutes left") {
		t.Fatalf("list = %q", res.Message)
	}

	r.now = r.now.Add(299 * time.Second)
	tm.fireDue(ctx, false)
	if len(r.ann.all()) != 0 {
		t.Fatal("rang early")
	}
	r.now = r.now.Add(2 * time.Second)
	tm.fireDue(ctx, false)
	tm.fireDue(ctx, false) // must not ring twice
	got := r.ann.all()
	if len(got) != 1 || !got[0].Alarm || got[0].Text != "Your pasta timer is done!" {
		t.Fatalf("announcements = %+v", got)
	}
	if res, _ := r.run("list_timers", nil); res.Message != "No timers are running." {
		t.Fatalf("after firing: %q", res.Message)
	}
}

func TestTimerValidationCancelAndLimits(t *testing.T) {
	r := newRig(t)
	(&Timers{Common: r.common}).RegisterActions(r.eng)
	for name, args := range map[string]map[string]any{
		"zero":       {"seconds": float64(0)},
		"too long":   {"seconds": float64(90000)},
		"missing":    {},
		"bad label":  {"seconds": float64(10), "label": "badword party"},
		"extra":      {"seconds": float64(10), "shell": "rm"},
		"fractional": {"seconds": 1.5},
	} {
		if _, err := r.run("set_timer", args); !errors.Is(err, actions.ErrInvalidParams) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for i := 0; i < maxTimers; i++ {
		if _, err := r.run("set_timer", map[string]any{"seconds": float64(60 + i), "label": "t"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.run("set_timer", map[string]any{"seconds": float64(60)}); !errors.Is(err, actions.ErrInvalidParams) {
		t.Fatalf("limit not enforced: %v", err)
	}
	if res, _ := r.run("cancel_timer", map[string]any{"label": "T"}); !strings.Contains(res.Message, "10") {
		t.Fatalf("cancel = %q", res.Message)
	}
	if res, _ := r.run("cancel_timer", nil); res.Message != "There was no timer to cancel." {
		t.Fatalf("cancel again = %q", res.Message)
	}
}

func TestTimersThatRanOutWhileOffAreNotRungLate(t *testing.T) {
	r := newRig(t)
	tm := &Timers{Common: r.common}
	ctx := context.Background()
	r.store.CreateTimer(ctx, "old", r.now.Add(-2*time.Hour))
	r.store.CreateTimer(ctx, "recent", r.now.Add(-2*time.Minute))
	tm.fireDue(ctx, true)
	got := r.ann.all()
	if len(got) != 1 || !strings.Contains(got[0].Text, "recent") {
		t.Fatalf("after reboot: %+v (only the recent timer may ring)", got)
	}
	if pending, _ := r.store.PendingTimers(ctx); len(pending) != 0 {
		t.Fatal("the old timer was left pending")
	}
}

func TestTimersFeatureLoopFires(t *testing.T) {
	r := newRig(t)
	tm := &Timers{Common: r.common, Tick: 10 * time.Millisecond}
	ctx := context.Background()
	r.store.CreateTimer(ctx, "", r.now.Add(-time.Second))
	if err := tm.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer tm.Stop(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for len(r.ann.all()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("loop never fired")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---------------------------------------------------------------- reminders

func TestRemindersInSecondsAndAtTime(t *testing.T) {
	r := newRig(t)
	rm := &Reminders{Common: r.common}
	rm.RegisterActions(r.eng)
	ctx := context.Background()

	if res, err := r.run("create_reminder", map[string]any{"text": "brush your teeth", "in_seconds": float64(600)}); err != nil || res.Message != "I will remind you in 10 minutes." {
		t.Fatalf("in_seconds = %+v, %v", res, err)
	}
	// It is 15:00: 17:30 is later today, 09:00 is tomorrow.
	if res, _ := r.run("create_reminder", map[string]any{"text": "snack", "at_time": "17:30"}); res.Message != "I will remind you at 17:30." {
		t.Fatalf("at_time = %q", res.Message)
	}
	r.run("create_reminder", map[string]any{"text": "school", "at_time": "09:00"})
	pending, _ := r.store.PendingReminders(ctx)
	byText := map[string]time.Time{}
	for _, p := range pending {
		byText[p.Text] = p.DueAt
	}
	if !byText["snack"].Equal(time.Date(2026, 3, 2, 17, 30, 0, 0, time.UTC)) || !byText["school"].Equal(time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("due times = %v", byText)
	}

	r.now = r.now.Add(11 * time.Minute)
	rm.fireDue(ctx)
	rm.fireDue(ctx)
	got := r.ann.all()
	if len(got) != 1 || got[0].Text != "Reminder: brush your teeth" || got[0].Alarm {
		t.Fatalf("announcements = %+v", got)
	}
	if res, _ := r.run("list_reminders", nil); !strings.Contains(res.Message, "snack at Mon 17:30") {
		t.Fatalf("list = %q", res.Message)
	}
	if res, _ := r.run("cancel_reminder", map[string]any{"text": "SNACK"}); !strings.Contains(res.Message, "1") {
		t.Fatalf("cancel = %q", res.Message)
	}
}

func TestReminderValidation(t *testing.T) {
	r := newRig(t)
	(&Reminders{Common: r.common}).RegisterActions(r.eng)
	for name, args := range map[string]map[string]any{
		"neither":      {"text": "x"},
		"both":         {"text": "x", "in_seconds": float64(60), "at_time": "10:00"},
		"bad clock":    {"text": "x", "at_time": "25:99"},
		"not a clock":  {"text": "x", "at_time": "noon"},
		"too soon":     {"text": "x", "in_seconds": float64(1)},
		"blocked text": {"text": "say badword", "in_seconds": float64(60)},
		"blank":        {"text": "   ", "in_seconds": float64(60)},
		"too long":     {"text": strings.Repeat("x", 300), "in_seconds": float64(60)},
	} {
		if _, err := r.run("create_reminder", args); !errors.Is(err, actions.ErrInvalidParams) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestNextClockRollsToTomorrow(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Madrid")
	now := time.Date(2026, 3, 2, 20, 0, 0, 0, loc)
	if got := NextClock(now, 20, 0, loc); !got.Equal(time.Date(2026, 3, 3, 20, 0, 0, 0, loc)) {
		t.Errorf("same minute must roll to tomorrow, got %v", got)
	}
	if got := NextClock(now, 21, 15, loc); !got.Equal(time.Date(2026, 3, 2, 21, 15, 0, 0, loc)) {
		t.Errorf("later today, got %v", got)
	}
}

// ---------------------------------------------------------------- music

func writeWAV(t *testing.T, path string, rate, channels int, d time.Duration) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	frames := int(d.Seconds() * float64(rate))
	pcm := make([]byte, frames*channels*2)
	for i := 0; i < frames*channels; i++ {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16((i%100)*100)))
	}
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+len(pcm)))
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1)
	binary.LittleEndian.PutUint16(hdr[22:], uint16(channels))
	binary.LittleEndian.PutUint32(hdr[24:], uint32(rate))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(rate*channels*2))
	binary.LittleEndian.PutUint16(hdr[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(len(pcm)))
	if err := os.WriteFile(path, append(hdr, pcm...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func musicDir(t *testing.T) string {
	dir := t.TempDir()
	writeWAV(t, filepath.Join(dir, "Twinkle_Twinkle.wav"), 16000, 1, 3*time.Second)
	writeWAV(t, filepath.Join(dir, "Kids", "Happy-Birthday.wav"), 22050, 2, 3*time.Second)
	writeWAV(t, filepath.Join(dir, "Kids", "Frozen Let It Go.wav"), 16000, 1, 3*time.Second)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not music"), 0o644)
	os.WriteFile(filepath.Join(dir, ".hidden.wav"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "broken.mp3"), []byte("this is not an mp3"), 0o644)
	return dir
}

func TestScanLibraryAndMatching(t *testing.T) {
	dir := musicDir(t)
	outside := t.TempDir()
	writeWAV(t, filepath.Join(outside, "Secret.wav"), 16000, 1, time.Second)
	os.Symlink(outside, filepath.Join(dir, "link")) // must not be followed

	tracks, err := ScanLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tr := range tracks {
		names = append(names, tr.Title)
	}
	joined := strings.Join(names, "|")
	if len(tracks) != 4 || strings.Contains(joined, "Secret") || strings.Contains(joined, "hidden") || strings.Contains(joined, "notes") {
		t.Fatalf("tracks = %v", names)
	}
	for _, c := range []struct{ q, want string }{
		{"frozen", "Frozen Let It Go"}, {"play the frozen song", "Frozen Let It Go"}, {"happy birthday", "Happy Birthday"},
		{"twinkle", "Twinkle Twinkle"}, {"kids happy", "Happy Birthday"},
	} {
		got, ok := BestMatch(tracks, c.q)
		if !ok || got.Title != c.want {
			t.Errorf("BestMatch(%q) = %q, %v; want %q", c.q, got.Title, ok, c.want)
		}
	}
	for _, q := range []string{"", "   ", "metallica", "the a of"} {
		if _, ok := BestMatch(tracks, q); ok {
			t.Errorf("BestMatch(%q) matched something", q)
		}
	}
}

func newMusic(t *testing.T) (*Music, *audio.Mock, *rig) {
	t.Helper()
	r := newRig(t)
	mock := audio.NewMock()
	mock.StreamSpeed = 50
	m := &Music{Common: r.common, Audio: mock, Dir: musicDir(t)}
	m.RegisterActions(r.eng)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Halt)
	return m, mock, r
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func playedMusic(m *audio.Mock) []string {
	var out []string
	for _, p := range m.Played() {
		if p.Kind == "music" {
			out = append(out, p.Label)
		}
	}
	return out
}

func TestPlayMusicByNameAndKeepGoing(t *testing.T) {
	m, mock, r := newMusic(t)
	mock.StreamSpeed = 400 // tracks finish in a few milliseconds
	res, err := r.run("play_music", map[string]any{"query": "frozen"})
	if err != nil || res.Message != "Playing Frozen Let It Go." {
		t.Fatalf("play = %+v, %v", res, err)
	}
	// It continues through the rest of the folder on its own, without repeating tracks.
	// Three playable tracks; the corrupt .mp3 is skipped without stopping the music.
	waitFor(t, "all tracks played", func() bool { return len(playedMusic(mock)) >= 3 })
	got := playedMusic(mock)
	if got[0] != "Frozen Let It Go" {
		t.Fatalf("played %v", got)
	}
	seen := map[string]bool{}
	for _, g := range got[:3] {
		if seen[g] {
			t.Fatalf("track repeated within one round: %v", got)
		}
		seen[g] = true
	}
	waitFor(t, "player to stop at the end", func() bool { return m.Info(context.Background()).State == StateStopped })
}

func TestPauseResumeStopAndNext(t *testing.T) {
	m, mock, r := newMusic(t)
	mock.StreamSpeed = 1
	ctx := context.Background()
	if _, err := r.run("play_music", map[string]any{"query": "twinkle"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "playing", func() bool { return m.Info(ctx).State == StatePlaying })
	time.Sleep(300 * time.Millisecond)

	if res, _ := r.run("pause_music", nil); res.Message != "Paused." {
		t.Fatalf("pause = %q", res.Message)
	}
	if st := m.Info(ctx); st.State != StatePaused || st.Title != "Twinkle Twinkle" {
		t.Fatalf("after pause: %+v", st)
	}
	m.mu.Lock()
	pausedAt := m.offset
	m.mu.Unlock()
	if pausedAt == 0 {
		t.Fatal("the position was not remembered")
	}

	before := len(playedMusic(mock))
	if res, err := r.run("play_music", nil); err != nil || res.Message != "Playing Twinkle Twinkle." {
		t.Fatalf("resume = %+v, %v", res, err)
	}
	waitFor(t, "resume", func() bool { return len(playedMusic(mock)) > before })

	if res, err := r.run("next_song", nil); err != nil || !strings.HasPrefix(res.Message, "Next: ") {
		t.Fatalf("next = %+v, %v", res, err)
	}
	waitFor(t, "next track", func() bool { return m.Info(ctx).Title != "Twinkle Twinkle" })

	r.run("stop_music", nil)
	if st := m.Info(ctx); st.State != StateStopped {
		t.Fatalf("after stop: %+v", st)
	}
	if res, _ := r.run("pause_music", nil); res.Message != "Nothing is playing." {
		t.Fatalf("pause when stopped = %q", res.Message)
	}
}

func TestNotificationsInterruptMusic(t *testing.T) {
	m, mock, r := newMusic(t)
	mock.StreamSpeed = 1
	ctx := context.Background()
	r.run("play_music", map[string]any{"query": "twinkle"})
	waitFor(t, "the stream to start", func() bool { return len(playedMusic(mock)) == 1 })

	m.QuietFor = 60 * time.Millisecond
	mock.PlayNotification(ctx, audio.NotifyMessage, 100) // a "piii" arrives
	// The stream yields to the sound and, once it is quiet again, the same
	// track carries on (a second stream of the same title, not the next song).
	waitFor(t, "music to resume", func() bool { return len(playedMusic(mock)) == 2 })
	if got := playedMusic(mock); got[0] != got[1] {
		t.Fatalf("resumed a different track: %v", got)
	}
	if st := m.Info(ctx); st.State != StatePlaying {
		t.Fatalf("state = %s", st.State)
	}
	m.mu.Lock()
	resumedAt := m.offset
	m.mu.Unlock()
	if resumedAt == 0 {
		t.Fatal("the position was lost")
	}
	// A deliberate stop never resumes.
	m.Halt()
	time.Sleep(200 * time.Millisecond)
	if n := len(playedMusic(mock)); n != 2 || m.Info(ctx).State != StateStopped {
		t.Fatalf("music came back after stop (%d streams, %s)", n, m.Info(ctx).State)
	}
}

func TestMusicWaitsWhileTheDeviceIsBusy(t *testing.T) {
	m, mock, r := newMusic(t)
	mock.StreamSpeed = 1
	ctx := context.Background()
	m.QuietFor = 50 * time.Millisecond
	var busy atomic.Bool
	busy.Store(true)
	m.Idle = func() bool { return !busy.Load() }
	r.run("play_music", map[string]any{"query": "twinkle"})
	waitFor(t, "the stream to start", func() bool { return len(playedMusic(mock)) == 1 })
	mock.PlayNotification(ctx, audio.NotifyMessage, 100)
	time.Sleep(400 * time.Millisecond)
	if n := len(playedMusic(mock)); n != 1 {
		t.Fatalf("music resumed while the device was busy (%d streams)", n)
	}
	busy.Store(false)
	waitFor(t, "resume once quiet", func() bool { return len(playedMusic(mock)) == 2 })
}

func TestPlayMusicErrors(t *testing.T) {
	m, _, r := newMusic(t)
	if _, err := r.run("play_music", map[string]any{"query": "metallica"}); err == nil || !strings.Contains(err.Error(), "could not find") {
		t.Fatalf("no match err = %v", err)
	}
	if _, err := r.run("play_music", map[string]any{"query": strings.Repeat("x", 200)}); !errors.Is(err, actions.ErrInvalidParams) {
		t.Fatalf("long query err = %v", err)
	}
	// Queries can never reach files outside the library.
	if _, err := r.run("play_music", map[string]any{"query": "../../etc/passwd"}); err == nil {
		t.Fatal("path-like query played something")
	}
	empty := &Music{Common: m.Common, Audio: audio.NewMock(), Dir: t.TempDir()}
	empty.Start(context.Background())
	if _, err := empty.Play(context.Background(), ""); err == nil {
		t.Fatal("empty library must report an error")
	}
	missing := &Music{Common: m.Common, Audio: audio.NewMock(), Dir: "/nonexistent/music"}
	if err := missing.Start(context.Background()); err == nil {
		t.Fatal("missing folder must fail to start")
	}
}

func TestOpenTrackHandlesFormatsAndGarbage(t *testing.T) {
	dir := musicDir(t)
	src, err := openTrack(filepath.Join(dir, "Kids", "Happy-Birthday.wav"))
	if err != nil || src.sampleRate != 22050 || src.channels != 2 {
		t.Fatalf("stereo wav: %+v, %v", src, err)
	}
	data, _ := io.ReadAll(src.r)
	src.closer.Close()
	if len(data) != 22050*2*2*3 {
		t.Fatalf("read %d bytes of sample data", len(data))
	}
	if _, err := openTrack(filepath.Join(dir, "broken.mp3")); err == nil {
		t.Fatal("garbage mp3 accepted")
	}
	bad := filepath.Join(dir, "bad.wav")
	os.WriteFile(bad, []byte("RIFF....WAVEjunk"), 0o644)
	if _, err := openTrack(bad); err == nil {
		t.Fatal("garbage wav accepted")
	}
}

// ---------------------------------------------------------------- weather

func weatherServer(t *testing.T, lastQuery *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/search":
			if r.URL.Query().Get("name") == "Atlantis" {
				w.Write([]byte(`{}`))
				return
			}
			w.Write([]byte(`{"results":[{"name":"Madrid","country":"Spain","latitude":40.4165,"longitude":-3.7026}]}`))
		case "/v1/forecast":
			if lastQuery != nil {
				*lastQuery = r.URL.RawQuery
			}
			w.Write([]byte(`{"current":{"temperature_2m":18.4,"weather_code":2},"daily":{"weather_code":[3,61],
				"temperature_2m_max":[22.2,19.6],"temperature_2m_min":[11.1,12.5],"precipitation_probability_max":[20,80]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWeatherGeocodeForecastAndUnits(t *testing.T) {
	r := newRig(t)
	var query string
	srv := weatherServer(t, &query)
	ctx := context.Background()
	r.store.SetSetting(ctx, settingWeatherAPI, srv.URL)
	r.store.SetSetting(ctx, settingGeoAPI, srv.URL)
	w := &Weather{Common: r.common}
	w.RegisterActions(r.eng)

	if err := w.Start(ctx); err == nil {
		t.Fatal("started without a place")
	}
	if _, err := r.run("get_weather", nil); err == nil {
		t.Fatal("forecast without a place")
	}
	if _, err := w.SetPlace(ctx, "Atlantis"); err == nil {
		t.Fatal("unknown place accepted")
	}
	p, err := w.SetPlace(ctx, "Madrid")
	if err != nil || p.Name != "Madrid" {
		t.Fatalf("SetPlace = %+v, %v", p, err)
	}
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}

	res, err := r.run("get_weather", nil)
	want := "In Madrid, Spain it is 18 degrees and partly cloudy. Today: overcast, a high of 22 and a low of 11, with a 20 percent chance of rain."
	if err != nil || res.Message != want {
		t.Fatalf("today = %q, %v", res.Message, err)
	}
	if !strings.Contains(query, "latitude=40.4165") || !strings.Contains(query, "temperature_unit=celsius") {
		t.Fatalf("query = %s", query)
	}
	res, _ = r.run("get_weather", map[string]any{"day": "tomorrow"})
	if res.Message != "Tomorrow in Madrid, Spain: rainy, a high of 20 and a low of 13, with a 80 percent chance of rain." {
		t.Fatalf("tomorrow = %q", res.Message)
	}
	r.store.SetSetting(ctx, SettingWeatherUnits, "fahrenheit")
	r.run("get_weather", nil)
	if !strings.Contains(query, "temperature_unit=fahrenheit") {
		t.Fatalf("units not sent: %s", query)
	}
	if _, err := r.run("get_weather", map[string]any{"day": "yesterday"}); !errors.Is(err, actions.ErrInvalidParams) {
		t.Fatalf("bad day err = %v", err)
	}
}

func TestWeatherFailuresAndInsecureAddresses(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.store.SetSetting(ctx, SettingWeatherLat, "40.4")
	r.store.SetSetting(ctx, SettingWeatherLon, "-3.7")
	r.store.SetSetting(ctx, settingWeatherAPI, "http://127.0.0.1:1")
	w := &Weather{Common: r.common}
	if _, err := w.Describe(ctx, "today"); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("unreachable err = %v", err)
	}
	strict := r.common
	strict.AllowHTTP = false
	r.store.SetSetting(ctx, settingWeatherAPI, "http://example.test")
	if _, err := (&Weather{Common: strict}).Describe(ctx, "today"); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("plain http accepted in production: %v", err)
	}
	for code, want := range map[int]string{0: "clear", 2: "partly cloudy", 45: "foggy", 63: "rainy", 75: "snowy", 81: "showery", 96: "stormy", 999: "stormy"} {
		if got := WeatherWords(code); got != want {
			t.Errorf("WeatherWords(%d) = %q, want %q", code, got, want)
		}
	}
}

// ---------------------------------------------------------------- calendar

const testICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:1\r\nDTSTAMP:20260301T000000Z\r\nDTSTART:20260302T160000Z\r\nDTEND:20260302T170000Z\r\nSUMMARY:Dentist\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:2\r\nDTSTAMP:20260301T000000Z\r\nDTSTART:20260302T120000Z\r\nDTEND:20260302T130000Z\r\nSUMMARY:Lunch (already over)\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:3\r\nDTSTAMP:20260301T000000Z\r\nDTSTART;VALUE=DATE:20260302\r\nDTEND;VALUE=DATE:20260303\r\nSUMMARY:School trip\\, museum\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:4\r\nDTSTAMP:20260301T000000Z\r\nDTSTART:20260223T180000Z\r\nDTEND:20260223T190000Z\r\nRRULE:FREQ=WEEKLY;BYDAY=MO\r\nEXDATE:20260309T180000Z\r\nSUMMARY:Piano\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:5\r\nDTSTAMP:20260301T000000Z\r\nDTSTART:20260302T170000Z\r\nDTEND:20260302T180000Z\r\nSTATUS:CANCELLED\r\nSUMMARY:Cancelled thing\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:6\r\nDTSTAMP:20260301T000000Z\r\nDTSTART:20260303T080000Z\r\nDTEND:20260303T090000Z\r\nSUMMARY:Swimming\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func newCalendar(t *testing.T, r *rig, body string, hits *int) *Calendar {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if hits != nil {
			*hits++
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	r.vault.Put(context.Background(), SecretCalendarURL, []byte(srv.URL+"/secret-token-abc123/basic.ics"))
	c := &Calendar{Common: r.common, Vault: r.vault}
	c.RegisterActions(r.eng)
	return c
}

func TestCalendarAgenda(t *testing.T) {
	r := newRig(t) // Monday 2026-03-02 15:00 UTC
	c := newCalendar(t, r, testICS, nil)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := r.run("get_calendar", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Lunch already ended; cancelled events are skipped; the recurring Piano (18:00) shows up.
	want := "You have today: School trip, museum all day; Dentist at 4:00 PM; Piano at 6:00 PM."
	if res.Message != want {
		t.Fatalf("today = %q\nwant    %q", res.Message, want)
	}
	res, _ = r.run("get_calendar", map[string]any{"day": "tomorrow"})
	if res.Message != "You have tomorrow: Swimming at 8:00 AM." {
		t.Fatalf("tomorrow = %q", res.Message)
	}
	res, _ = r.run("get_calendar", map[string]any{"day": "week"})
	if !strings.Contains(res.Message, "Dentist Monday at 4:00 PM") || !strings.Contains(res.Message, "Swimming Tuesday at 8:00 AM") {
		t.Fatalf("week = %q", res.Message)
	}
}

func TestRecurrenceAndExceptions(t *testing.T) {
	from := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	events, err := ParseEvents([]byte(testICS), from, from.AddDate(0, 0, 21), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	var piano []string
	for _, e := range events {
		if e.Title == "Piano" {
			piano = append(piano, e.Start.Format("Jan 2"))
		}
	}
	// Weekly on Mondays: Mar 2, (Mar 9 is an EXDATE), Mar 16.
	if strings.Join(piano, ",") != "Mar 2,Mar 16" {
		t.Fatalf("piano lessons = %v", piano)
	}
	for _, e := range events {
		if e.Title == "Cancelled thing" {
			t.Fatal("cancelled event listed")
		}
	}
}

func TestCalendarQuietDayCacheAndSecrecy(t *testing.T) {
	r := newRig(t)
	hits := 0
	c := newCalendar(t, r, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nEND:VCALENDAR\r\n", &hits)
	if res, _ := r.run("get_calendar", nil); res.Message != "You have nothing planned today." {
		t.Fatalf("empty = %q", res.Message)
	}
	r.run("get_calendar", nil)
	r.run("get_calendar", map[string]any{"day": "tomorrow"})
	if hits != 1 {
		t.Fatalf("calendar downloaded %d times, want 1 (cached)", hits)
	}
	r.now = r.now.Add(11 * time.Minute)
	r.run("get_calendar", nil)
	if hits != 2 {
		t.Fatalf("cache did not expire: %d hits", hits)
	}

	// Errors never repeat the secret address.
	r.vault.Put(context.Background(), SecretCalendarURL, []byte("http://127.0.0.1:1/secret-token-abc123/basic.ics"))
	c.Invalidate()
	_, err := r.run("get_calendar", nil)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("err = %v", err)
	}
	strict := r.common
	strict.AllowHTTP = false
	sc := &Calendar{Common: strict, Vault: r.vault}
	if _, err := sc.Events(context.Background(), r.now, r.now.Add(time.Hour)); err == nil || strings.Contains(err.Error(), "secret-token") || !strings.Contains(err.Error(), "https") {
		t.Fatalf("plain http calendar accepted or leaked: %v", err)
	}
	bad := newCalendar(t, newRig(t), "this is not a calendar", nil)
	_ = bad
	if _, err := ParseEvents([]byte("garbage"), r.now, r.now.Add(time.Hour), time.UTC); err == nil {
		t.Fatal("garbage parsed as a calendar")
	}
	if err := (&Calendar{Common: r.common, Vault: newRig(t).vault}).Start(context.Background()); err == nil {
		t.Fatal("started without an address")
	}
}

func TestReminderCapAndEmptyCancel(t *testing.T) {
	r := newRig(t)
	(&Reminders{Common: r.common}).RegisterActions(r.eng)
	for i := 0; i < maxReminders; i++ {
		if _, err := r.run("create_reminder", map[string]any{"text": "x", "in_seconds": float64(60)}); err != nil {
			t.Fatalf("reminder %d: %v", i, err)
		}
	}
	if _, err := r.run("create_reminder", map[string]any{"text": "x", "in_seconds": float64(60)}); !errors.Is(err, actions.ErrInvalidParams) {
		t.Fatalf("over the cap: err = %v", err)
	}
	if _, err := r.run("cancel_reminder", map[string]any{"text": "   "}); !errors.Is(err, actions.ErrInvalidParams) {
		t.Fatalf("blank cancel: err = %v", err)
	}
	if p, _ := r.store.PendingReminders(context.Background()); len(p) != maxReminders {
		t.Fatalf("%d pending after a blank cancel", len(p))
	}
}

func TestReadWavHeaderCapsFmtChunk(t *testing.T) {
	b := append([]byte("RIFF\x00\x00\x00\x00WAVEfmt "), 0xff, 0xff, 0xff, 0x7f)
	if _, _, err := readWavHeader(strings.NewReader(string(b))); err == nil || err.Error() != "bad fmt chunk" {
		t.Fatalf("err = %v", err)
	}
}

func TestRedirectsToLocalAddressesAreRefused(t *testing.T) {
	r := newRig(t) // AllowHTTP is on
	strict := r.common
	strict.AllowHTTP = false
	for _, tc := range []struct {
		c    Common
		url  string
		fail bool
	}{
		{r.common, "http://127.0.0.1/x", true},
		{r.common, "http://[::1]/x", true},
		{r.common, "http://169.254.169.254/x", true},
		{r.common, "http://localhost/x", true},
		{strict, "http://example.com/x", true},
		{strict, "https://example.com/x", false},
	} {
		req, _ := http.NewRequest(http.MethodGet, tc.url, nil)
		if err := tc.c.checkRedirect(req, nil); (err != nil) != tc.fail {
			t.Errorf("%s: err = %v", tc.url, err)
		}
	}
}

// ---------------------------------------------------------------- audit fixes

func rruleICS(rules ...string) []byte {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\n")
	for i, rule := range rules {
		b.WriteString("BEGIN:VEVENT\r\nUID:" + string(rune('a'+i)) + "\r\nDTSTAMP:20260301T000000Z\r\nDTSTART:20100101T000000Z\r\nDTEND:20100101T000100Z\r\nSUMMARY:Spam\r\nRRULE:" + rule + "\r\nEND:VEVENT\r\n")
	}
	b.WriteString("END:VCALENDAR\r\n")
	return []byte(b.String())
}

func TestRecurrenceExpansionIsBounded(t *testing.T) {
	from := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 7)
	start := time.Now()
	events, err := ParseEvents(rruleICS("FREQ=SECONDLY", "FREQ=MINUTELY", "FREQ=HOURLY", "FREQ=HOURLY;INTERVAL=1;BYMINUTE=0,1,2"), from, to, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("parsing took %v", took)
	}
	if len(events) > maxTotalEvents {
		t.Fatalf("%d occurrences kept, cap is %d", len(events), maxTotalEvents)
	}
	// Sub-hourly rules are not expanded at all.
	events, _ = ParseEvents(rruleICS("FREQ=SECONDLY"), from, to, time.UTC)
	if len(events) != 0 {
		t.Fatalf("FREQ=SECONDLY gave %d events", len(events))
	}
	// A rule the walk budget cannot reach the window of is cut off, not run to the end.
	events, _ = ParseEvents(rruleICS("FREQ=HOURLY"), from, to, time.UTC)
	if len(events) > maxPerEvent {
		t.Fatalf("%d occurrences for one event, cap is %d", len(events), maxPerEvent)
	}
}

func TestRecurrenceExpansionHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	from := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	events, err := ParseEventsContext(ctx, rruleICS("FREQ=HOURLY", "FREQ=DAILY"), from, from.AddDate(0, 0, 7), time.UTC)
	if err != nil || len(events) != 0 {
		t.Fatalf("cancelled parse = %d events, %v", len(events), err)
	}
}

func TestLongRunningDailyEventStillShows(t *testing.T) {
	from := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	events, _ := ParseEvents(rruleICS("FREQ=DAILY"), from, from.AddDate(0, 0, 3), time.UTC)
	if len(events) != 3 {
		t.Fatalf("a daily event since 2010 gave %d occurrences in 3 days", len(events))
	}
}

type closedSchedule struct{ closed atomic.Bool }

func (c *closedSchedule) allowed(context.Context, string, time.Time) (bool, error) {
	return !c.closed.Load(), nil
}

func (c *closedSchedule) Allowed(ctx context.Context, f string, at time.Time) (bool, error) {
	return c.allowed(ctx, f, at)
}

func TestTimerAndReminderWaitForTheirWindow(t *testing.T) {
	r := newRig(t)
	sched := &closedSchedule{}
	sched.closed.Store(true)
	r.common.Allowed = sched.allowed
	ctx := context.Background()
	tm, rm := &Timers{Common: r.common}, &Reminders{Common: r.common}
	r.store.CreateTimer(ctx, "night", r.now.Add(-time.Minute))
	r.store.CreateTimer(ctx, "stale", r.now.Add(-time.Hour))
	r.store.CreateReminder(ctx, "wake up", r.now.Add(-time.Minute), "assistant")

	tm.fireDue(ctx, false)
	rm.fireDue(ctx)
	if got := r.ann.all(); len(got) != 0 {
		t.Fatalf("announced outside the window: %+v", got)
	}
	// The recent timer and the reminder wait; the stale timer is dropped.
	if p, _ := r.store.PendingTimers(ctx); len(p) != 1 || p[0].Label != "night" {
		t.Fatalf("pending timers = %+v", p)
	}
	if p, _ := r.store.PendingReminders(ctx); len(p) != 1 {
		t.Fatalf("pending reminders = %+v", p)
	}
	// Once the window opens they are announced.
	sched.closed.Store(false)
	tm.fireDue(ctx, false)
	rm.fireDue(ctx)
	if got := r.ann.all(); len(got) != 2 {
		t.Fatalf("after the window opened: %+v", got)
	}
}

func TestMusicStopsWhenItsWindowCloses(t *testing.T) {
	m, mock, r := newMusic(t)
	mock.StreamSpeed = 5 // slow enough to close the window while the first track plays
	var open atomic.Bool
	open.Store(true)
	m.Common.Allowed = func(context.Context, string, time.Time) (bool, error) { return open.Load(), nil }
	if _, err := r.run("play_music", map[string]any{"query": "twinkle"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first track to start", func() bool { return len(playedMusic(mock)) >= 1 })
	started := len(playedMusic(mock))
	open.Store(false)
	waitFor(t, "the player to stop", func() bool { return m.Info(context.Background()).State == StateStopped })
	time.Sleep(100 * time.Millisecond)
	// The track in progress may finish, and one more may have begun just before the window
	// closed; the rest of the shuffled library must not play.
	if got := playedMusic(mock); len(got) > started+1 {
		t.Fatalf("%d tracks were playing when the window closed but %v played: the queue went on", started, got)
	}
}

func TestChildCanAlwaysStopMusicOutsideItsWindow(t *testing.T) {
	m, _, r := newMusic(t)
	sched := &closedSchedule{}
	r.eng.SetSchedules(sched)
	if _, err := r.run("play_music", map[string]any{"query": "twinkle"}); err != nil {
		t.Fatal(err)
	}
	sched.closed.Store(true)
	if _, err := r.run("play_music", nil); !errors.Is(err, actions.ErrScheduleBlocked) {
		t.Fatalf("play outside the window: %v", err)
	}
	for _, name := range []string{"next_song", "pause_music", "stop_music"} {
		if _, err := r.run(name, nil); errors.Is(err, actions.ErrScheduleBlocked) {
			t.Fatalf("%s refused by the schedule", name)
		}
	}
	if st := m.Info(context.Background()).State; st == StatePlaying {
		t.Fatalf("still %s", st)
	}
}

func TestMalformedMP3DoesNotCrash(t *testing.T) {
	src, err := openTrack(filepath.Join("testdata", "panic0.mp3"))
	if err == nil {
		defer src.closer.Close()
		io.Copy(io.Discard, src.r) // a panic while streaming becomes a read error
	}
	if err != nil && !strings.Contains(err.Error(), "cannot decode") {
		t.Fatalf("err = %v", err)
	}

	m, mock, r := newMusic(t)
	data, _ := os.ReadFile(filepath.Join("testdata", "panic0.mp3"))
	os.WriteFile(filepath.Join(m.Dir, "Zzz panic.mp3"), data, 0o644)
	m.Rescan(context.Background())
	if _, err := r.run("play_music", map[string]any{"query": "zzz panic"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the queue to finish", func() bool { return len(playedMusic(mock)) >= 3 && m.Info(context.Background()).State == StateStopped })
}

type panicAudio struct{ *audio.Mock }

func (panicAudio) PlayStream(context.Context, audio.Stream) error { panic("boom") }

func TestPlayerPanicIsContained(t *testing.T) {
	m, _, r := newMusic(t)
	m.Audio = panicAudio{audio.NewMock()}
	if _, err := r.run("play_music", map[string]any{"query": "twinkle"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the player to stop", func() bool { return m.Info(context.Background()).State == StateStopped })
}
