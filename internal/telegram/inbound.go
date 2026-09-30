package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/i18n"
	"github.com/juancer/milo/internal/permissions"
	"github.com/juancer/milo/internal/store"
)

const (
	maxVoiceSeconds = 120
	// strangerLogGap is the shortest time between two "unauthorized user" warnings.
	strangerLogGap = 10 * time.Second
)

// pollLoop long-polls Telegram until ctx ends. Failures back off and retry, so
// a Wi-Fi outage never stops the device; buttons and stored messages keep working.
func (s *Service) pollLoop(ctx context.Context, c *Client) {
	offset := int64(0)
	if v, err := s.Store.GetSetting(ctx, settingUpdateOffset); err == nil {
		offset, _ = strconv.ParseInt(v, 10, 64)
	}
	backoff := time.Second
	for ctx.Err() == nil {
		ups, err := c.GetUpdates(ctx, offset, 25)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.setPollErr(err)
			wait := backoff
			var api *APIError
			if errors.As(err, &api) {
				if api.RetryAfter > 0 {
					wait = time.Duration(api.RetryAfter) * time.Second
				} else if api.Permanent() {
					wait = time.Minute
				}
			}
			s.Log.Warn("telegram poll failed", "error", err, "retry_in", wait)
			if !sleep(ctx, wait) {
				return
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		s.setPollErr(nil)
		backoff = time.Second
		for _, u := range ups {
			s.safeHandle(ctx, c, u)
			offset = u.UpdateID + 1
			// At-least-once: a crash between handling and this write repeats one update.
			if err := s.Store.SetSetting(ctx, settingUpdateOffset, strconv.FormatInt(offset, 10)); err != nil {
				s.Log.Warn("telegram: cannot save update offset", "error", err)
			}
		}
	}
}

func (s *Service) safeHandle(ctx context.Context, c *Client, u Update) {
	defer func() {
		if r := recover(); r != nil {
			s.Log.Error("telegram update handler panicked", "panic", fmt.Sprint(r))
		}
	}()
	s.handleUpdate(ctx, c, u)
}

func (s *Service) handleUpdate(ctx context.Context, c *Client, u Update) {
	switch {
	case u.CallbackQuery != nil:
		s.handleCallback(ctx, c, u.CallbackQuery)
	case u.Message != nil:
		m := u.Message
		// Only private conversations with a real person are served.
		if m.Chat.Type != "private" || m.From == nil || m.From.IsBot {
			return
		}
		user, err := s.Store.GetUserByTelegramID(ctx, m.From.ID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !user.Active) {
			s.handleStranger(ctx, c, m)
			return
		}
		if err != nil {
			s.Log.Error("telegram: user lookup failed", "error", err)
			return
		}
		s.handleKnown(ctx, c, user, m)
	}
}

// ---------------------------------------------------------------- strangers

// handleStranger implements the "no auto-authorization" rule. The only thing a
// stranger can do is redeem an invitation, which creates a *pending* request
// for the admin to approve. Replies are rate-limited so the bot cannot be used
// to spam or probe.
func (s *Service) handleStranger(ctx context.Context, c *Client, m *Message) {
	lang := i18n.Notification(ctx, s.Store) // strangers have no language of their own
	from := m.From
	token := ""
	if rest, ok := strings.CutPrefix(strings.TrimSpace(m.Text), "/start"); ok {
		token = strings.TrimSpace(rest)
	}

	if token == "" {
		if s.strangerCoolingDown(from.ID) {
			return
		}
		if pending, _ := s.Store.HasPending(ctx, from.ID); pending {
			s.reply(ctx, c, from.ID, tr(lang, "telegram.awaiting_approval"), nil)
			return
		}
		s.warnStranger(from.ID)
		s.reply(ctx, c, from.ID, tr(lang, "telegram.not_authorized"), nil)
		return
	}

	// A valid invitation is always honoured, even right after other chatter;
	// only the replies to bad guesses are rate-limited.
	// The invitation is only burned if the request is recorded.
	inv, err := s.Store.RedeemInvitation(ctx, HashToken(token), s.now(), store.PendingAuthorization{
		TelegramID: from.ID, Username: from.Username, FirstName: from.FirstName,
	})
	if errors.Is(err, store.ErrNotFound) {
		if !s.strangerCoolingDown(from.ID) { // one log line and one reply per cooldown
			s.Log.Warn("telegram: invalid invitation used", "telegram_id", from.ID)
			s.reply(ctx, c, from.ID, tr(lang, "telegram.invalid_invitation"), nil)
		}
		return
	}
	if err != nil {
		s.reply(ctx, c, from.ID, tr(lang, "telegram.awaiting_approval"), nil)
		return
	}
	s.Audit.Record(ctx, "telegram", "authorization.requested", strconv.FormatInt(from.ID, 10),
		map[string]string{"role": inv.Role, "username": from.Username})
	s.reply(ctx, c, from.ID, tr(lang, "telegram.request_sent"), nil)
	s.notifyRequest(ctx, c, from.ID)
}

// warnStranger logs an unauthorized contact, at most once every strangerLogGap across all
// senders, so many different accounts cannot flood the log ring and the journal.
func (s *Service) warnStranger(id int64) {
	s.mu.Lock()
	now := s.now()
	if !s.strangerLogAt.IsZero() && now.Sub(s.strangerLogAt) < strangerLogGap {
		s.strangerLogSkips++
		s.mu.Unlock()
		return
	}
	skipped := s.strangerLogSkips
	s.strangerLogAt, s.strangerLogSkips = now, 0
	s.mu.Unlock()
	s.Log.Warn("telegram: unauthorized user contacted the bot", "telegram_id", id, "suppressed_since_last", skipped)
}

func (s *Service) strangerCoolingDown(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	cooldown := s.strangerCooldown
	if cooldown == 0 {
		cooldown = 30 * time.Second
	}
	if t, ok := s.replyAt[id]; ok && now.Sub(t) < cooldown {
		return true
	}
	s.replyAt[id] = now
	if len(s.replyAt) > 1000 { // bound memory against id-cycling floods
		for k, t := range s.replyAt {
			if now.Sub(t) > cooldown {
				delete(s.replyAt, k)
			}
		}
		if len(s.replyAt) > 1000 { // all entries are recent: start over
			clear(s.replyAt)
			s.replyAt[id] = now
		}
	}
	return false
}

// ---------------------------------------------------------------- authorized users

func (s *Service) handleKnown(ctx context.Context, c *Client, user store.User, m *Message) {
	lang := s.langFor(ctx, user)
	text := strings.TrimSpace(m.Text)
	switch {
	case m.Voice != nil:
		s.receiveVoice(ctx, c, user, m)
	case text == "/emergency":
		s.confirmEmergency(ctx, c, user, m.From.ID)
	case text == "/start" || strings.HasPrefix(text, "/start ") || text == "/menu" || text == "/help":
		s.sendMenu(ctx, c, user, m.From.ID)
	case strings.HasPrefix(text, "/"):
		s.reply(ctx, c, m.From.ID, tr(lang, "telegram.help"), nil)
	case text != "":
		s.deliver(ctx, c, user, m.From.ID, "text", text, nil)
	}
}

func (s *Service) receiveVoice(ctx context.Context, c *Client, user store.User, m *Message) {
	lang := s.langFor(ctx, user)
	if m.Voice.Duration > maxVoiceSeconds || m.Voice.FileSize > MaxVoiceBytes {
		s.reply(ctx, c, m.From.ID, tr(lang, "telegram.voice_too_long"), nil)
		return
	}
	if ok, _ := s.Perms.Has(ctx, user.ID, permissions.SendMessage); !ok {
		s.reply(ctx, c, m.From.ID, explain(lang, actions.ErrForbidden), nil) // before any download
		return
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := c.DownloadFile(dctx, m.Voice.FileID)
	if err != nil {
		s.Log.Warn("telegram: cannot download voice", "error", err)
		s.reply(ctx, c, m.From.ID, tr(lang, "telegram.voice_download_failed"), nil)
		return
	}
	s.deliver(ctx, c, user, m.From.ID, "voice", "", data)
}

// deliver hands a message to the Action Engine, which checks the sender's
// permission before anything is stored.
func (s *Service) deliver(ctx context.Context, c *Client, user store.User, chatID int64, kind, text string, audio []byte) {
	lang := s.langFor(ctx, user)
	args := map[string]any{"kind": kind}
	if text != "" {
		args["text"] = text
	}
	if audio != nil {
		args["audio"] = audio
	}
	actor := actions.Actor{Kind: actions.ActorTelegram, UserID: user.ID, Label: "telegram:" + user.DisplayName}
	res, err := s.Actions.Execute(ctx, actor, "deliver_message", args)
	if err != nil {
		s.reply(ctx, c, chatID, explain(lang, err), nil)
		return
	}
	s.reply(ctx, c, chatID, res.Message, nil)
}

func explain(lang string, err error) string {
	switch {
	case errors.Is(err, actions.ErrForbidden):
		return tr(lang, "telegram.err_forbidden")
	case errors.Is(err, actions.ErrFeatureDisabled):
		return tr(lang, "telegram.err_feature_off")
	case errors.Is(err, actions.ErrInvalidParams):
		return tr(lang, "telegram.err_invalid")
	}
	return tr(lang, "telegram.err_generic")
}

func (s *Service) sendMenu(ctx context.Context, c *Client, user store.User, chatID int64) {
	s.reply(ctx, c, chatID, tr(s.langFor(ctx, user), "telegram.menu_greeting", "name", user.DisplayName), s.menuKeyboard(ctx, user))
}

func (s *Service) menuKeyboard(ctx context.Context, user store.User) Keyboard {
	lang := s.langFor(ctx, user)
	kb := Keyboard{{
		{Text: "🎤 " + tr(lang, "telegram.btn_voice"), CallbackData: "menu:voice"},
		{Text: "💬 " + tr(lang, "telegram.btn_text"), CallbackData: "menu:text"},
	}}
	if ok, _ := s.Perms.Has(ctx, user.ID, permissions.EmergencyAlert); ok {
		kb = append(kb, []InlineButton{{Text: "🚨 " + tr(lang, "telegram.btn_emergency"), CallbackData: "menu:emergency"}})
	}
	if s.adminSections(ctx, user).any() {
		kb = append(kb, []InlineButton{{Text: "⚙️ " + tr(lang, "telegram.btn_admin"), CallbackData: "adm:home"}})
	}
	return kb
}

func (s *Service) handleCallback(ctx context.Context, c *Client, q *CallbackQuery) {
	notice := i18n.Notification(ctx, s.Store)
	answer := func(text string) { c.AnswerCallback(ctx, q.ID, text) } //nolint:errcheck
	user, err := s.Store.GetUserByTelegramID(ctx, q.From.ID)
	if err != nil || !user.Active {
		answer(tr(notice, "telegram.not_authorized"))
		return
	}
	lang := s.langFor(ctx, user)
	if strings.HasPrefix(q.Data, adminPrefix) {
		s.handleAdminCallback(ctx, c, q, user)
		return
	}
	switch q.Data {
	case "menu:voice":
		answer("")
		s.reply(ctx, c, q.From.ID, tr(lang, "telegram.prompt_voice"), nil)
	case "menu:text":
		answer("")
		s.reply(ctx, c, q.From.ID, tr(lang, "telegram.prompt_text"), nil)
	case "menu:emergency":
		answer("")
		s.confirmEmergency(ctx, c, user, q.From.ID)
	case "emergency:yes":
		answer("")
		s.fireEmergency(ctx, c, user, q.From.ID)
	case "emergency:no":
		answer(tr(lang, "telegram.cancelled_short"))
		s.mu.Lock()
		delete(s.emergencyConfirm, user.ID) // a cancelled confirmation can never be revived
		s.mu.Unlock()
		s.reply(ctx, c, q.From.ID, tr(lang, "telegram.emergency_cancelled"), nil)
	default:
		answer("")
	}
}

func (s *Service) reply(ctx context.Context, c *Client, chatID int64, text string, kb Keyboard) {
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := c.SendMessage(rctx, chatID, text, kb); err != nil && ctx.Err() == nil {
		s.Log.Warn("telegram reply failed", "error", err)
	}
}
