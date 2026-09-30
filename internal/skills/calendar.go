package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	ics "github.com/arran4/golang-ical"
	"github.com/teambition/rrule-go"

	"github.com/juancer/nexo/internal/actions"
	"github.com/juancer/nexo/internal/secrets"
)

// SecretCalendarURL is the secret address of the family's iCalendar feed.
const SecretCalendarURL = "calendar.ics_url"

const (
	maxICSBytes   = 2 << 20
	calendarCache = 10 * time.Minute
	maxSpoken     = 8

	// Recurrence expansion works on untrusted feed data, so it is bounded.
	maxPerEvent    = 500             // occurrences kept per recurring event
	maxTotalEvents = 2000            // occurrences kept per parse
	maxRuleSteps   = 20000           // occurrences a rule may be walked through, from its start, per event
	parseDeadline  = 2 * time.Second // time budget for one parse
)

// Event is one calendar entry occurring in a time window.
type Event struct {
	Title  string
	Start  time.Time
	End    time.Time
	AllDay bool
}

// Calendar is the read-only "calendar" feature. The family's calendar (Google
// Calendar, Nextcloud, iCloud...) is read through its secret iCalendar (.ics)
// address, which is stored encrypted. Nexo never writes to it. Recurring events
// are expanded; exotic recurrence rules may be missed.
type Calendar struct {
	Common
	Vault  *secrets.Vault
	Client *http.Client

	mu       sync.Mutex
	cached   []byte
	cachedAt time.Time
}

// Name implements features.Feature.
func (c *Calendar) Name() string { return "calendar" }

// Start implements features.Feature.
func (c *Calendar) Start(ctx context.Context) error {
	if _, err := c.Vault.Get(ctx, SecretCalendarURL); errors.Is(err, secrets.ErrNotFound) {
		return errors.New("no calendar address is set: add your calendar's secret .ics address in the Skills tab")
	}
	return nil
}

// Stop implements features.Feature.
func (c *Calendar) Stop(context.Context) error { return nil }

// Invalidate drops the cached copy (after the address changes).
func (c *Calendar) Invalidate() {
	c.mu.Lock()
	c.cached = nil
	c.mu.Unlock()
}

func (c *Calendar) fetch(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	if c.cached != nil && c.now().Sub(c.cachedAt) < calendarCache {
		data := c.cached
		c.mu.Unlock()
		return data, nil
	}
	c.mu.Unlock()

	raw, err := c.Vault.Get(ctx, SecretCalendarURL)
	if err != nil {
		return nil, errors.New("no calendar address is set")
	}
	if err := c.checkURL(string(raw)); err != nil {
		return nil, fmt.Errorf("the calendar address is not usable: %w", err)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, CheckRedirect: c.checkRedirect}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, string(raw), nil)
	if err != nil {
		return nil, errors.New("the calendar address is not usable")
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // never echo the address: it contains the calendar's secret
		}
		return nil, fmt.Errorf("the calendar cannot be reached: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the calendar answered %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxICSBytes+1))
	if err != nil || len(data) > maxICSBytes {
		return nil, errors.New("the calendar is too large or unreadable")
	}
	c.mu.Lock()
	c.cached, c.cachedAt = data, c.now()
	c.mu.Unlock()
	return data, nil
}

// Events returns the events overlapping [from, to), oldest first.
func (c *Calendar) Events(ctx context.Context, from, to time.Time) ([]Event, error) {
	data, err := c.fetch(ctx)
	if err != nil {
		return nil, err
	}
	return ParseEventsContext(ctx, data, from, to, c.loc(ctx))
}

// ParseEvents extracts events overlapping [from, to) from iCalendar data.
func ParseEvents(data []byte, from, to time.Time, loc *time.Location) ([]Event, error) {
	return ParseEventsContext(context.Background(), data, from, to, loc)
}

// ParseEventsContext is ParseEvents with bounded cost: recurrence expansion
// stops at maxPerEvent occurrences per event, maxTotalEvents in all, after
// parseDeadline or when ctx ends (keeping what was found), and sub-hourly rules
// are not expanded.
func ParseEventsContext(ctx context.Context, data []byte, from, to time.Time, loc *time.Location) ([]Event, error) {
	ctx, cancel := context.WithTimeout(ctx, parseDeadline)
	defer cancel()
	cal, err := ics.ParseCalendar(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("the calendar could not be read")
	}
	var out []Event
	for _, ev := range cal.Events() {
		if ctx.Err() != nil || len(out) >= maxTotalEvents {
			break
		}
		if p := ev.GetProperty(ics.ComponentPropertyStatus); p != nil && strings.EqualFold(p.Value, "CANCELLED") {
			continue
		}
		title := "Busy"
		if p := ev.GetProperty(ics.ComponentPropertySummary); p != nil && strings.TrimSpace(p.Value) != "" {
			title = strings.TrimSpace(unescapeICS(p.Value))
		}
		start, end, allDay, ok := eventTimes(ev, loc)
		if !ok {
			continue
		}
		dur := end.Sub(start)
		starts := []time.Time{start}
		if rp := ev.GetProperty(ics.ComponentPropertyRrule); rp != nil {
			opt, err := rrule.StrToROptionInLocation(rp.Value, start.Location())
			if err == nil && opt.Freq <= rrule.HOURLY { // sub-hourly rules are not expanded
				opt.Dtstart = start
				if r, err := rrule.NewRRule(*opt); err == nil {
					starts = expandRule(ctx, r, from.Add(-dur), to)
				}
			}
		}
		if ex, err := ev.GetExDates(); err == nil && len(ex) > 0 {
			skip := map[int64]bool{}
			for _, x := range ex {
				skip[x.Unix()] = true
			}
			kept := starts[:0]
			for _, s := range starts {
				if !skip[s.Unix()] {
					kept = append(kept, s)
				}
			}
			starts = kept
		}
		for _, s := range starts {
			if e := s.Add(dur); s.Before(to) && e.After(from) {
				out = append(out, Event{Title: title, Start: s, End: e, AllDay: allDay})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// expandRule lists the occurrences of r in [lo, hi], at most maxPerEvent, after
// walking at most maxRuleSteps occurrences from the rule's start.
func expandRule(ctx context.Context, r *rrule.RRule, lo, hi time.Time) []time.Time {
	var out []time.Time
	next := r.Iterator()
	for steps := 0; steps < maxRuleSteps && len(out) < maxPerEvent; steps++ {
		if steps%256 == 0 && ctx.Err() != nil {
			break
		}
		t, ok := next()
		if !ok || t.After(hi) {
			break
		}
		if !t.Before(lo) {
			out = append(out, t)
		}
	}
	return out
}

func eventTimes(ev *ics.VEvent, loc *time.Location) (start, end time.Time, allDay, ok bool) {
	if p := ev.GetProperty(ics.ComponentPropertyDtStart); p != nil {
		if v := p.ICalParameters["VALUE"]; len(v) > 0 && strings.EqualFold(v[0], "DATE") {
			s, err := ev.GetAllDayStartAt()
			if err != nil {
				return time.Time{}, time.Time{}, false, false
			}
			start = time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, loc)
			end = start.AddDate(0, 0, 1)
			if e, err := ev.GetAllDayEndAt(); err == nil && e.After(s) {
				end = time.Date(e.Year(), e.Month(), e.Day(), 0, 0, 0, 0, loc)
			}
			return start, end, true, true
		}
	}
	s, err := ev.GetStartAt()
	if err != nil {
		return time.Time{}, time.Time{}, false, false
	}
	start = s
	if e, err := ev.GetEndAt(); err == nil && e.After(s) {
		end = e
	} else {
		end = s.Add(time.Hour)
	}
	return start, end, false, true
}

func unescapeICS(s string) string {
	return strings.NewReplacer(`\,`, ",", `\;`, ";", `\n`, " ", `\N`, " ", `\\`, `\`).Replace(s)
}

// Describe returns a spoken agenda for today, tomorrow or the next 7 days.
func (c *Calendar) Describe(ctx context.Context, day string) (string, error) {
	loc := c.loc(ctx)
	now := c.now().In(loc)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	from, to, label := midnight, midnight.AddDate(0, 0, 1), "today"
	switch day {
	case "tomorrow":
		from, to, label = midnight.AddDate(0, 0, 1), midnight.AddDate(0, 0, 2), "tomorrow"
	case "week":
		from, to, label = midnight, midnight.AddDate(0, 0, 7), "in the next seven days"
	default:
		from = now // skip what already ended today
	}
	events, err := c.Events(ctx, from, to)
	if err != nil {
		return "", err
	}
	if len(events) == 0 {
		return "You have nothing planned " + label + ".", nil
	}
	var parts []string
	for i, e := range events {
		if i == maxSpoken {
			parts = append(parts, fmt.Sprintf("and %d more", len(events)-maxSpoken))
			break
		}
		when := "all day"
		if !e.AllDay {
			when = "at " + e.Start.In(loc).Format("3:04 PM")
		}
		if day == "week" {
			when = e.Start.In(loc).Format("Monday") + " " + when
		}
		parts = append(parts, e.Title+" "+when)
	}
	return "You have " + label + ": " + strings.Join(parts, "; ") + ".", nil
}

// RegisterActions registers get_calendar.
func (c *Calendar) RegisterActions(e *actions.Engine) {
	e.MustRegister(actions.Action{
		Name: "get_calendar", Description: "Read what is on the family calendar today, tomorrow or this week.",
		Feature: "calendar", Exposure: actions.ExposeDevice | actions.ExposeAssistant,
		Params: []actions.Param{{Name: "day", Description: "today, tomorrow or week", Type: actions.String, Enum: []string{"today", "tomorrow", "week"}}},
		Handler: func(ctx context.Context, call actions.Call) (actions.Result, error) {
			msg, err := c.Describe(ctx, call.Args.String("day"))
			if err != nil {
				return actions.Result{}, err
			}
			return actions.Result{Message: msg}, nil
		},
	})
}
