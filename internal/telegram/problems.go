package telegram

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/juancer/milo/internal/permissions"
	"github.com/juancer/milo/internal/store"
)

// Problems in the log (a WARN or an ERROR) can be sent to the people who opted in to device alerts. What is
// sent is chosen in the admin panel: nothing (the default), the message only, a short censored text, or the
// whole log line. A repeat of the same message waits ten minutes and at most six go out in ten minutes, so a
// failure that repeats can never flood a phone or loop through Telegram itself.

const (
	SettingErrorAlerts       = "telegram.error_alerts"        // "1" switches them on; off by default
	SettingErrorAlertsLevel  = "telegram.error_alerts_level"  // ErrorLevelError or ErrorLevelWarn
	SettingErrorAlertsDetail = "telegram.error_alerts_detail" // ErrorDetailNone, ErrorDetailSafe or ErrorDetailFull

	ErrorLevelError = "error"
	ErrorLevelWarn  = "warn"

	ErrorDetailNone = "none" // only the message, without the values logged with it
	ErrorDetailSafe = "safe" // the message and values, 200 characters at most, addresses and tokens censored
	ErrorDetailFull = "full" // the log line as it is

	problemRepeatEvery  = 10 * time.Minute
	problemMaxPerWindow = 6
	problemSafeRunes    = 200
	problemFullRunes    = 3500 // Telegram refuses more than 4096
)

// ErrorAlertSettings are the panel's choices.
type ErrorAlertSettings struct {
	Enabled bool   `json:"enabled"`
	Level   string `json:"level"`
	Detail  string `json:"detail"`
}

// ErrorAlertSettings reads the choices, with their defaults.
func (s *Service) ErrorAlertSettings(ctx context.Context) ErrorAlertSettings {
	get := func(key, def string) string {
		if v, err := s.Store.GetSetting(ctx, key); err == nil && v != "" {
			return v
		}
		return def
	}
	cfg := ErrorAlertSettings{
		Enabled: get(SettingErrorAlerts, "") == "1",
		Level:   get(SettingErrorAlertsLevel, ErrorLevelWarn),
		Detail:  get(SettingErrorAlertsDetail, ErrorDetailSafe),
	}
	if cfg.Level != ErrorLevelError && cfg.Level != ErrorLevelWarn {
		cfg.Level = ErrorLevelWarn
	}
	if cfg.Detail != ErrorDetailNone && cfg.Detail != ErrorDetailSafe && cfg.Detail != ErrorDetailFull {
		cfg.Detail = ErrorDetailSafe
	}
	return cfg
}

// ValidErrorAlertSettings reports whether the values are ones the panel may save.
func ValidErrorAlertSettings(c ErrorAlertSettings) bool {
	return (c.Level == ErrorLevelError || c.Level == ErrorLevelWarn) &&
		(c.Detail == ErrorDetailNone || c.Detail == ErrorDetailSafe || c.Detail == ErrorDetailFull)
}

// SaveErrorAlertSettings stores the choices.
func (s *Service) SaveErrorAlertSettings(ctx context.Context, c ErrorAlertSettings) error {
	on := ""
	if c.Enabled {
		on = "1"
	}
	for k, v := range map[string]string{SettingErrorAlerts: on, SettingErrorAlertsLevel: c.Level, SettingErrorAlertsDetail: c.Detail} {
		if err := s.Store.SetSetting(ctx, k, v); err != nil {
			return err
		}
	}
	return nil
}

type problemLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
	sent []time.Time
}

func (l *problemLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	if t, ok := l.last[key]; ok && now.Sub(t) < problemRepeatEvery {
		return false
	}
	kept := l.sent[:0]
	for _, t := range l.sent {
		if now.Sub(t) < problemRepeatEvery {
			kept = append(kept, t)
		}
	}
	l.sent = kept
	if len(l.sent) >= problemMaxPerWindow {
		return false
	}
	if len(l.last) > 200 { // forget old messages
		for k, t := range l.last {
			if now.Sub(t) >= problemRepeatEvery {
				delete(l.last, k)
			}
		}
	}
	l.last[key] = now
	l.sent = append(l.sent, now)
	return true
}

var (
	attrStart   = regexp.MustCompile(` [A-Za-z_][A-Za-z0-9_.]*=`)
	urlInText   = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"']+`)
	secretShape = regexp.MustCompile(`\b\d{6,}:[A-Za-z0-9_-]{20,}\b|\b(?:sk|gsk|ghp|xox[a-z])[-_][A-Za-z0-9_-]{16,}\b`)
)

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// AlertProblem is the log's hook: line is one formatted log line ("15:04:05 WARN  message key=value").
func (s *Service) AlertProblem(line string) {
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 3 {
		return
	}
	level, rest := parts[1], strings.TrimLeft(parts[2], " ")
	msg, attrs := rest, ""
	if loc := attrStart.FindStringIndex(rest); loc != nil {
		msg, attrs = rest[:loc[0]], rest[loc[0]+1:]
	}
	// Telegram's own trouble cannot be reported through Telegram, and trying would loop.
	if strings.HasPrefix(strings.ToLower(msg), "telegram") {
		return
	}
	c := s.currentClient()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	cfg := s.ErrorAlertSettings(ctx)
	cancel()
	if !cfg.Enabled || (level == "WARN" && cfg.Level != ErrorLevelWarn) {
		return
	}
	if !s.problems.allow(msg, time.Now()) {
		return
	}
	var text string
	switch cfg.Detail {
	case ErrorDetailNone:
		text = msg
	case ErrorDetailFull:
		text = cutRunes(rest, problemFullRunes)
	default:
		text = cutRunes(secretShape.ReplaceAllString(urlInText.ReplaceAllString(strings.TrimSpace(msg+" "+attrs), "[url]"), "[censored]"), problemSafeRunes)
	}
	text = "⚠️ " + level + ": " + text
	s.spawn(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, u := range s.recipients(sctx, permissions.ManageConfiguration, func(u store.TelegramUser) bool { return u.DeviceAlerts }) {
			s.reply(sctx, c, u.TelegramID, text, nil)
		}
	})
}
