package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/juancer/milo/internal/skills"
)

// POST /api/reminders obeys the same feature switch and cap as the create_reminder action.
func TestCreateReminderNeedsTheFeatureAndHonoursTheCap(t *testing.T) {
	e := newEnv(t, true)
	cookie := e.login(t)
	ctx := context.Background()
	due := time.Now().Add(48 * time.Hour)
	body := `{"text":"again","due":"` + due.Format("2006-01-02T15:04") + `"}`

	if resp := e.do(t, "POST", "/api/reminders", body, cookie, nil); resp.StatusCode != 409 {
		t.Fatalf("with the feature off = %d, want 409", resp.StatusCode)
	}
	if err := e.app.Features.SetEnabled(ctx, "reminders", true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < skills.MaxReminders; i++ {
		if _, err := e.store.CreateReminder(ctx, "r"+strconv.Itoa(i), due, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	if resp := e.do(t, "POST", "/api/reminders", body, cookie, nil); resp.StatusCode != 400 {
		t.Fatalf("over the cap = %d, want 400", resp.StatusCode)
	}
	if p, _ := e.store.PendingReminders(ctx); len(p) != skills.MaxReminders {
		t.Fatalf("%d pending, want %d", len(p), skills.MaxReminders)
	}
}

// POST /api/weather/detect needs a session and the CSRF header, then saves the detected place.
func TestDetectWeatherLocation(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	geo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"city":"Valencia","country":"Spain","latitude":39.4699,"longitude":-0.3763}`))
	}))
	t.Cleanup(geo.Close)
	if err := e.store.SetSetting(ctx, "weather.ipgeo_base", geo.URL); err != nil {
		t.Fatal(err)
	}

	if resp := e.do(t, "POST", "/api/weather/detect", "", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("without a session = %d, want 401", resp.StatusCode)
	}
	cookie := e.login(t)
	if resp := e.do(t, "POST", "/api/weather/detect", "", cookie, map[string]string{clientHeader: ""}); resp.StatusCode != 403 {
		t.Fatalf("without the client header = %d, want 403", resp.StatusCode)
	}
	if got, _ := e.store.GetSetting(ctx, skills.SettingWeatherLat); got != "" {
		t.Fatalf("a refused request saved a location: %q", got)
	}

	resp := e.do(t, "POST", "/api/weather/detect", "", cookie, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("detect = %d, want 200", resp.StatusCode)
	}
	var p struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil || p.Name != "Valencia" {
		t.Fatalf("answer = %+v, %v", p, err)
	}
	if got, _ := e.store.GetSetting(ctx, skills.SettingWeatherPlace); got != "Valencia, Spain" {
		t.Fatalf("saved place = %q", got)
	}

	// A service that answers nothing useful gives a friendly 422 that suggests typing the town.
	geo.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	resp = e.do(t, "POST", "/api/weather/detect", "", cookie, nil)
	var bad struct {
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&bad)
	if resp.StatusCode != 422 || !strings.Contains(bad.Error, "town") {
		t.Fatalf("empty answer = %d %q, want 422 suggesting the town", resp.StatusCode, bad.Error)
	}
}
