package telegram

import (
	"context"

	"github.com/juancer/nexo/internal/store"
)

// Emergency alerts arrive in Phase 4. Until then the callbacks are inert.

func (s *Service) confirmEmergency(ctx context.Context, c *Client, _ store.User, chatID int64) {
	s.reply(ctx, c, chatID, "Emergency alerts are not available yet.", nil)
}

func (s *Service) fireEmergency(ctx context.Context, c *Client, _ store.User, chatID int64) {
	s.reply(ctx, c, chatID, "Emergency alerts are not available yet.", nil)
}
