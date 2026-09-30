package telegram

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/permissions"
	"github.com/juancer/milo/internal/store"
)

// Emergency is a high-priority message, not a phone call: the Bot API cannot
// place a real call. It bypasses usage schedules, sounds a distinct alarm and
// is played before any other message when the child presses LISTEN.
//
// To avoid accidental alerts: the button is only shown to users holding
// emergency_alert, the alert needs an explicit confirmation that expires after
// two minutes, and a second alert from the same person is refused for a minute.
const (
	emergencyConfirmTTL  = 2 * time.Minute
	emergencyCooldown    = time.Minute
	urgentFollowupWindow = 2 * time.Minute
	urgentFollowupMax    = 3 // messages marked urgent after an alert, within the window
)

// ErrEmergencyCooldown is returned when an alert was sent moments ago.
var ErrEmergencyCooldown = errors.New("an emergency alert was just sent")

func (s *Service) registerEmergencyAction(e *actions.Engine) {
	e.MustRegister(actions.Action{
		Name:        "send_emergency",
		Description: "Raise an emergency alert on the device.",
		Feature:     FeatureName, Permission: permissions.EmergencyAlert,
		Exposure:       actions.ExposeTelegram, // never the assistant, never a button
		BypassSchedule: true,
		Handler:        s.sendEmergency,
	})
}

func (s *Service) sendEmergency(ctx context.Context, c actions.Call) (actions.Result, error) {
	now := s.now()
	s.mu.Lock()
	if last, ok := s.emergencyAt[c.Actor.UserID]; ok && now.Sub(last) < emergencyCooldown {
		s.mu.Unlock()
		return actions.Result{}, ErrEmergencyCooldown
	}
	s.mu.Unlock()

	lang := s.langForID(ctx, c.Actor.UserID)
	if s.senderAtCeiling(ctx, c.Actor.UserID, true) {
		return actions.Result{Message: tr(lang, "telegram.too_many_waiting")}, nil
	}

	spoken := s.spokenLang(ctx)
	name := tr(spoken, "telegram.family_member")
	if u, err := s.Store.GetUser(ctx, c.Actor.UserID); err == nil {
		name = u.DisplayName
	}
	id, err := s.Store.InsertMessage(ctx, store.Message{
		Direction: "in", Kind: "text", Type: "emergency", Priority: "critical",
		Body: tr(spoken, "telegram.emergency_body", "name", name), UserID: c.Actor.UserID,
	})
	if err != nil {
		return actions.Result{}, err
	}
	s.mu.Lock()
	s.emergencyAt[c.Actor.UserID] = now
	s.urgentUntil[c.Actor.UserID] = now.Add(urgentFollowupWindow)
	s.urgentLeft[c.Actor.UserID] = urgentFollowupMax
	s.mu.Unlock()

	s.announce(ctx, id, true)
	s.Audit.Record(ctx, "telegram", "emergency.sent", strconv.FormatInt(c.Actor.UserID, 10), map[string]string{"from": name})
	return actions.Result{Message: "🚨 " + tr(lang, "telegram.emergency_sent")}, nil
}

// urgentActive reports whether the user's next message is urgent because they
// just raised an alert. It uses up one of a few follow-ups and re-checks that
// the user may still raise alerts.
func (s *Service) urgentActive(ctx context.Context, userID int64) bool {
	s.mu.Lock()
	until, ok := s.urgentUntil[userID]
	if ok && (s.now().After(until) || s.urgentLeft[userID] <= 0) {
		delete(s.urgentUntil, userID)
		delete(s.urgentLeft, userID)
		ok = false
	}
	if ok {
		s.urgentLeft[userID]--
	}
	s.mu.Unlock()
	if !ok {
		return false
	}
	allowed, _ := s.Perms.Has(ctx, userID, permissions.EmergencyAlert)
	return allowed
}

func (s *Service) confirmEmergency(ctx context.Context, c *Client, user store.User, chatID int64) {
	lang := s.langFor(ctx, user)
	if ok, _ := s.Perms.Has(ctx, user.ID, permissions.EmergencyAlert); !ok {
		s.Log.Warn("telegram: emergency requested without permission", "user", user.ID)
		s.reply(ctx, c, chatID, tr(lang, "telegram.err_emergency_forbidden"), nil)
		return
	}
	s.mu.Lock()
	s.emergencyConfirm[user.ID] = s.now().Add(emergencyConfirmTTL)
	s.mu.Unlock()
	s.reply(ctx, c, chatID, "⚠️ "+tr(lang, "telegram.emergency_confirm"),
		Keyboard{{
			{Text: tr(lang, "telegram.btn_emergency_yes"), CallbackData: "emergency:yes"},
			{Text: tr(lang, "telegram.btn_emergency_cancel"), CallbackData: "emergency:no"},
		}})
}

func (s *Service) fireEmergency(ctx context.Context, c *Client, user store.User, chatID int64) {
	lang := s.langFor(ctx, user)
	s.mu.Lock()
	expires, ok := s.emergencyConfirm[user.ID]
	delete(s.emergencyConfirm, user.ID) // a confirmation works once
	s.mu.Unlock()
	if !ok || s.now().After(expires) {
		s.reply(ctx, c, chatID, tr(lang, "telegram.emergency_expired"), nil)
		return
	}
	actor := actions.Actor{Kind: actions.ActorTelegram, UserID: user.ID, Label: "telegram:" + user.DisplayName}
	res, err := s.Actions.Execute(ctx, actor, "send_emergency", nil)
	switch {
	case errors.Is(err, ErrEmergencyCooldown):
		s.reply(ctx, c, chatID, tr(lang, "telegram.emergency_cooldown"), nil)
	case errors.Is(err, actions.ErrForbidden):
		s.reply(ctx, c, chatID, tr(lang, "telegram.err_emergency_forbidden"), nil)
	case err != nil:
		s.Log.Error("emergency failed", "error", err)
		s.reply(ctx, c, chatID, tr(lang, "telegram.emergency_failed", "reason", explain(lang, err)), nil)
	default:
		s.reply(ctx, c, chatID, res.Message, nil)
	}
}
