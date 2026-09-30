package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/features"
	"github.com/juancer/milo/internal/permissions"
	"github.com/juancer/milo/internal/store"
)

// Remote administration through the bot. A person sees a section only while they hold the
// permission behind it, and every tap re-checks that permission (and that the person is still
// active) on the server: the buttons carry no authority of their own. Nothing secret ever
// travels through Telegram: tokens, API keys, passwords and backups stay in the web admin.
//
//	Status and volume, My alerts   manage_configuration
//	Access requests, message copy  manage_users
//	Features                       manage_features
//	Schedules                      manage_schedules
const (
	adminPrefix = "adm:"
	// defaultAdminTapGap ignores a second tap from the same person right after one, so a nervous
	// thumb cannot fire the same change twice.
	defaultAdminTapGap = 250 * time.Millisecond
	// maxRequestsShown bounds the list of access requests so the message and keyboard stay small.
	maxRequestsShown = 5
	// quietFor is how long a feature that a person just switched on stays out of failure alerts.
	quietFor = 5 * time.Second
	// featureToggleTimeout bounds a feature start, which may call a network service.
	featureToggleTimeout = 20 * time.Second
)

// adminSections says which sections a person may see right now.
type adminSections struct{ config, users, features, schedules bool }

func (a adminSections) any() bool { return a.config || a.users || a.features || a.schedules }

func (s *Service) adminSections(ctx context.Context, u store.User) adminSections {
	has := func(p permissions.Permission) bool {
		ok, _ := s.Perms.Has(ctx, u.ID, p)
		return ok
	}
	return adminSections{
		config: has(permissions.ManageConfiguration), users: has(permissions.ManageUsers),
		features: has(permissions.ManageFeatures), schedules: has(permissions.ManageSchedules),
	}
}

// adminTapAllowed rate-limits taps per person.
func (s *Service) adminTapAllowed(userID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	gap := s.adminTapGap
	if gap == 0 {
		gap = defaultAdminTapGap
	}
	if t, ok := s.adminTapAt[userID]; ok && now.Sub(t) < gap {
		return false
	}
	s.adminTapAt[userID] = now
	if len(s.adminTapAt) > 200 {
		clear(s.adminTapAt)
		s.adminTapAt[userID] = now
	}
	return true
}

func (s *Service) adminActor(u store.User) string { return "telegram:" + u.DisplayName }

// adminView is a screen of the admin menu.
type adminView struct {
	text string
	kb   Keyboard
}

// handleAdminCallback serves a tap on an "adm:" button. Every branch re-checks the permission it needs.
func (s *Service) handleAdminCallback(ctx context.Context, c *Client, q *CallbackQuery, user store.User) {
	lang := s.langFor(ctx, user)
	answer := func(text string) { c.AnswerCallback(ctx, q.ID, text) } //nolint:errcheck
	if !s.adminTapAllowed(user.ID) {
		answer("")
		return
	}
	sec := s.adminSections(ctx, user)
	parts := strings.Split(strings.TrimPrefix(q.Data, adminPrefix), ":")
	cmd, arg := parts[0], ""
	if len(parts) > 1 {
		arg = parts[1]
	}
	deny := func() { answer(tr(lang, "telegram.adm_denied")) }
	notice := ""
	var view adminView

	switch cmd {
	case "home":
		if !sec.any() {
			deny()
			return
		}
		view = s.viewHome(lang, sec)
	case "st", "vd", "vu":
		if !sec.config {
			deny()
			return
		}
		if cmd != "st" {
			notice = s.changeVolume(ctx, user, lang, map[string]int{"vd": -10, "vu": 10}[cmd])
		}
		view = s.viewStatus(ctx, lang)
	case "rq", "ap", "rj":
		if !sec.users {
			deny()
			return
		}
		if cmd != "rq" {
			notice = s.decideRequest(ctx, user, lang, cmd == "ap", arg)
		}
		view = s.viewRequests(ctx, lang)
	case "ft", "fx":
		if !sec.features {
			deny()
			return
		}
		if cmd == "fx" {
			notice = s.toggleFeature(ctx, user, lang, arg)
		}
		view = s.viewFeatures(ctx, lang)
	case "sc", "sw":
		if !sec.schedules {
			deny()
			return
		}
		if cmd == "sw" {
			notice = s.toggleSchedule(ctx, user, lang, arg)
		}
		view = s.viewSchedules(ctx, lang)
	case "me", "al", "fw":
		if !sec.config && !sec.users {
			deny()
			return
		}
		if cmd != "me" {
			notice = s.toggleOwnFlag(ctx, user, lang, sec, cmd)
			if fresh, err := s.Store.GetUser(ctx, user.ID); err == nil {
				user = fresh
			}
		}
		view = s.viewMine(lang, user, sec)
	default:
		answer("") // a stale or unknown button
		return
	}
	answer(notice)
	s.show(ctx, c, q, view)
}

// show puts a screen in place of the message that was tapped, or sends it as a new message when
// that message can no longer be edited.
func (s *Service) show(ctx context.Context, c *Client, q *CallbackQuery, v adminView) {
	if q.Message != nil {
		ectx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := c.EditMessage(ectx, q.From.ID, q.Message.MessageID, v.text, v.kb); err == nil {
			return
		}
	}
	s.reply(ctx, c, q.From.ID, v.text, v.kb)
}

func backRow(lang string) []InlineButton {
	return []InlineButton{{Text: "« " + tr(lang, "telegram.adm_back"), CallbackData: "adm:home"}}
}

// ---------------------------------------------------------------- screens

func (s *Service) viewHome(lang string, sec adminSections) adminView {
	var kb Keyboard
	add := func(ok bool, label, data string) {
		if ok {
			kb = append(kb, []InlineButton{{Text: label, CallbackData: data}})
		}
	}
	add(sec.config, "📊 "+tr(lang, "telegram.adm_status"), "adm:st")
	add(sec.users, "👥 "+tr(lang, "telegram.adm_requests"), "adm:rq")
	add(sec.features, "🔌 "+tr(lang, "telegram.adm_features"), "adm:ft")
	add(sec.schedules, "🕗 "+tr(lang, "telegram.adm_schedules"), "adm:sc")
	add(sec.config || sec.users, "🔔 "+tr(lang, "telegram.adm_mine"), "adm:me")
	return adminView{text: tr(lang, "telegram.adm_home"), kb: kb}
}

func (s *Service) viewStatus(ctx context.Context, lang string) adminView {
	var on, off []string
	if list, err := s.Features.List(ctx); err == nil {
		for _, f := range list {
			if f.Running {
				on = append(on, f.Name)
			} else if !f.Enabled {
				off = append(off, f.Name)
			}
		}
	}
	list := func(names []string) string {
		if len(names) == 0 {
			return "-"
		}
		return strings.Join(names, ", ")
	}
	vol := s.Audio.Status(ctx).Volume
	s.mu.Lock()
	version := s.version
	s.mu.Unlock()
	text := tr(lang, "telegram.adm_status_text",
		"version", version, "uptime", formatUptime(s.now().Sub(s.started)),
		"volume10", volumeTen(vol), "volume", vol, "on", list(on), "off", list(off))
	return adminView{text: text, kb: Keyboard{
		{{Text: "🔉 −", CallbackData: "adm:vd"}, {Text: "🔊 +", CallbackData: "adm:vu"}},
		{{Text: "🔄 " + tr(lang, "telegram.adm_refresh"), CallbackData: "adm:st"}},
		backRow(lang),
	}}
}

// volumeTen is the volume on the 0-10 scale people say aloud ("volume 7").
func volumeTen(percent int) int { return (percent + 5) / 10 }

func formatUptime(d time.Duration) string {
	m := int(d.Minutes())
	switch {
	case m >= 24*60:
		return fmt.Sprintf("%dd %dh", m/(24*60), m%(24*60)/60)
	case m >= 60:
		return fmt.Sprintf("%dh %dm", m/60, m%60)
	}
	return fmt.Sprintf("%dm", m)
}

func (s *Service) viewRequests(ctx context.Context, lang string) adminView {
	pending, _ := s.Store.ListPending(ctx)
	if len(pending) == 0 {
		return adminView{text: tr(lang, "telegram.adm_no_requests"), kb: Keyboard{backRow(lang)}}
	}
	var b strings.Builder
	b.WriteString(tr(lang, "telegram.adm_requests_title", "count", len(pending)))
	var kb Keyboard
	for i, p := range pending {
		if i == maxRequestsShown {
			b.WriteString("\n" + tr(lang, "telegram.adm_more_in_web", "count", len(pending)-maxRequestsShown))
			break
		}
		name := requestName(p)
		fmt.Fprintf(&b, "\n• %s · %s", name, p.Role)
		id := strconv.FormatInt(p.ID, 10)
		kb = append(kb, []InlineButton{
			{Text: "✅ " + name, CallbackData: "adm:ap:" + id},
			{Text: "❌ " + name, CallbackData: "adm:rj:" + id},
		})
	}
	return adminView{text: b.String(), kb: append(kb, backRow(lang))}
}

// requestName is how a pending request is shown: the Telegram first name, else the username.
func requestName(p store.PendingAuthorization) string {
	switch {
	case p.FirstName != "" && p.Username != "":
		return p.FirstName + " (@" + p.Username + ")"
	case p.FirstName != "":
		return p.FirstName
	case p.Username != "":
		return "@" + p.Username
	}
	return strconv.FormatInt(p.TelegramID, 10)
}

func (s *Service) viewFeatures(ctx context.Context, lang string) adminView {
	list, _ := s.Features.List(ctx)
	var kb Keyboard
	for _, f := range list {
		if f.Name == FeatureName {
			continue // turning Telegram off from Telegram would cut off the way back in
		}
		mark := "⚪ "
		if f.Enabled && f.Running {
			mark = "🟢 "
		} else if f.Enabled {
			mark = "🟡 "
		}
		kb = append(kb, []InlineButton{{Text: mark + f.Name, CallbackData: "adm:fx:" + f.Name}})
	}
	return adminView{text: tr(lang, "telegram.adm_features_text"), kb: append(kb, backRow(lang))}
}

func (s *Service) viewSchedules(ctx context.Context, lang string) adminView {
	loc := s.Schedules.Location(ctx)
	list, _ := s.Features.List(ctx)
	var b strings.Builder
	b.WriteString(tr(lang, "telegram.adm_schedules_text", "time", s.now().In(loc).Format("Mon 15:04"), "zone", loc.String()))
	var kb Keyboard
	for _, f := range list {
		on, _ := s.Store.ScheduleEnabled(ctx, f.Name)
		rules, _ := s.Store.ListScheduleRules(ctx, f.Name)
		if !on && len(rules) == 0 {
			continue // nothing to switch: hours are created in the web admin
		}
		state, mark := tr(lang, "telegram.adm_off"), "⚪ "
		if on {
			open, _ := s.Schedules.Allowed(ctx, f.Name, s.now())
			state, mark = tr(lang, "telegram.adm_hours_closed"), "🔴 "
			if open {
				state, mark = tr(lang, "telegram.adm_hours_open"), "🟢 "
			}
		}
		fmt.Fprintf(&b, "\n• %s: %s", f.Name, state)
		kb = append(kb, []InlineButton{{Text: mark + f.Name, CallbackData: "adm:sw:" + f.Name}})
	}
	if len(kb) == 0 {
		b.WriteString("\n" + tr(lang, "telegram.adm_no_schedules"))
	}
	return adminView{text: b.String(), kb: append(kb, backRow(lang))}
}

func (s *Service) viewMine(lang string, u store.User, sec adminSections) adminView {
	label := func(key string, on bool) string {
		mark := "⚪ "
		if on {
			mark = "🟢 "
		}
		return mark + tr(lang, key)
	}
	var kb Keyboard
	text := tr(lang, "telegram.adm_mine_text")
	if sec.config || u.DeviceAlerts { // switching off stays possible after losing the permission
		kb = append(kb, []InlineButton{{Text: label("telegram.adm_alerts", u.DeviceAlerts), CallbackData: "adm:al"}})
	}
	if sec.users || u.ForwardIncoming {
		kb = append(kb, []InlineButton{{Text: label("telegram.adm_copies", u.ForwardIncoming), CallbackData: "adm:fw"}})
		text += "\n\n" + tr(lang, "telegram.adm_copies_text")
	}
	return adminView{text: text, kb: append(kb, backRow(lang))}
}

// ---------------------------------------------------------------- changes (each returns the short notice shown on the tap)

func (s *Service) changeVolume(ctx context.Context, u store.User, lang string, delta int) string {
	actor := actions.Actor{Kind: actions.ActorTelegram, UserID: u.ID, Label: s.adminActor(u)}
	if _, err := s.Actions.Execute(ctx, actor, "set_volume", map[string]any{"change": float64(delta)}); err != nil {
		return explain(lang, err)
	}
	s.Audit.Record(ctx, s.adminActor(u), "volume.changed", "", map[string]string{"volume": strconv.Itoa(s.Audio.Status(ctx).Volume)})
	return ""
}

func (s *Service) decideRequest(ctx context.Context, u store.User, lang string, approve bool, arg string) string {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || id <= 0 {
		return ""
	}
	if approve {
		var pendingRole string
		pending, _ := s.Store.ListPending(ctx)
		for _, p := range pending {
			if p.ID == id {
				pendingRole = p.Role
			}
		}
		// A child is not added as someone the child can talk to, like the default in the web admin.
		user, err := s.ApproveRequest(ctx, id, "", pendingRole != "" && pendingRole != string(permissions.RoleChild))
		switch {
		case errors.Is(err, store.ErrNotFound):
			return tr(lang, "telegram.adm_request_gone")
		case err != nil:
			s.Log.Warn("telegram admin: approve failed", "error", err)
			return tr(lang, "telegram.err_generic")
		}
		s.Audit.Record(ctx, s.adminActor(u), "authorization.approved", strconv.FormatInt(user.TelegramID, 10), map[string]string{"role": user.Role})
		return tr(lang, "telegram.adm_approved", "name", user.DisplayName)
	}
	tg, err := s.RejectRequest(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return tr(lang, "telegram.adm_request_gone")
	case err != nil:
		s.Log.Warn("telegram admin: reject failed", "error", err)
		return tr(lang, "telegram.err_generic")
	}
	s.Audit.Record(ctx, s.adminActor(u), "authorization.rejected", strconv.FormatInt(tg, 10), nil)
	return tr(lang, "telegram.adm_rejected")
}

// toggleFeature switches a feature. Turning one on that cannot start yet (no key, no place...)
// leaves it off and says what to do, instead of leaving a half-enabled feature behind.
func (s *Service) toggleFeature(ctx context.Context, u store.User, lang, name string) string {
	list, err := s.Features.List(ctx)
	if err != nil || name == FeatureName {
		return ""
	}
	var cur *features.Status
	for i := range list {
		if list[i].Name == name {
			cur = &list[i]
		}
	}
	if cur == nil {
		return tr(lang, "telegram.adm_feature_gone")
	}
	fctx, cancel := context.WithTimeout(ctx, featureToggleTimeout)
	defer cancel()
	if cur.Enabled {
		if err := s.Features.SetEnabled(fctx, name, false); err != nil {
			return tr(lang, "telegram.err_generic")
		}
		s.Audit.Record(ctx, s.adminActor(u), "feature.disabled", name, nil)
		return tr(lang, "telegram.adm_feature_off", "name", name)
	}

	// The person is told right here, so the failure alert (sent a moment later, from another
	// goroutine) is held back for a few seconds.
	s.quiet(name, true)
	defer time.AfterFunc(quietFor, func() { s.quiet(name, false) })
	if err := s.Features.SetEnabled(fctx, name, true); err != nil {
		return tr(lang, "telegram.err_generic")
	}
	if after, _ := s.Features.List(ctx); !featureRunning(after, name) {
		s.Features.SetEnabled(fctx, name, false) //nolint:errcheck // put it back how it was
		return tr(lang, "telegram.adm_feature_needs_setup", "name", name)
	}
	s.Audit.Record(ctx, s.adminActor(u), "feature.enabled", name, nil)
	return tr(lang, "telegram.adm_feature_on", "name", name)
}

func featureRunning(list []features.Status, name string) bool {
	for _, f := range list {
		if f.Name == name {
			return f.Running
		}
	}
	return false
}

// toggleSchedule switches a feature's usage hours on or off, keeping the windows as they are.
// Hours are only created and edited in the web admin, and switching them on with no window would
// shut the feature for good, so that is refused.
func (s *Service) toggleSchedule(ctx context.Context, u store.User, lang, feature string) string {
	on, _ := s.Store.ScheduleEnabled(ctx, feature)
	rules, err := s.Store.ListScheduleRules(ctx, feature)
	if err != nil || (!on && len(rules) == 0) {
		return tr(lang, "telegram.adm_no_hours")
	}
	if list, _ := s.Features.List(ctx); !hasFeature(list, feature) {
		return tr(lang, "telegram.adm_feature_gone")
	}
	if err := s.Store.SaveSchedule(ctx, feature, !on, rules); err != nil {
		return tr(lang, "telegram.err_generic")
	}
	state := "off"
	if !on {
		state = "on"
	}
	s.Audit.Record(ctx, s.adminActor(u), "schedule.updated", feature, map[string]string{"schedule": state})
	return ""
}

func hasFeature(list []features.Status, name string) bool {
	for _, f := range list {
		if f.Name == name {
			return true
		}
	}
	return false
}

// toggleOwnFlag flips the person's own alerts ("al") or message copies ("fw"). Switching on needs
// the permission behind it; switching off is always allowed.
func (s *Service) toggleOwnFlag(ctx context.Context, u store.User, lang string, sec adminSections, cmd string) string {
	if cmd == "al" {
		if !u.DeviceAlerts && !sec.config {
			return tr(lang, "telegram.adm_denied")
		}
		if err := s.Store.SetUserDeviceAlerts(ctx, u.ID, !u.DeviceAlerts); err != nil {
			return tr(lang, "telegram.err_generic")
		}
		s.Audit.Record(ctx, s.adminActor(u), "telegram.alerts", "", map[string]string{"alerts": onOff(!u.DeviceAlerts)})
		return ""
	}
	if !u.ForwardIncoming && !sec.users {
		return tr(lang, "telegram.adm_denied")
	}
	if err := s.Store.SetUserForwardIncoming(ctx, u.ID, !u.ForwardIncoming); err != nil {
		return tr(lang, "telegram.err_generic")
	}
	s.Audit.Record(ctx, s.adminActor(u), "telegram.forward", "", map[string]string{"copies": onOff(!u.ForwardIncoming)})
	return ""
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// quiet marks a feature whose failure is being handled by the person who just switched it on.
func (s *Service) quiet(name string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		s.quietFeatures[name] = true
	} else {
		delete(s.quietFeatures, name)
	}
}
