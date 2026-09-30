package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/juancer/milo/internal/wakeword"
)

func (s *server) wakeRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/wakeword", s.requireAuth(s.getWake))
	mux.Handle("PUT /api/wakeword", s.requireAuth(s.putWake))
	mux.Handle("POST /api/wakeword/samples", s.requireAuth(s.enrollWake))
	mux.Handle("DELETE /api/wakeword/samples/{id}", s.requireAuth(s.deleteWakeSample))
	if s.app.MockGPIO != nil {
		mux.Handle("POST /api/sim/wake", s.requireAuth(s.simWake))
	}
}

type wakeSettingsDTO struct {
	Engine       string `json:"engine"`
	Phrase       string `json:"phrase"`
	Sensitivity  int    `json:"sensitivity"`
	ExternalPath string `json:"external_path"`
}

func (s *server) getWake(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	get := func(k, def string) string { return s.app.Setting(ctx, k, def) }
	sens, _ := strconv.Atoi(get(wakeword.SettingSensitivity, "5"))
	tpls, _ := s.app.Store.ListWakeTemplates(ctx)
	type tpl struct {
		ID        int64     `json:"id"`
		Frames    int       `json:"frames"`
		CreatedAt time.Time `json:"created_at"`
	}
	samples := make([]tpl, 0, len(tpls))
	for _, t := range tpls {
		samples = append(samples, tpl{t.ID, t.Frames, t.CreatedAt})
	}
	feature := map[string]any{"enabled": false, "running": false}
	if list, err := s.app.Features.List(ctx); err == nil {
		for _, f := range list {
			if f.Name == wakeword.FeatureName {
				feature = map[string]any{"enabled": f.Enabled, "running": f.Running, "last_error": f.LastError}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": wakeSettingsDTO{get(wakeword.SettingEngine, "template"), get(wakeword.SettingPhrase, "Hey Milo"), sens, get(wakeword.SettingExternalPath, "")},
		"feature":  feature, "status": s.app.Wake.Status(ctx), "samples": samples, "max_samples": wakeword.MaxTemplates,
		"simulated": s.app.MockGPIO != nil,
	})
}

func (s *server) putWake(w http.ResponseWriter, r *http.Request) {
	var d wakeSettingsDTO
	if !decodeJSON(w, r, &d) {
		return
	}
	d.Phrase = strings.TrimSpace(d.Phrase)
	switch {
	case d.Engine != "template" && d.Engine != "external":
		writeError(w, http.StatusBadRequest, "web.err.engine_must_be_template_or_external")
		return
	case d.Phrase == "" || len([]rune(d.Phrase)) > 40:
		writeError(w, http.StatusBadRequest, "web.err.the_phrase_label_must_be_1_40_characters")
		return
	case d.Sensitivity < 1 || d.Sensitivity > 10:
		writeError(w, http.StatusBadRequest, "web.err.sensitivity_must_be_1_10")
		return
	}
	// Required for the external engine, and checked for any stored path, so an
	// unvalidated one never sits in the settings waiting for the engine to change.
	if d.Engine == "external" || d.ExternalPath != "" {
		if err := wakeword.ValidateExternalPath(d.ExternalPath); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	ctx := r.Context()
	for k, v := range map[string]string{
		wakeword.SettingEngine: d.Engine, wakeword.SettingPhrase: d.Phrase,
		wakeword.SettingSensitivity: strconv.Itoa(d.Sensitivity), wakeword.SettingExternalPath: d.ExternalPath,
	} {
		if err := s.app.Store.SetSetting(ctx, k, v); err != nil {
			writeError(w, http.StatusInternalServerError, "web.err.internal_error")
			return
		}
	}
	s.audit(r, "wakeword.settings_updated", d.Engine, nil)
	s.restartFeature(ctx, wakeword.FeatureName)
	s.getWake(w, r)
}

func (s *server) enrollWake(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.app.Wake.Enroll(ctx); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.audit(r, "wakeword.sample_added", "", nil)
	s.restartFeature(r.Context(), wakeword.FeatureName)
	s.getWake(w, r)
}

func (s *server) deleteWakeSample(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.app.Store.DeleteWakeTemplate(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "web.err.no_such_sample")
		return
	}
	s.audit(r, "wakeword.sample_removed", strconv.FormatInt(id, 10), nil)
	s.restartFeature(r.Context(), wakeword.FeatureName)
	w.WriteHeader(http.StatusNoContent)
}

// simWake pretends the wake word was heard (development mode).
func (s *server) simWake(w http.ResponseWriter, _ *http.Request) {
	if err := s.app.Wake.Trigger(); err != nil {
		writeError(w, http.StatusConflict, "web.err.turn_the_wakeword_feature_on_first")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
