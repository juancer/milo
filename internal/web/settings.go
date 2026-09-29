package web

import (
	"net/http"

	"github.com/juancer/nexo/internal/notifications"
)

func (s *server) settingsRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/settings/notifications", s.requireAuth(s.getNotifications))
	mux.Handle("PUT /api/settings/notifications", s.requireAuth(s.putNotifications))
}

func (s *server) getNotifications(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, notifications.Load(r.Context(), s.app.Store))
}

func (s *server) putNotifications(w http.ResponseWriter, r *http.Request) {
	var c notifications.Config
	if !decodeJSON(w, r, &c) {
		return
	}
	if c.Volume < 0 || c.Volume > 100 {
		writeError(w, http.StatusBadRequest, "volume must be 0-100")
		return
	}
	if err := notifications.Save(r.Context(), s.app.Store, c); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r, "settings.notifications", "", nil)
	writeJSON(w, http.StatusOK, notifications.Load(r.Context(), s.app.Store))
}
