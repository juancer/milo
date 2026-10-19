package telegram

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/juancer/milo/internal/audio"
	"github.com/juancer/milo/internal/store"
)

// Announcing a message by voice. When a message arrives (and usage hours are open, and nobody is in the
// middle of something), Milo says who it is from and asks whether to read it; the answer is understood
// by rules, without the language model. A message that is not read stays queued and is offered again
// ("por cierto...") the next time somebody talks to Milo, once per conversation.

// SettingAnnounceByVoice switches the spoken announcement off ("0"); on by default. With it off, or when
// Milo cannot listen (no speech-to-text) or speak, a new message gets the notification sound as before.
const SettingAnnounceByVoice = "telegram.announce_by_voice"

const (
	defaultAnnounceBatch = 3 * time.Second        // wait for more messages of a burst before speaking
	defaultIdlePoll      = 300 * time.Millisecond // how often to check that nothing else is going on
	maxIdleWait          = 2 * time.Minute        // give up the announcement after this (it stays queued)
	maxNamesSpoken       = 3
)

// announceState is what the announcement flow keeps in memory.
type announceState struct {
	mu         sync.Mutex // one announcement or offer at a time
	sig        chan struct{}
	asked      map[int64]bool // messages already announced (not announced again by themselves)
	declined   map[int64]bool // messages the person said no to (not offered again by "por cierto")
	announcing atomic.Bool
}

func newAnnounceState() *announceState {
	return &announceState{sig: make(chan struct{}, 1), asked: map[int64]bool{}, declined: map[int64]bool{}}
}

// SetAnnounceTiming shortens the waits (tests).
func (s *Service) SetAnnounceTiming(batch, idlePoll time.Duration) {
	s.annBatch, s.annIdle = batch, idlePoll
}

func (s *Service) voiceAnnounceOn(ctx context.Context) bool {
	if s.Ask == nil || s.Speak == nil {
		return false
	}
	v, err := s.Store.GetSetting(ctx, SettingAnnounceByVoice)
	return err != nil || v != "0"
}

// Announcing reports whether a spoken announcement or offer is going on right now.
func (s *Service) Announcing() bool { return s.ann.announcing.Load() }

// requestAnnounce asks the announcement loop to look at the queue soon.
func (s *Service) requestAnnounce() {
	select {
	case s.ann.sig <- struct{}{}:
	default:
	}
}

func (s *Service) announceLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.ann.sig:
		}
		batch := s.annBatch
		if batch == 0 {
			batch = defaultAnnounceBatch
		}
		if !sleep(ctx, batch) { // let the rest of a burst arrive
			return
		}
		s.announceNew(ctx)
	}
}

// waitIdle waits until nothing else is playing or being said, up to maxIdleWait.
func (s *Service) waitIdle(ctx context.Context) bool {
	poll := s.annIdle
	if poll == 0 {
		poll = defaultIdlePoll
	}
	deadline := s.now().Add(maxIdleWait)
	for s.now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		if !s.playing.Load() && (s.Idle == nil || s.Idle()) {
			return true
		}
		if !sleep(ctx, poll) {
			return false
		}
	}
	return false
}

// pendingFor are the pending, non-urgent messages from people still allowed, minus those skip drops
// (urgent ones have their own, louder path).
func (s *Service) pendingFor(ctx context.Context, skip func(m store.Message) bool) []store.Message {
	all, err := s.Store.PendingIncoming(ctx)
	if err != nil {
		return nil
	}
	var out []store.Message
	for _, m := range all {
		if m.Type == "emergency" || m.Priority == "critical" || !s.senderMayBeHeard(ctx, m) || skip(m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// senders names who the messages are from, each once, in order of arrival, at most maxNamesSpoken.
func (s *Service) senders(ctx context.Context, lang string, msgs []store.Message) string {
	var names []string
	seen := map[int64]bool{}
	for _, m := range msgs {
		if seen[m.UserID] {
			continue
		}
		seen[m.UserID] = true
		name := tr(lang, "telegram.ann_someone")
		if u, err := s.Store.GetUser(ctx, m.UserID); err == nil && strings.TrimSpace(u.DisplayName) != "" {
			name = u.DisplayName
		}
		names = append(names, name)
	}
	if len(names) > maxNamesSpoken {
		names = names[:maxNamesSpoken]
	}
	switch len(names) {
	case 0:
		return tr(lang, "telegram.ann_someone")
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " " + tr(lang, "telegram.ann_and") + " " + names[len(names)-1]
}

// announceNew speaks the announcement for the messages that arrived and asks whether to read them.
func (s *Service) announceNew(ctx context.Context) {
	if !s.waitIdle(ctx) { // before taking the lock: a "por cierto" must never wait behind this
		return
	}
	if !s.ann.mu.TryLock() {
		return
	}
	defer s.ann.mu.Unlock()
	if !s.scheduleOpen(ctx) { // outside usage hours nothing is ever announced
		return
	}
	msgs := s.pendingFor(ctx, func(m store.Message) bool { return s.ann.asked[m.ID] || s.ann.declined[m.ID] })
	if len(msgs) == 0 {
		return
	}
	lang := s.spokenLang(ctx)
	who := s.senders(ctx, lang, msgs)
	prompt := tr(lang, "telegram.ann_one", "who", who)
	if len(msgs) > 1 {
		prompt = tr(lang, "telegram.ann_many", "n", len(msgs), "who", who)
	}
	s.offer(ctx, msgs, prompt, true)
}

// OfferBacklog is "por cierto, tienes N mensajes por escuchar", said after Milo answered something.
// It does nothing outside usage hours, with nothing waiting, or when the person already said no.
func (s *Service) OfferBacklog(ctx context.Context) {
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if s.Ask == nil || s.Speak == nil || !running || !s.ann.mu.TryLock() {
		return
	}
	defer s.ann.mu.Unlock()
	if !s.scheduleOpen(ctx) {
		return
	}
	msgs := s.pendingFor(ctx, func(m store.Message) bool { return s.ann.declined[m.ID] })
	if len(msgs) == 0 {
		return
	}
	lang := s.spokenLang(ctx)
	prompt := tr(lang, "telegram.ann_btw_one")
	if len(msgs) > 1 {
		prompt = tr(lang, "telegram.ann_btw_many", "n", len(msgs))
	}
	s.offer(ctx, msgs, prompt, false)
}

// offer asks the question and acts on the answer. The caller holds s.ann.mu.
func (s *Service) offer(ctx context.Context, msgs []store.Message, prompt string, soundIfCannotAsk bool) {
	s.ann.announcing.Store(true)
	defer s.ann.announcing.Store(false)
	var release func()
	if s.Hold != nil {
		release = s.Hold() // the music stays paused, and no conversation starts, until we are done
		defer release()
	}
	for _, m := range msgs {
		s.ann.asked[m.ID] = true
		s.Store.MarkNotified(ctx, m.ID, s.now()) //nolint:errcheck
	}
	yes, heard, err := s.Ask(ctx, prompt)
	switch {
	case err != nil:
		s.Log.Info("telegram: could not ask about the new message", "error", err)
		if soundIfCannotAsk && ctx.Err() == nil {
			s.Notify.Notify(ctx, audio.NotifyMessage) //nolint:errcheck // the old behaviour: a sound
		}
	case yes:
		s.readAll(ctx, len(msgs))
	case heard: // a clear no: it waits, and is not offered again by itself
		for _, m := range msgs {
			s.ann.declined[m.ID] = true
		}
	}
	// No answer (nobody there): nothing more is said; the messages stay queued for the next "por cierto".
}

// readAll plays the pending messages, most urgent first, as readMessages does.
func (s *Service) readAll(ctx context.Context, n int) {
	for i := 0; i < min(max(n, 1), maxReadAtOnce) && ctx.Err() == nil; i++ {
		played, err := s.playNext(ctx)
		if err != nil || !played {
			return
		}
	}
}
