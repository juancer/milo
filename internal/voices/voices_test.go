package voices

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func md5hex(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }

// fakeHub serves a two-voice catalog, their files, a demo, and a fake Piper archive.
type fakeHub struct {
	srv      *httptest.Server
	model    []byte
	config   []byte
	piperTGZ []byte
	corrupt  bool // serve a model that does not match the catalog
}

func tgz(t *testing.T, entries []tar.Header, bodies map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		h := h
		body := bodies[h.Name]
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func newHub(t *testing.T) *fakeHub {
	t.Helper()
	h := &fakeHub{model: bytes.Repeat([]byte("m"), 5000), config: []byte(`{"audio":{"sample_rate":22050}}`)}
	h.piperTGZ = tgz(t, []tar.Header{
		{Name: "piper/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "piper/piper", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "piper/libx.so.1", Typeflag: tar.TypeReg, Mode: 0o644},
		{Name: "piper/libx.so", Typeflag: tar.TypeSymlink, Linkname: "libx.so.1"},
	}, map[string]string{"piper/piper": "#!/bin/sh\nexit 0\n", "piper/libx.so.1": "lib"})
	mux := http.NewServeMux()
	mux.HandleFunc("/voices.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{
		 "es_ES-davefx-medium": {"key":"es_ES-davefx-medium","name":"davefx","language":{"code":"es_ES","family":"es","name_native":"Español","name_english":"Spanish","country_english":"Spain"},"quality":"medium","num_speakers":1,
		   "files":{"es/es_ES/davefx/medium/es_ES-davefx-medium.onnx":{"size_bytes":%d,"md5_digest":"%s"},"es/es_ES/davefx/medium/es_ES-davefx-medium.onnx.json":{"size_bytes":%d,"md5_digest":"%s"},"es/es_ES/davefx/medium/MODEL_CARD":{"size_bytes":3,"md5_digest":"x"}}},
		 "es_ES-carlfm-x_low": {"key":"es_ES-carlfm-x_low","name":"carlfm","language":{"code":"es_ES","family":"es","name_native":"Español","name_english":"Spanish","country_english":"Spain"},"quality":"x_low","num_speakers":1,
		   "files":{"es/es_ES/carlfm/x_low/es_ES-carlfm-x_low.onnx":{"size_bytes":%d,"md5_digest":"%s"},"es/es_ES/carlfm/x_low/es_ES-carlfm-x_low.onnx.json":{"size_bytes":%d,"md5_digest":"%s"}}},
		 "en_US-amy-low": {"key":"en_US-amy-low","name":"amy","language":{"code":"en_US","family":"en","name_native":"English","name_english":"English","country_english":"United States"},"quality":"low","num_speakers":1,
		   "files":{"en/en_US/amy/low/en_US-amy-low.onnx":{"size_bytes":10,"md5_digest":"x"},"en/en_US/amy/low/en_US-amy-low.onnx.json":{"size_bytes":10,"md5_digest":"x"}}},
		 "../evil-key": {"key":"../evil-key","name":"x","language":{"code":"xx","family":"xx"},"quality":"low","files":{}},
		 "xx_XX-nofiles-low": {"key":"xx_XX-nofiles-low","name":"x","language":{"code":"xx_XX","family":"xx"},"quality":"low","files":{}}
		}`, len(h.model), md5hex(h.model), len(h.config), md5hex(h.config), len(h.model), md5hex(h.model), len(h.config), md5hex(h.config))
	})
	serve := func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".onnx"):
			if h.corrupt {
				w.Write(bytes.Repeat([]byte("x"), len(h.model)))
				return
			}
			w.Write(h.model)
		case strings.HasSuffix(r.URL.Path, ".onnx.json"):
			w.Write(h.config)
		case strings.HasSuffix(r.URL.Path, "samples/speaker_0.mp3"):
			w.Write([]byte("ID3demo"))
		case strings.HasSuffix(r.URL.Path, ".tar.gz"):
			w.Write(h.piperTGZ)
		default:
			http.NotFound(w, r)
		}
	}
	mux.HandleFunc("/es/", serve)
	mux.HandleFunc("/piper/", serve)
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHub) manager(t *testing.T) *Manager {
	sum := sha256.Sum256(h.piperTGZ)
	return &Manager{Dir: t.TempDir(), CatalogBase: h.srv.URL + "/", PiperBase: h.srv.URL + "/piper/", PiperSHA256: hex.EncodeToString(sum[:]), AllowHTTP: true}
}

func waitJob(t *testing.T, m *Manager) Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if j := m.Status(); !j.Running {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("download did not finish")
	return Job{}
}

func TestCatalogListsVoicesByLanguageFastestFirst(t *testing.T) {
	m := newHub(t).manager(t)
	ctx := context.Background()
	langs, err := m.Languages(ctx)
	if err != nil || len(langs) != 2 || langs[0].Code != "en" || langs[1].Name != "Spanish (Español)" {
		t.Fatalf("languages = %+v, %v (voices without files and bad keys must not count)", langs, err)
	}
	vs, err := m.List(ctx, "es")
	if err != nil || len(vs) != 2 {
		t.Fatalf("voices = %+v, %v", vs, err)
	}
	if vs[0].Key != "es_ES-carlfm-x_low" || !vs[0].Fast || vs[1].Key != "es_ES-davefx-medium" || vs[1].Fast {
		t.Errorf("order or speed flags wrong: %+v", vs)
	}
}

func TestInstallDownloadsPiperAndTheVoiceThenIsIdempotent(t *testing.T) {
	hub := newHub(t)
	m := hub.manager(t)
	ctx := context.Background()
	applied := 0
	if err := m.Install(ctx, "es_ES-carlfm-x_low", func(context.Context) error { applied++; return nil }); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, m); j.Error != "" || j.Key != "es_ES-carlfm-x_low" {
		t.Fatalf("job = %+v", j)
	}
	if applied != 1 {
		t.Errorf("the done callback ran %d times", applied)
	}
	if !m.Installed("es_ES-carlfm-x_low") || m.Installed("es_ES-davefx-medium") {
		t.Error("Installed flags wrong")
	}
	bin := m.PiperBinary()
	if bin == "" {
		t.Fatal("Piper was not installed")
	}
	if fi, _ := os.Stat(bin); fi.Mode()&0o111 == 0 {
		t.Error("the piper program is not executable")
	}
	if tgt, err := os.Readlink(filepath.Join(m.Dir, "piper", "piper", "libx.so")); err != nil || tgt != "libx.so.1" {
		t.Errorf("symlink not kept: %q %v", tgt, err)
	}
	// A second voice reuses the Piper that is there: its archive is not needed any more.
	hub.piperTGZ = nil
	if err := m.Install(ctx, "es_ES-davefx-medium", nil); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, m); j.Error != "" {
		t.Fatalf("second install: %+v", j)
	}
	vs, _ := m.List(ctx, "es")
	for _, v := range vs {
		if !v.Installed {
			t.Errorf("%s not installed", v.Key)
		}
	}
	if err := m.Remove("es_ES-davefx-medium"); err != nil || m.Installed("es_ES-davefx-medium") {
		t.Errorf("remove: %v", err)
	}
}

func TestInstallRefusesWhatDoesNotMatchTheCatalog(t *testing.T) {
	hub := newHub(t)
	hub.corrupt = true
	m := hub.manager(t)
	ctx := context.Background()
	if err := m.Install(ctx, "es_ES-carlfm-x_low", nil); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, m); !strings.Contains(j.Error, "checksum") {
		t.Fatalf("job = %+v, want a checksum error", j)
	}
	if m.Installed("es_ES-carlfm-x_low") {
		t.Error("a corrupt voice counted as installed")
	}
	for _, key := range []string{"nope", "../evil-key", "es_ES-unknown-low", "xx_XX-nofiles-low"} {
		if m.Install(ctx, key, nil) == nil {
			t.Errorf("%q accepted", key)
		}
	}
	// A Piper archive that does not match the pinned hash is refused too.
	m2 := hub.manager(t)
	m2.PiperSHA256 = strings.Repeat("0", 64)
	hub.corrupt = false
	m2.Install(ctx, "es_ES-carlfm-x_low", nil)
	if j := waitJob(t, m2); !strings.Contains(j.Error, "checksum") || m2.PiperBinary() != "" {
		t.Errorf("bad Piper archive: %+v", j)
	}
}

func TestUntarRefusesUnsafeArchives(t *testing.T) {
	for name, entries := range map[string][]tar.Header{
		"parent path":   {{Name: "../x", Typeflag: tar.TypeReg, Mode: 0o644}},
		"absolute":      {{Name: "/etc/x", Typeflag: tar.TypeReg, Mode: 0o644}},
		"escaping link": {{Name: "piper/l", Typeflag: tar.TypeSymlink, Linkname: "../../etc"}},
		"absolute link": {{Name: "piper/l", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}},
	} {
		dir := t.TempDir()
		archive := filepath.Join(dir, "a.tgz")
		os.WriteFile(archive, tgz(t, entries, map[string]string{}), 0o600)
		out := filepath.Join(dir, "out")
		os.Mkdir(out, 0o755)
		if err := untar(archive, out); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSampleIsFetchedOnceAndOnlyForCatalogVoices(t *testing.T) {
	m := newHub(t).manager(t)
	ctx := context.Background()
	b, err := m.Sample(ctx, "es_ES-davefx-medium")
	if err != nil || string(b) != "ID3demo" {
		t.Fatalf("sample = %q, %v", b, err)
	}
	if _, err := m.Sample(ctx, "es_ES-unknown-low"); err == nil {
		t.Error("a voice that is not in the catalog got a demo")
	}
}

func TestKeyValidation(t *testing.T) {
	for _, ok := range []string{"es_ES-davefx-medium", "es_ES-carlfm-x_low", "en_US-lessac-high", "en_GB-northern_english_male-medium"} {
		if !ValidKey(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "../x", "es_ES-a/b-low", "es_ES-davefx", "ES_es-x-low", "es_ES-x-low\n"} {
		if ValidKey(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
