package telegram

import (
	"context"
	"errors"
	"time"

	"github.com/juancer/milo/internal/permissions"
	"github.com/juancer/milo/internal/store"
)

const maxAttempts = 20

// outboxLoop delivers queued outgoing messages. Messages stay in SQLite until
// Telegram accepts them, so an Internet outage delays but never loses them.
func (s *Service) outboxLoop(ctx context.Context, c *Client) {
	for ctx.Err() == nil {
		for ctx.Err() == nil {
			m, err := s.Store.NextQueued(ctx, s.now())
			if err != nil {
				break
			}
			s.sendOne(ctx, c, m)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-time.After(2 * time.Second):
		}
	}
}

// wakeOutbox asks the loop to look for new work immediately.
func (s *Service) wakeOutbox() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) sendOne(ctx context.Context, c *Client, m store.Message) {
	fail := func(reason string, retry bool) {
		var at *time.Time
		if retry && m.Attempts+1 < maxAttempts {
			t := s.now().Add(s.backoff(m.Attempts))
			at = &t
		}
		s.Store.MarkAttemptFailed(ctx, m.ID, reason, at) //nolint:errcheck
	}

	chat, err := s.Store.GetChat(ctx, m.ChatID)
	if err != nil || !chat.Enabled {
		fail("destination chat no longer exists or is disabled", false)
		return
	}
	// The destination must still be someone the parents authorized.
	if chat.Kind == "user" {
		ok, err := s.Perms.Has(ctx, chat.UserID, permissions.ReceiveMessage)
		if err != nil {
			fail("cannot check permissions", true)
			return
		}
		if !ok {
			fail("recipient is not authorized to receive messages", false)
			return
		}
	}

	sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if m.Kind == "voice" {
		_, err = c.SendVoice(sctx, chat.TelegramChatID, m.Audio, 0)
	} else {
		_, err = c.SendMessage(sctx, chat.TelegramChatID, m.Body, nil)
	}
	if err == nil {
		s.Store.MarkSent(ctx, m.ID) //nolint:errcheck
		return
	}
	if ctx.Err() != nil {
		return
	}
	var api *APIError
	if errors.As(err, &api) && api.Permanent() {
		fail(api.Error(), false)
		return
	}
	s.Log.Warn("telegram send failed; will retry", "message", m.ID, "attempt", m.Attempts+1, "error", err)
	fail(err.Error(), true)
}

// backoff grows 5s, 10s, 20s ... capped at 10 minutes.
func (s *Service) backoff(attempts int) time.Duration {
	base := s.retryBase
	if base == 0 {
		base = 5 * time.Second
	}
	return min(base<<min(attempts, 7), 10*time.Minute)
}
