package web

import (
	"net/http"

	"github.com/juancer/milo/internal/i18n"
)

func (s *server) languageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/locale", s.getLocale) // public: the sign-in page needs the language too
	mux.Handle("GET /api/locales", s.requireAuth(s.listLocales))
	mux.Handle("GET /api/settings/language", s.requireAuth(s.getLanguage))
	mux.Handle("PUT /api/settings/language", s.requireAuth(s.putLanguage))
}

type languageDTO struct {
	Interface    string `json:"interface_language"`
	Notification string `json:"notification_language"`
}

func (s *server) getLocale(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"interface_language": i18n.Interface(r.Context(), s.app.Store)})
}

func (s *server) listLocales(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, i18n.Supported())
}

func (s *server) getLanguage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, languageDTO{i18n.Interface(r.Context(), s.app.Store), i18n.Notification(r.Context(), s.app.Store)})
}

func (s *server) putLanguage(w http.ResponseWriter, r *http.Request) {
	var d languageDTO
	if !decodeJSON(w, r, &d) {
		return
	}
	if !validLanguage(d.Interface) || !validLanguage(d.Notification) {
		writeError(w, http.StatusBadRequest, "web.err.the_language_must_be_a_supported_language")
		return
	}
	for k, v := range map[string]string{i18n.KeyInterface: d.Interface, i18n.KeyNotification: d.Notification} {
		if err := s.app.Store.SetSetting(r.Context(), k, v); err != nil {
			writeError(w, http.StatusInternalServerError, "web.err.internal_error")
			return
		}
	}
	s.audit(r, "settings.language", "", map[string]string{"interface": d.Interface, "notification": d.Notification})
	writeJSON(w, http.StatusOK, d)
}
