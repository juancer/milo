package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/juancer/nexo/internal/actions"
	"github.com/juancer/nexo/internal/store"
)

const (
	msgNotAuthorized = "You are not authorized to use this device."
	msgBadInvite     = "This invitation is invalid or has expired."
	msgRequestSent   = "Thanks! Your request was sent to the family admin. You will get a message here once it is approved."
	msgAwaiting      = "Your request is waiting for approval by the family admin."
	maxVoiceSeconds  = 120
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
			s.Store.SetSetting(ctx, settingUpdateOffset, strconv.FormatInt(offset, 10)) //nolint:errcheck
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
			s.reply(ctx, c, from.ID, msgAwaiting, nil)
			return
		}
		s.Log.Warn("telegram: unauthorized user contacted the bot", "telegram_id", from.ID)
		s.reply(ctx, c, from.ID, msgNotAuthorized, nil)
		return
	}

	// A valid invitation is always honoured, even right after other chatter;
	// only the replies to bad guesses are rate-limited.
	inv, err := s.Store.ConsumeInvitation(ctx, HashToken(token), s.now())
	if err != nil {
		s.Log.Warn("telegram: invalid invitation used", "telegram_id", from.ID)
		if !s.strangerCoolingDown(from.ID) {
			s.reply(ctx, c, from.ID, msgBadInvite, nil)
		}
		return
	}
	_, err = s.Store.CreatePending(ctx, store.PendingAuthorization{
		TelegramID: from.ID, Username: from.Username, FirstName: from.FirstName,
		Role: inv.Role, InvitationID: inv.ID,
	})
	if err != nil {
		s.reply(ctx, c, from.ID, msgAwaiting, nil)
		return
	}
	s.Audit.Record(ctx, "telegram", "authorization.requested", strconv.FormatInt(from.ID, 10),
		map[string]string{"role": inv.Role, "username": from.Username})
	s.reply(ctx, c, from.ID, msgRequestSent, nil)
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
	}
	return false
}

// ---------------------------------------------------------------- authorized users

func (s *Service) handleKnown(ctx context.Context, c *Client, user store.User, m *Message) {
	text := strings.TrimSpace(m.Text)
	switch {
	case m.Voice != nil:
		s.receiveVoice(ctx, c, user, m)
	case text == "/start" || strings.HasPrefix(text, "/start ") || text == "/menu" || text == "/help":
		s.sendMenu(ctx, c, user, m.From.ID)
	case strings.HasPrefix(text, "/"):
		s.reply(ctx, c, m.From.ID, "Send a voice message or a text and it will be delivered. Use /menu for options.", nil)
	case text != "":
		s.deliver(ctx, c, user, m.From.ID, "text", text, nil)
	}
}

func (s *Service) receiveVoice(ctx context.Context, c *Client, user store.User, m *Message) {
	if m.Voice.Duration > maxVoiceSeconds {
		s.reply(ctx, c, m.From.ID, "Voice messages can be up to 2 minutes long.", nil)
		return
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := c.DownloadFile(dctx, m.Voice.FileID)
	if err != nil {
		s.Log.Warn("telegram: cannot download voice", "error", err)
		s.reply(ctx, c, m.From.ID, "I could not download that voice message. Please try again.", nil)
		return
	}
	s.deliver(ctx, c, user, m.From.ID, "voice", "", data)
}

// deliver hands a message to the Action Engine, which checks the sender's
// permission before anything is stored.
func (s *Service) deliver(ctx context.Context, c *Client, user store.User, chatID int64, kind, text string, audio []byte) {
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
		s.reply(ctx, c, chatID, explain(err), nil)
		return
	}
	s.reply(ctx, c, chatID, res.Message, nil)
}

func explain(err error) string {
	switch {
	case errors.Is(err, actions.ErrForbidden):
		return "You are not allowed to send messages to this device."
	case errors.Is(err, actions.ErrFeatureDisabled):
		return "Messaging is turned off on this device right now."
	case errors.Is(err, actions.ErrInvalidParams):
		return "That message cannot be delivered (it is empty or too long)."
	}
	return "Something went wrong. Please try again."
}

func (s *Service) sendMenu(ctx context.Context, c *Client, user store.User, chatID int64) {
	s.reply(ctx, c, chatID, fmt.Sprintf("Hi %s! Send a voice message or a text and it will be delivered to the device. "+
		"The child hears it when they press LISTEN.", user.DisplayName), s.menuKeyboard(ctx, user))
}

func (s *Service) menuKeyboard(ctx context.Context, user store.User) Keyboard {
	kb := Keyboard{{
		{Text: "🎤 Send voice message", CallbackData: "menu:voice"},
		{Text: "💬 Send message", CallbackData: "menu:text"},
	}}
	return kb
}

func (s *Service) handleCallback(ctx context.Context, c *Client, q *CallbackQuery) {
	answer := func(text string) { c.AnswerCallback(ctx, q.ID, text) } //nolint:errcheck
	user, err := s.Store.GetUserByTelegramID(ctx, q.From.ID)
	if err != nil || !user.Active {
		answer(msgNotAuthorized)
		return
	}
	switch q.Data {
	case "menu:voice":
		answer("")
		s.reply(ctx, c, q.From.ID, "Record a voice message and send it here.", nil)
	case "menu:text":
		answer("")
		s.reply(ctx, c, q.From.ID, "Type your message and send it here.", nil)
	case "menu:emergency":
		answer("")
		s.confirmEmergency(ctx, c, user, q.From.ID)
	case "emergency:yes":
		answer("")
		s.fireEmergency(ctx, c, user, q.From.ID)
	case "emergency:no":
		answer("Cancelled")
		s.reply(ctx, c, q.From.ID, "Cancelled. No alert was sent.", nil)
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
