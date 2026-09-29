package web

import (
	"net/http"
	"time"

	"github.com/juancer/nexo/internal/schedules"
	"github.com/juancer/nexo/internal/store"
)

func (s *server) scheduleRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/schedules", s.requireAuth(s.getSchedules))
	mux.Handle("PUT /api/schedules/{feature}", s.requireAuth(s.putSchedule))
	mux.Handle("PUT /api/settings/timezone", s.requireAuth(s.putTimezone))
}

type ruleDTO struct {
	Day     int    `json:"day_of_week"`
	Start   string `json:"start"`
	End     string `json:"end"`
	Enabled bool   `json:"enabled"`
}

type scheduleDTO struct {
	Feature string    `json:"feature"`
	Enabled bool      `json:"enabled"`
	OpenNow bool      `json:"open_now"`
	Rules   []ruleDTO `json:"rules"`
}

func (s *server) getSchedules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	feats, err := s.app.Features.List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	loc := s.app.Schedules.Location(ctx)
	out := make([]scheduleDTO, 0, len(feats))
	for _, f := range feats {
		on, _ := s.app.Store.ScheduleEnabled(ctx, f.Name)
		rules, _ := s.app.Store.ListScheduleRules(ctx, f.Name)
		open, _ := s.app.Schedules.Allowed(ctx, f.Name, time.Now())
		d := scheduleDTO{Feature: f.Name, Enabled: on, OpenNow: open, Rules: []ruleDTO{}}
		for _, ru := range rules {
			d.Rules = append(d.Rules, ruleDTO{ru.DayOfWeek, schedules.FormatClock(ru.StartMinute), schedules.FormatClock(ru.EndMinute), ru.Enabled})
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"timezone": loc.String(), "local_time": time.Now().In(loc).Format("Mon 15:04"), "features": out,
	})
}

func (s *server) putSchedule(w http.ResponseWriter, r *http.Request) {
	feature := r.PathValue("feature")
	ctx := r.Context()
	feats, _ := s.app.Features.List(ctx)
	known := false
	for _, f := range feats {
		known = known || f.Name == feature
	}
	if !known {
		writeError(w, http.StatusNotFound, "unknown feature")
		return
	}
	var body struct {
		Enabled bool      `json:"enabled"`
		Rules   []ruleDTO `json:"rules"`
	}
	if !decodeJSONLimit(w, r, &body, 32<<10) {
		return
	}
	if len(body.Rules) > 100 {
		writeError(w, http.StatusBadRequest, "at most 100 windows per feature")
		return
	}
	rules := make([]store.ScheduleRule, 0, len(body.Rules))
	for _, ru := range body.Rules {
		start, err1 := schedules.ParseClock(ru.Start)
		end, err2 := schedules.ParseClock(ru.End)
		switch {
		case err1 != nil || err2 != nil || start >= 1440:
			writeError(w, http.StatusBadRequest, "times must be HH:MM")
			return
		case ru.Day < 0 || ru.Day > 6:
			writeError(w, http.StatusBadRequest, "day_of_week must be 0 (Sunday) to 6 (Saturday)")
			return
		case start == end || end == 0:
			writeError(w, http.StatusBadRequest, "start and end must differ (use 24:00 for midnight)")
			return
		}
		rules = append(rules, store.ScheduleRule{Feature: feature, DayOfWeek: ru.Day, StartMinute: start, EndMinute: end, Enabled: ru.Enabled})
	}
	if err := s.app.Store.SaveSchedule(ctx, feature, body.Enabled, rules); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save the schedule")
		return
	}
	state := "off"
	if body.Enabled {
		state = "on"
	}
	s.audit(r, "schedule.updated", feature, map[string]string{"schedule": state})
	s.getSchedules(w, r)
}

func (s *server) putTimezone(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Timezone string `json:"timezone"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if _, err := time.LoadLocation(body.Timezone); err != nil || body.Timezone == "" || body.Timezone == "Local" || len(body.Timezone) > 64 {
		writeError(w, http.StatusBadRequest, "unknown timezone; use a name like Europe/Madrid")
		return
	}
	if err := s.app.Store.SetSetting(r.Context(), schedules.SettingTimezone, body.Timezone); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r, "settings.timezone", body.Timezone, nil)
	s.getSchedules(w, r)
}
