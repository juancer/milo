package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/juancer/nexo/internal/actions"
	"github.com/juancer/nexo/internal/permissions"
	"github.com/juancer/nexo/internal/store"
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

	name := "a family member"
	if u, err := s.Store.GetUser(ctx, c.Actor.UserID); err == nil {
		name = u.DisplayName
	}
	id, err := s.Store.InsertMessage(ctx, store.Message{
		Direction: "in", Kind: "text", Type: "emergency", Priority: "critical",
		Body: "Emergency alert from " + name, UserID: c.Actor.UserID,
	})
	if err != nil {
		return actions.Result{}, err
	}
	s.mu.Lock()
	s.emergencyAt[c.Actor.UserID] = now
	s.urgentUntil[c.Actor.UserID] = now.Add(urgentFollowupWindow)
	s.mu.Unlock()

	s.announce(ctx, id, true)
	s.Audit.Record(ctx, "telegram", "emergency.sent", strconv.FormatInt(c.Actor.UserID, 10), map[string]string{"from": name})
	return actions.Result{Message: "🚨 Emergency alert sent. Anything you send in the next 2 minutes is also marked urgent."}, nil
}

// urgentActive reports whether the user's messages are currently urgent
// because they just raised an alert.
func (s *Service) urgentActive(userID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.urgentUntil[userID]
	if ok && s.now().After(until) {
		delete(s.urgentUntil, userID)
		return false
	}
	return ok
}

func (s *Service) confirmEmergency(ctx context.Context, c *Client, user store.User, chatID int64) {
	if ok, _ := s.Perms.Has(ctx, user.ID, permissions.EmergencyAlert); !ok {
		s.Log.Warn("telegram: emergency requested without permission", "user", user.ID)
		s.reply(ctx, c, chatID, "You are not allowed to send emergency alerts.", nil)
		return
	}
	s.mu.Lock()
	s.emergencyConfirm[user.ID] = s.now().Add(emergencyConfirmTTL)
	s.mu.Unlock()
	s.reply(ctx, c, chatID, "⚠️ Send emergency alert?\nThe device will sound an alarm right away, even outside usage hours.",
		Keyboard{{
			{Text: "YES, SEND", CallbackData: "emergency:yes"},
			{Text: "CANCEL", CallbackData: "emergency:no"},
		}})
}

func (s *Service) fireEmergency(ctx context.Context, c *Client, user store.User, chatID int64) {
	s.mu.Lock()
	expires, ok := s.emergencyConfirm[user.ID]
	delete(s.emergencyConfirm, user.ID) // a confirmation works once
	s.mu.Unlock()
	if !ok || s.now().After(expires) {
		s.reply(ctx, c, chatID, "That confirmation expired. Open /menu and tap Emergency again.", nil)
		return
	}
	actor := actions.Actor{Kind: actions.ActorTelegram, UserID: user.ID, Label: "telegram:" + user.DisplayName}
	res, err := s.Actions.Execute(ctx, actor, "send_emergency", nil)
	switch {
	case errors.Is(err, ErrEmergencyCooldown):
		s.reply(ctx, c, chatID, "An alert was just sent. Please wait a minute before sending another.", nil)
	case errors.Is(err, actions.ErrForbidden):
		s.reply(ctx, c, chatID, "You are not allowed to send emergency alerts.", nil)
	case err != nil:
		s.Log.Error("emergency failed", "error", err)
		s.reply(ctx, c, chatID, fmt.Sprintf("The alert could not be delivered: %s", explain(err)), nil)
	default:
		s.reply(ctx, c, chatID, res.Message, nil)
	}
}
