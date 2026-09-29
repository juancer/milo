package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/juancer/nexo/internal/actions"
	"github.com/juancer/nexo/internal/audio"
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
		return fmt.Errorf("%w: add the AI API key in the Assistant tab", err)
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
// recording early and lets Nexo answer; pressed while Nexo is thinking or
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
		e.note(ctx, audio.NotifyRecordStart)
		// Pressing the button again finishes the recording early (not the session).
		recCtx, finish := context.WithCancel(ctx)
		e.mu.Lock()
		e.listening = finish
		e.mu.Unlock()
		wav, err := e.Audio.Record(recCtx, audio.RecordOptions{MaxDuration: maxListen, StopOnSilence: true})
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

		text, err := stt.Transcribe(ctx, wav)
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

		reply, err := e.Converse(ctx, sess, text)
		switch {
		case errors.Is(err, ErrUnavailable):
			e.log.Warn("assistant unavailable", "error", err)
			e.say(ctx, ReplyUnavailable)
			return nil
		case err != nil:
			e.note(ctx, audio.NotifyError)
			return err
		}
		if err := e.say(ctx, reply); err != nil {
			return err
		}
		if !sess.settings.FollowUp {
			return nil
		}
	}
	return nil
}

// say speaks text; when speech synthesis is not available it plays a tone so
// the child knows something happened.
func (e *Engine) say(ctx context.Context, text string) error {
	if err := e.Speak(ctx, text); err != nil && ctx.Err() == nil {
		e.log.Warn("cannot speak", "error", err)
		e.note(ctx, audio.NotifyError)
		return err
	}
	return nil
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
	wav, err := tts.Synthesize(ctx, text)
	if err != nil {
		return err
	}
	return e.Audio.Play(ctx, wav)
}
