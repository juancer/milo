// Package schedules decides when each feature may be used ("Telegram 08:00-20:30",
// "assistant 09:00-20:00"). Times are evaluated in the device's configured
// timezone. Emergency messages bypass schedules; that decision lives with the
// emergency action (BypassSchedule), not here.
package schedules

import (
	"context"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // embedded, so timezones work without system tzdata

	"github.com/juancer/milo/internal/store"
)

// SettingTimezone is the system_settings key holding an IANA timezone name.
const SettingTimezone = "system.timezone"

// Rule is an allowed window.
type Rule = store.ScheduleRule

// Matches reports whether local time t falls inside any enabled rule.
func Matches(rules []Rule, t time.Time) bool {
	day, minute := int(t.Weekday()), t.Hour()*60+t.Minute()
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		switch {
		case r.StartMinute < r.EndMinute: // same day
			if day == r.DayOfWeek && minute >= r.StartMinute && minute < r.EndMinute {
				return true
			}
		default: // continues past midnight into the next day
			if day == r.DayOfWeek && minute >= r.StartMinute {
				return true
			}
			if day == (r.DayOfWeek+1)%7 && minute < r.EndMinute {
				return true
			}
		}
	}
	return false
}

// Service evaluates schedules against SQLite. It implements actions.ScheduleChecker.
type Service struct {
	store *store.Store
}

// New returns a Service.
func New(s *store.Store) *Service { return &Service{store: s} }

// Location returns the configured timezone, falling back to the system's.
func (s *Service) Location(ctx context.Context) *time.Location {
	if name, err := s.store.GetSetting(ctx, SettingTimezone); err == nil && name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.Local
}

// Allowed reports whether a feature may be used at the given moment.
func (s *Service) Allowed(ctx context.Context, feature string, at time.Time) (bool, error) {
	on, err := s.store.ScheduleEnabled(ctx, feature)
	if err != nil || !on {
		return true, err
	}
	rules, err := s.store.ListScheduleRules(ctx, feature)
	if err != nil {
		return false, err
	}
	return Matches(rules, at.In(s.Location(ctx))), nil
}

// ParseClock parses "HH:MM" (24 h) into minutes after midnight; "24:00" is allowed as an end time.
func ParseClock(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d", &h, &m); err != nil || m < 0 || m > 59 || h < 0 || h > 24 || (h == 24 && m != 0) {
		return 0, fmt.Errorf("invalid time %q (use HH:MM)", s)
	}
	return h*60 + m, nil
}

// FormatClock renders minutes after midnight as "HH:MM".
func FormatClock(minutes int) string { return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60) }
