package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/audio"
	"github.com/juancer/milo/internal/i18n"
)

const (
	maxVoiceTurns = 5
	maxListen     = 20 * time.Second
)

// Name implements features.Feature.
func (e *Engine) Name() string { return FeatureName }

// Start implements features.Feature. The assistant only checks that it has what
// it needs; providers are created per conversation from the current settings.
func (e *Engine) Start(ctx context.Context) error {
	s, err := e.store.GetGPTSettings(ctx)
	if err != nil {
		return err
	}
	if _, err := e.backends.LLM(ctx, s); errors.Is(err, ErrNotConfigured) {
		return fmt.Errorf("%w: add the AI API key under Assistant in the admin", err)
	} else if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.running, e.runCtx = true, runCtx
	prevSession, prevRun := e.cancel, e.cancelRun
	e.cancel, e.cancelRun = nil, cancel
	e.mu.Unlock()
	if prevSession != nil {
		prevSession()
	}
	if prevRun != nil {
		prevRun()
	}
	return nil
}

// Stop implements features.Feature.
func (e *Engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	e.running = false
	cancel, session := e.cancelRun, e.cancel
	e.cancel = nil
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if session != nil {
		session()
	}
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

// RegisterActions registers the assistant's actions.
func (e *Engine) RegisterActions(a *actions.Engine) {
	a.MustRegister(actions.Action{
		Name:        "gpt",
		Description: "Talk to the assistant: press to start listening, press again to stop.",
		Feature:     FeatureName, Exposure: actions.ExposeDevice,
		Handler: func(ctx context.Context, _ actions.Call) (actions.Result, error) {
			state, err := e.ToggleVoice()
			if err != nil {
				return actions.Result{}, err
			}
			return actions.Result{Message: strings.ToUpper(string(state)[:1]) + string(state)[1:]}, nil
		},
	})
}

// Busy reports whether a conversation is in progress.
func (e *Engine) Busy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cancel != nil || e.holds.Load() > 0
}

// offerBacklog lets the rest of Milo mention what is waiting, once per conversation, right after an
// answer has been spoken.
func (e *Engine) offerBacklog(ctx context.Context, sess *Session) {
	if e.Backlog == nil || sess.backlogOffered || ctx.Err() != nil {
		return
	}
	sess.backlogOffered = true
	e.Backlog(ctx)
}

// Listening reports whether the microphone is currently open for the child.
func (e *Engine) Listening() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.listening != nil
}

// VoiceState says what a press of the GPT button did.
type VoiceState string

const (
	VoiceListening  VoiceState = "listening"  // a conversation started
	VoiceProcessing VoiceState = "processing" // the child finished talking early
	VoiceStopped    VoiceState = "stopped"    // the conversation was interrupted
)

// ToggleVoice is what the GPT button does. With nothing running it starts a
// conversation. Pressed again while the microphone is open, it ends the
// recording early and lets Milo answer; pressed while Milo is thinking or
// speaking, it interrupts. The wake word will use the same entry point.
func (e *Engine) ToggleVoice() (VoiceState, error) {
	e.mu.Lock()
	if e.listening != nil {
		finish := e.listening
		e.mu.Unlock()
		finish()
		return VoiceProcessing, nil
	}
	if e.cancel != nil {
		cancel := e.cancel
		e.mu.Unlock()
		cancel()
		if e.Audio != nil {
			e.Audio.Stop()
		}
		return VoiceStopped, nil
	}
	if !e.running {
		e.mu.Unlock()
		return "", ErrNotConfigured
	}
	ctx, cancel := context.WithCancel(e.runCtx)
	e.cancel = cancel
	e.wg.Add(1)
	e.mu.Unlock()

	go func() {
		defer e.wg.Done()
		defer func() {
			cancel()
			e.mu.Lock()
			e.cancel, e.listening = nil, nil
			e.mu.Unlock()
		}()
		defer func() {
			if r := recover(); r != nil {
				e.log.Error("voice session panicked", "panic", fmt.Sprint(r))
			}
		}()
		if err := e.RunVoice(ctx, "button"); err != nil && ctx.Err() == nil {
			e.log.Warn("voice session ended with an error", "error", err)
		}
	}()
	return VoiceListening, nil
}

func (e *Engine) note(ctx context.Context, n audio.Notification) {
	if e.Notify != nil {
		e.Notify.Notify(ctx, n) //nolint:errcheck // a missing beep is not worth failing for
	}
}

// RunVoice listens, understands, answers and speaks, for up to a few turns.
// Every failure ends in something the child can hear (a tone or a kind
// sentence), never in silence.
func (e *Engine) RunVoice(ctx context.Context, source string) error {
	sess, err := e.NewSession(ctx, source)
	if err != nil {
		return err
	}
	stt, err := e.backends.STT(ctx, sess.settings)
	if err != nil {
		e.note(ctx, audio.NotifyError)
		return fmt.Errorf("speech-to-text: %w", err)
	}

	for turn := 0; turn < maxVoiceTurns && ctx.Err() == nil; turn++ {
		if turn > 0 && !e.sessionAllowed(ctx) { // the schedule may have closed since the last turn
			return nil
		}
		e.note(ctx, audio.NotifyRecordStart)
		// Pressing the button again finishes the recording early (not the session).
		recCtx, finish := context.WithCancel(ctx)
		e.mu.Lock()
		e.listening = finish
		e.mu.Unlock()
		giveUp := 8 * time.Second
		if turn > 0 {
			giveUp = 4 * time.Second // a follow-up that nobody answers should not keep the microphone open
		}
		t0 := time.Now()
		wav, err := e.Audio.Record(recCtx, audio.RecordOptions{MaxDuration: maxListen, StopOnSilence: true, NoSpeechFor: giveUp})
		recordMS := time.Since(t0).Milliseconds()
		e.mu.Lock()
		e.listening = nil
		e.mu.Unlock()
		finish()
		if ctx.Err() != nil {
			return nil
		}
		e.note(ctx, audio.NotifyRecordStop)
		if err != nil {
			e.note(ctx, audio.NotifyError)
			return fmt.Errorf("recording: %w", err)
		}

		// Only the room's noise: skip the speech service, which would invent words from it.
		if hs, ok := e.Audio.(interface{ HasSpeech(wav []byte) bool }); ok && !hs.HasSpeech(wav) {
			e.log.Info("voice turn: no speech heard", "record_ms", recordMS)
			if turn == 0 {
				e.note(ctx, audio.NotifyNone)
			}
			return nil
		}
		t1 := time.Now()
		text, err := stt.Transcribe(ctx, wav)
		sttMS := time.Since(t1).Milliseconds()
		if err != nil {
			e.note(ctx, audio.NotifyError)
			return fmt.Errorf("transcription: %w", err)
		}
		if text == "" { // nobody spoke
			if turn == 0 {
				e.note(ctx, audio.NotifyNone)
			}
			return nil
		}

		e.log.Info("voice turn", "record_ms", recordMS, "stt_ms", sttMS, "chars", len(text))
		if e.tryIntent(ctx, sess, text) { // a plain command: no round trip to the model
			if sess.finished || !sess.settings.FollowUp {
				return nil
			}
			continue
		}
		t2 := time.Now()
		reply, err := e.Converse(ctx, sess, text)
		replyMS := time.Since(t2).Milliseconds()
		switch {
		case errors.Is(err, ErrUnavailable):
			e.log.Warn("assistant unavailable", "error", err)
			e.say(ctx, i18n.T(sess.settings.Language, ReplyUnavailable, nil))
			return nil
		case err != nil:
			e.note(ctx, audio.NotifyError)
			return err
		}
		t3 := time.Now()
		if err := e.say(ctx, reply); err != nil {
			return err
		}
		e.log.Info("voice turn answered", "reply_ms", replyMS, "speak_ms", time.Since(t3).Milliseconds())
		e.offerBacklog(ctx, sess)
		if !sess.settings.FollowUp || sess.finished {
			return nil
		}
	}
	return nil
}

// sessionAllowed reports whether the assistant may still be used right now: its feature is on and
// its schedule is open. The Action Engine already applies both to the "gpt" action, so ask it which
// device tools are currently usable. An error counts as closed.
func (e *Engine) sessionAllowed(ctx context.Context) bool {
	for _, t := range e.actions.ToolsFor(ctx, actions.ActorDevice) {
		if t.Name == "gpt" {
			return true
		}
	}
	return false
}

// say speaks text; when speech synthesis is not available it plays a tone so
// the child knows something happened.
func (e *Engine) say(ctx context.Context, text string) error {
	e.mu.Lock()
	e.spoken = text
	e.mu.Unlock()
	if err := e.Speak(ctx, text); err != nil && ctx.Err() == nil {
		e.log.Warn("cannot speak", "error", err)
		e.note(ctx, audio.NotifyError)
		return err
	}
	return nil
}

// lastAnswer is what Milo last said aloud ("" if nothing yet).
func (e *Engine) lastAnswer() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.spoken
}

// Speak reads text aloud with the configured text-to-speech engine. Telegram
// uses it to read messages and chat names.
func (e *Engine) Speak(ctx context.Context, text string) error {
	s, err := e.store.GetGPTSettings(ctx)
	if err != nil {
		return err
	}
	tts, err := e.backends.TTS(ctx, s)
	if err != nil {
		return err
	}
	chunks := speechChunks(text)
	if len(chunks) <= 1 {
		wav, err := tts.Synthesize(ctx, text)
		if err != nil {
			return err
		}
		return e.Audio.Play(ctx, wav)
	}
	// A long answer starts with its first sentence while the next one is being made: on a Raspberry Pi
	// making the voice takes about as long as saying it, so waiting for all of it would double the wait.
	type piece struct {
		wav []byte
		err error
	}
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make(chan piece, 1)
	go func() {
		defer close(ready)
		for _, c := range chunks {
			wav, err := tts.Synthesize(pctx, c)
			select {
			case ready <- piece{wav, err}:
			case <-pctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for p := range ready {
		if p.err != nil {
			return p.err
		}
		if err := e.Audio.Play(ctx, p.wav); err != nil {
			return err
		}
	}
	return nil
}

// speechChunks cuts an answer into pieces of about a sentence, to be made and spoken one after the
// other: no piece is shorter than minChunk characters (a short one joins its neighbour) and there are
// at most maxChunks.
func speechChunks(text string) []string {
	const minChunk, maxChunks = 40, 5
	var sentences []string
	var cur strings.Builder
	rs := []rune(strings.TrimSpace(text))
	for i, r := range rs {
		cur.WriteRune(r)
		end := r == '.' || r == '!' || r == '?' || r == '…' || r == '\n'
		if end && (i+1 == len(rs) || rs[i+1] == ' ' || rs[i+1] == '\n') {
			if s := strings.TrimSpace(cur.String()); s != "" {
				sentences = append(sentences, s)
			}
			cur.Reset()
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		sentences = append(sentences, s)
	}
	var chunks []string
	for _, s := range sentences {
		if n := len(chunks); n > 0 && (len([]rune(chunks[n-1])) < minChunk || len(chunks) >= maxChunks) {
			chunks[n-1] += " " + s
			continue
		}
		chunks = append(chunks, s)
	}
	if n := len(chunks); n > 1 && len([]rune(chunks[n-1])) < minChunk {
		chunks[n-2] += " " + chunks[n-1]
		chunks = chunks[:n-1]
	}
	return chunks
}
