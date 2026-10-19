package speech

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// A Piper process loads its voice model each time it starts, which takes about 8 s on a Raspberry
// Pi 3 for a phrase that takes 1 s to say. In --json-input mode one process reads a line per phrase
// and stays loaded: the first phrase pays for the load, the next ones take a second or two. One
// process is kept per voice and speed; it is stopped after a few idle minutes to give its memory back.

const piperIdle = 10 * time.Minute

// piperTimeout is how long one phrase may take, including the first one, which loads the model.
var piperTimeout = 60 * time.Second

type piperProc struct {
	mu     sync.Mutex
	broken bool // this Piper does not answer in --json-input mode: use one process per phrase
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Reader
	dir    string
	idle   *time.Timer
}

var piperPool = &pool{procs: map[string]*piperProc{}}

type pool struct {
	mu    sync.Mutex
	procs map[string]*piperProc
}

func (pl *pool) get(binary, model, scale string) *piperProc {
	key := binary + "|" + model + "|" + scale
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if p := pl.procs[key]; p != nil {
		return p
	}
	p := &piperProc{}
	pl.procs[key] = p
	return p
}

// synthesize speaks text with the long-lived process for this voice, starting it when needed.
func (pl *pool) synthesize(ctx context.Context, binary, model, scale, text string) ([]byte, error) {
	if !validBinary(binary) || !ValidModelPath(model) {
		return nil, errors.New("speech: invalid piper command")
	}
	p := pl.get(binary, model, scale)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.broken {
		return nil, errors.New("piper does not keep running")
	}
	if p.cmd == nil {
		if err := p.start(binary, model, scale); err != nil {
			return nil, err
		}
	}
	wav, err := p.speak(ctx, text)
	if err != nil {
		p.stop()
		if errors.Is(err, errPiperSilent) {
			p.broken = true // an old Piper, or one that ignores --json-input: do not wait for it again
		}
		return nil, err
	}
	if p.idle != nil {
		p.idle.Reset(piperIdle)
	} else {
		p.idle = time.AfterFunc(piperIdle, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.stop()
		})
	}
	return wav, nil
}

func (p *piperProc) start(binary, model, scale string) error {
	dir, err := os.MkdirTemp("", "milo-tts-")
	if err != nil {
		return err
	}
	args := []string{"--model", model, "--json-input", "--output_dir", dir}
	if scale != "" {
		args = append(args, "--length_scale", scale)
	}
	cmd := exec.Command(binary, args...)
	cmd.Stderr = io.Discard // piper's log; unread, it could fill its pipe and stop it
	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("piper not found: install Piper or set the text-to-speech engine to none")
	}
	p.cmd, p.stdin, p.out, p.dir = cmd, stdin, bufio.NewReader(stdout), dir
	return nil
}

// speak sends one phrase and waits for the path of the WAV Piper wrote.
func (p *piperProc) speak(ctx context.Context, text string) ([]byte, error) {
	line, err := json.Marshal(map[string]string{"text": strings.ReplaceAll(strings.ReplaceAll(text, "\n", " "), "\r", " ")})
	if err != nil {
		return nil, err
	}
	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("piper stopped: %w", err)
	}
	type reply struct {
		path string
		err  error
	}
	ch := make(chan reply, 1)
	go func() {
		s, err := p.out.ReadString('\n')
		ch <- reply{strings.TrimSpace(s), err}
	}()
	select {
	case r := <-ch:
		if r.err != nil || r.path == "" {
			return nil, fmt.Errorf("piper gave no audio: %v", r.err)
		}
		path := r.path
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.dir, path)
		}
		// Only files Piper wrote in its own directory are read.
		if filepath.Dir(filepath.Clean(path)) != filepath.Clean(p.dir) {
			return nil, errors.New("piper wrote outside its directory")
		}
		defer os.Remove(path)
		if info, err := os.Stat(path); err != nil || info.Size() > maxTTSFile {
			return nil, errors.New("piper output is missing or too large")
		}
		return os.ReadFile(path)
	case <-time.After(piperTimeout):
		return nil, errPiperSilent
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var errPiperSilent = errors.New("piper took too long")

// stop ends the process. The caller holds p.mu.
func (p *piperProc) stop() {
	if p.cmd == nil {
		return
	}
	p.stdin.Close()
	if p.cmd.Process != nil {
		p.cmd.Process.Kill() //nolint:errcheck // it may already have exited
	}
	go p.cmd.Wait() //nolint:errcheck // reap it without holding anything up
	os.RemoveAll(p.dir)
	p.cmd, p.stdin, p.out, p.dir = nil, nil, nil, ""
	if p.idle != nil {
		p.idle.Stop()
		p.idle = nil
	}
}

// StopPiper ends every long-lived Piper process (shutdown, tests).
func StopPiper() {
	piperPool.mu.Lock()
	procs := make([]*piperProc, 0, len(piperPool.procs))
	for _, p := range piperPool.procs {
		procs = append(procs, p)
	}
	piperPool.procs = map[string]*piperProc{}
	piperPool.mu.Unlock()
	for _, p := range procs {
		p.mu.Lock()
		p.stop()
		p.mu.Unlock()
	}
}

// ---------------------------------------------------------------- phrase cache

// Phrases Milo says again and again ("Hello!", a goodbye, the same time of day) are kept as WAV files,
// so saying them costs nothing the second time.
const (
	cacheMaxFiles  = 300
	cacheMaxPhrase = 200 // characters
)

var (
	cacheMu  sync.Mutex
	cacheDir string
)

// SetCacheDir turns the phrase cache on, in dir ("" turns it off).
func SetCacheDir(dir string) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cacheDir = dir
	if dir != "" {
		os.MkdirAll(dir, 0o750) //nolint:errcheck // a cache that cannot be made is just off
	}
}

func cachePath(model, scale, text string) string {
	if cacheDir == "" || len(text) > cacheMaxPhrase {
		return ""
	}
	sum := sha256.Sum256([]byte(model + "|" + scale + "|" + text))
	return filepath.Join(cacheDir, hex.EncodeToString(sum[:16])+".wav")
}

func cacheGet(model, scale, text string) ([]byte, bool) {
	cacheMu.Lock()
	path := cachePath(model, scale, text)
	cacheMu.Unlock()
	if path == "" {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil || checkTTSWAV(b) != nil {
		return nil, false
	}
	now := time.Now()
	os.Chtimes(path, now, now) //nolint:errcheck // keeps recently used phrases when the cache is pruned
	return b, true
}

func cachePut(model, scale, text string, wav []byte) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	path := cachePath(model, scale, text)
	if path == "" {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, wav, 0o640) != nil || os.Rename(tmp, path) != nil {
		os.Remove(tmp)
		return
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil || len(entries) <= cacheMaxFiles {
		return
	}
	type f struct {
		name string
		at   time.Time
	}
	var files []f
	for _, e := range entries {
		if info, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".wav") {
			files = append(files, f{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].at.Before(files[j].at) })
	for i := 0; i < len(files)-cacheMaxFiles; i++ {
		os.Remove(filepath.Join(cacheDir, files[i].name))
	}
}
