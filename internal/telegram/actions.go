package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/juancer/nexo/internal/actions"
	"github.com/juancer/nexo/internal/audio"
	"github.com/juancer/nexo/internal/permissions"
	"github.com/juancer/nexo/internal/store"
)

const (
	maxTextLen        = 1000
	maxRecording      = 60 * time.Second
	minRecording      = 600 * time.Millisecond
	maxReadAtOnce     = 10
	deliveredMsg      = "Delivered ✓"
	deliveredLaterMsg = "Delivered ✓ It will be heard when the device is available again."
	tooManyWaitingMsg = "Not delivered: too many of your messages are still waiting to be heard."

	// MaxPendingPerSender bounds the unheard messages one person can leave, so a single
	// account cannot fill the device's storage. Emergency follow-ups are not counted.
	MaxPendingPerSender = 50
	// EmergencyPendingAllowance is the absolute extra room above the cap for an
	// emergency alert and its urgent follow-ups.
	EmergencyPendingAllowance = 8

	// maxPlayAttempts is how often a voice message that cannot be decoded is retried. After
	// that it is marked failed, so one bad recording cannot hold back the messages behind it.
	maxPlayAttempts = 3
)

// RegisterActions registers Telegram's actions with the Action Engine. They
// only run while the "telegram" feature is enabled.
func (s *Service) RegisterActions(e *actions.Engine) {
	device := actions.ExposeDevice
	deviceAndAssistant := actions.ExposeDevice | actions.ExposeAssistant

	e.MustRegister(actions.Action{
		Name:        "deliver_message",
		Description: "Store a message from an authorized Telegram user for the child to hear.",
		Feature:     FeatureName, Permission: permissions.SendMessage,
		Exposure: actions.ExposeTelegram,
		// Messages are always stored, even outside usage hours; only the
		// notification sound is held back until the schedule allows it.
		BypassSchedule: true,
		Params: []actions.Param{
			{Name: "kind", Type: actions.String, Required: true, Enum: []string{"text", "voice"}},
			{Name: "text", Type: actions.String, MaxLen: maxTextLen},
			{Name: "audio", Type: actions.Bytes, MaxLen: MaxVoiceBytes},
		},
		Handler: s.deliverMessage,
	})

	s.registerEmergencyAction(e)

	e.MustRegister(actions.Action{
		Name:        "listen",
		Description: "Play the next pending message (urgent ones first).",
		Feature:     FeatureName, Exposure: device,
		// Urgent messages can always be heard; the handler restricts the rest.
		BypassSchedule: true,
		Handler:        s.listen,
	})

	e.MustRegister(actions.Action{
		Name:        "read_messages",
		Description: "Play all pending messages, oldest first.",
		Feature:     FeatureName, Exposure: deviceAndAssistant, BypassSchedule: true,
		Handler: s.readMessages,
	})

	e.MustRegister(actions.Action{
		Name:        "get_pending_messages",
		Description: "Say how many messages are waiting, without revealing their content.",
		Feature:     FeatureName, Exposure: deviceAndAssistant,
		Handler: s.pendingSummary,
	})

	e.MustRegister(actions.Action{
		Name:        "talk",
		Description: "Press once to start recording a voice message for the selected chat, press again to send it.",
		Feature:     FeatureName, Exposure: device,
		Handler: s.talk,
	})

	e.MustRegister(actions.Action{
		Name:        "chat_select",
		Description: "Choose who your next voice message goes to: cycles through the favorite chats.",
		Feature:     FeatureName, Exposure: device,
		Handler: s.chatSelect,
	})

	e.MustRegister(actions.Action{
		Name:        "send_message",
		Description: "Send a text message to one of the family chats, by its name.",
		Feature:     FeatureName, Exposure: deviceAndAssistant,
		Params: []actions.Param{
			{Name: "chat", Description: "Name of the chat, for example Dad", Type: actions.String, Required: true, MaxLen: 60},
			{Name: "text", Description: "The message to send", Type: actions.String, Required: true, MaxLen: maxTextLen},
		},
		Handler: s.sendMessage,
	})
}

// ---------------------------------------------------------------- deliver

func (s *Service) deliverMessage(ctx context.Context, c actions.Call) (actions.Result, error) {
	kind := c.Args.String("kind")
	text := strings.TrimSpace(c.Args.String("text"))
	audioData := c.Args.Bytes("audio")
	switch {
	case kind == "text" && text == "":
		return actions.Result{}, fmt.Errorf("%w: text is required", actions.ErrInvalidParams)
	case kind == "voice" && len(audioData) == 0:
		return actions.Result{}, fmt.Errorf("%w: audio is required", actions.ErrInvalidParams)
	}
	msg := store.Message{Direction: "in", Kind: kind, Body: text, Audio: audioData, UserID: c.Actor.UserID}
	if s.urgentActive(ctx, c.Actor.UserID) { // follows an emergency alert from the same person
		msg.Type, msg.Priority = "emergency", "critical"
	}
	if s.senderAtCeiling(ctx, c.Actor.UserID, msg.Priority == "critical") {
		return actions.Result{Message: tooManyWaitingMsg}, nil
	}
	id, err := s.Store.InsertMessage(ctx, msg)
	if err != nil {
		return actions.Result{}, err
	}
	if s.announce(ctx, id, false) {
		return actions.Result{Message: deliveredMsg}, nil
	}
	return actions.Result{Message: deliveredLaterMsg}, nil
}

// senderAtCeiling reports whether the sender already has as many unheard messages
// as they may leave. Urgent messages get a small absolute allowance on top of the
// normal cap, so an emergency is never refused for a full queue yet stays bounded.
func (s *Service) senderAtCeiling(ctx context.Context, userID int64, urgent bool) bool {
	limit := MaxPendingPerSender
	if urgent {
		limit += EmergencyPendingAllowance
	}
	n, err := s.Store.PendingFrom(ctx, userID)
	return err == nil && n >= limit
}

// senderMayBeHeard reports whether a pending message may still be played: its
// sender must remain active and permitted (emergency alerts need emergency_alert,
// everything else send_message).
func (s *Service) senderMayBeHeard(ctx context.Context, m store.Message) bool {
	if m.UserID == 0 {
		return true
	}
	p := permissions.SendMessage
	if m.Type == "emergency" {
		p = permissions.EmergencyAlert
	}
	ok, err := s.Perms.Has(ctx, m.UserID, p)
	return err != nil || ok // when the check fails the message stays pending
}

// announce plays the notification sound for a stored message when allowed and
// reports whether it did. Outside usage hours the message stays pending and silent.
func (s *Service) announce(ctx context.Context, id int64, emergency bool) bool {
	if !emergency && !s.scheduleOpen(ctx) {
		return false
	}
	kind := audio.NotifyMessage
	if emergency {
		kind = audio.NotifyEmergency
	}
	s.Store.MarkNotified(ctx, id, s.now()) //nolint:errcheck
	s.spawn(func() {
		nctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.Notify.Notify(nctx, kind); err != nil {
			s.Log.Warn("notification sound failed", "error", err)
		}
	})
	return true
}

// ---------------------------------------------------------------- listen

func (s *Service) listen(ctx context.Context, _ actions.Call) (actions.Result, error) {
	if s.playing.Load() { // pressing LISTEN again stops the message
		s.Audio.Stop()
		return actions.Result{Message: "Stopped"}, nil
	}
	played, err := s.playNext(ctx)
	if err != nil {
		return actions.Result{}, err
	}
	if !played {
		return actions.Result{Message: "No messages"}, nil
	}
	return actions.Result{Message: "Played a message"}, nil
}

func (s *Service) readMessages(ctx context.Context, _ actions.Call) (actions.Result, error) {
	n := 0
	for n < maxReadAtOnce && ctx.Err() == nil {
		played, err := s.playNext(ctx)
		if err != nil {
			return actions.Result{}, err
		}
		if !played {
			break
		}
		n++
	}
	if n == 0 {
		return actions.Result{Message: "No messages"}, nil
	}
	return actions.Result{Message: fmt.Sprintf("Played %d messages", n)}, nil
}

// playNext plays the most urgent pending message. Outside usage hours only
// emergency messages may be heard.
func (s *Service) playNext(ctx context.Context) (bool, error) {
	open := s.scheduleOpen(ctx)
	m, err := s.Store.NextPending(ctx, !open)
	for err == nil && !s.senderMayBeHeard(ctx, m) {
		s.Store.ExpireMessage(ctx, m.ID) //nolint:errcheck
		m, err = s.Store.NextPending(ctx, !open)
	}
	if errors.Is(err, store.ErrNotFound) {
		if total, _, _ := s.Store.PendingCounts(ctx); total > 0 && !open {
			s.Notify.Notify(ctx, audio.NotifyError) //nolint:errcheck // "not now"
			return false, nil
		}
		s.Notify.Notify(ctx, audio.NotifyNone) //nolint:errcheck
		return false, nil
	}
	if err != nil {
		return false, err
	}

	s.playing.Store(true)
	defer s.playing.Store(false)

	if s.announceSender(ctx) && s.Speak != nil && m.UserID != 0 {
		if u, err := s.Store.GetUser(ctx, m.UserID); err == nil {
			s.Speak(ctx, u.DisplayName) //nolint:errcheck
		}
	}
	switch m.Kind {
	case "voice":
		wav, err := s.Codec.OggToWAV(ctx, m.Audio)
		if err != nil {
			s.Notify.Notify(ctx, audio.NotifyError) //nolint:errcheck
			if ctx.Err() == nil {
				// The retry time is not used for incoming messages; a non-nil value keeps the
				// message pending until the last attempt.
				var retry *time.Time
				if m.Attempts+1 < maxPlayAttempts {
					now := s.now()
					retry = &now
				}
				s.Store.MarkAttemptFailed(ctx, m.ID, "cannot decode voice message", retry) //nolint:errcheck
			}
			return false, fmt.Errorf("cannot decode voice message: %w", err)
		}
		if err := s.Audio.Play(ctx, wav); err != nil {
			return false, err
		}
	default:
		spoke := false
		if s.Speak != nil {
			if err := s.Speak(ctx, m.Body); err != nil {
				s.Log.Warn("cannot read message aloud", "error", err)
			} else {
				spoke = true
			}
		}
		if !spoke {
			// No text-to-speech available: a distinct sound instead of silence.
			s.Notify.Notify(ctx, audio.NotifyMessage) //nolint:errcheck
		}
	}
	if ctx.Err() == nil {
		s.Store.MarkPlayed(ctx, m.ID, s.now()) //nolint:errcheck
	}
	return true, nil
}

func (s *Service) announceSender(ctx context.Context) bool {
	v, err := s.Store.GetSetting(ctx, SettingAnnounceName)
	return err == nil && v == "1"
}

func (s *Service) pendingSummary(ctx context.Context, _ actions.Call) (actions.Result, error) {
	total, urgent, err := s.Store.PendingCounts(ctx)
	if err != nil {
		return actions.Result{}, err
	}
	msg := "You have no new messages."
	switch {
	case total == 1:
		msg = "You have 1 new message."
	case total > 1:
		msg = fmt.Sprintf("You have %d new messages.", total)
	}
	if urgent > 0 {
		msg += fmt.Sprintf(" %d of them urgent.", urgent)
	}
	return actions.Result{Message: msg, Data: map[string]any{"pending": total, "urgent": urgent}}, nil
}

// ---------------------------------------------------------------- talk / chat_select

// currentChat returns the selected destination, falling back to the first favorite.
func (s *Service) currentChat(ctx context.Context) (store.Chat, error) {
	chats, err := s.Store.ListChats(ctx)
	if err != nil {
		return store.Chat{}, err
	}
	var favorites []store.Chat
	for _, c := range chats {
		if c.Enabled && c.Favorite {
			favorites = append(favorites, c)
		}
	}
	if len(favorites) == 0 {
		return store.Chat{}, store.ErrNotFound
	}
	if v, err := s.Store.GetSetting(ctx, SettingCurrentChat); err == nil {
		for _, c := range favorites {
			if fmt.Sprint(c.ID) == v {
				return c, nil
			}
		}
	}
	return favorites[0], nil
}

func (s *Service) chatSelect(ctx context.Context, _ actions.Call) (actions.Result, error) {
	chats, err := s.Store.ListChats(ctx)
	if err != nil {
		return actions.Result{}, err
	}
	var favorites []store.Chat
	for _, c := range chats {
		if c.Enabled && c.Favorite {
			favorites = append(favorites, c)
		}
	}
	if len(favorites) == 0 {
		s.Notify.Notify(ctx, audio.NotifyError) //nolint:errcheck
		return actions.Result{}, errors.New("no favorite chats are configured")
	}
	cur, _ := s.currentChat(ctx)
	next := favorites[0]
	for i, c := range favorites {
		if c.ID == cur.ID {
			next = favorites[(i+1)%len(favorites)]
		}
	}
	if err := s.Store.SetSetting(ctx, SettingCurrentChat, fmt.Sprint(next.ID)); err != nil {
		return actions.Result{}, err
	}
	idx := 1
	for i, c := range favorites {
		if c.ID == next.ID {
			idx = i + 1
		}
	}
	// Say the name when text-to-speech exists, otherwise beep the chat's number.
	if s.Speak == nil || s.Speak(ctx, next.Name) != nil {
		s.Audio.Play(ctx, audio.Blips(idx)) //nolint:errcheck
	}
	return actions.Result{Message: next.Name, Data: map[string]any{"chat": next.Name}}, nil
}

// talk toggles recording: the first call starts, the second stops and sends.
func (s *Service) talk(ctx context.Context, _ actions.Call) (actions.Result, error) {
	s.mu.Lock()
	if s.recCancel != nil {
		cancel := s.recCancel
		s.mu.Unlock()
		cancel() // the recording goroutine finishes and sends
		return actions.Result{Message: "Sending"}, nil
	}
	if !s.running {
		s.mu.Unlock()
		return actions.Result{}, errors.New("telegram is not running")
	}
	chat, err := s.currentChat(ctx)
	if err != nil {
		s.mu.Unlock()
		s.Notify.Notify(ctx, audio.NotifyError) //nolint:errcheck
		return actions.Result{}, errors.New("no chat is selected: add a favorite chat first")
	}
	rctx, cancel := context.WithCancel(s.runCtx)
	s.recCancel = cancel
	s.mu.Unlock()

	s.spawn(func() {
		defer func() {
			cancel()
			s.mu.Lock()
			s.recCancel = nil
			s.mu.Unlock()
		}()
		s.recordAndQueue(rctx, chat)
	})
	return actions.Result{Message: "Recording for " + chat.Name}, nil
}

func (s *Service) recordAndQueue(ctx context.Context, chat store.Chat) {
	s.Notify.Notify(context.Background(), audio.NotifyRecordStart) //nolint:errcheck
	wav, err := s.Audio.Record(ctx, audio.RecordOptions{MaxDuration: maxRecording})
	s.Notify.Notify(context.Background(), audio.NotifyRecordStop) //nolint:errcheck
	bg := context.Background()
	if err != nil {
		s.Log.Error("recording failed", "error", err)
		s.Notify.Notify(bg, audio.NotifyError) //nolint:errcheck
		return
	}
	if audio.Duration(wav) < minRecording {
		s.Notify.Notify(bg, audio.NotifyNone) //nolint:errcheck // too short to send
		return
	}
	ogg, err := s.Codec.WAVToOgg(bg, wav)
	if err != nil {
		s.Log.Error("cannot encode voice message", "error", err)
		s.Notify.Notify(bg, audio.NotifyError) //nolint:errcheck
		return
	}
	if _, err := s.Store.InsertMessage(bg, store.Message{Direction: "out", Kind: "voice", ChatID: chat.ID, Audio: ogg}); err != nil {
		s.Log.Error("cannot queue voice message", "error", err)
		s.Notify.Notify(bg, audio.NotifyError) //nolint:errcheck
		return
	}
	s.wakeOutbox()
	s.Notify.Notify(bg, audio.NotifyOK) //nolint:errcheck
}

// ---------------------------------------------------------------- send_message

func (s *Service) sendMessage(ctx context.Context, c actions.Call) (actions.Result, error) {
	chats, err := s.Store.ListChats(ctx)
	if err != nil {
		return actions.Result{}, err
	}
	want := strings.ToLower(strings.TrimSpace(c.Args.String("chat")))
	var names []string
	for _, ch := range chats {
		if !ch.Enabled {
			continue
		}
		names = append(names, ch.Name)
		if strings.ToLower(ch.Name) == want {
			text := strings.TrimSpace(c.Args.String("text"))
			if text == "" {
				return actions.Result{}, fmt.Errorf("%w: text is empty", actions.ErrInvalidParams)
			}
			if _, err := s.Store.InsertMessage(ctx, store.Message{Direction: "out", Kind: "text", ChatID: ch.ID, Body: text}); err != nil {
				return actions.Result{}, err
			}
			s.wakeOutbox()
			return actions.Result{Message: "Message queued for " + ch.Name, Data: map[string]any{"chat": ch.Name}}, nil
		}
	}
	return actions.Result{}, fmt.Errorf("%w: unknown chat %q; available chats: %s",
		actions.ErrInvalidParams, c.Args.String("chat"), strings.Join(names, ", "))
}
