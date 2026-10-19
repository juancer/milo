package assistant

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/juancer/milo/internal/audio"
)

// Answer is what a person said to a yes/no question.
type Answer int

const (
	AnswerNone Answer = iota // nothing understood
	AnswerYes
	AnswerNo
)

var (
	noRe  = regexp.MustCompile(`^(?:no|nop|nope|no gracias|ahora no|luego|mas tarde|despues|luego lo oigo|luego lo escucho|ahora no puedo|dejalo|olvidalo|no quiero|no hace falta|no lo leas|paso|ni hablar|not now|later|no thanks|no thank you|not right now|leave it|skip it|skip|never mind|don t)(?: .*)?$`)
	yesRe = regexp.MustCompile(`^(?:si|sii|siii|claro|vale|venga|ok|okay|dale|por supuesto|adelante|leelo|leela|leelos|leelas|leemelo|leemela|leemelos|lee el mensaje|lee los mensajes|ponlo|ponlos|reproducelo|reproducelos|quiero|hazlo|yes|yeah|yep|yup|sure|go ahead|read it|read them|play it|play them|of course|do it|sure thing)(?: .*)?$`)
)

// answerPoliteHead are the words a person puts before a yes or a no ("por favor, léelo").
var answerPoliteHead = regexp.MustCompile(`^(?:(?:oye|hey|eh|milo|por favor|porfa|please|pues|bueno|bien|well|oh) )+`)

// answerHedge are words that turn a spoken yes into a no when they follow it ("sí, no, luego").
var answerHedge = regexp.MustCompile(`(?:^| )(?:no|luego|later|not|despues)(?: |$)`)

// MatchAnswer reads a short spoken answer to "do you want me to read it?". A refusal wins over a
// yes when both appear, and anything else is not an answer.
func MatchAnswer(text string) Answer {
	n := strings.TrimSpace(nonWord.ReplaceAllString(accentFold.Replace(strings.ToLower(text)), " "))
	n = strings.Join(strings.Fields(n), " ")
	n = strings.TrimSpace(answerPoliteHead.ReplaceAllString(n+" ", ""))
	switch {
	case n == "":
		return AnswerNone
	case noRe.MatchString(n):
		return AnswerNo
	case yesRe.MatchString(n):
		if answerHedge.MatchString(n) {
			return AnswerNo
		}
		return AnswerYes
	}
	return AnswerNone
}

// ErrNoListener means the question could not even be asked (no speech-to-text configured), so the
// caller can fall back to something that needs no answer.
var ErrNoListener = errors.New("assistant: cannot listen for an answer")

// Hold marks the assistant busy until the returned function is called, so music stays paused and the
// wake word does not start a conversation while something else is talking to the person.
func (e *Engine) Hold() (release func()) {
	e.holds.Add(1)
	var once sync.Once
	return func() { once.Do(func() { e.holds.Add(-1) }) }
}

// AskYesNo says prompt, listens for a few seconds and reports what was answered. Nothing is sent to
// the language model. heard is false when nobody answered or the answer was not a yes or a no.
func (e *Engine) AskYesNo(ctx context.Context, prompt string) (yes, heard bool, err error) {
	if e.Audio == nil {
		return false, false, ErrNoListener
	}
	s, err := e.store.GetGPTSettings(ctx)
	if err != nil {
		return false, false, err
	}
	stt, err := e.backends.STT(ctx, s)
	if err != nil {
		return false, false, ErrNoListener // before saying anything: the caller falls back to a sound
	}
	release := e.Hold()
	defer release()
	if err := e.say(ctx, prompt); err != nil {
		return false, false, err
	}
	e.note(ctx, audio.NotifyRecordStart)
	wav, err := e.Audio.Record(ctx, audio.RecordOptions{MaxDuration: 8 * time.Second, StopOnSilence: true, NoSpeechFor: 5 * time.Second})
	e.note(ctx, audio.NotifyRecordStop)
	if err != nil || ctx.Err() != nil {
		return false, false, err
	}
	if hs, ok := e.Audio.(interface{ HasSpeech(wav []byte) bool }); ok && !hs.HasSpeech(wav) {
		return false, false, nil // only the noise of the room: nobody is there
	}
	text, err := stt.Transcribe(ctx, wav)
	if err != nil {
		return false, false, nil
	}
	switch MatchAnswer(text) {
	case AnswerYes:
		return true, true, nil
	case AnswerNo:
		return false, true, nil
	}
	return false, false, nil
}
