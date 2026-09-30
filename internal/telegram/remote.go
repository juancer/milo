package telegram

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/juancer/milo/internal/permissions"
	"github.com/juancer/milo/internal/store"
)

// Messages the bot sends to administrators on its own: new access requests (with Approve and
// Reject buttons), device alerts for people who opted in, and copies of incoming messages for
// people who opted in. All are best effort: a failure is logged and never affects the device.

const (
	// copyAttempts bounds how long a copy is retried when Telegram is unreachable.
	copyAttempts = 3
)

// recipients returns the active, Telegram-linked people for whom pick is true and who still hold
// the permission. The permission is checked now, at send time, never remembered.
func (s *Service) recipients(ctx context.Context, perm permissions.Permission, pick func(store.TelegramUser) bool) []store.TelegramUser {
	all, err := s.Store.ListTelegramUsers(ctx)
	if err != nil {
		return nil
	}
	var out []store.TelegramUser
	for _, u := range all {
		if !u.Active || u.TelegramID == 0 || !pick(u) {
			continue
		}
		if ok, _ := s.Perms.Has(ctx, u.ID, perm); ok {
			out = append(out, u)
		}
	}
	return out
}

// notifyRequest tells everyone who can approve people that a new request arrived.
func (s *Service) notifyRequest(ctx context.Context, c *Client, telegramID int64) {
	var req *store.PendingAuthorization
	pending, _ := s.Store.ListPending(ctx)
	for i := range pending {
		if pending[i].TelegramID == telegramID {
			req = &pending[i]
		}
	}
	if req == nil {
		return
	}
	id := strconv.FormatInt(req.ID, 10)
	name := requestName(*req)
	for _, u := range s.recipients(ctx, permissions.ManageUsers, func(store.TelegramUser) bool { return true }) {
		lang := s.langFor(ctx, u.User)
		s.reply(ctx, c, u.TelegramID, tr(lang, "telegram.adm_new_request", "name", name, "role", req.Role), Keyboard{{
			{Text: "✅ " + tr(lang, "telegram.adm_approve"), CallbackData: "adm:ap:" + id},
			{Text: "❌ " + tr(lang, "telegram.adm_reject"), CallbackData: "adm:rj:" + id},
		}})
	}
}

// alert tells the people who opted in to device alerts. Only a feature name or a fixed phrase
// is ever sent, never an error text.
func (s *Service) alert(key string, kv ...any) {
	c := s.currentClient()
	if c == nil {
		return
	}
	s.spawn(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, u := range s.recipients(ctx, permissions.ManageConfiguration, func(u store.TelegramUser) bool { return u.DeviceAlerts }) {
			s.reply(ctx, c, u.TelegramID, "🔔 "+tr(s.langFor(ctx, u.User), key, kv...), nil)
		}
	})
}

// FeatureFailed is the features manager's failure hook.
func (s *Service) FeatureFailed(name string) {
	s.mu.Lock()
	quiet := s.quietFeatures[name]
	s.mu.Unlock()
	if !quiet && name != FeatureName {
		s.alert("telegram.alert_feature_failed", "name", name)
	}
}

// announceStart tells alert subscribers the device started, once per process.
func (s *Service) announceStart() {
	s.bootOnce.Do(func() { s.alert("telegram.alert_started") })
}

// forwardCopies sends a copy of a message that someone left for the device to each administrator
// who asked for copies. It never forwards to the sender, never delays or fails the delivery to
// the device (it runs on its own), and gives up after a few tries.
func (s *Service) forwardCopies(senderID int64, kind, text string, audio []byte) {
	c := s.currentClient()
	if c == nil {
		return
	}
	s.spawn(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		sender, err := s.Store.GetUser(ctx, senderID)
		if err != nil {
			return
		}
		loc := s.Schedules.Location(ctx)
		at := s.now().In(loc).Format("15:04")
		for _, u := range s.recipients(ctx, permissions.ManageUsers, func(u store.TelegramUser) bool { return u.ForwardIncoming && u.ID != senderID }) {
			lang := s.langFor(ctx, u.User)
			caption := tr(lang, "telegram.copy_caption", "name", sender.DisplayName, "time", at)
			for attempt := 0; attempt < copyAttempts; attempt++ {
				var err error
				if kind == "voice" {
					_, err = c.SendVoiceCaption(ctx, u.TelegramID, audio, 0, caption)
				} else {
					_, err = c.SendMessage(ctx, u.TelegramID, caption+"\n"+text, nil)
				}
				if err == nil {
					break
				}
				if ctx.Err() != nil || (attempt == copyAttempts-1) || permanent(err) {
					s.Log.Warn("telegram: copy of a message not delivered", "error", err)
					break
				}
				if !sleep(ctx, s.backoff(attempt)) {
					break
				}
			}
		}
	})
}

// permanent reports whether retrying a failed call cannot help (for example the person blocked the bot).
func permanent(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Permanent()
}
