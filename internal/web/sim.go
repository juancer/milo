package web

import (
	"net/http"
	"strconv"
	"time"
)

// The simulator drives mock hardware so the whole device can be seen working
// without a Raspberry Pi. Its routes exist only when MILO_HARDWARE=mock.
func (s *server) simRoutes(mux *http.ServeMux) {
	if s.app.MockGPIO == nil {
		return
	}
	mux.Handle("POST /api/sim/buttons/{id}/press", s.requireAuth(s.simPress))
	mux.Handle("GET /api/sim/audio", s.requireAuth(s.simAudio))
	mux.Handle("POST /api/sim/utterance", s.requireAuth(s.simUtterance))
}

func (s *server) simPress(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		HoldMS int `json:"hold_ms"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.HoldMS < 10 || body.HoldMS > 5000 {
		writeError(w, http.StatusBadRequest, "hold_ms must be 10-5000")
		return
	}
	b, err := s.app.Store.GetButton(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such button")
		return
	}
	go s.app.MockGPIO.Hold(b.GPIO, time.Duration(body.HoldMS)*time.Millisecond)
	w.WriteHeader(http.StatusAccepted)
}

func (s *server) simAudio(w http.ResponseWriter, r *http.Request) {
	played := s.app.MockAudio.Played()
	type row struct {
		At         time.Time `json:"at"`
		Kind       string    `json:"kind"`
		Label      string    `json:"label"`
		DurationMS int64     `json:"duration_ms"`
	}
	out := make([]row, 0, len(played))
	for i := len(played) - 1; i >= 0; i-- { // newest first
		p := played[i]
		out = append(out, row{p.At, p.Kind, p.Label, p.Duration.Milliseconds()})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"played": out,
		"volume": s.app.MockAudio.Status(r.Context()).Volume,
	})
}

func (s *server) simUtterance(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len([]rune(body.Text)) > 300 {
		writeError(w, http.StatusBadRequest, "text too long")
		return
	}
	s.app.MockAudio.SetUtterance(body.Text)
	w.WriteHeader(http.StatusNoContent)
}
