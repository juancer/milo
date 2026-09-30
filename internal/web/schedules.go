package web

import (
	"net/http"
	"time"

	"github.com/juancer/milo/internal/schedules"
	"github.com/juancer/milo/internal/store"
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
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
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
		writeError(w, http.StatusNotFound, "web.err.unknown_feature")
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
		writeError(w, http.StatusBadRequest, "web.err.at_most_100_windows_per_feature")
		return
	}
	rules := make([]store.ScheduleRule, 0, len(body.Rules))
	for _, ru := range body.Rules {
		start, err1 := schedules.ParseClock(ru.Start)
		end, err2 := schedules.ParseClock(ru.End)
		switch {
		case err1 != nil || err2 != nil || start >= 1440:
			writeError(w, http.StatusBadRequest, "web.err.times_must_be_hh_mm")
			return
		case ru.Day < 0 || ru.Day > 6:
			writeError(w, http.StatusBadRequest, "web.err.day_of_week_must_be_0_sunday_to_6_saturday")
			return
		case start == end || end == 0:
			writeError(w, http.StatusBadRequest, "web.err.start_and_end_must_differ_use_24_00_for_midn")
			return
		}
		rules = append(rules, store.ScheduleRule{Feature: feature, DayOfWeek: ru.Day, StartMinute: start, EndMinute: end, Enabled: ru.Enabled})
	}
	if err := s.app.Store.SaveSchedule(ctx, feature, body.Enabled, rules); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.could_not_save_the_schedule")
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
		writeError(w, http.StatusBadRequest, "web.err.unknown_timezone_use_a_name_like_europe_madr")
		return
	}
	if err := s.app.Store.SetSetting(r.Context(), schedules.SettingTimezone, body.Timezone); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.audit(r, "settings.timezone", body.Timezone, nil)
	s.getSchedules(w, r)
}
