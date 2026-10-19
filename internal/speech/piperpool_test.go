package speech

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juancer/milo/internal/audio"
)

func init() { piperTimeout = 700 * time.Millisecond } // a fake that never answers must not hold a test up

// fakeJSONPiper is a Piper that understands --json-input: it counts how often it was started and how
// many phrases it was asked for, and answers each with the path of a WAV it writes.
func fakeJSONPiper(t *testing.T) (bin, starts, phrases string) {
	t.Helper()
	dir := t.TempDir()
	wav := filepath.Join(dir, "template.wav")
	if err := os.WriteFile(wav, audio.EncodeWAV(audio.Silence(300*time.Millisecond), audio.SampleRate), 0o644); err != nil {
		t.Fatal(err)
	}
	bin, starts, phrases = filepath.Join(dir, "piper"), filepath.Join(dir, "starts"), filepath.Join(dir, "phrases")
	script := "#!/bin/sh\n" +
		"echo start >> " + starts + "\n" +
		"out=\"\"; while [ $# -gt 0 ]; do [ \"$1\" = \"--output_dir\" ] && out=\"$2\"; shift; done\n" +
		"n=0\n" +
		"while read -r line; do n=$((n+1)); echo \"$line\" >> " + phrases + "; cp " + wav + " \"$out/phrase$n.wav\"; echo \"$out/phrase$n.wav\"; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return
}

func lines(path string) int {
	b, _ := os.ReadFile(path)
	return strings.Count(string(b), "\n")
}

func TestPiperStaysLoadedBetweenPhrases(t *testing.T) {
	bin, starts, phrases := fakeJSONPiper(t)
	SetCacheDir("")
	t.Cleanup(StopPiper)
	model := filepath.Join(t.TempDir(), "es_ES-test-low.onnx")
	p, err := NewPiper(bin, model)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Hola.", "Son las tres.", "Adiós."} {
		if _, err := p.Synthesize(context.Background(), text); err != nil {
			t.Fatal(err)
		}
	}
	if lines(starts) != 1 || lines(phrases) != 3 {
		t.Fatalf("started %d times for %d phrases, want one process for three phrases", lines(starts), lines(phrases))
	}
}

func TestRepeatedPhrasesComeFromTheCache(t *testing.T) {
	bin, _, phrases := fakeJSONPiper(t)
	SetCacheDir(t.TempDir())
	t.Cleanup(func() { SetCacheDir(""); StopPiper() })
	model := filepath.Join(t.TempDir(), "es_ES-test-low.onnx")
	p, _ := NewPiper(bin, model)
	for i := 0; i < 3; i++ {
		if _, err := p.Synthesize(context.Background(), "Hasta luego."); err != nil {
			t.Fatal(err)
		}
	}
	if lines(phrases) != 1 {
		t.Errorf("Piper was asked %d times for the same phrase, want once", lines(phrases))
	}
	// Another voice, or another speed, is another phrase.
	other, _ := NewPiper(bin, filepath.Join(t.TempDir(), "es_ES-other-low.onnx"))
	if _, err := other.Synthesize(context.Background(), "Hasta luego."); err != nil {
		t.Fatal(err)
	}
	if lines(phrases) != 2 {
		t.Errorf("a different voice reused the cache (%d requests)", lines(phrases))
	}
}

func TestAPiperThatIgnoresJSONModeIsUsedOneShotAfterTheFirstFailure(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "template.wav")
	os.WriteFile(wav, audio.EncodeWAV(audio.Silence(300*time.Millisecond), audio.SampleRate), 0o644) //nolint:errcheck
	starts := filepath.Join(dir, "starts")
	bin := filepath.Join(dir, "piper")
	// An old Piper: it ignores --json-input, reads one text from stdin and writes --output_file.
	script := "#!/bin/sh\necho start >> " + starts + "\nout=\"\"; json=0; while [ $# -gt 0 ]; do [ \"$1\" = \"--output_file\" ] && out=\"$2\"; [ \"$1\" = \"--json-input\" ] && json=1; shift; done\n" +
		"[ $json = 1 ] && sleep 30\ncat >/dev/null; cp " + wav + " \"$out\"\n"
	os.WriteFile(bin, []byte(script), 0o755) //nolint:errcheck
	SetCacheDir("")
	t.Cleanup(StopPiper)
	p, _ := NewPiper(bin, filepath.Join(dir, "es_ES-old-low.onnx"))
	start := time.Now()
	for _, text := range []string{"Uno.", "Dos."} {
		if _, err := p.Synthesize(context.Background(), text); err != nil {
			t.Fatalf("%q: %v", text, err)
		}
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v: the second phrase waited for the long-lived mode again", time.Since(start))
	}
}
