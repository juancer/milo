package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Fake Open-Meteo (weather) and a fake family calendar, so the weather and
// calendar skills can be demonstrated with no Internet.

func (s *sim) weatherSearch(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	w.Header().Set("Content-Type", "application/json")
	if name == "" {
		fmt.Fprint(w, `{}`)
		return
	}
	fmt.Fprintf(w, `{"results":[{"name":%q,"country":"Demoland","latitude":40.4168,"longitude":-3.7038}]}`, strings.Title(name)) //nolint:staticcheck
}

func (s *sim) weatherForecast(w http.ResponseWriter, r *http.Request) {
	temp := 21.0
	if r.URL.Query().Get("temperature_unit") == "fahrenheit" {
		temp = 69.8
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"current":{"temperature_2m":%.1f,"weather_code":2},"daily":{"weather_code":[2,61],
		"temperature_2m_max":[%.1f,%.1f],"temperature_2m_min":[%.1f,%.1f],"precipitation_probability_max":[10,70]}}`,
		temp, temp+3, temp+1, temp-8, temp-6)
}

// calendar serves an iCalendar feed whose events are relative to "now", so the
// demo always has something on today and tomorrow.
func (s *sim) calendar(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().UTC()
	stamp := func(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
	day := func(t time.Time) string { return t.Format("20060102") }
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//nexo-sim//EN\r\n")
	event := func(uid, summary, extra string) {
		fmt.Fprintf(&b, "BEGIN:VEVENT\r\nUID:%s\r\nDTSTAMP:%s\r\nSUMMARY:%s\r\n%sEND:VEVENT\r\n", uid, stamp(now), summary, extra)
	}
	next := now.Truncate(time.Hour).Add(2 * time.Hour)
	event("dentist", "Dentist", fmt.Sprintf("DTSTART:%s\r\nDTEND:%s\r\n", stamp(next), stamp(next.Add(time.Hour))))
	event("trip", "School trip to the museum", fmt.Sprintf("DTSTART;VALUE=DATE:%s\r\nDTEND;VALUE=DATE:%s\r\n", day(now), day(now.AddDate(0, 0, 1))))
	tomorrow := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	event("swim", "Swimming lesson", fmt.Sprintf("DTSTART:%s\r\nDTEND:%s\r\n", stamp(tomorrow), stamp(tomorrow.Add(time.Hour))))
	piano := time.Date(now.Year(), now.Month(), now.Day(), 17, 30, 0, 0, time.UTC).AddDate(0, 0, -7)
	event("piano", "Piano", fmt.Sprintf("DTSTART:%s\r\nDTEND:%s\r\nRRULE:FREQ=WEEKLY\r\n", stamp(piano), stamp(piano.Add(45*time.Minute))))
	b.WriteString("END:VCALENDAR\r\n")
	w.Header().Set("Content-Type", "text/calendar")
	fmt.Fprint(w, b.String())
}
