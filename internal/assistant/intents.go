package assistant

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/audio"
	"github.com/juancer/milo/internal/moderation"
)

// Some requests are plain commands: "play some music", "next song", "turn it up". Sending them to a
// language model costs a round trip to the cloud, takes seconds and can go wrong; a rule that
// recognises the words does the same instantly and the same way every time. These rules run after
// speech recognition and before the model. Anything they do not recognise, or that the action
// refuses ("Spotify is not connected"), goes to the model as usual, which can explain in the
// child's language. The actions run as the assistant, so every switch, schedule and permission that
// applies to the model applies here too.

type intent struct {
	re   *regexp.Regexp
	tool string
	args func(m []string) map[string]any
	// music marks the requests that start sound, after which Milo does not keep listening.
	music bool
	// lang is the language of the words that matched; the spoken answer uses it.
	lang string
	// say words the answer to a question (the time, the weather) from what the action returned.
	say func(lang string, args, data map[string]any) (string, bool)
	// local answers without any action (a sum); the captured text is its input.
	local func(lang, text string) (string, bool)
}

// words used inside several patterns (the text is lower case, without accents or punctuation).
const (
	verbPlay  = `(?:pon|ponme|pone|poner|ponga|reproduce|reproduceme|quiero escuchar|quiero oir|play|put on|i want to hear|i would like to hear)`
	musicWord = `(?:musica|cancion|canciones|temas?|songs?|music|tunes)`
)

var intentRules = []intent{
	// A named playlist.
	{re: regexp.MustCompile(`^` + verbPlay + `(?: me)? (?:la |mi |my |the )?(?:playlist|lista de reproduccion|lista) (?:de |of |called |llamada )?(?P<q>[a-z0-9 ]{2,60})$`),
		tool: "spotify_play_playlist", args: func(m []string) map[string]any { return map[string]any{"name": m[0]} }, music: true},
	// Something, anything.
	{re: regexp.MustCompile(`^` + verbPlay + `(?: me)? (?:(?:un poco de |algo de |alguna |una |some |any |a little )?` + musicWord + `|algo|something|anything|cualquier cosa)$`),
		tool: "play_music", args: func([]string) map[string]any { return map[string]any{} }, music: true},
	{re: regexp.MustCompile(`^(?:lets|let s)(?: just)+ play(?: some music| music| something)?$`),
		tool: "play_music", args: func([]string) map[string]any { return map[string]any{} }, music: true},
	{re: regexp.MustCompile(`^(?:quiero|me apetece|necesito) (?:que suene |escuchar |oir )(?:algo de |un poco de )?(?:musica|algo)$`),
		tool: "play_music", args: func([]string) map[string]any { return map[string]any{} }, music: true},
	{re: regexp.MustCompile(`^(?:sigue|continua|continuar|reanuda|reanudar|resume|continue|play)$`),
		tool: "play_music", args: func([]string) map[string]any { return map[string]any{} }, music: true},
	// Music by someone ("pon musica de belinda", "play songs by queen", "pon algo de frozen").
	{re: regexp.MustCompile(`^` + verbPlay + `(?: me)? (?:(?:un poco de |algo de |alguna |una )?(?:la |las |el |los )?` + musicWord + `|something|some music) (?:de la |de los |de las |del |de |by |from |of )(?P<q>[a-z0-9 ]{2,60})$`),
		tool: "play_music", args: func(m []string) map[string]any { return map[string]any{"query": strings.TrimSpace(m[0])} }, music: true},
	// Stopping and skipping.
	{re: regexp.MustCompile(`^(?:pausa|pausar|pausa la musica|pause|pause the music|pause it)$`),
		tool: "pause_music", args: func([]string) map[string]any { return map[string]any{} }},
	{re: regexp.MustCompile(`^(?:para|parar|para la musica|parar la musica|detente|detener|stop|stop the music|stop it|apaga la musica|callate|silencio)$`),
		tool: "stop_music", args: func([]string) map[string]any { return map[string]any{} }},
	{re: regexp.MustCompile(`^(?:siguiente|siguiente cancion|la siguiente|otra|otra cancion|salta|saltar|salta esta|next|next song|skip|skip this|skip it)$`),
		tool: "next_song", args: func([]string) map[string]any { return map[string]any{} }},
	// Volume.
	{re: regexp.MustCompile(`^(?:(?:sube|subir|subele|aumenta|aumentar) (?:el )?(?:volumen|sonido|musica)|mas volumen|mas alto|turn it up|turn up(?: the volume)?|volume up|louder)$`),
		tool: "set_volume", args: func([]string) map[string]any { return map[string]any{"change": 10} }},
	{re: regexp.MustCompile(`^(?:(?:baja|bajar|bajale|reduce|reducir) (?:el )?(?:volumen|sonido|musica)|menos volumen|mas bajo|turn it down|turn down(?: the volume)?|volume down|quieter)$`),
		tool: "set_volume", args: func([]string) map[string]any { return map[string]any{"change": -10} }},
	{re: regexp.MustCompile(`^(?:volumen|volume|pon el volumen|pon volumen|volumen al|volume at|set the volume to|set volume to) (?:al |a |to )?(?P<n>10|[0-9])$`),
		tool: "set_volume", args: func(m []string) map[string]any {
			n := 0
			for _, c := range m[0] {
				n = n*10 + int(c-'0')
			}
			return map[string]any{"level_10": n}
		}},
	// Questions with a short answer: the time, the date, the weather, a sum.
	{re: regexp.MustCompile(`^(?:que hora es|que hora son|que horas son|dime la hora|dime que hora es|me dices la hora|la hora|que hora tenemos)$`),
		tool: "get_time", args: noArgs, lang: langES, say: sayClock(false)},
	{re: regexp.MustCompile(`^(?:what time is it|what s the time|what is the time|tell me the time|do you know the time)$`),
		tool: "get_time", args: noArgs, lang: langEN, say: sayClock(false)},
	{re: regexp.MustCompile(`^(?:que dia es hoy|que dia es|a que dia estamos|que fecha es hoy|que fecha es|dime la fecha|dime que dia es hoy)$`),
		tool: "get_time", args: noArgs, lang: langES, say: sayClock(true)},
	{re: regexp.MustCompile(`^(?:what day is it|what day is it today|what s the date|what is the date|what is today s date|what s today s date|what is the date today)$`),
		tool: "get_time", args: noArgs, lang: langEN, say: sayClock(true)},
	{re: regexp.MustCompile(`^(?:dime el clima|dime el tiempo|que tiempo hace|que clima hace|como esta el tiempo|como esta el clima|el clima|el tiempo|que tiempo va a hacer|dime el clima de hoy|dime el tiempo de hoy|que tiempo hace hoy|que clima hace hoy)$`),
		tool: "get_weather", args: dayArgs("today", false), lang: langES, say: sayForecast},
	{re: regexp.MustCompile(`^(?:dime el clima de manana|dime el tiempo de manana|que tiempo hace manana|que clima hace manana|que tiempo va a hacer manana|como va a estar el tiempo manana)$`),
		tool: "get_weather", args: dayArgs("tomorrow", false), lang: langES, say: sayForecast},
	{re: regexp.MustCompile(`^(?:va a llover|llovera|hay probabilidad de lluvia)(?: (?P<d>hoy|manana))?$`),
		tool: "get_weather", args: func(m []string) map[string]any {
			return dayArgs(map[bool]string{true: "tomorrow", false: "today"}[m[0] == "manana"], true)(nil)
		}, lang: langES, say: sayForecast},
	{re: regexp.MustCompile(`^(?:what s the weather|what is the weather|how s the weather|tell me the weather|weather|what s the weather like|what is the weather like)(?: like)?(?: today)?$`),
		tool: "get_weather", args: dayArgs("today", false), lang: langEN, say: sayForecast},
	{re: regexp.MustCompile(`^(?:what s the weather|what is the weather|how s the weather|tell me the weather|weather)(?: like)? tomorrow$`),
		tool: "get_weather", args: dayArgs("tomorrow", false), lang: langEN, say: sayForecast},
	{re: regexp.MustCompile(`^(?:is it going to rain|will it rain)(?: (?P<d>today|tomorrow))?$`),
		tool: "get_weather", args: func(m []string) map[string]any {
			return dayArgs(map[bool]string{true: "tomorrow", false: "today"}[m[0] == "tomorrow"], true)(nil)
		}, lang: langEN, say: sayForecast},
	{re: regexp.MustCompile(`^(?:cuanto es|cuanto son|cuanto da|cuanto suman|cuanto sale|cuanto vale|calcula|dime cuanto es|dime cuanto son)(?: la suma de| el resultado de)? (?P<e>.+)$`),
		args: exprArgs, lang: langES, local: answerCalc},
	{re: regexp.MustCompile(`^(?:how much is|what is|what s|calculate|tell me how much is|whats) (?P<e>.+)$`),
		args: exprArgs, lang: langEN, local: answerCalc},
	{re: regexp.MustCompile(`^(?P<e>[0-9]+(?:dec[0-9]+)? [a-z ]*?[0-9]+(?:dec[0-9]+)?.*)$`),
		args: exprArgs, lang: langES, local: answerCalc},
	{re: regexp.MustCompile(`^(?P<e>[0-9]+(?:dec[0-9]+)? [a-z ]*?[0-9]+(?:dec[0-9]+)?.*)$`),
		args: exprArgs, lang: langEN, local: answerCalc},
}

func noArgs([]string) map[string]any { return map[string]any{} }

func exprArgs(m []string) map[string]any { return map[string]any{"expr": strings.TrimSpace(m[0])} }

func dayArgs(day string, rain bool) func([]string) map[string]any {
	return func([]string) map[string]any {
		a := map[string]any{"day": day}
		if rain {
			a["rain"] = true
		}
		return a
	}
}

func sayClock(date bool) func(lang string, args, data map[string]any) (string, bool) {
	return func(lang string, _, data map[string]any) (string, bool) {
		c, ok := clockFrom(data)
		if !ok {
			return "", false
		}
		if date {
			return sayDate(lang, c), true
		}
		return sayTime(lang, c), true
	}
}

func sayForecast(lang string, args, data map[string]any) (string, bool) {
	r, ok := reportFrom(data)
	if !ok {
		return "", false
	}
	rainOnly, _ := args["rain"].(bool)
	return sayWeather(lang, r, rainOnly), true
}

var (
	accentFold  = strings.NewReplacer("á", "a", "à", "a", "ä", "a", "é", "e", "è", "e", "ë", "e", "í", "i", "ï", "i", "ó", "o", "ò", "o", "ö", "o", "ú", "u", "ü", "u", "ñ", "n", "ç", "c", "'", " ", "’", " ")
	nonWord     = regexp.MustCompile(`[^a-z0-9]+`)
	decimalMark = regexp.MustCompile(`([0-9])[.,]([0-9])`)
	timesX      = regexp.MustCompile(`([0-9]) ?x ?([0-9])`)
	minusMark   = regexp.MustCompile(`([0-9]) ?- ?([0-9])`)
	fillerHead  = regexp.MustCompile(`^(?:(?:oye|hey|eh|ok|okay|vale|venga|milo|por favor|porfa|please|puedes|podrias|puedes poner|me puedes|quiero que|ahora|ya|and|y) )+`)
	fillerTail  = regexp.MustCompile(`(?: (?:por favor|porfa|please|ahora|ya|milo|gracias|thanks))+$`)
)

// normalizeUtterance lower-cases, removes accents and punctuation and drops polite filler.
func normalizeUtterance(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = accentFold.Replace(s)
	// Symbols of a sum would be dropped as punctuation: turn them into words first.
	s = decimalMark.ReplaceAllString(s, "${1}dec${2}")
	s = timesX.ReplaceAllString(s, "${1} star ${2}")
	s = minusMark.ReplaceAllString(s, "${1} minusop ${2}")
	s = strings.NewReplacer("*", " star ", "×", " star ", "+", " plusop ", "÷", " slashop ", "/", " slashop ").Replace(s)
	s = strings.TrimSpace(nonWord.ReplaceAllString(s, " "))
	s = strings.TrimSpace(fillerHead.ReplaceAllString(s+" ", ""))
	s = strings.TrimSpace(fillerTail.ReplaceAllString(s, ""))
	return s
}

// matchIntent returns the tool and arguments a request maps to, if it is one of the known commands.
func matchIntent(text string) (it intent, args map[string]any, ok bool) {
	it, args, _, ok = matchIntentAnswer(text)
	return it, args, ok
}

// matchIntentAnswer is matchIntent, and also returns the answer of a rule that needs no action.
func matchIntentAnswer(text string) (it intent, args map[string]any, answer string, ok bool) {
	n := normalizeUtterance(text)
	if n == "" || len(n) > 120 {
		return intent{}, nil, "", false
	}
	for _, r := range intentRules {
		m := r.re.FindStringSubmatch(n)
		if m == nil {
			continue
		}
		a := r.args(m[1:])
		if r.local != nil {
			ans, good := r.local(r.lang, m[1])
			if !good {
				continue // not a sum after all: try the next rule, or leave it to the model
			}
			return r, a, ans, true
		}
		return r, a, "", true
	}
	return intent{}, nil, "", false
}

// tryIntent carries out a recognised command without the model and reports whether it did. It does
// nothing when the request is not a known command, is blocked by the moderation rules, or when the
// action refuses; the model then gets the request as usual.
func (e *Engine) tryIntent(ctx context.Context, sess *Session, text string) bool {
	it, args, answer, ok := matchIntentAnswer(text)
	if !ok {
		return false
	}
	if sess.settings.InputModeration {
		if rules, err := e.store.ListModerationRules(ctx); err == nil {
			if moderation.New(toRules(rules)).Check(text, moderation.Input).Blocked {
				return false // the normal path answers blocked input
			}
		}
	}
	var res actions.Result
	if it.tool != "" {
		var err error
		res, err = e.actions.Execute(ctx, actions.Actor{Kind: actions.ActorAssistant}, it.tool, args)
		if err != nil || res.Refused {
			e.log.Info("assistant: command left to the model", "tool", it.tool, "refused", res.Refused, "error", err)
			return false
		}
		if it.say != nil {
			var ok bool
			if answer, ok = it.say(it.lang, args, res.Data); !ok {
				e.log.Info("assistant: could not word the answer; left to the model", "tool", it.tool)
				return false
			}
		}
	}
	e.log.Info("assistant: done without the model", "tool", it.tool, "answer", answer != "")
	e.record(ctx, sess, "user", text, "")
	if it.tool != "" {
		out, _ := json.Marshal(map[string]any{"ok": true, "result": res.Message})
		e.record(ctx, sess, "tool", string(out), it.tool)
	}
	switch {
	case answer != "":
		e.record(ctx, sess, "assistant", answer, "")
		e.say(ctx, answer) //nolint:errcheck // say already plays a tone when it cannot speak
	case it.tool != "set_volume": // set_volume already beeps
		e.note(ctx, audio.NotifyOK)
	}
	if it.music {
		sess.finished = true // sound is on: do not keep a microphone open over it
	}
	return true
}
