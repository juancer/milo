package web

import (
	"net/http"

	"github.com/juancer/milo/internal/notifications"
)

func (s *server) settingsRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/settings/notifications", s.requireAuth(s.getNotifications))
	mux.Handle("PUT /api/settings/notifications", s.requireAuth(s.putNotifications))
}

func (s *server) getNotifications(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, notifications.Load(r.Context(), s.app.Store))
}

func (s *server) putNotifications(w http.ResponseWriter, r *http.Request) {
	// Pointers, so a body that leaves a field out is refused instead of silently
	// switching the sound off or the volume to 0.
	var d struct {
		NewMessageSound    *bool `json:"new_message_sound_enabled"`
		EmergencySound     *bool `json:"emergency_sound_enabled"`
		NotifyPendingAfter *bool `json:"notify_pending_after_schedule"`
		Volume             *int  `json:"volume"`
	}
	if !decodeJSON(w, r, &d) {
		return
	}
	if d.NewMessageSound == nil || d.EmergencySound == nil || d.NotifyPendingAfter == nil || d.Volume == nil {
		writeError(w, http.StatusBadRequest, "send all four notification settings")
		return
	}
	c := notifications.Config{NewMessageSound: *d.NewMessageSound, EmergencySound: *d.EmergencySound, NotifyPendingAfter: *d.NotifyPendingAfter, Volume: *d.Volume}
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
