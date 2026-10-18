package web

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/juancer/milo/internal/assistant"
	"github.com/juancer/milo/internal/speech"
	"github.com/juancer/milo/internal/voices"
)

func (s *server) voiceRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/voices", s.requireAuth(s.listVoices))
	mux.Handle("GET /api/voices/status", s.requireAuth(s.voiceStatus))
	mux.Handle("GET /api/voices/{key}/sample", s.requireAuth(s.voiceSample))
	mux.Handle("GET /api/voices/{key}/similar", s.requireAuth(s.similarVoices))
	mux.Handle("POST /api/voices/{key}/install", s.requireAuth(s.installVoice))
	mux.Handle("POST /api/voices/{key}/use", s.requireAuth(s.useVoice))
	mux.Handle("DELETE /api/voices/{key}", s.requireAuth(s.deleteVoice))
}

// listVoices answers with the languages that have voices and the voices of one of them. The
// language defaults to the assistant's own.
func (s *server) listVoices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	g, err := s.app.Store.GetGPTSettings(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	family := r.URL.Query().Get("lang")
	if family == "" {
		family, _, _ = strings.Cut(strings.ReplaceAll(g.SpeechLanguage(), "-", "_"), "_")
	}
	if len(family) < 2 || len(family) > 3 {
		family = "en"
	}
	langs, err := s.app.Voices.Languages(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "web.err.could_not_reach_the_voice_catalog")
		return
	}
	list, err := s.app.Voices.List(ctx, family)
	if err != nil {
		writeError(w, http.StatusBadGateway, "web.err.could_not_reach_the_voice_catalog")
		return
	}
	if list == nil {
		list = []voices.Voice{}
	}
	inUse := s.voicesInUse(ctx, g.TTSEngine, g.TTSModelPath)
	current := ""
	for _, u := range inUse {
		if u.Language == family {
			current = u.Key // the voice in use for the language being listed
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"language": family, "languages": langs, "voices": list, "current": current, "in_use": inUse,
		"piper_installed": s.app.Voices.PiperInstalled(), "job": s.app.Voices.Status(),
	})
}

// voiceInUse is the voice Milo speaks with in one language.
type voiceInUse struct {
	Language string `json:"language"` // family: es
	Key      string `json:"key"`
}

// voicesInUse lists the voice chosen for each language. A voice chosen before per-language choices
// existed (the single voice in the assistant settings) counts for its own language until one is
// chosen explicitly.
func (s *server) voicesInUse(ctx context.Context, engine, fallbackModel string) []voiceInUse {
	chosen := s.app.VoiceChoices(ctx)
	out := []voiceInUse{}
	for _, key := range s.app.Voices.InstalledKeys() {
		family, path := voices.Family(key), s.app.Voices.ModelPath(key)
		if explicit, ok := chosen[family]; (ok && explicit == path) || (!ok && engine == "piper" && fallbackModel == path) {
			out = append(out, voiceInUse{Language: family, Key: key})
		}
	}
	return out
}

// similarVoices suggests voices of another language that sound like one already chosen. It
// matches the pitch of the voices' demos only, so the owner has to listen to the suggestions.
func (s *server) similarVoices(w http.ResponseWriter, r *http.Request) {
	key, lang := r.PathValue("key"), r.URL.Query().Get("lang")
	if !voices.ValidKey(key) || len(lang) < 2 || len(lang) > 3 {
		writeError(w, http.StatusNotFound, "web.err.unknown_voice")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	res, err := s.app.Voices.Similar(ctx, key, strings.ToLower(lang), 3)
	if err != nil {
		writeError(w, http.StatusBadGateway, "web.err.could_not_compare_the_voices")
		return
	}
	if res.Matches == nil {
		res.Matches = []voices.Match{}
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) voiceStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.Voices.Status())
}

func (s *server) voiceSample(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !voices.ValidKey(key) {
		writeError(w, http.StatusNotFound, "web.err.unknown_voice")
		return
	}
	b, err := s.app.Voices.Sample(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusBadGateway, "web.err.could_not_reach_the_voice_catalog")
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(b) //nolint:errcheck
}

// installVoice starts the download. With {"use": true} the voice becomes the assistant's voice
// as soon as it has arrived.
func (s *server) installVoice(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var body struct {
		Use bool `json:"use"`
	}
	if r.ContentLength > 0 && !decodeJSON(w, r, &body) {
		return
	}
	if !voices.ValidKey(key) {
		writeError(w, http.StatusNotFound, "web.err.unknown_voice")
		return
	}
	var done func(context.Context) error
	if body.Use {
		done = func(c context.Context) error { return s.applyVoice(c, key) }
	}
	if err := s.app.Voices.Install(r.Context(), key, done); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.audit(r, "voice.install_started", key, nil)
	writeJSON(w, http.StatusAccepted, s.app.Voices.Status())
}

func (s *server) useVoice(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !voices.ValidKey(key) || !s.app.Voices.Installed(key) {
		writeError(w, http.StatusNotFound, "web.err.unknown_voice")
		return
	}
	if err := s.applyVoice(r.Context(), key); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.could_not_save_the_settings")
		return
	}
	s.audit(r, "voice.selected", key, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) deleteVoice(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !voices.ValidKey(key) {
		writeError(w, http.StatusNotFound, "web.err.unknown_voice")
		return
	}
	if g, err := s.app.Store.GetGPTSettings(r.Context()); err == nil {
		for _, u := range s.voicesInUse(r.Context(), g.TTSEngine, g.TTSModelPath) {
			if u.Key == key {
				writeError(w, http.StatusConflict, "web.err.that_voice_is_in_use_choose_another_first")
				return
			}
		}
	}
	if err := s.app.Voices.Remove(key); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.audit(r, "voice.removed", key, nil)
	w.WriteHeader(http.StatusNoContent)
}

// applyVoice makes an installed voice the one Milo speaks with in that voice's language. The voice
// in the assistant settings stays as the fallback for languages with no voice of their own: it is
// replaced when it is the first voice, in the same language, or no longer on the device, and kept
// when it is a voice file of the owner's own.
func (s *server) applyVoice(ctx context.Context, key string) error {
	g, err := s.app.Store.GetGPTSettings(ctx)
	if err != nil {
		return err
	}
	family, path := voices.Family(key), s.app.Voices.ModelPath(key)
	if err := s.app.Store.SetSetting(ctx, voices.SettingKey(family), path); err != nil {
		return err
	}
	_, statErr := os.Stat(g.TTSModelPath)
	if g.TTSEngine != "piper" || g.TTSModelPath == "" || statErr != nil || speech.ModelFamily(g.TTSModelPath) == family {
		g.TTSEngine, g.TTSModelPath = "piper", path
		if err := s.app.Store.UpdateGPTSettings(ctx, g); err != nil {
			return err
		}
	}
	s.restartFeature(ctx, assistant.FeatureName)
	return nil
}
