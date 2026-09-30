package telegram

import (
	"context"
	"strconv"
	"time"

	"github.com/juancer/milo/internal/audio"
	"github.com/juancer/milo/internal/notifications"
)

const settingRetentionDays = "messages.retention_days"

// maintenanceLoop announces messages that arrived while usage hours were closed
// (once, when hours begin) and prunes old, finished messages.
func (s *Service) maintenanceLoop(ctx context.Context) {
	tick := s.tick
	if tick == 0 {
		tick = 30 * time.Second
	}
	wasOpen := false // so a message waiting at boot is announced on the first tick
	var lastPrune time.Time
	for {
		open := s.scheduleOpen(ctx)
		if open && !wasOpen {
			s.announceBacklog(ctx)
		}
		wasOpen = open
		if s.now().Sub(lastPrune) > time.Hour {
			lastPrune = s.now()
			s.pruneOld(ctx)
		}
		if !sleep(ctx, tick) {
			return
		}
	}
}

// announceBacklog plays a single sound for messages that were stored silently.
func (s *Service) announceBacklog(ctx context.Context) {
	pending, err := s.Store.UnnotifiedPending(ctx)
	if err != nil || len(pending) == 0 {
		return
	}
	cfg := notifications.Load(ctx, s.Store)
	for _, m := range pending {
		s.Store.MarkNotified(ctx, m.ID, s.now()) //nolint:errcheck
	}
	if !cfg.NotifyPendingAfter {
		return
	}
	if err := s.Notify.Notify(ctx, audio.NotifyMessage); err != nil {
		s.Log.Warn("backlog notification failed", "error", err)
	}
}

func (s *Service) pruneOld(ctx context.Context) {
	days := 30
	if v, err := s.Store.GetSetting(ctx, settingRetentionDays); err == nil {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	if days <= 0 {
		return // keep everything
	}
	n, err := s.Store.PruneMessages(ctx, s.now().AddDate(0, 0, -days))
	if err != nil {
		s.Log.Warn("message pruning failed", "error", err)
	} else if n > 0 {
		s.Log.Info("old messages removed", "count", n)
	}
}
