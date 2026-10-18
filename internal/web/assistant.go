package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/juancer/milo/internal/app"
	"github.com/juancer/milo/internal/assistant"
	"github.com/juancer/milo/internal/i18n"
	"github.com/juancer/milo/internal/secrets"
	"github.com/juancer/milo/internal/speech"
	"github.com/juancer/milo/internal/store"
)

var modelRe = regexp.MustCompile(`^[A-Za-z0-9._:/@+-]{1,100}$`)

func (s *server) assistantRoutes(mux *http.ServeMux) {
	s.voiceRoutes(mux)
	mux.Handle("GET /api/assistant", s.requireAuth(s.getAssistant))
	mux.Handle("PUT /api/assistant", s.requireAuth(s.putAssistant))
	mux.Handle("PUT /api/assistant/keys", s.requireAuth(s.putKeys))
	mux.Handle("DELETE /api/assistant/keys/{which}", s.requireAuth(s.deleteKey))
	mux.Handle("GET /api/assistant/models", s.requireAuth(s.listProviderModels))
	mux.Handle("GET /api/moderation-rules", s.requireAuth(s.listRules))
	mux.Handle("POST /api/moderation-rules", s.requireAuth(s.createRule))
	mux.Handle("DELETE /api/moderation-rules/{id}", s.requireAuth(s.deleteRule))
	mux.Handle("GET /api/conversations", s.requireAuth(s.listConversations))
	mux.Handle("DELETE /api/conversations", s.requireAuth(s.deleteConversations))
}

type assistantDTO struct {
	ChildName         string  `json:"child_name"`
	Age               int     `json:"age"`
	Personality       string  `json:"personality"`
	Language          string  `json:"language"`
	MaxResponseWords  int     `json:"max_response_words"`
	ExtraInstructions string  `json:"extra_instructions"`
	RetentionDays     int     `json:"retention_days"`
	InputModeration   bool    `json:"input_moderation"`
	OutputModeration  bool    `json:"output_moderation"`
	AlertParents      bool    `json:"alert_parents"`
	FollowUp          bool    `json:"follow_up"`
	MaxTurnsPerHour   int     `json:"max_turns_per_hour"`
	LLMBaseURL        string  `json:"llm_base_url"`
	LLMModel          string  `json:"llm_model"`
	STTEngine         string  `json:"stt_engine"`
	STTBaseURL        string  `json:"stt_base_url"`
	STTModel          string  `json:"stt_model"`
	STTModelPath      string  `json:"stt_model_path"`
	TTSEngine         string  `json:"tts_engine"`
	TTSModelPath      string  `json:"tts_model_path"`
	TTSBaseURL        string  `json:"tts_base_url"`
	TTSModel          string  `json:"tts_model"`
	TTSVoice          string  `json:"tts_voice"`
	TTSLanguage       string  `json:"tts_language"` // "" follows the assistant language
	TTSSpeed          float64 `json:"tts_speed"`
	TTSVolume         int     `json:"tts_volume"`
}

func toAssistantDTO(g store.GPTSettings) assistantDTO {
	return assistantDTO{g.ChildName, g.Age, g.Personality, g.Language, g.MaxResponseWords, g.ExtraInstructions,
		g.RetentionDays, g.InputModeration, g.OutputModeration, g.AlertParents, g.FollowUp, g.MaxTurnsPerHour,
		g.LLMBaseURL, g.LLMModel, g.STTEngine, g.STTBaseURL, g.STTModel, g.STTModelPath,
		g.TTSEngine, g.TTSModelPath, g.TTSBaseURL, g.TTSModel, g.TTSVoice, g.TTSLanguage, g.TTSSpeed, g.TTSVolume}
}

func (d assistantDTO) toStore() store.GPTSettings {
	return store.GPTSettings{ChildName: d.ChildName, Age: d.Age, Personality: d.Personality, Language: d.Language,
		MaxResponseWords: d.MaxResponseWords, ExtraInstructions: d.ExtraInstructions, RetentionDays: d.RetentionDays,
		InputModeration: d.InputModeration, OutputModeration: d.OutputModeration, AlertParents: d.AlertParents,
		FollowUp: d.FollowUp, MaxTurnsPerHour: d.MaxTurnsPerHour, LLMBaseURL: d.LLMBaseURL, LLMModel: d.LLMModel,
		STTEngine: d.STTEngine, STTBaseURL: d.STTBaseURL, STTModel: d.STTModel, STTModelPath: d.STTModelPath,
		TTSEngine: d.TTSEngine, TTSModelPath: d.TTSModelPath, TTSBaseURL: d.TTSBaseURL, TTSModel: d.TTSModel, TTSVoice: d.TTSVoice,
		TTSLanguage: d.TTSLanguage, TTSSpeed: d.TTSSpeed, TTSVolume: d.TTSVolume}
}

func (s *server) validAPIURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || len(raw) > 200 || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http": // plain HTTP only for local development
		host := u.Hostname()
		return s.app.Cfg.Hardware == "mock" || host == "localhost" || host == "127.0.0.1"
	}
	return false
}

func (s *server) validateAssistant(d assistantDTO) string {
	switch {
	case len([]rune(d.ChildName)) > 40:
		return "web.err.the_child_s_name_must_be_at_most_40_characte"
	case d.Age < 3 || d.Age > 17:
		return "web.err.age_must_be_between_3_and_17"
	case d.Personality == "" || len([]rune(d.Personality)) > 200:
		return "web.err.personality_must_be_1_200_characters"
	case !validAssistantLanguage(d.Language):
		return "web.err.the_assistant_language_must_be_a_supported_l"
	case d.MaxResponseWords < 10 || d.MaxResponseWords > 300:
		return "web.err.response_length_must_be_10_300_words"
	case len([]rune(d.ExtraInstructions)) > 600:
		return "web.err.extra_instructions_must_be_at_most_600_chara"
	case d.RetentionDays != 0 && d.RetentionDays != 7 && d.RetentionDays != 30 && d.RetentionDays != 90:
		return "web.err.history_retention_must_be_0_off_7_30_or_90_d"
	case d.MaxTurnsPerHour < 5 || d.MaxTurnsPerHour > 600:
		return "web.err.questions_per_hour_must_be_5_600"
	case !s.validAPIURL(d.LLMBaseURL) || !s.validAPIURL(d.STTBaseURL) || !s.validAPIURL(d.TTSBaseURL):
		return "web.err.provider_addresses_must_be_https_urls"
	case !modelRe.MatchString(d.LLMModel) || !modelRe.MatchString(d.STTModel) || (d.TTSModel != "" && !modelRe.MatchString(d.TTSModel)):
		return "web.err.model_names_may_only_contain_letters_digits"
	case len(d.TTSVoice) > 60 || strings.ContainsAny(d.TTSVoice, "\r\n\t"):
		return "web.err.the_voice_name_is_too_long_or_invalid"
	case d.TTSLanguage != "" && !validLanguage(d.TTSLanguage):
		return "web.err.the_voice_language_must_be_a_supported_langu"
	case d.TTSSpeed < 0.5 || d.TTSSpeed > 2:
		return "web.err.voice_speed_must_be_0_5_2"
	case d.TTSVolume < 0 || d.TTSVolume > 100:
		return "web.err.voice_volume_must_be_0_100"
	case d.STTEngine != "cloud" && d.STTEngine != "whispercpp":
		return "web.err.speech_to_text_engine_must_be_cloud_or_whisp"
	case d.STTEngine == "whispercpp" && d.STTModelPath != "" && !speech.ValidWhisperModelPath(d.STTModelPath):
		return "web.err.the_whisper_cpp_model_must_be_a_path_ending"
	case d.TTSEngine != "piper" && d.TTSEngine != "cloud" && d.TTSEngine != "none":
		return "web.err.text_to_speech_engine_must_be_piper_cloud_or"
	case d.TTSEngine == "piper" && d.TTSModelPath != "" && !speech.ValidModelPath(d.TTSModelPath):
		return "web.err.the_piper_voice_must_be_a_path_ending_in_onn"
	}
	return ""
}

func (s *server) hasKey(ctx context.Context, name string) bool {
	v, err := s.app.Vault.Get(ctx, name)
	return err == nil && len(v) > 0
}

// mayHaveKey is hasKey for deciding whether to delete: a key that exists but cannot be read
// counts as present.
func (s *server) mayHaveKey(ctx context.Context, name string) bool {
	v, err := s.app.Vault.Get(ctx, name)
	if errors.Is(err, secrets.ErrNotFound) {
		return false
	}
	return err != nil || len(v) > 0
}

func (s *server) getAssistant(w http.ResponseWriter, r *http.Request) { s.writeAssistant(w, r, nil) }

// writeAssistant answers with the settings; cleared names the keys that were
// removed because their provider address changed.
func (s *server) writeAssistant(w http.ResponseWriter, r *http.Request, cleared []string) {
	ctx := r.Context()
	g, err := s.app.Store.GetGPTSettings(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	status := map[string]any{"enabled": false, "running": false}
	if list, err := s.app.Features.List(ctx); err == nil {
		for _, f := range list {
			if f.Name == assistant.FeatureName {
				status = map[string]any{"enabled": f.Enabled, "running": f.Running, "last_error": f.LastError}
			}
		}
	}
	resp := map[string]any{
		"settings": toAssistantDTO(g), "status": status,
		"llm_key_configured": s.hasKey(ctx, app.SecretLLMKey), "stt_key_configured": s.hasKey(ctx, app.SecretSTTKey),
		"tts_key_configured": s.hasKey(ctx, app.SecretTTSKey),
	}
	if len(cleared) > 0 {
		resp["keys_cleared"] = cleared
		resp["notice"] = i18n.Tf(i18n.Interface(r.Context(), s.app.Store), "web.notice.provider_key_removed", "keys", strings.Join(cleared, ", "))
	}
	writeJSON(w, http.StatusOK, resp)
}

// sameHost reports whether two provider addresses point at the same host.
func sameHost(a, b string) bool {
	ua, ea := url.Parse(a)
	ub, eb := url.Parse(b)
	return ea == nil && eb == nil && strings.EqualFold(ua.Host, ub.Host)
}

func (s *server) putAssistant(w http.ResponseWriter, r *http.Request) {
	old, err := s.app.Store.GetGPTSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	// A body that leaves out the voice options keeps what is stored.
	d := assistantDTO{TTSLanguage: old.TTSLanguage, TTSSpeed: old.TTSSpeed, TTSVolume: old.TTSVolume}
	if !decodeJSONLimit(w, r, &d, 16<<10) {
		return
	}
	d.ChildName, d.Personality = strings.TrimSpace(d.ChildName), strings.TrimSpace(d.Personality)
	if msg := s.validateAssistant(d); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	// A stored key must never be sent to a different host than the one it was
	// entered for: drop it when the address changes (keys are set separately).
	// The key goes first, so no provider call can pair the new address with the
	// old key. A key that cannot be read counts as present.
	var cleared []string
	for _, k := range []struct{ label, secret, oldURL, newURL string }{
		{"AI", app.SecretLLMKey, old.LLMBaseURL, d.LLMBaseURL},
		{"speech-to-text", app.SecretSTTKey, old.STTBaseURL, d.STTBaseURL},
		{"text-to-speech", app.SecretTTSKey, old.TTSBaseURL, d.TTSBaseURL},
	} {
		if !sameHost(k.oldURL, k.newURL) && s.mayHaveKey(r.Context(), k.secret) {
			if err := s.app.Store.DeleteSecret(r.Context(), k.secret); err != nil {
				writeError(w, http.StatusInternalServerError, "web.err.internal_error")
				return
			}
			s.audit(r, "assistant.key_removed", k.label+" (address changed)", nil)
			cleared = append(cleared, k.label)
		}
	}
	saveErr := s.app.Store.UpdateGPTSettings(r.Context(), d.toStore())
	if len(cleared) > 0 {
		s.restartFeature(r.Context(), assistant.FeatureName)
	}
	if saveErr != nil {
		writeError(w, http.StatusInternalServerError, "web.err.could_not_save_the_settings")
		return
	}
	s.audit(r, "assistant.settings_updated", "", nil)
	s.app.Assistant.PruneHistory(r.Context()) // apply a shorter retention right away
	s.writeAssistant(w, r, cleared)
}

func (s *server) putKeys(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LLMKey string `json:"llm_api_key"`
		STTKey string `json:"stt_api_key"`
		TTSKey string `json:"tts_api_key"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	set := func(name, label, value string) bool {
		value = strings.TrimSpace(value)
		if value == "" {
			return true // unchanged
		}
		if len(value) < 8 || len(value) > 300 || strings.ContainsAny(value, " \t\r\n") {
			writeError(w, http.StatusBadRequest, "web.err.not_an_api_key", "label", label)
			return false
		}
		if err := s.app.Vault.Put(r.Context(), name, []byte(value)); err != nil {
			writeError(w, http.StatusInternalServerError, "web.err.could_not_store_the_key")
			return false
		}
		s.audit(r, "assistant.key_set", label, nil) // never the value
		return true
	}
	if !set(app.SecretLLMKey, "AI key", body.LLMKey) || !set(app.SecretSTTKey, "speech-to-text key", body.STTKey) ||
		!set(app.SecretTTSKey, "text-to-speech key", body.TTSKey) {
		return
	}
	s.restartFeature(r.Context(), assistant.FeatureName)
	s.getAssistant(w, r)
}

func (s *server) deleteKey(w http.ResponseWriter, r *http.Request) {
	name := map[string]string{"llm": app.SecretLLMKey, "stt": app.SecretSTTKey, "tts": app.SecretTTSKey}[r.PathValue("which")]
	if name == "" {
		writeError(w, http.StatusNotFound, "web.err.unknown_key")
		return
	}
	if err := s.app.Store.DeleteSecret(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.audit(r, "assistant.key_removed", r.PathValue("which"), nil)
	s.restartFeature(r.Context(), assistant.FeatureName)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) restartFeature(ctx context.Context, name string) {
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.app.Features.Restart(rctx, name); err != nil {
		s.app.Log.Warn("restart feature", "feature", name, "error", err)
	}
}

// ---------------------------------------------------------------- moderation rules

func (s *server) listRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.app.Store.ListModerationRules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	type row struct {
		ID        int64  `json:"id"`
		Pattern   string `json:"pattern"`
		AppliesTo string `json:"applies_to"`
		Enabled   bool   `json:"enabled"`
	}
	out := make([]row, 0, len(rules))
	for _, ru := range rules {
		out = append(out, row{ru.ID, ru.Pattern, ru.AppliesTo, ru.Enabled})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) createRule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Pattern   string `json:"pattern"`
		AppliesTo string `json:"applies_to"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Pattern = strings.TrimSpace(body.Pattern)
	if l := len([]rune(body.Pattern)); l < 2 || l > 80 || (body.AppliesTo != "input" && body.AppliesTo != "output" && body.AppliesTo != "both") {
		writeError(w, http.StatusBadRequest, "web.err.a_word_or_phrase_2_80_characters_and_where_i")
		return
	}
	id, err := s.app.Store.CreateModerationRule(r.Context(), store.ModerationRule{Pattern: body.Pattern, AppliesTo: body.AppliesTo, Enabled: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.audit(r, "moderation.rule_added", strconv.FormatInt(id, 10), nil)
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *server) deleteRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.app.Store.DeleteModerationRule(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "web.err.no_such_rule")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.audit(r, "moderation.rule_removed", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- conversations

func (s *server) listConversations(w http.ResponseWriter, r *http.Request) {
	convs, err := s.app.Store.ListConversations(r.Context(), 20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	type line struct {
		Role    string    `json:"role"`
		Content string    `json:"content"`
		Tool    string    `json:"tool,omitempty"`
		At      time.Time `json:"at"`
	}
	type conv struct {
		ID        int64     `json:"id"`
		StartedAt time.Time `json:"started_at"`
		Source    string    `json:"source"`
		Lines     []line    `json:"lines"`
	}
	out := make([]conv, 0, len(convs))
	for _, c := range convs {
		x := conv{ID: c.ID, StartedAt: c.StartedAt, Source: c.Source, Lines: []line{}}
		for _, m := range c.Messages {
			x.Lines = append(x.Lines, line{m.Role, m.Content, m.ToolName, m.At})
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) deleteConversations(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Store.DeleteAllConversations(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.audit(r, "assistant.history_erased", "", nil)
	w.WriteHeader(http.StatusNoContent)
}

// validLanguage reports whether code is an installed language.
func validLanguage(code string) bool {
	c, ok := i18n.Normalize(code)
	return ok && c == code
}

// validAssistantLanguage also accepts "auto" (answer in the language the child speaks).
func validAssistantLanguage(code string) bool { return code == i18n.Auto || validLanguage(code) }
