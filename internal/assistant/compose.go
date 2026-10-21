package assistant

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/moderation"
	"github.com/juancer/milo/internal/store"
)

// "Hey Milo, send a message to Dad" with no message in it: Milo asks whether it is text or a voice note
// ("de texto o de voz"), listens, and sends it. This is how a child with no buttons writes to a relative.
// A request that already holds the message ("dile a papi que ya voy") is not matched here: it goes to the
// model, which calls send_message.

const (
	composeAnswerFor  = 8 * time.Second  // how long to wait for "texto" or "voz"
	composeMessageMax = 30 * time.Second // the longest dictated text message
)

var (
	composeVoiceWords = regexp.MustCompile(`\b(?:voz|audio|nota|notas|hablar|hablando|voice|speak|talk|recording)\b`)
	composeTextWords  = regexp.MustCompile(`\b(?:texto|escrito|escribir|escribe|escribele|teclear|text|written|write|type|typed)\b`)
)

type composeKind int

const (
	composeUnknown composeKind = iota
	composeText
	composeVoice
)

// whichKind tells "voice" from "text" in what was said or asked. Voice wins when both words appear.
func whichKind(said string) composeKind {
	n := normalizeUtterance(said)
	switch {
	case composeVoiceWords.MatchString(n):
		return composeVoice
	case composeTextWords.MatchString(n):
		return composeText
	}
	return composeUnknown
}

// foldName lowercases and removes accents and punctuation, so "Papá" and "papa" are the same name.
func foldName(s string) string {
	s = accentFold.Replace(strings.ToLower(strings.TrimSpace(s)))
	return strings.Join(strings.Fields(nonWord.ReplaceAllString(s, " ")), " ")
}

// pickChat returns the enabled chat that the spoken name refers to. An empty name picks the only chat there
// is. ok is false when nothing or several chats fit.
func pickChat(chats []store.Chat, said string) (store.Chat, bool) {
	var enabled []store.Chat
	for _, c := range chats {
		if c.Enabled {
			enabled = append(enabled, c)
		}
	}
	want := foldName(said)
	for _, art := range []string{"mi ", "my ", "a ", "al ", "to "} {
		want = strings.TrimPrefix(want, art)
	}
	if want == "" {
		if len(enabled) == 1 {
			return enabled[0], true
		}
		return store.Chat{}, false
	}
	var exact, partial []store.Chat
	for _, c := range enabled {
		n := foldName(c.Name)
		switch {
		case n == want:
			exact = append(exact, c)
		case strings.Contains(n, want) || strings.Contains(want, n):
			partial = append(partial, c)
		}
	}
	if len(exact) == 1 {
		return exact[0], true
	}
	if len(exact) == 0 && len(partial) == 1 {
		return partial[0], true
	}
	return store.Chat{}, false
}

func chatNames(chats []store.Chat) string {
	var names []string
	for _, c := range chats {
		if c.Enabled {
			names = append(names, c.Name)
		}
	}
	return strings.Join(names, ", ")
}

type composeWords struct {
	who, kind, kindAgain, whatText, afterBeep, sentText, sentVoice, notHeard, notFound, notSent, blocked string
}

func composeSpeech(lang, name string) composeWords {
	if lang == langES {
		return composeWords{
			who:       "¿A quién se lo mando?",
			kind:      "¿Un mensaje de texto o de voz para " + name + "?",
			kindAgain: "Perdona, ¿de texto o de voz?",
			whatText:  "Dime qué quieres decirle.",
			afterBeep: "Di tu mensaje después del pitido.",
			sentText:  "Listo, mensaje enviado a " + name + ".",
			sentVoice: "Listo, nota de voz enviada a " + name + ".",
			notHeard:  "No te he oído. Prueba otra vez cuando quieras.",
			notFound:  "No encuentro a ese contacto.",
			notSent:   "No he podido enviarlo.",
			blocked:   "Ese mensaje no se puede enviar.",
		}
	}
	return composeWords{
		who:       "Who should I send it to?",
		kind:      "A text or a voice message for " + name + "?",
		kindAgain: "Sorry, text or voice?",
		whatText:  "Tell me what you want to say.",
		afterBeep: "Say your message after the beep.",
		sentText:  "Done, message sent to " + name + ".",
		sentVoice: "Done, voice message sent to " + name + ".",
		notHeard:  "I did not hear you. Try again whenever you like.",
		notFound:  "I cannot find that contact.",
		notSent:   "I could not send it.",
		blocked:   "That message cannot be sent.",
	}
}

// compose runs the exchange. said is the whole request (it may already say "voice" or "text"); who is the
// name that followed "to" or "a", possibly empty. It always finishes the conversation.
func (e *Engine) compose(ctx context.Context, sess *Session, said, who, lang string) {
	e.record(ctx, sess, "user", said, "")
	sess.finished = true
	finish := func(text string) {
		e.record(ctx, sess, "assistant", text, "")
		e.say(ctx, text) //nolint:errcheck // say already plays a tone when it cannot speak
	}
	chats, err := e.store.ListChats(ctx)
	if err != nil {
		finish(composeSpeech(lang, "").notSent)
		return
	}
	w := composeSpeech(lang, "")
	chat, ok := pickChat(chats, who)
	if !ok && strings.TrimSpace(who) == "" && chatNames(chats) != "" {
		// Several people and none named: ask.
		answer, heard, err := e.listenText(ctx, w.who, composeAnswerFor, 5*time.Second)
		if err != nil || !heard {
			finish(w.notHeard)
			return
		}
		chat, ok = pickChat(chats, answer)
	}
	if !ok {
		finish(w.notFound)
		return
	}
	w = composeSpeech(lang, chat.Name)

	kind := whichKind(said)
	for try := 0; kind == composeUnknown && try < 2; try++ {
		prompt := w.kind
		if try == 1 {
			prompt = w.kindAgain
		}
		answer, heard, err := e.listenText(ctx, prompt, composeAnswerFor, 5*time.Second)
		if err != nil || !heard {
			finish(w.notHeard)
			return
		}
		kind = whichKind(answer)
	}
	switch kind {
	case composeVoice:
		e.say(ctx, w.afterBeep) //nolint:errcheck
		res, err := e.actions.Execute(ctx, actions.Actor{Kind: actions.ActorDevice}, "send_voice_message", map[string]any{"chat": chat.Name})
		switch {
		case err != nil || (res.Refused && res.Data["reason"] != "nothing_recorded"):
			finish(w.notSent)
		case res.Refused:
			finish(w.notHeard)
		default:
			finish(w.sentVoice)
		}
	case composeText:
		text, heard, err := e.listenText(ctx, w.whatText, composeMessageMax, 6*time.Second)
		if err != nil || !heard {
			finish(w.notHeard)
			return
		}
		if sess.settings.InputModeration {
			if rules, err := e.store.ListModerationRules(ctx); err == nil && moderation.New(toRules(rules)).Check(text, moderation.Input).Blocked {
				finish(w.blocked)
				return
			}
		}
		res, err := e.actions.Execute(ctx, actions.Actor{Kind: actions.ActorDevice}, "send_message", map[string]any{"chat": chat.Name, "text": strings.TrimSpace(text)})
		if err != nil || res.Refused {
			finish(w.notSent)
			return
		}
		finish(w.sentText)
	default:
		finish(w.notHeard)
	}
}
