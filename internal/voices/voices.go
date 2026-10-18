// Package voices lets the owner pick a Piper text-to-speech voice from the official catalog,
// listen to a demo, and have Milo download the voice (and Piper itself) on the device.
//
// Piper and the voices are separate programs and data under their own licences, so Milo never
// ships them: it downloads them from the voices' official repository and the Piper releases,
// checks every file against the catalog's MD5 (voices) or a pinned SHA-256 (Piper), and keeps
// them under the data directory, which is the only place the service may write.
package voices

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	catalogBase  = "https://huggingface.co/rhasspy/piper-voices/resolve/main/"
	piperVersion = "2023.11.14-2"
	piperBase    = "https://github.com/rhasspy/piper/releases/download/" + piperVersion + "/"

	catalogTTL     = 24 * time.Hour
	maxCatalog     = 8 << 20   // bytes of voices.json
	maxVoiceFile   = 300 << 20 // bytes of one voice file
	maxSample      = 2 << 20   // bytes of one demo
	maxPiperUnpack = 300 << 20 // bytes after unpacking Piper
	maxPiperFiles  = 500
	maxSamples     = 20 // demos kept in memory
)

type piperAsset struct{ name, sha256 string }

// piperAssets maps GOARCH to the Piper release archive (32-bit Raspberry Pi OS is "arm").
var piperAssets = map[string]piperAsset{
	"arm":   {"piper_linux_armv7l.tar.gz", "c6946fcd57c705ed1d4666ea880f80ba0bbbd14de62ecbdd13460baf3bac8e37"},
	"arm64": {"piper_linux_aarch64.tar.gz", "fea0fd2d87c54dbc7078d0f878289f404bd4d6eea6e7444a77835d1537ab88eb"},
	"amd64": {"piper_linux_x86_64.tar.gz", "a50cb45f355b7af1f6d758c1b360717877ba0a398cc8cbe6d2a7a3a26e225992"},
}

var keyRe = regexp.MustCompile(`^[a-z]{2,3}_[A-Z]{2}-[A-Za-z0-9_]+-[a-z_]+$`)

// ValidKey reports whether key looks like a catalog voice key such as es_ES-davefx-medium.
func ValidKey(key string) bool { return len(key) <= 80 && keyRe.MatchString(key) }

type entry struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Language struct {
		Code     string `json:"code"`
		Family   string `json:"family"`
		Native   string `json:"name_native"`
		English  string `json:"name_english"`
		Country  string `json:"country_english"`
		RegionID string `json:"region"`
	} `json:"language"`
	Quality  string `json:"quality"`
	Speakers int    `json:"num_speakers"`
	Files    map[string]struct {
		Size int64  `json:"size_bytes"`
		MD5  string `json:"md5_digest"`
	} `json:"files"`
}

// Voice is one catalog voice as shown in the admin.
type Voice struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Quality   string `json:"quality"`
	Language  string `json:"language"` // es_ES
	Country   string `json:"country"`
	Speakers  int    `json:"speakers"`
	SizeBytes int64  `json:"size_bytes"`
	Installed bool   `json:"installed"`
	// Fast is true for the qualities a Raspberry Pi 3B can speak in about real time. A "medium"
	// voice took 11 s to say a 3 s phrase on one.
	Fast bool `json:"fast"`
}

// Language is a language with at least one voice.
type Language struct {
	Code string `json:"code"` // es
	Name string `json:"name"` // Spanish (Español)
}

// Job is the state of the download in progress, or of the last one.
type Job struct {
	Key     string `json:"key"`
	Running bool   `json:"running"`
	Stage   string `json:"stage"` // "piper", "voice" or ""
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Error   string `json:"error,omitempty"`
}

// Manager downloads and tracks voices. The zero value works once Dir is set.
type Manager struct {
	Dir string // data directory (/var/lib/milo)

	// Overridable for tests.
	CatalogBase string
	PiperBase   string
	PiperSHA256 string
	AllowHTTP   bool
	Client      *http.Client

	mu      sync.Mutex
	cat     map[string]entry
	catAt   time.Time
	job     Job
	samples map[string][]byte

	done, total atomic.Int64
}

func (m *Manager) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return &http.Client{Timeout: 30 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || (req.URL.Scheme != "https" && !m.AllowHTTP) {
			return errors.New("too many or insecure redirects")
		}
		return nil
	}}
}

func (m *Manager) catalogBase() string {
	if m.CatalogBase != "" {
		return m.CatalogBase
	}
	return catalogBase
}

func (m *Manager) get(ctx context.Context, rawURL string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && !m.AllowHTTP) {
		return nil, errors.New("voices: only https downloads are allowed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("voices: download failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("voices: download answered %d", resp.StatusCode)
	}
	return resp, nil
}

// ---------------------------------------------------------------- catalog

func (m *Manager) catalog(ctx context.Context) (map[string]entry, error) {
	m.mu.Lock()
	cat, at := m.cat, m.catAt
	m.mu.Unlock()
	if cat != nil && time.Since(at) < catalogTTL {
		return cat, nil
	}
	fetched, err := m.fetchCatalog(ctx)
	if err != nil {
		if cat != nil {
			return cat, nil // an old catalog beats none
		}
		return nil, err
	}
	m.mu.Lock()
	m.cat, m.catAt = fetched, time.Now()
	m.mu.Unlock()
	return fetched, nil
}

func (m *Manager) fetchCatalog(ctx context.Context) (map[string]entry, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := m.get(ctx, m.catalogBase()+"voices.json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var cat map[string]entry
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxCatalog)).Decode(&cat); err != nil {
		return nil, fmt.Errorf("voices: the catalog is not usable: %w", err)
	}
	for k := range cat { // keep only well-formed keys: they become file names
		if !ValidKey(k) || cat[k].Key != k {
			delete(cat, k)
		}
	}
	return cat, nil
}

// onnxPath returns the catalog path of a voice's model and its config, or "" when it has none.
func (e entry) files() (model, config string) {
	for p := range e.Files {
		switch {
		case strings.HasSuffix(p, ".onnx.json"):
			config = p
		case strings.HasSuffix(p, ".onnx"):
			model = p
		}
	}
	if model == "" || config == "" || path.Dir(model) != path.Dir(config) || strings.Contains(model, "..") || strings.HasPrefix(model, "/") {
		return "", ""
	}
	return model, config
}

var qualityOrder = map[string]int{"x_low": 0, "low": 1, "medium": 2, "high": 3}

// Languages lists the languages the catalog has voices for.
func (m *Manager) Languages(ctx context.Context) ([]Language, error) {
	cat, err := m.catalog(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]Language{}
	for _, e := range cat {
		if model, _ := e.files(); model == "" {
			continue
		}
		name := e.Language.English
		if e.Language.Native != "" && e.Language.Native != name {
			name += " (" + e.Language.Native + ")"
		}
		seen[e.Language.Family] = Language{Code: e.Language.Family, Name: name}
	}
	out := make([]Language, 0, len(seen))
	for _, l := range seen {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// List returns the voices of one language family (es, en, ...), the fastest first.
func (m *Manager) List(ctx context.Context, family string) ([]Voice, error) {
	cat, err := m.catalog(ctx)
	if err != nil {
		return nil, err
	}
	var out []Voice
	for _, e := range cat {
		model, _ := e.files()
		if model == "" || e.Language.Family != family {
			continue
		}
		out = append(out, Voice{
			Key: e.Key, Name: e.Name, Quality: e.Quality, Language: e.Language.Code, Country: e.Language.Country,
			Speakers: e.Speakers, SizeBytes: e.Files[model].Size, Installed: m.Installed(e.Key),
			Fast: e.Quality == "x_low" || e.Quality == "low",
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if qa, qb := qualityOrder[a.Quality], qualityOrder[b.Quality]; qa != qb {
			return qa < qb
		}
		if a.Language != b.Language {
			return a.Language < b.Language
		}
		return a.Name < b.Name
	})
	return out, nil
}

// ---------------------------------------------------------------- what is installed

func (m *Manager) voicesDir() string { return filepath.Join(m.Dir, "voices") }

// ModelPath is where the voice's model lives (whether or not it is installed yet).
func (m *Manager) ModelPath(key string) string { return filepath.Join(m.voicesDir(), key+".onnx") }

// Installed reports whether both files of a voice are on the device.
func (m *Manager) Installed(key string) bool {
	if !ValidKey(key) {
		return false
	}
	for _, p := range []string{m.ModelPath(key), m.ModelPath(key) + ".json"} {
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// PiperBinary is the Piper that Milo downloaded, or "" when there is none (a "piper" on the
// PATH may still exist).
func (m *Manager) PiperBinary() string {
	p := filepath.Join(m.Dir, "piper", "piper", "piper")
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
		return p
	}
	return ""
}

// PiperInstalled reports whether any Piper can be run.
func (m *Manager) PiperInstalled() bool {
	if m.PiperBinary() != "" {
		return true
	}
	_, err := exec.LookPath("piper")
	return err == nil
}

// Remove deletes a downloaded voice.
func (m *Manager) Remove(key string) error {
	if !ValidKey(key) {
		return errors.New("voices: not a voice")
	}
	for _, p := range []string{m.ModelPath(key), m.ModelPath(key) + ".json"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- demo

// Sample returns the demo recording (MP3) of a voice. Demos are small and kept in memory.
func (m *Manager) Sample(ctx context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	if b, ok := m.samples[key]; ok {
		m.mu.Unlock()
		return b, nil
	}
	m.mu.Unlock()
	cat, err := m.catalog(ctx)
	if err != nil {
		return nil, err
	}
	e, ok := cat[key]
	if !ok {
		return nil, errors.New("voices: unknown voice")
	}
	model, _ := e.files()
	if model == "" {
		return nil, errors.New("voices: unknown voice")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := m.get(ctx, m.catalogBase()+path.Join(path.Dir(model), "samples", "speaker_0.mp3"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSample+1))
	if err != nil || len(b) > maxSample {
		return nil, errors.New("voices: the demo is not usable")
	}
	m.mu.Lock()
	if m.samples == nil || len(m.samples) >= maxSamples {
		m.samples = map[string][]byte{}
	}
	m.samples[key] = b
	m.mu.Unlock()
	return b, nil
}

// ---------------------------------------------------------------- install

// Status returns the state of the download in progress, or of the last one.
func (m *Manager) Status() Job {
	m.mu.Lock()
	j := m.job
	m.mu.Unlock()
	if j.Running {
		j.Done, j.Total = m.done.Load(), m.total.Load()
	}
	return j
}

// Install starts downloading Piper (if missing) and the voice in the background. Only one
// download runs at a time. done, when not nil, is called after a successful install.
func (m *Manager) Install(ctx context.Context, key string, done func(context.Context) error) error {
	cat, err := m.catalog(ctx)
	if err != nil {
		return err
	}
	e, ok := cat[key]
	if !ok || !ValidKey(key) {
		return errors.New("voices: unknown voice")
	}
	if model, _ := e.files(); model == "" {
		return errors.New("voices: unknown voice")
	}
	m.mu.Lock()
	if m.job.Running {
		m.mu.Unlock()
		return errors.New("voices: another download is already running")
	}
	m.job = Job{Key: key, Running: true}
	m.mu.Unlock()
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		err := m.install(bg, e)
		if err == nil && done != nil {
			err = done(bg)
		}
		m.mu.Lock()
		m.job.Running, m.job.Stage = false, ""
		if err != nil {
			m.job.Error = err.Error()
		}
		m.mu.Unlock()
	}()
	return nil
}

func (m *Manager) setStage(stage string, total int64) {
	m.mu.Lock()
	m.job.Stage = stage
	m.mu.Unlock()
	m.done.Store(0)
	m.total.Store(total)
}

func (m *Manager) install(ctx context.Context, e entry) error {
	if m.PiperBinary() == "" {
		if _, err := exec.LookPath("piper"); err != nil {
			if err := m.installPiper(ctx); err != nil {
				return err
			}
		}
	}
	model, config := e.files()
	if err := os.MkdirAll(m.voicesDir(), 0o750); err != nil {
		return err
	}
	m.setStage("voice", e.Files[model].Size+e.Files[config].Size)
	// The config first (tiny), the model last: a voice counts as installed only when both exist.
	for _, p := range []string{config, model} {
		f := e.Files[p]
		if f.Size <= 0 || f.Size > maxVoiceFile {
			return errors.New("voices: the catalog gave an unusable size")
		}
		dest := filepath.Join(m.voicesDir(), path.Base(p))
		if err := m.download(ctx, m.catalogBase()+p, dest, 0o640, md5.New(), f.MD5, f.Size); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) installPiper(ctx context.Context) error {
	a, ok := piperAssets[runtime.GOARCH]
	if !ok {
		return fmt.Errorf("voices: no Piper build for %s", runtime.GOARCH)
	}
	sum := a.sha256
	if m.PiperSHA256 != "" {
		sum = m.PiperSHA256
	}
	base := piperBase
	if m.PiperBase != "" {
		base = m.PiperBase
	}
	if err := os.MkdirAll(m.Dir, 0o750); err != nil {
		return err
	}
	m.setStage("piper", 0)
	archive := filepath.Join(m.Dir, ".piper-download.tgz")
	defer os.Remove(archive)
	if err := m.download(ctx, base+a.name, archive, 0o600, sha256.New(), sum, 100<<20); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(m.Dir, ".piper-unpack-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := untar(archive, staging); err != nil {
		return err
	}
	if fi, err := os.Stat(filepath.Join(staging, "piper", "piper")); err != nil || !fi.Mode().IsRegular() {
		return errors.New("voices: the Piper archive has no piper program")
	}
	final := filepath.Join(m.Dir, "piper")
	if err := os.RemoveAll(final); err != nil {
		return err
	}
	return os.Rename(staging, final)
}

// download fetches rawURL to dest through a temporary file, checking the digest (hex, compared
// case-insensitively) and that the size is as expected (limit is the most it will read).
func (m *Manager) download(ctx context.Context, rawURL, dest string, mode os.FileMode, h hash.Hash, want string, limit int64) error {
	resp, err := m.get(ctx, rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.ContentLength > 0 && m.total.Load() < resp.ContentLength && limit > 0 && resp.ContentLength <= limit {
		m.total.Store(resp.ContentLength)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".voices-dl-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(io.MultiWriter(tmp, h, progress{&m.done}), io.LimitReader(resp.Body, limit+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("voices: download interrupted: %w", err)
	}
	if n > limit {
		return errors.New("voices: the download is larger than expected")
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("voices: the download does not match its checksum (%s)", filepath.Base(dest))
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

type progress struct{ n *atomic.Int64 }

func (p progress) Write(b []byte) (int, error) { p.n.Add(int64(len(b))); return len(b), nil }

// untar unpacks a .tar.gz into dir. Only plain files, directories and relative symlinks that stay
// inside dir are accepted, and the amount of data is capped.
func untar(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	for count := 0; ; count++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if count >= maxPiperFiles {
			return errors.New("voices: the Piper archive has too many files")
		}
		name := path.Clean(hdr.Name)
		if name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("voices: unsafe path %q in the Piper archive", hdr.Name)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += hdr.Size
			if total > maxPiperUnpack {
				return errors.New("voices: the Piper archive is too large")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, os.FileMode(hdr.Mode)&0o755|0o444)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, io.LimitReader(tr, hdr.Size)); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			dest := path.Clean(path.Join(path.Dir(name), hdr.Linkname))
			if path.IsAbs(hdr.Linkname) || dest == ".." || strings.HasPrefix(dest, "../") {
				return fmt.Errorf("voices: unsafe link %q in the Piper archive", hdr.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			// Anything else (devices, hard links) is skipped: Piper does not need it.
		}
	}
}
