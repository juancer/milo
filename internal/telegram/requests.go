package telegram

import (
	"context"
	"time"

	"github.com/juancer/milo/internal/i18n"
	"github.com/juancer/milo/internal/store"
)

// ApproveRequest turns a pending authorization request into a person, optionally adds them as a
// chat the child can talk to, and tells them. The web admin and the Telegram admin menu share it,
// so both behave the same. It returns store.ErrNotFound when the request was already decided.
func (s *Service) ApproveRequest(ctx context.Context, id int64, displayName string, createChat bool) (store.TelegramUser, error) {
	user, err := s.Store.ApprovePending(ctx, id, displayName, time.Now())
	if err != nil {
		return store.TelegramUser{}, err
	}
	if createChat {
		existing, _ := s.Store.ListChats(ctx)
		if _, err := s.Store.CreateChat(ctx, store.Chat{
			Name: user.DisplayName, Kind: "user", UserID: user.ID, TelegramChatID: user.TelegramID,
			Favorite: true, Position: len(existing) + 1, Enabled: true, // new chats go last
		}); err != nil {
			s.Log.Warn("create chat for approved user", "error", err) // name clash: parents can add it by hand
		}
	}
	s.SendText(user.TelegramID, i18n.T(i18n.Notification(ctx, s.Store), "telegram.approved", nil))
	return user, nil
}

// RejectRequest declines a pending request and tells the person. It returns the Telegram id of
// the person, or store.ErrNotFound when the request was already decided.
func (s *Service) RejectRequest(ctx context.Context, id int64) (int64, error) {
	tg, err := s.Store.RejectPending(ctx, id, time.Now())
	if err != nil {
		return 0, err
	}
	s.SendText(tg, i18n.T(i18n.Notification(ctx, s.Store), "telegram.declined", nil))
	return tg, nil
}
