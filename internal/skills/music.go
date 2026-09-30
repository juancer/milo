package skills

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	"github.com/hajimehoshi/go-mp3"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/audio"
)

const (
	maxTracks    = 2000
	maxDepth     = 4
	rescanEvery  = time.Minute
	settingMusic = "music.dir"
	// maxMP3Bytes and maxID3Bytes bound what the mp3 decoder is asked to read: it
	// allocates a leading ID3 tag whole, and a 10-byte file may claim 256 MiB.
	maxMP3Bytes = 200 << 20
	maxID3Bytes = 1 << 20
	// DefaultMusicDir is where parents put audio files (.mp3 and .wav).
	DefaultMusicDir = "/var/lib/milo/music"
)

// Track is a playable file found in the music folder.
type Track struct {
	Path  string // absolute, discovered by scanning; never taken from user input
	Title string
	Rel   string // path relative to the music folder, used for matching
}

// ScanLibrary lists .mp3 and .wav files below dir. Only regular files count:
// symbolic links, pipes, devices and hidden files are ignored, so nothing
// outside the folder can be reached and nothing can block the player.
func ScanLibrary(dir string) ([]Track, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	var tracks []Track
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") && p != root {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			if rel != "." && strings.Count(rel, string(filepath.Separator)) >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		if (ext == ".mp3" || ext == ".wav") && len(tracks) < maxTracks {
			rel, _ := filepath.Rel(root, p)
			tracks = append(tracks, Track{Path: p, Rel: rel, Title: titleOf(name)})
		}
		return nil
	})
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].Rel < tracks[j].Rel })
	return tracks, err
}

func titleOf(filename string) string {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	return strings.Join(strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(base)), " ")
}

// fillerWords are ignored in spoken requests such as "play the frozen song".
var fillerWords = map[string]bool{
	"play": true, "the": true, "a": true, "an": true, "some": true, "song": true, "songs": true, "music": true,
	"please": true, "of": true, "to": true, "my": true, "me": true, "i": true, "want": true, "listen": true,
	"put": true, "on": true, "from": true, "by": true, "and": true,
}

func words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if !fillerWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// BestMatch picks the track whose path contains the most of the query's words.
// It returns false when nothing matches at all.
func BestMatch(tracks []Track, query string) (Track, bool) {
	qw := words(query)
	if len(qw) == 0 {
		return Track{}, false
	}
	best, bestScore := -1, 0
	for i, t := range tracks {
		haystack := strings.ToLower(t.Rel)
		score := 0
		for _, w := range qw {
			if strings.Contains(haystack, w) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 || bestScore*2 < len(qw) { // at least half of the words must match
		return Track{}, false
	}
	return tracks[best], true
}

// ---------------------------------------------------------------- decoding

type pcmSource struct {
	r          io.Reader
	closer     io.Closer
	sampleRate int
	channels   int
}

// openTrackFunc is openTrack; tests replace it.
var openTrackFunc = openTrack

// openTrackCtx runs openTrackFunc but returns as soon as ctx ends, so stopping
// the player never waits on a slow file. A source that arrives late is closed.
func openTrackCtx(ctx context.Context, path string) (*pcmSource, error) {
	type result struct {
		src *pcmSource
		err error
	}
	ch := make(chan result, 1)
	go func() {
		src, err := openTrackFunc(path)
		ch <- result{src, err}
	}()
	select {
	case r := <-ch:
		return r.src, r.err
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.src != nil {
				r.src.closer.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// openTrack opens an .mp3 or .wav as raw 16-bit PCM. The file must be a regular
// file (a symbolic link or a pipe swapped in after the scan is refused, and
// opening never blocks).
func openTrack(path string) (*pcmSource, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("not a regular file")
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		if info.Size() > maxMP3Bytes {
			f.Close()
			return nil, fmt.Errorf("cannot decode %s: file too large", filepath.Base(path))
		}
		if err := checkID3Size(f); err != nil {
			f.Close()
			return nil, fmt.Errorf("cannot decode %s: %w", filepath.Base(path), err)
		}
		// bufio hides the file's Seek, so the decoder does not scan the whole file up front.
		dec, err := decodeMP3Safe(bufio.NewReaderSize(f, 64<<10))
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("cannot decode %s: %w", filepath.Base(path), err)
		}
		return &pcmSource{r: &safeReader{r: dec}, closer: f, sampleRate: dec.SampleRate(), channels: 2}, nil
	case ".wav":
		rate, ch, err := readWavHeader(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("cannot read %s: %w", filepath.Base(path), err)
		}
		return &pcmSource{r: f, closer: f, sampleRate: rate, channels: ch}, nil
	}
	f.Close()
	return nil, errors.New("unsupported file type")
}

// checkID3Size refuses a file whose leading ID3v2 tag is larger than maxID3Bytes
// and leaves f at the start.
func checkID3Size(f *os.File) error {
	var h [10]byte
	n, _ := io.ReadFull(f, h[:])
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if n == len(h) && string(h[:3]) == "ID3" {
		size := int(h[6]&0x7f)<<21 | int(h[7]&0x7f)<<14 | int(h[8]&0x7f)<<7 | int(h[9]&0x7f)
		if size > maxID3Bytes {
			return errors.New("ID3 tag too large")
		}
	}
	return nil
}

// decodeMP3Safe is mp3.NewDecoder, but a decoder panic on a malformed file
// becomes an error instead of ending the process.
func decodeMP3Safe(r io.Reader) (dec *mp3.Decoder, err error) {
	defer func() {
		if p := recover(); p != nil {
			dec, err = nil, fmt.Errorf("mp3 decoder panic: %v", p)
		}
	}()
	return mp3.NewDecoder(r)
}

// safeReader turns a decoder panic during Read into an error. Read may run in
// the audio player's stdin copy goroutine, where no other recover can see it.
type safeReader struct{ r io.Reader }

func (s *safeReader) Read(p []byte) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			n, err = 0, fmt.Errorf("mp3 decoder panic: %v", r)
		}
	}()
	return s.r.Read(p)
}

// readWavHeader positions r at the start of the sample data of a 16-bit PCM
// WAV (mono or stereo) and returns its format.
func readWavHeader(r io.Reader) (rate, channels int, err error) {
	var hdr [12]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil || string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return 0, 0, errors.New("not a WAV file")
	}
	for {
		var ch [8]byte
		if _, err = io.ReadFull(r, ch[:]); err != nil {
			return 0, 0, errors.New("no audio data")
		}
		size := int64(binary.LittleEndian.Uint32(ch[4:]))
		switch string(ch[:4]) {
		case "fmt ":
			if size < 16 || size > 64 { // a real fmt chunk is 16 to 40 bytes
				return 0, 0, errors.New("bad fmt chunk")
			}
			buf := make([]byte, size+size%2)
			if _, err = io.ReadFull(r, buf); err != nil {
				return 0, 0, errors.New("bad fmt chunk")
			}
			format, chans, bits := binary.LittleEndian.Uint16(buf), int(binary.LittleEndian.Uint16(buf[2:])), binary.LittleEndian.Uint16(buf[14:])
			rate = int(binary.LittleEndian.Uint32(buf[4:]))
			if format != 1 || bits != 16 || (chans != 1 && chans != 2) {
				return 0, 0, errors.New("only 16-bit PCM WAV files (mono or stereo) are supported")
			}
			channels = chans
		case "data":
			if rate == 0 {
				return 0, 0, errors.New("data before format")
			}
			return rate, channels, nil
		default:
			if _, err = io.CopyN(io.Discard, r, size+size%2); err != nil {
				return 0, 0, errors.New("truncated file")
			}
		}
	}
}

// countingReader counts consumed bytes and notices the end of the file.
type countingReader struct {
	r   io.Reader
	n   atomic.Int64
	eof atomic.Bool
	bad atomic.Bool // a read failed for a reason other than the end of the file
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	if errors.Is(err, io.EOF) {
		c.eof.Store(true)
	} else if err != nil {
		c.bad.Store(true)
	}
	return n, err
}

// ---------------------------------------------------------------- player

// PlayerState is what the player is doing.
type PlayerState string

const (
	StateStopped PlayerState = "stopped"
	StatePlaying PlayerState = "playing"
	StatePaused  PlayerState = "paused"
)

// Music is the "music" feature: it plays the parents' audio files and keeps
// going through the folder in shuffled order until told to stop.
type Music struct {
	Common
	Audio audio.Audio
	// Dir overrides the folder setting (tests).
	Dir string
	// Idle reports whether the device is otherwise quiet (no speech, alert or
	// conversation). Music that was interrupted resumes once it stays idle for
	// QuietFor. Defaults to "the audio device is not busy".
	Idle     func() bool
	QuietFor time.Duration

	mu       sync.Mutex
	tracks   []Track
	scanned  time.Time
	state    PlayerState
	queue    []Track
	current  Track
	offset   int64 // bytes already played of the current track (for resume)
	cancel   context.CancelFunc
	skipping bool
	done     chan struct{}
	runCtx   context.Context
	rng      *rand.Rand
}

// Name implements features.Feature.
func (m *Music) Name() string { return "music" }

func (m *Music) dir(ctx context.Context) string {
	if m.Dir != "" {
		return m.Dir
	}
	if v, err := m.Store.GetSetting(ctx, settingMusic); err == nil && v != "" {
		return v
	}
	return DefaultMusicDir
}

// Start implements features.Feature.
func (m *Music) Start(ctx context.Context) error {
	dir := m.dir(ctx)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("the music folder %s does not exist: create it and copy .mp3 or .wav files into it", dir)
	}
	m.mu.Lock()
	m.runCtx, m.rng, m.state = context.WithoutCancel(ctx), rand.New(rand.NewSource(time.Now().UnixNano())), StateStopped
	m.mu.Unlock()
	_, err := m.Rescan(ctx)
	return err
}

// Stop implements features.Feature.
func (m *Music) Stop(ctx context.Context) error {
	m.Halt()
	return nil
}

// Rescan re-reads the music folder and returns the number of tracks.
func (m *Music) Rescan(ctx context.Context) (int, error) {
	tracks, err := ScanLibrary(m.dir(ctx))
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	m.tracks, m.scanned = tracks, m.now()
	m.mu.Unlock()
	return len(tracks), nil
}

// Info describes the library and player for the admin UI.
type Info struct {
	Dir    string      `json:"dir"`
	Tracks []string    `json:"tracks"`
	Count  int         `json:"count"`
	State  PlayerState `json:"state"`
	Title  string      `json:"title,omitempty"`
}

// Info reports the library and what is playing.
func (m *Music) Info(ctx context.Context) Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	titles := make([]string, 0, min(len(m.tracks), 200))
	for _, t := range m.tracks {
		if len(titles) < 200 {
			titles = append(titles, t.Title)
		}
	}
	st := m.state
	if st == "" {
		st = StateStopped
	}
	return Info{Dir: m.dir(ctx), Tracks: titles, Count: len(m.tracks), State: st, Title: m.current.Title}
}

// Play starts playing the best match for query, or a random track when query is empty.
func (m *Music) Play(ctx context.Context, query string) (string, error) {
	m.mu.Lock()
	if m.runCtx == nil {
		m.mu.Unlock()
		return "", errors.New("music is not running")
	}
	stale := m.now().Sub(m.scanned) > rescanEvery
	m.mu.Unlock()
	if stale {
		m.Rescan(ctx) //nolint:errcheck // keep the old list if the folder is unreadable
	}

	m.mu.Lock()
	// "play" with nothing asked resumes a paused song.
	if strings.TrimSpace(query) == "" && m.state == StatePaused {
		title := m.current.Title
		m.mu.Unlock()
		m.startRun(m.current, m.offset, nil)
		return title, nil
	}
	if len(m.tracks) == 0 {
		m.mu.Unlock()
		return "", errors.New("there is no music in the music folder yet")
	}
	var first Track
	if strings.TrimSpace(query) == "" {
		first = m.tracks[m.rng.Intn(len(m.tracks))]
	} else {
		t, ok := BestMatch(m.tracks, query)
		if !ok {
			m.mu.Unlock()
			return "", fmt.Errorf("I could not find music called %q", query)
		}
		first = t
	}
	rest := make([]Track, 0, len(m.tracks))
	for _, t := range m.tracks {
		if t.Path != first.Path {
			rest = append(rest, t)
		}
	}
	m.rng.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	m.mu.Unlock()
	m.startRun(first, 0, rest)
	return first.Title, nil
}

// startRun stops any current playback and starts a new run.
func (m *Music) startRun(first Track, offset int64, queue []Track) {
	m.Halt()
	m.mu.Lock()
	ctx, cancel := context.WithCancel(m.runCtx)
	done := make(chan struct{})
	m.cancel, m.done, m.state, m.current, m.offset = cancel, done, StatePlaying, first, offset
	if queue != nil {
		m.queue = queue
	}
	m.skipping = false
	m.mu.Unlock()
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				m.Log.Error("music: player panic", "panic", r)
				m.mu.Lock()
				m.state = StateStopped
				m.mu.Unlock()
			}
		}()
		m.run(ctx)
	}()
}

const maxResumes = 50

// waitQuiet blocks until the device has been quiet for QuietFor, or returns
// false if the music was stopped or nothing quiet happens for three minutes.
func (m *Music) waitQuiet(ctx context.Context) bool {
	quiet := m.QuietFor
	if quiet == 0 {
		quiet = 1500 * time.Millisecond
	}
	idle := m.Idle
	if idle == nil {
		idle = func() bool { return m.Audio == nil || !m.Audio.Busy() }
	}
	step := min(100*time.Millisecond, quiet/3+time.Millisecond)
	var since time.Time
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if !sleepCtx(ctx, step) {
			return false
		}
		if !idle() {
			since = time.Time{}
			continue
		}
		if since.IsZero() {
			since = time.Now()
		}
		if time.Since(since) >= quiet {
			return true
		}
	}
	return false
}

func (m *Music) run(ctx context.Context) {
	resumes := 0
	for first := true; ctx.Err() == nil; first = false {
		// The schedule was checked when the play action was called; check it
		// again at every track boundary and resume, so a closed window ends the music.
		if !first && !m.allowedNow(ctx, "music") {
			m.Log.Info("music: usage window closed, stopping")
			m.mu.Lock()
			m.state = StateStopped
			m.mu.Unlock()
			return
		}
		m.mu.Lock()
		track, offset := m.current, m.offset
		m.mu.Unlock()

		src, err := openTrackCtx(ctx, track.Path)
		if ctx.Err() != nil {
			if err == nil {
				src.closer.Close()
			}
			return // stopped while opening
		}
		if err != nil {
			m.Log.Warn("music: cannot open track", "track", track.Rel, "error", err)
		} else {
			if offset > 0 {
				io.CopyN(io.Discard, src.r, offset) //nolint:errcheck
			}
			cr := &countingReader{r: src.r}
			cr.n.Store(offset)
			err = m.Audio.PlayStream(ctx, audio.Stream{Reader: cr, SampleRate: src.sampleRate, Channels: src.channels, Label: track.Title})
			src.closer.Close()
			m.mu.Lock()
			m.offset = cr.n.Load()
			skipping := m.skipping
			m.skipping = false
			m.mu.Unlock()
			if err != nil {
				m.Log.Warn("music: playback failed", "error", err)
			}
			switch {
			case ctx.Err() != nil:
				return // paused or stopped by the caller, who set the state
			case skipping:
				// fall through to the next track
			case err == nil && !cr.eof.Load() && resumes < maxResumes:
				// Interrupted by something else (a notification, speech): let it
				// finish, then carry on from the same spot.
				resumes++
				if !m.waitQuiet(ctx) {
					m.mu.Lock()
					m.state = StateStopped
					m.mu.Unlock()
					return
				}
				continue
			case !cr.eof.Load():
				m.mu.Lock()
				m.state = StateStopped
				m.mu.Unlock()
				return
			}
		}
		m.mu.Lock()
		if len(m.queue) == 0 {
			m.state = StateStopped
			m.mu.Unlock()
			return
		}
		m.current, m.queue, m.offset = m.queue[0], m.queue[1:], 0
		m.mu.Unlock()
	}
}

// Halt stops playback immediately and forgets the position.
func (m *Music) Halt() {
	m.stopRun(StateStopped)
}

// Pause stops playback but remembers where it was.
func (m *Music) Pause() bool {
	m.mu.Lock()
	playing := m.state == StatePlaying
	m.mu.Unlock()
	if !playing {
		return false
	}
	m.stopRun(StatePaused)
	return true
}

func (m *Music) stopRun(state PlayerState) {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.cancel, m.done = nil, nil
	if cancel != nil || m.state == StatePlaying {
		m.state = state
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		if m.Audio != nil {
			m.Audio.Stop()
		}
		if done != nil {
			<-done
		}
	}
	if state == StateStopped {
		m.mu.Lock()
		m.offset = 0
		m.mu.Unlock()
	}
}

// Next skips to the following track.
func (m *Music) Next() (string, error) {
	m.mu.Lock()
	if m.state != StatePlaying {
		m.mu.Unlock()
		return "", errors.New("music is not playing")
	}
	if len(m.queue) == 0 {
		m.mu.Unlock()
		return "", errors.New("that was the last song")
	}
	next := m.queue[0]
	m.skipping = true
	m.mu.Unlock()
	if m.Audio != nil {
		m.Audio.Stop() // ends this track's stream; run() then moves on
	}
	return next.Title, nil
}

// RegisterActions registers the music actions.
func (m *Music) RegisterActions(e *actions.Engine) {
	both := actions.ExposeDevice | actions.ExposeAssistant
	e.MustRegister(actions.Action{
		Name: "play_music", Description: "Play a song from the family's music folder. Give words from its name, or nothing for a surprise. Continues with other songs until stopped.",
		Feature: "music", Exposure: both,
		Params: []actions.Param{{Name: "query", Description: "Words from the song or folder name, for example frozen", Type: actions.String, MaxLen: 100}},
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			title, err := m.Play(ctx, c.Args.String("query"))
			if err != nil {
				return actions.Result{}, err
			}
			return actions.Result{Message: "Playing " + title + ".", Data: map[string]any{"title": title}}, nil
		},
	})
	e.MustRegister(actions.Action{
		Name: "pause_music", Description: "Pause the music. Playing again with no song name resumes it.",
		Feature: "music", Exposure: both, BypassSchedule: true, // the child can always stop it
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			if !m.Pause() {
				return actions.Result{Message: "Nothing is playing."}, nil
			}
			return actions.Result{Message: "Paused."}, nil
		},
	})
	e.MustRegister(actions.Action{
		Name: "stop_music", Description: "Stop the music.",
		Feature: "music", Exposure: both, BypassSchedule: true, // the child can always stop it
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			m.Halt()
			return actions.Result{Message: "Music stopped."}, nil
		},
	})
	e.MustRegister(actions.Action{
		Name: "next_song", Description: "Skip to the next song.",
		Feature: "music", Exposure: both, BypassSchedule: true, // the child can always stop it
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			title, err := m.Next()
			if err != nil {
				return actions.Result{Message: err.Error()}, nil
			}
			return actions.Result{Message: "Next: " + title + "."}, nil
		},
	})
}
