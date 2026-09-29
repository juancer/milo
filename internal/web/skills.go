package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/juancer/nexo/internal/actions"
	"github.com/juancer/nexo/internal/skills"
	"github.com/juancer/nexo/internal/store"
)

var dirRe = regexp.MustCompile(`^/[A-Za-z0-9_./ -]{1,200}$`)

func (s *server) skillRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/skills", s.requireAuth(s.getSkills))
	mux.Handle("DELETE /api/timers/{id}", s.requireAuth(s.deleteTimer))
	mux.Handle("POST /api/reminders", s.requireAuth(s.createReminder))
	mux.Handle("DELETE /api/reminders/{id}", s.requireAuth(s.deleteReminder))
	mux.Handle("PUT /api/music", s.requireAuth(s.putMusic))
	mux.Handle("POST /api/music/rescan", s.requireAuth(s.rescanMusic))
	mux.Handle("POST /api/music/play", s.requireAuth(s.playMusic))
	mux.Handle("POST /api/music/stop", s.requireAuth(s.stopMusic))
	mux.Handle("PUT /api/weather", s.requireAuth(s.putWeather))
	mux.Handle("POST /api/weather/test", s.requireAuth(s.testWeather))
	mux.Handle("PUT /api/calendar", s.requireAuth(s.putCalendar))
	mux.Handle("DELETE /api/calendar", s.requireAuth(s.deleteCalendar))
	mux.Handle("POST /api/calendar/test", s.requireAuth(s.testCalendar))
}

// runAction runs an action as the authenticated administrator and maps its
// errors to HTTP answers. Feature switches and parameter checks still apply.
func (s *server) runAction(w http.ResponseWriter, r *http.Request, name string, args map[string]any) (actions.Result, bool) {
	res, err := s.app.Actions.Execute(r.Context(), actions.Actor{Kind: actions.ActorAdmin, Label: adminFrom(r).Username}, name, args)
	switch {
	case err == nil:
		return res, true
	case errors.Is(err, actions.ErrFeatureDisabled):
		writeError(w, http.StatusConflict, "turn this feature on in the Dashboard first")
	case errors.Is(err, actions.ErrInvalidParams):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	}
	return actions.Result{}, false
}

func (s *server) getSkills(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()
	loc := s.app.Schedules.Location(ctx)

	type timer struct {
		ID      int64  `json:"id"`
		Label   string `json:"label"`
		Seconds int    `json:"seconds_left"`
	}
	timers := []timer{}
	if list, err := s.app.Store.PendingTimers(ctx); err == nil {
		for _, t := range list {
			timers = append(timers, timer{t.ID, t.Label, max(0, int(t.FiresAt.Sub(now).Seconds()))})
		}
	}
	type reminder struct {
		ID     int64     `json:"id"`
		Text   string    `json:"text"`
		DueAt  time.Time `json:"due_at"`
		Source string    `json:"source"`
	}
	reminders := []reminder{}
	if list, err := s.app.Store.PendingReminders(ctx); err == nil {
		for _, rm := range list {
			reminders = append(reminders, reminder{rm.ID, rm.Text, rm.DueAt.In(loc), rm.Source})
		}
	}
	_, calErr := s.app.Vault.Get(ctx, skills.SecretCalendarURL)
	writeJSON(w, http.StatusOK, map[string]any{
		"timers": timers, "reminders": reminders, "music": s.app.Music.Info(ctx),
		"weather": map[string]any{
			"place":      s.app.Setting(ctx, skills.SettingWeatherPlace, ""),
			"units":      s.app.Setting(ctx, skills.SettingWeatherUnits, "celsius"),
			"configured": s.app.Setting(ctx, skills.SettingWeatherLat, "") != "",
		},
		"calendar_configured": calErr == nil,
		"timezone":            loc.String(),
	})
}

func (s *server) deleteTimer(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.app.Store.CancelTimer(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such timer")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r, "timer.cancelled", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) createReminder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
		Due  string `json:"due"` // local date and time, "2026-03-05T08:30"
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	loc := s.app.Schedules.Location(r.Context())
	due, err := time.ParseInLocation("2006-01-02T15:04", body.Due, loc)
	text := strings.TrimSpace(body.Text)
	switch {
	case err != nil:
		writeError(w, http.StatusBadRequest, "choose a date and time")
		return
	case text == "" || len([]rune(text)) > 200:
		writeError(w, http.StatusBadRequest, "the reminder must be 1-200 characters")
		return
	case !due.After(time.Now()):
		writeError(w, http.StatusBadRequest, "choose a time in the future")
		return
	case s.app.CheckText(r.Context(), text) != nil:
		writeError(w, http.StatusBadRequest, "that text contains a blocked word")
		return
	}
	id, err := s.app.Store.CreateReminder(r.Context(), text, due, "admin")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r, "reminder.created", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusCreated)
}

func (s *server) deleteReminder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.app.Store.CancelReminder(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such reminder")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r, "reminder.cancelled", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- music

func (s *server) putMusic(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dir string `json:"dir"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Dir = strings.TrimSpace(body.Dir)
	if !dirRe.MatchString(body.Dir) || strings.Contains(body.Dir, "..") {
		writeError(w, http.StatusBadRequest, "enter an absolute folder path such as /var/lib/nexo/music")
		return
	}
	if info, err := os.Stat(body.Dir); err != nil || !info.IsDir() {
		writeError(w, http.StatusBadRequest, "that folder does not exist on the device")
		return
	}
	if err := s.app.Store.SetSetting(r.Context(), "music.dir", body.Dir); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r, "music.folder_changed", body.Dir, nil)
	s.rescanMusic(w, r)
}

func (s *server) rescanMusic(w http.ResponseWriter, r *http.Request) {
	if n, err := s.app.Music.Rescan(r.Context()); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"tracks": n})
	}
}

func (s *server) playMusic(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query string `json:"query"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	args := map[string]any{}
	if strings.TrimSpace(body.Query) != "" {
		args["query"] = body.Query
	}
	if res, ok := s.runAction(w, r, "play_music", args); ok {
		writeJSON(w, http.StatusOK, res)
	}
}

func (s *server) stopMusic(w http.ResponseWriter, r *http.Request) {
	if res, ok := s.runAction(w, r, "stop_music", nil); ok {
		writeJSON(w, http.StatusOK, res)
	}
}

// ---------------------------------------------------------------- weather

func (s *server) putWeather(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Place string `json:"place"`
		Units string `json:"units"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Place = strings.TrimSpace(body.Place)
	if body.Units != "celsius" && body.Units != "fahrenheit" || len([]rune(body.Place)) > 80 {
		writeError(w, http.StatusBadRequest, "units must be celsius or fahrenheit and the place at most 80 characters")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if body.Place != "" {
		if _, err := s.app.Weather.SetPlace(ctx, body.Place); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}
	s.app.Store.SetSetting(ctx, skills.SettingWeatherUnits, body.Units) //nolint:errcheck
	s.audit(r, "weather.settings_updated", "", nil)
	s.restartFeature(r.Context(), "weather")
	s.getSkills(w, r)
}

func (s *server) testWeather(w http.ResponseWriter, r *http.Request) {
	if res, ok := s.runAction(w, r, "get_weather", nil); ok {
		writeJSON(w, http.StatusOK, res)
	}
}

// ---------------------------------------------------------------- calendar

func (s *server) putCalendar(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.URL = strings.TrimSpace(body.URL)
	if len(body.URL) > 500 || strings.ContainsAny(body.URL, " \t\r\n") || s.app.Calendar.CheckURL(body.URL) != nil {
		writeError(w, http.StatusBadRequest, "enter the calendar's secret address; it must start with https://")
		return
	}
	if err := s.app.Vault.Put(r.Context(), skills.SecretCalendarURL, []byte(body.URL)); err != nil {
		writeError(w, http.StatusInternalServerError, "could not store the address")
		return
	}
	s.app.Calendar.Invalidate()
	s.audit(r, "calendar.address_set", "", nil) // the address itself is a secret
	s.restartFeature(r.Context(), "calendar")
	s.getSkills(w, r)
}

func (s *server) deleteCalendar(w http.ResponseWriter, r *http.Request) {
	s.app.Store.DeleteSecret(r.Context(), skills.SecretCalendarURL) //nolint:errcheck
	s.app.Calendar.Invalidate()
	s.audit(r, "calendar.address_removed", "", nil)
	s.restartFeature(r.Context(), "calendar")
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) testCalendar(w http.ResponseWriter, r *http.Request) {
	if res, ok := s.runAction(w, r, "get_calendar", map[string]any{"day": "week"}); ok {
		writeJSON(w, http.StatusOK, res)
	}
}
