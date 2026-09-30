package skills

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestRefuseLocalRejectsPrivateAndLocalAddresses(t *testing.T) {
	for addr, refused := range map[string]bool{
		"127.0.0.1:443": true, "[::1]:443": true, "10.1.2.3:443": true, "172.16.0.9:443": true,
		"192.168.1.5:443": true, "169.254.169.254:80": true, "[fe80::1]:443": true, "[fd00::1]:443": true,
		"0.0.0.0:443": true, "[::ffff:10.0.0.1]:443": true,
		"93.184.216.34:443": false, "[2606:4700::1111]:443": false,
	} {
		if err := refuseLocal("tcp", addr, nil); (err != nil) != refused {
			t.Errorf("%s: err = %v, want refused=%v", addr, err, refused)
		}
	}
}

// A calendar or weather address that is (or resolves to) a local machine is not
// connected to, and the error carries no status code from the internal host.
func TestSkillClientsRefuseLocalDestinations(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(418) }))
	t.Cleanup(srv.Close)
	r := newRig(t)
	strict := r.common
	strict.AllowHTTP = false
	client := strict.httpClient(5 * time.Second)
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // reach the handshake if the dial is allowed
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	for _, host := range []string{"127.0.0.1", "localhost"} { // "localhost" is a name that resolves to loopback
		_, err := client.Get("https://" + host + ":" + port + "/")
		if err == nil || !strings.Contains(err.Error(), "refused") {
			t.Errorf("%s: err = %v, want a refusal", host, err)
		}
	}
	// With AllowHTTP (the simulator) local servers stay reachable.
	dev := r.common.httpClient(5 * time.Second)
	dev.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	if resp, err := dev.Get(srv.URL); err != nil || resp.StatusCode != 418 {
		t.Fatalf("dev client: %v", err)
	}
}

func TestWeatherPlaceNameIsCapped(t *testing.T) {
	r := newRig(t)
	long := strings.Repeat("é", 300)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"results":[{"name":"` + long + `","country":"Spain","latitude":1,"longitude":2}]}`))
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	r.store.SetSetting(ctx, settingGeoAPI, srv.URL)
	w := &Weather{Common: r.common}
	if _, err := w.SetPlace(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.store.GetSetting(ctx, SettingWeatherPlace); len([]rune(got)) != maxPlaceLabel {
		t.Fatalf("stored %d runes, want %d", len([]rune(got)), maxPlaceLabel)
	}
}

// cancelAnn stands for a process that is stopped while it is speaking.
type cancelAnn struct{ cancel context.CancelFunc }

func (c cancelAnn) Announce(context.Context, string, bool) { c.cancel() }

// A reminder or timer is marked done only after it was announced, so stopping in
// between leaves it pending to be announced again.
func TestRemindersAndTimersAreAtLeastOnce(t *testing.T) {
	r := newRig(t)
	bg := context.Background()
	r.store.CreateReminder(bg, "wake up", r.now.Add(-time.Minute), "assistant")
	r.store.CreateTimer(bg, "pasta", r.now.Add(-time.Second))

	ctx, cancel := context.WithCancel(bg)
	c := r.common
	c.Announce = cancelAnn{cancel}
	(&Reminders{Common: c}).fireDue(ctx)
	if p, _ := r.store.PendingReminders(bg); len(p) != 1 {
		t.Fatalf("reminder lost when stopped while speaking: %+v", p)
	}
	ctx, cancel = context.WithCancel(bg)
	c.Announce = cancelAnn{cancel}
	(&Timers{Common: c}).fireDue(ctx, false)
	if p, _ := r.store.PendingTimers(bg); len(p) != 1 {
		t.Fatalf("timer lost when stopped while ringing: %+v", p)
	}

	// The next run announces them once and marks them.
	(&Reminders{Common: r.common}).fireDue(bg)
	(&Timers{Common: r.common}).fireDue(bg, false)
	if got := r.ann.all(); len(got) != 2 {
		t.Fatalf("announcements = %+v", got)
	}
	if p, _ := r.store.PendingReminders(bg); len(p) != 0 {
		t.Fatalf("reminder still pending: %+v", p)
	}
	if p, _ := r.store.PendingTimers(bg); len(p) != 0 {
		t.Fatalf("timer still pending: %+v", p)
	}
}

func TestRunnerRestartsALoopThatPanics(t *testing.T) {
	old := restartDelay
	restartDelay = time.Millisecond
	t.Cleanup(func() { restartDelay = old })
	var runs atomic.Int32
	var rn runner
	rn.start(func(ctx context.Context) {
		if runs.Add(1) == 1 {
			panic("boom")
		}
		<-ctx.Done()
	})
	waitFor(t, "the loop to be restarted", func() bool { return runs.Load() >= 2 })
	rn.stop(context.Background())
}

// ---------------------------------------------------------------- music files

// A 10-byte file claiming a 256 MiB ID3 tag must be refused before the decoder allocates it.
func TestOpenTrackRefusesHugeID3Tags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evil.mp3")
	os.WriteFile(path, []byte{'I', 'D', '3', 4, 0, 0, 0x7f, 0x7f, 0x7f, 0x7f}, 0o644)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := openTrack(path)
	runtime.ReadMemStats(&after)
	if err == nil || !strings.Contains(err.Error(), "ID3") {
		t.Fatalf("err = %v, want an ID3 refusal", err)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 32<<20 {
		t.Fatalf("allocated %d MiB", got>>20)
	}
}

func TestOpenTrackRefusesHugeFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.mp3")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(maxMP3Bytes + 1) // sparse
	f.Close()
	if _, err := openTrack(path); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want too large", err)
	}
}

func TestOnlyRegularFilesAreScannedAndOpened(t *testing.T) {
	dir := musicDir(t)
	fifo := filepath.Join(dir, "pipe.mp3")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("no fifo support:", err)
	}
	tracks, err := ScanLibrary(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range tracks {
		if tr.Path == fifo {
			t.Fatal("a FIFO was listed as a track")
		}
	}
	target := filepath.Join(dir, "Kids", "Happy-Birthday.wav")
	link := filepath.Join(dir, "swapped.wav")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() { _, err := openTrack(fifo); done <- err }() // must not block on a pipe with no writer
	go func() { _, err := openTrack(link); done <- err }() // a link swapped in after the scan
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("a FIFO or symlink was opened")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("openTrack blocked")
		}
	}
}

// Stopping the player does not wait for a file that is slow to open.
func TestOpenTrackCtxReturnsWhenCancelled(t *testing.T) {
	release := make(chan struct{})
	old := openTrackFunc
	openTrackFunc = func(string) (*pcmSource, error) { <-release; return nil, os.ErrNotExist }
	t.Cleanup(func() { openTrackFunc = old })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := openTrackCtx(ctx, "x"); done <- err }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("openTrackCtx did not return after cancel")
	}
	close(release)
}
