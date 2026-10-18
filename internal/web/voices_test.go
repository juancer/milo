package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/juancer/milo/internal/voices"
)

// installFakeVoice puts the two files of a voice where the Manager looks for them.
func installFakeVoice(t *testing.T, e *env, key string) string {
	t.Helper()
	dir := filepath.Join(e.app.Voices.Dir, "voices")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := e.app.Voices.ModelPath(key)
	for _, p := range []string{path, path + ".json"} {
		if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func inUseMap(t *testing.T, e *env) map[string]string {
	t.Helper()
	g, err := e.store.GetGPTSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, u := range e.impl.voicesInUse(context.Background(), g.TTSEngine, g.TTSModelPath) {
		out[u.Language] = u.Key
	}
	return out
}

func TestChoosingVoicesPerLanguage(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	ctx := context.Background()
	spanish := installFakeVoice(t, e, "es_ES-carlfm-x_low")
	english := installFakeVoice(t, e, "en_US-amy-low")
	otherEnglish := installFakeVoice(t, e, "en_US-lessac-low")

	use := func(key string) {
		t.Helper()
		if resp := e.do(t, "POST", "/api/voices/"+key+"/use", "", cookie, nil); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("use %s: status %d", key, resp.StatusCode)
		}
	}

	// The first voice becomes the speaking voice and the fallback.
	use("es_ES-carlfm-x_low")
	g, _ := e.store.GetGPTSettings(ctx)
	if g.TTSEngine != "piper" || g.TTSModelPath != spanish {
		t.Fatalf("first voice: engine %q, model %q", g.TTSEngine, g.TTSModelPath)
	}

	// A voice in another language is added next to it: it does not replace the fallback.
	use("en_US-amy-low")
	g, _ = e.store.GetGPTSettings(ctx)
	if g.TTSModelPath != spanish {
		t.Errorf("a voice in another language replaced the fallback: %q", g.TTSModelPath)
	}
	if got := e.app.VoiceChoices(ctx); got["es"] != spanish || got["en"] != english || len(got) != 2 {
		t.Errorf("choices = %v", got)
	}
	if got := inUseMap(t, e); got["es"] != "es_ES-carlfm-x_low" || got["en"] != "en_US-amy-low" || len(got) != 2 {
		t.Errorf("in use = %v", got)
	}

	// Choosing another voice for a language replaces that language's voice only.
	use("en_US-lessac-low")
	if got := e.app.VoiceChoices(ctx); got["en"] != otherEnglish || got["es"] != spanish {
		t.Errorf("choices after changing English = %v", got)
	}
	if got := inUseMap(t, e); got["en"] != "en_US-lessac-low" {
		t.Errorf("in use after changing English = %v", got)
	}

	// Choosing a voice in the fallback's language moves the fallback too.
	installFakeVoice(t, e, "es_ES-davefx-low")
	use("es_ES-davefx-low")
	g, _ = e.store.GetGPTSettings(ctx)
	if g.TTSModelPath != e.app.Voices.ModelPath("es_ES-davefx-low") {
		t.Errorf("fallback = %q, want the new Spanish voice", g.TTSModelPath)
	}

	// A voice that is in use cannot be removed; one that is not can.
	for key, want := range map[string]int{"en_US-lessac-low": http.StatusConflict, "es_ES-davefx-low": http.StatusConflict, "en_US-amy-low": http.StatusNoContent, "es_ES-carlfm-x_low": http.StatusNoContent} {
		if resp := e.do(t, "DELETE", "/api/voices/"+key, "", cookie, nil); resp.StatusCode != want {
			t.Errorf("DELETE %s: status %d, want %d", key, resp.StatusCode, want)
		}
	}
	if e.app.Voices.Installed("en_US-amy-low") {
		t.Error("the unused voice was not removed")
	}
}

func TestAVoiceFileOfTheOwnersOwnStaysAsTheFallback(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	ctx := context.Background()
	own := filepath.Join(t.TempDir(), "mine.onnx")
	if err := os.WriteFile(own, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	g, _ := e.store.GetGPTSettings(ctx)
	g.TTSEngine, g.TTSModelPath = "piper", own
	if err := e.store.UpdateGPTSettings(ctx, g); err != nil {
		t.Fatal(err)
	}
	installFakeVoice(t, e, "es_ES-carlfm-x_low")
	if resp := e.do(t, "POST", "/api/voices/es_ES-carlfm-x_low/use", "", cookie, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", resp.StatusCode)
	}
	g, _ = e.store.GetGPTSettings(ctx)
	if g.TTSModelPath != own {
		t.Errorf("the owner's own voice file was replaced: %q", g.TTSModelPath)
	}
	if got := e.app.VoiceChoices(ctx); got["es"] == "" {
		t.Errorf("the Spanish choice was not saved: %v", got)
	}
}

func TestSimilarVoicesEndpointValidatesInput(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	for _, path := range []string{"/api/voices/not-a-key/similar?lang=es", "/api/voices/es_ES-carlfm-x_low/similar", "/api/voices/es_ES-carlfm-x_low/similar?lang=e", "/api/voices/es_ES-carlfm-x_low/similar?lang=spanish"} {
		if resp := e.do(t, "GET", path, "", cookie, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404", path, resp.StatusCode)
		}
	}
	if resp := e.do(t, "GET", "/api/voices/es_ES-carlfm-x_low/similar?lang=es", "", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated: status %d", resp.StatusCode)
	}
}

func TestVoiceKeysAreFamiliesAndSettingKeys(t *testing.T) {
	if voices.Family("es_ES-carlfm-x_low") != "es" || voices.SettingKey("es") != "tts.voice.es" {
		t.Error("helpers changed")
	}
}
