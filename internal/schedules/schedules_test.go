package schedules

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/juancer/nexo/internal/store"
)

func at(t *testing.T, zone, ts string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	v, err := time.ParseInLocation("2006-01-02 15:04", ts, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// 2026-03-02 is a Monday.
func rule(day, start, end int) Rule {
	return Rule{Feature: "telegram", DayOfWeek: day, StartMinute: start, EndMinute: end, Enabled: true}
}

func TestMatchesSameDayWindow(t *testing.T) {
	rules := []Rule{rule(1, 8*60, 20*60+30)} // Monday 08:00-20:30
	cases := map[string]bool{
		"2026-03-02 07:59": false,
		"2026-03-02 08:00": true,
		"2026-03-02 20:29": true,
		"2026-03-02 20:30": false, // end is exclusive
		"2026-03-03 10:00": false, // Tuesday
	}
	for ts, want := range cases {
		if got := Matches(rules, at(t, "UTC", ts)); got != want {
			t.Errorf("%s = %v, want %v", ts, got, want)
		}
	}
}

func TestMatchesOvernightWindow(t *testing.T) {
	rules := []Rule{rule(5, 22*60, 6*60)} // Friday 22:00 -> Saturday 06:00
	cases := map[string]bool{
		"2026-03-06 21:59": false, // Friday
		"2026-03-06 22:00": true,
		"2026-03-06 23:59": true,
		"2026-03-07 00:00": true, // Saturday early morning belongs to Friday's rule
		"2026-03-07 05:59": true,
		"2026-03-07 06:00": false,
		"2026-03-07 22:30": false, // Saturday evening: no Saturday rule
		"2026-03-02 03:00": false,
	}
	for ts, want := range cases {
		if got := Matches(rules, at(t, "UTC", ts)); got != want {
			t.Errorf("%s = %v, want %v", ts, got, want)
		}
	}
	// Sunday rule that wraps into Monday must wrap the week correctly.
	sun := []Rule{rule(0, 23*60, 2*60)}
	if !Matches(sun, at(t, "UTC", "2026-03-02 01:00")) { // Monday 01:00
		t.Error("Sunday->Monday overnight window not matched")
	}
	if !Matches([]Rule{rule(6, 23*60, 2*60)}, at(t, "UTC", "2026-03-08 01:00")) { // Sat->Sun
		t.Error("Saturday->Sunday overnight window not matched")
	}
}

func TestMatchesUntilMidnightAndDisabledRules(t *testing.T) {
	if !Matches([]Rule{rule(1, 20*60, 24*60)}, at(t, "UTC", "2026-03-02 23:59")) {
		t.Error("window ending at 24:00 must include 23:59")
	}
	off := rule(1, 0, 24*60)
	off.Enabled = false
	if Matches([]Rule{off}, at(t, "UTC", "2026-03-02 12:00")) {
		t.Error("disabled rule matched")
	}
	if Matches(nil, at(t, "UTC", "2026-03-02 12:00")) {
		t.Error("no rules must match nothing")
	}
}

func TestServiceUsesConfiguredTimezone(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(ctx, filepath.Join(t.TempDir(), "n.db"))
	defer st.Close()
	svc := New(st)

	// No schedule configured: always allowed.
	if ok, _ := svc.Allowed(ctx, "telegram", time.Now()); !ok {
		t.Fatal("feature without schedule must be allowed")
	}

	st.SetSetting(ctx, SettingTimezone, "Europe/Madrid")
	st.SaveSchedule(ctx, "telegram", true, []Rule{rule(1, 8*60, 20*60+30)})
	// 07:30 UTC on Monday 2026-03-02 is 08:30 in Madrid (CET, UTC+1): inside the window.
	utc := time.Date(2026, 3, 2, 7, 30, 0, 0, time.UTC)
	if ok, _ := svc.Allowed(ctx, "telegram", utc); !ok {
		t.Fatal("08:30 Madrid time must be allowed")
	}
	// 20:00 UTC is 21:00 in Madrid: outside.
	if ok, _ := svc.Allowed(ctx, "telegram", time.Date(2026, 3, 2, 20, 0, 0, 0, time.UTC)); ok {
		t.Fatal("21:00 Madrid time must be blocked")
	}
	// Other features are unaffected.
	if ok, _ := svc.Allowed(ctx, "assistant", time.Date(2026, 3, 2, 20, 0, 0, 0, time.UTC)); !ok {
		t.Fatal("a schedule for one feature affected another")
	}
	// Switching the schedule off frees the feature again.
	st.SaveSchedule(ctx, "telegram", false, []Rule{rule(1, 8*60, 20*60+30)})
	if ok, _ := svc.Allowed(ctx, "telegram", time.Date(2026, 3, 2, 20, 0, 0, 0, time.UTC)); !ok {
		t.Fatal("disabled schedule still blocked")
	}
	// Enabled with no windows blocks everything.
	st.SaveSchedule(ctx, "telegram", true, nil)
	if ok, _ := svc.Allowed(ctx, "telegram", utc); ok {
		t.Fatal("enabled schedule with no windows must block")
	}
}

func TestDaylightSavingIsHandled(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(ctx, filepath.Join(t.TempDir(), "n.db"))
	defer st.Close()
	svc := New(st)
	st.SetSetting(ctx, SettingTimezone, "Europe/Madrid")
	// Sunday 2026-03-29 is the spring-forward day; a 09:00-10:00 rule must follow local time.
	st.SaveSchedule(ctx, "assistant", true, []Rule{{Feature: "assistant", DayOfWeek: 0, StartMinute: 9 * 60, EndMinute: 10 * 60, Enabled: true}})
	if ok, _ := svc.Allowed(ctx, "assistant", time.Date(2026, 3, 29, 7, 30, 0, 0, time.UTC)); !ok { // 09:30 CEST
		t.Fatal("09:30 local (CEST) must be inside")
	}
	if ok, _ := svc.Allowed(ctx, "assistant", time.Date(2026, 3, 29, 8, 30, 0, 0, time.UTC)); ok { // 10:30 CEST
		t.Fatal("10:30 local must be outside")
	}
}

func TestInvalidTimezoneFallsBack(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(ctx, filepath.Join(t.TempDir(), "n.db"))
	defer st.Close()
	st.SetSetting(ctx, SettingTimezone, "Mars/Olympus")
	if New(st).Location(ctx) != time.Local {
		t.Fatal("invalid timezone must fall back to the system one")
	}
}

func TestClockParsing(t *testing.T) {
	for in, want := range map[string]int{"00:00": 0, "08:30": 510, "20:30": 1230, "24:00": 1440, " 9:05 ": 545} {
		if got, err := ParseClock(in); err != nil || got != want {
			t.Errorf("ParseClock(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "25:00", "12:60", "24:01", "noon", "-1:00"} {
		if _, err := ParseClock(bad); err == nil {
			t.Errorf("ParseClock(%q) accepted", bad)
		}
	}
	if FormatClock(1230) != "20:30" {
		t.Fatal("FormatClock")
	}
}

func TestStoreRejectsBadRules(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(ctx, filepath.Join(t.TempDir(), "n.db"))
	defer st.Close()
	for name, r := range map[string]Rule{
		"day 7":        rule(7, 60, 120),
		"zero length":  rule(1, 60, 60),
		"end past 24h": rule(1, 60, 1441),
		"negative":     rule(1, -5, 60),
	} {
		if err := st.SaveSchedule(ctx, "x", true, []Rule{r}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A failed save must not leave a half-written schedule behind.
	st.SaveSchedule(ctx, "x", true, []Rule{rule(1, 60, 120)})
	st.SaveSchedule(ctx, "x", true, []Rule{rule(1, 60, 120), rule(9, 1, 2)})
	if got, _ := st.ListScheduleRules(ctx, "x"); len(got) != 1 {
		t.Fatalf("rules after failed replace = %+v", got)
	}
}
