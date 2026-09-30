package speech

import (
	"context"
	"encoding/json"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juancer/nexo/internal/audio"
)

func TestOpenAICompatSTT(t *testing.T) {
	var gotAuth, gotModel string
	var gotFile []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			buf := make([]byte, 1<<16)
			n, _ := p.Read(buf)
			switch p.FormName() {
			case "model":
				gotModel = string(buf[:n])
			case "file":
				gotFile = append([]byte(nil), buf[:n]...)
			}
		}
		w.Write([]byte(`{"text":"  what time is it  "}`))
	}))
	defer srv.Close()

	wav := audio.EncodeWAV(audio.Silence(200*time.Millisecond), 16000)
	text, err := NewOpenAICompatSTT(srv.URL, "gsk-TEST", "whisper-large-v3-turbo").Transcribe(context.Background(), wav)
	if err != nil || text != "what time is it" {
		t.Fatalf("Transcribe = %q, %v", text, err)
	}
	if gotAuth != "Bearer gsk-TEST" || gotModel != "whisper-large-v3-turbo" || !strings.HasPrefix(string(gotFile), "RIFF") {
		t.Fatalf("auth=%q model=%q file=%q", gotAuth, gotModel, gotFile[:4])
	}
}

func TestSTTErrorsAreSafe(t *testing.T) {
	if _, err := NewOpenAICompatSTT("http://x", "", "m").Transcribe(context.Background(), nil); err == nil {
		t.Fatal("missing key must fail")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	_, err := NewOpenAICompatSTT(srv.URL, "gsk-SECRET", "m").Transcribe(context.Background(), []byte("x"))
	if err == nil || strings.Contains(err.Error(), "gsk-SECRET") {
		t.Fatalf("err = %v", err)
	}
	_, err = NewOpenAICompatSTT("http://127.0.0.1:1", "gsk-SECRET", "m").Transcribe(context.Background(), []byte("x"))
	if err == nil || strings.Contains(err.Error(), "gsk-SECRET") {
		t.Fatalf("network err leaks details: %v", err)
	}
}

func TestPiperValidation(t *testing.T) {
	good := "/opt/piper/voices/en_US-lessac-medium.onnx"
	if _, err := NewPiper("piper", good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "voice.txt", "/x/../etc/passwd.onnx", "a; rm -rf /.onnx", "$(id).onnx", "voice .onnx"} {
		if _, err := NewPiper("piper", bad); err == nil {
			t.Errorf("model %q accepted", bad)
		}
	}
	for _, bad := range []string{"piper; id", "../piper", "pi per", "`id`"} {
		if _, err := NewPiper(bad, good); err == nil {
			t.Errorf("binary %q accepted", bad)
		}
	}
}

// A stand-in for the piper binary: reads text on stdin, writes a tiny WAV to --output_file.
func fakePiper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "piper")
	src := `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
  case "$1" in --output_file) out="$2"; shift;; esac
  shift
done
cat > "$out.txt"
printf 'RIFF\044\000\000\000WAVEfmt \020\000\000\000\001\000\001\000\200\076\000\000\000\175\000\000\002\000\020\000data\000\000\000\000' > "$out"
`
	if err := os.WriteFile(script, []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestPiperSynthesizesThroughAFixedCommand(t *testing.T) {
	bin := fakePiper(t)
	p, err := NewPiper(bin, "/voices/x.onnx")
	if err != nil {
		t.Fatal(err)
	}
	wav, err := p.Synthesize(context.Background(), "hello $(rm -rf /) `id` ; there")
	if err != nil {
		t.Fatal(err)
	}
	if _, rate, err := audio.DecodeWAV(wav); err != nil || rate != 16000 {
		t.Fatalf("decode: rate=%d err=%v", rate, err)
	}
	if _, err := p.Synthesize(context.Background(), "   "); err == nil {
		t.Fatal("empty text accepted")
	}
	missing, _ := NewPiper("/nonexistent/piper", "/voices/x.onnx")
	if _, err := missing.Synthesize(context.Background(), "hi"); err == nil || !strings.Contains(err.Error(), "piper not found") {
		t.Fatalf("missing binary err = %v", err)
	}
}

func TestMockSpeechRoundTrip(t *testing.T) {
	wav, _ := MockTTS{}.Synthesize(context.Background(), "hello little one")
	if audio.ExtractText(wav) != "hello little one" {
		t.Fatal("mock TTS lost the text")
	}
	text, _ := EmbeddedTextSTT{}.Transcribe(context.Background(), audio.EmbedText(audio.EncodeWAV(nil, 16000), "play frozen"))
	if text != "play frozen" {
		t.Fatalf("EmbeddedTextSTT = %q", text)
	}
}

func TestOpenAICompatTTS(t *testing.T) {
	wav := audio.EncodeWAV(audio.Silence(100*time.Millisecond), 16000)
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/speech" || r.Header.Get("Authorization") != "Bearer tts-key" {
			w.WriteHeader(401)
			return
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(wav)
	}))
	defer srv.Close()
	got, err := NewOpenAICompatTTS(srv.URL, "tts-key", "playai-tts", "Fritz-PlayAI").Synthesize(context.Background(), "hello")
	if err != nil || len(got) != len(wav) {
		t.Fatalf("Synthesize = %d bytes, %v", len(got), err)
	}
	if body["model"] != "playai-tts" || body["voice"] != "Fritz-PlayAI" || body["input"] != "hello" || body["response_format"] != "wav" {
		t.Fatalf("request body = %v", body)
	}
	if _, err := NewOpenAICompatTTS(srv.URL, "", "m", "").Synthesize(context.Background(), "x"); err == nil {
		t.Fatal("missing key accepted")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("not audio")) }))
	defer bad.Close()
	if _, err := NewOpenAICompatTTS(bad.URL, "k", "m", "").Synthesize(context.Background(), "x"); err == nil {
		t.Fatal("garbage audio accepted")
	}
	_, err = NewOpenAICompatTTS("http://127.0.0.1:1", "SECRETKEY", "m", "").Synthesize(context.Background(), "x")
	if err == nil || strings.Contains(err.Error(), "SECRETKEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestWhisperCppUsesAFixedCommand(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "whisper-cli")
	os.WriteFile(bin, []byte("#!/bin/sh\n# args: -m model -f file -nt -np\nif [ \"$1\" != \"-m\" ] || [ \"$3\" != \"-f\" ] || [ \"$5\" != \"-nt\" ] || [ \"$6\" != \"-np\" ]; then exit 3; fi\n[ -s \"$4\" ] || exit 4\nprintf '  hello   from\\n the   device \\n'\n"), 0o755)
	w, err := NewWhisperCpp(bin, "/models/ggml-tiny.bin")
	if err != nil {
		t.Fatal(err)
	}
	wav := audio.EncodeWAV(audio.Silence(100*time.Millisecond), 16000)
	text, err := w.Transcribe(context.Background(), wav)
	if err != nil || text != "hello from the device" {
		t.Fatalf("Transcribe = %q, %v", text, err)
	}
	for _, bad := range []string{"", "model.txt", "../x.bin", "a b.bin", "$(id).bin"} {
		if _, err := NewWhisperCpp(bin, bad); err == nil {
			t.Errorf("model %q accepted", bad)
		}
	}
	if _, err := NewWhisperCpp("bad binary;", "/m/x.bin"); err == nil {
		t.Error("bad binary accepted")
	}
	missing, _ := NewWhisperCpp("/nonexistent/whisper", "/m/x.bin")
	if _, err := missing.Transcribe(context.Background(), wav); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing binary err = %v", err)
	}
}

func TestBinaryMustBeAbsoluteOrBare(t *testing.T) {
	good := "/opt/piper/voices/en_US-lessac-medium.onnx"
	for _, bad := range []string{"./piper", "bin/piper", "sub/dir/piper"} {
		if _, err := NewPiper(bad, good); err == nil {
			t.Errorf("piper binary %q accepted", bad)
		}
		if _, err := NewWhisperCpp(bad, "/m/ggml.bin"); err == nil {
			t.Errorf("whisper binary %q accepted", bad)
		}
	}
	if _, err := NewPiper("/usr/bin/piper", good); err != nil {
		t.Error(err)
	}
}
