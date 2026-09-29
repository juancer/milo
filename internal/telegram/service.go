package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/juancer/nexo/internal/actions"
	"github.com/juancer/nexo/internal/audio"
	"github.com/juancer/nexo/internal/audit"
	"github.com/juancer/nexo/internal/notifications"
	"github.com/juancer/nexo/internal/permissions"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
)

// FeatureName is the name under which the service is registered.
const FeatureName = "telegram"

// Setting and secret names.
const (
	SecretBotToken      = "telegram.bot_token"
	SettingBotUsername  = "telegram.bot_username"
	settingUpdateOffset = "telegram.update_offset"
	SettingCurrentChat  = "telegram.current_chat"
	SettingAnnounceName = "telegram.announce_sender"
)

// Deps are the collaborators of the Telegram feature.
type Deps struct {
	Store   *store.Store
	Vault   *secrets.Vault
	Audio   audio.Audio
	Codec   audio.Transcoder
	Notify  *notifications.Notifier
	Actions *actions.Engine
	Perms   *permissions.Checker
	Audit   *audit.Logger
	Log     *slog.Logger
	// BaseURL overrides the Bot API address (development and tests only).
	BaseURL string
	// Speak reads text aloud (text-to-speech). Nil until an assistant is configured.
	Speak func(ctx context.Context, text string) error
}

// Status is what the admin UI shows.
type Status struct {
	Configured  bool   `json:"configured"` // a bot token is stored
	Running     bool   `json:"running"`
	BotUsername string `json:"bot_username"`
	PollError   string `json:"poll_error,omitempty"`
}

// Service is the Telegram feature: it polls for updates, enforces
// authorization, stores messages and sends queued replies.
type Service struct {
	Deps

	mu               sync.Mutex
	client           *Client
	runCtx           context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	running          bool
	botUsername      string
	pollErr          string
	schedules        actions.ScheduleChecker
	replyAt          map[int64]time.Time // rate limit for replies to strangers
	recCancel        context.CancelFunc
	playing          atomic.Bool
	wake             chan struct{}
	now              func() time.Time
	retryBase        time.Duration // first retry delay; tests shorten it
	strangerCooldown time.Duration
	tick             time.Duration // maintenance interval; tests shorten it

	emergencyConfirm map[int64]time.Time // user id -> confirmation expiry
	emergencyAt      map[int64]time.Time // user id -> last alert
	urgentUntil      map[int64]time.Time // user id -> follow-up window end
}

// New creates the (not yet running) service.
func New(d Deps) *Service {
	return &Service{Deps: d, replyAt: map[int64]time.Time{}, wake: make(chan struct{}, 1), now: time.Now,
		emergencyConfirm: map[int64]time.Time{}, emergencyAt: map[int64]time.Time{}, urgentUntil: map[int64]time.Time{}}
}

// Name implements features.Feature.
func (s *Service) Name() string { return FeatureName }

// SetTick changes how often the maintenance loop runs (default 30s).
func (s *Service) SetTick(d time.Duration) { s.tick = d }

// SetStrangerCooldown changes how often the bot answers the same stranger (default 30s).
func (s *Service) SetStrangerCooldown(d time.Duration) { s.strangerCooldown = d }

// SetRetryBase changes the first retry delay for failed sends (default 5s).
func (s *Service) SetRetryBase(d time.Duration) { s.retryBase = d }

// SetSchedules installs the usage-schedule checker (nil means always allowed).
func (s *Service) SetSchedules(c actions.ScheduleChecker) {
	s.mu.Lock()
	s.schedules = c
	s.mu.Unlock()
}

// scheduleOpen reports whether the telegram feature may be used right now.
func (s *Service) scheduleOpen(ctx context.Context) bool {
	s.mu.Lock()
	sc := s.schedules
	s.mu.Unlock()
	if sc == nil {
		return true
	}
	ok, err := sc.Allowed(ctx, FeatureName, s.now())
	return err != nil || ok // never lock the child out because of a lookup error
}

// Start implements features.Feature. It returns quickly; work continues in
// background goroutines that survive Internet outages.
func (s *Service) Start(ctx context.Context) error {
	tok, err := s.Vault.Get(ctx, SecretBotToken)
	if errors.Is(err, secrets.ErrNotFound) {
		return errors.New("no Telegram bot token configured (add it in the Telegram tab)")
	}
	if err != nil {
		return err
	}
	client := NewClient(s.BaseURL, string(tok))

	me, err := client.GetMe(ctx)
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Permanent():
		return fmt.Errorf("Telegram rejected the bot token: %s", apiErr.Description)
	case err != nil:
		// Offline at boot: keep going, the poll loop will retry.
		s.Log.Warn("telegram unreachable at start; will retry", "error", err)
		if name, e := s.Store.GetSetting(ctx, SettingBotUsername); e == nil {
			me.Username = name
		}
	default:
		s.Store.SetSetting(ctx, SettingBotUsername, me.Username) //nolint:errcheck // cosmetic
	}

	runCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.client, s.runCtx, s.cancel = client, runCtx, cancel
	s.running, s.botUsername, s.pollErr = true, me.Username, ""
	s.mu.Unlock()

	s.spawn(func() { s.pollLoop(runCtx, client) })
	s.spawn(func() { s.outboxLoop(runCtx, client) })
	s.spawn(func() { s.maintenanceLoop(runCtx) })
	s.Log.Info("telegram started", "bot", me.Username)
	return nil
}

// Stop implements features.Feature.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel, rec := s.cancel, s.recCancel
	s.running, s.client = false, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if rec != nil {
		rec()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

// Restart applies a changed token without restarting Nexo.
func (s *Service) Restart(ctx context.Context) error {
	s.mu.Lock()
	was := s.running
	s.mu.Unlock()
	if !was {
		return nil
	}
	s.Stop(ctx) //nolint:errcheck
	return s.Start(ctx)
}

func (s *Service) spawn(f func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				s.Log.Error("telegram goroutine panicked", "panic", fmt.Sprint(r))
			}
		}()
		f()
	}()
}

// Status reports the current state.
func (s *Service) Status(ctx context.Context) Status {
	s.mu.Lock()
	st := Status{Running: s.running, BotUsername: s.botUsername, PollError: s.pollErr}
	s.mu.Unlock()
	_, err := s.Vault.Get(ctx, SecretBotToken)
	st.Configured = err == nil
	if st.BotUsername == "" {
		st.BotUsername, _ = s.Store.GetSetting(ctx, SettingBotUsername)
	}
	return st
}

// BotUsername returns this installation's bot name ("" if unknown).
func (s *Service) BotUsername(ctx context.Context) string { return s.Status(ctx).BotUsername }

func (s *Service) currentClient() *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

func (s *Service) setPollErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.pollErr = ""
		return
	}
	s.pollErr = err.Error()
}

// SendText sends a plain message to a Telegram account, best effort. Used for
// approval notices. It never blocks the caller.
func (s *Service) SendText(telegramID int64, text string) {
	c := s.currentClient()
	if c == nil {
		return
	}
	s.spawn(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := c.SendMessage(ctx, telegramID, text, nil); err != nil {
			s.Log.Warn("telegram notice not delivered", "error", err)
		}
	})
}

// sleep waits for d or until ctx ends; it reports whether the wait completed.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
