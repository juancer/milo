package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	// valid rejects a match whose captured words turn out not to mean anything (a duration that does not parse).
	valid func(m []string) bool
	// silent: nothing is said or played afterwards (goodbye, "be quiet").
	silent bool
	// device runs the action as the person at the device (not offered to the model) instead of as the assistant.
	device bool
	// repeat says again what was said last.
	repeat bool
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
	// "No escuches": the wake word is switched off until a button is held.
	{re: regexp.MustCompile(`^(?:milo )?(?:no escuches|no me escuches|no escuches mas|deja de escuchar|dejar de escuchar|para de escuchar|no sigas escuchando|silencia el microfono|apaga el microfono|desactiva el microfono)$`),
		tool: "listening_off", args: noArgs, lang: langES,
		say: func(string, map[string]any, map[string]any) (string, bool) {
			return "Vale, dejo de escuchar. Para activarme otra vez, mantén pulsado el botón de hablar.", true
		}},
	{re: regexp.MustCompile(`^(?:milo )?(?:stop listening|do not listen|do not listen to me|dont listen|don t listen|do not listen anymore|mute the microphone|mute your microphone|turn off the microphone)$`),
		tool: "listening_off", args: noArgs, lang: langEN,
		say: func(string, map[string]any, map[string]any) (string, bool) {
			return "Okay, I stop listening. To wake me up again, hold the talk button.", true
		}},
	// Stopping and skipping.
	{re: regexp.MustCompile(`^(?:pausa|pausar|pausa la musica|pause|pause the music|pause it)$`),
		tool: "pause_music", args: func([]string) map[string]any { return map[string]any{} }},
	{re: regexp.MustCompile(`^(?:apaga|apagate|apaga todo|apagalo|para todo|parar todo|para|parar|detente|detener|alto|para la musica|parar la musica|apaga la musica|quita la musica)$`),
		tool: "stop_audio", args: noArgs, lang: langES, silent: true},
	{re: regexp.MustCompile(`^(?:stop|stop everything|stop it|stop the music|turn it off|turn off|switch it off|power off)$`),
		tool: "stop_audio", args: noArgs, lang: langEN, silent: true},
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
	// The end of the conversation, said or asked for: silence, no answer, no new question.
	{re: regexp.MustCompile(`^(?:callate|calla|silencio|cierra la boca|para de hablar|deja de hablar|basta|ya basta|ya esta|apagate|no hables mas)$`),
		tool: "stop_audio", args: noArgs, lang: langES, silent: true},
	{re: regexp.MustCompile(`^(?:shut up|be quiet|quiet|silence|stop talking|enough|that s enough|that is enough)$`),
		tool: "stop_audio", args: noArgs, lang: langEN, silent: true},
	{re: regexp.MustCompile(`^(?:adios|hasta luego|hasta manana|hasta pronto|chao|chau|nos vemos|buenas noches|me voy|gracias adios|gracias y adios|muchas gracias|gracias|vale gracias|ok gracias)$`),
		args: noArgs, lang: langES, silent: true, local: func(string, string) (string, bool) { return "", true }},
	{re: regexp.MustCompile(`^(?:bye|goodbye|good bye|see you|see you later|see you tomorrow|good night|thanks bye|thank you bye|thanks|thank you|thank you very much)$`),
		args: noArgs, lang: langEN, silent: true, local: func(string, string) (string, bool) { return "", true }},
	// Timers.
	{re: regexp.MustCompile(`^(?:pon|ponme|programa|activa|inicia|empieza|crea)(?: un| una| el)? (?:temporizador|cronometro|timer|alarma|cuenta atras)(?: de| para| a| en)? (?P<d>.+)$`),
		tool: "set_timer", args: timerArgs(langES), lang: langES, valid: durationOK(langES), say: sayTimer},
	{re: regexp.MustCompile(`^(?:avisame|despiertame|recuerdame)(?: dentro de| en) (?P<d>.+)$`),
		tool: "set_timer", args: timerArgs(langES), lang: langES, valid: durationOK(langES), say: sayTimer},
	{re: regexp.MustCompile(`^(?:temporizador|timer|alarma)(?: de| para)? (?P<d>.+)$`),
		tool: "set_timer", args: timerArgs(langES), lang: langES, valid: durationOK(langES), say: sayTimer},
	{re: regexp.MustCompile(`^(?:set|start|put|create)(?: a| an| the)? (?:timer|alarm|countdown)(?: for| of)? (?P<d>.+)$`),
		tool: "set_timer", args: timerArgs(langEN), lang: langEN, valid: durationOK(langEN), say: sayTimer},
	{re: regexp.MustCompile(`^(?:timer|alarm)(?: for)? (?P<d>.+)$`),
		tool: "set_timer", args: timerArgs(langEN), lang: langEN, valid: durationOK(langEN), say: sayTimer},
	{re: regexp.MustCompile(`^(?:cancela|borra|quita|anula|para|apaga)(?: el| los| todos los)? (?:temporizador|temporizadores|timer|timers|alarma|alarmas|cronometro)$`),
		tool: "cancel_timer", args: noArgs, lang: langES, say: func(lang string, _, _ map[string]any) (string, bool) { return "Temporizadores cancelados.", true }},
	{re: regexp.MustCompile(`^(?:cancel|stop|delete|clear)(?: the| all| all the)? (?:timer|timers|alarm|alarms)$`),
		tool: "cancel_timer", args: noArgs, lang: langEN, say: func(lang string, _, _ map[string]any) (string, bool) { return "Timers cancelled.", true }},
	// What is happening, and a few questions about the device.
	{re: regexp.MustCompile(`^(?:que esta sonando|que suena|que cancion es esta|que cancion es|que estoy escuchando|que estas poniendo|que musica es esta)$`),
		tool: "now_playing", args: noArgs, lang: langES, say: sayNowPlaying},
	{re: regexp.MustCompile(`^(?:what s playing|what is playing|what song is this|what s this song|what am i listening to)$`),
		tool: "now_playing", args: noArgs, lang: langEN, say: sayNowPlaying},
	{re: regexp.MustCompile(`^(?:que volumen tengo|que volumen hay|que volumen tienes|cuanto volumen hay|a que volumen estamos|cual es el volumen)$`),
		tool: "get_device_status", args: noArgs, lang: langES, say: sayVolume},
	{re: regexp.MustCompile(`^(?:what s the volume|what is the volume|how loud is it|what volume is it at)$`),
		tool: "get_device_status", args: noArgs, lang: langEN, say: sayVolume},
	{re: regexp.MustCompile(`^(?:pon el volumen|pon volumen|volumen|sube el volumen|ponlo|pon la musica)?(?: a| al)? ?(?:maximo|tope|el maximo)$`),
		tool: "set_volume", args: func([]string) map[string]any { return map[string]any{"level": 100} }, lang: langES},
	{re: regexp.MustCompile(`^(?:pon el volumen|pon volumen|volumen|baja el volumen|ponlo)(?: a| al)? ?(?:minimo|el minimo|lo minimo)$`),
		tool: "set_volume", args: func([]string) map[string]any { return map[string]any{"level_10": 1} }, lang: langES},
	{re: regexp.MustCompile(`^(?:silencia|silenciar|mute|quita el sonido|sin sonido)$`),
		tool: "set_volume", args: func([]string) map[string]any { return map[string]any{"level": 0} }, lang: langES, silent: true},
	{re: regexp.MustCompile(`^(?:volume|set the volume|set volume|turn it|put it)?(?: to| up to| at)? ?(?:max|maximum|full|the max|full volume)$`),
		tool: "set_volume", args: func([]string) map[string]any { return map[string]any{"level": 100} }, lang: langEN},
	{re: regexp.MustCompile(`^(?:volume|set the volume|set volume|turn it|put it)?(?: to| down to| at)? ?(?:min|minimum|lowest|the minimum)$`),
		tool: "set_volume", args: func([]string) map[string]any { return map[string]any{"level_10": 1} }, lang: langEN},
	{re: regexp.MustCompile(`^(?:cuanto tiempo llevas encendido|cuanto llevas encendido|desde cuando estas encendido|cuanto tiempo llevas funcionando)$`),
		tool: "get_uptime", args: noArgs, lang: langES, device: true, say: sayUptime},
	{re: regexp.MustCompile(`^(?:how long have you been on|how long have you been running|what s your uptime)$`),
		tool: "get_uptime", args: noArgs, lang: langEN, device: true, say: sayUptime},
	{re: regexp.MustCompile(`^(?:que ip tienes|cual es tu ip|dime tu ip|dime la ip|cual es tu direccion)$`),
		tool: "get_ip", args: noArgs, lang: langES, device: true, say: sayIP},
	{re: regexp.MustCompile(`^(?:what is your ip|what s your ip|what is your ip address|what s your ip address|tell me your ip|tell me your ip address)$`),
		tool: "get_ip", args: noArgs, lang: langEN, device: true, say: sayIP},
	{re: regexp.MustCompile(`^(?:tengo mensajes|hay mensajes|tengo algun mensaje|hay algun mensaje|hay mensajes nuevos|tengo mensajes nuevos|cuantos mensajes tengo|cuantos mensajes hay)$`),
		tool: "get_pending_messages", args: noArgs, lang: langES, say: sayPending},
	{re: regexp.MustCompile(`^(?:do i have messages|do i have any messages|any messages|how many messages do i have|do i have new messages)$`),
		tool: "get_pending_messages", args: noArgs, lang: langEN, say: sayPending},
	{re: regexp.MustCompile(`^(?:leeme los mensajes|lee los mensajes|lee mis mensajes|reproduce los mensajes|pon los mensajes|dime los mensajes|leeme mis mensajes)$`),
		tool: "read_messages", args: noArgs, lang: langES, silent: true},
	{re: regexp.MustCompile(`^(?:play my messages|read my messages|play the messages|read the messages|read me my messages)$`),
		tool: "read_messages", args: noArgs, lang: langEN, silent: true},
	{re: regexp.MustCompile(`^(?:repite|repitelo|repite eso|otra vez|dilo otra vez|que has dicho|que dijiste|no te he oido|no te oi)$`),
		args: noArgs, lang: langES, repeat: true},
	{re: regexp.MustCompile(`^(?:repeat|repeat that|say that again|say it again|what did you say|again please|once more)$`),
		args: noArgs, lang: langEN, repeat: true},
	// Games for the child: nothing to ask a model.
	{re: regexp.MustCompile(`^(?:tira|lanza|echa)(?: un| el)? dado$`), args: noArgs, lang: langES, local: rollDie},
	{re: regexp.MustCompile(`^roll(?: a| the)? (?:die|dice)$`), args: noArgs, lang: langEN, local: rollDie},
	{re: regexp.MustCompile(`^(?:cara o cruz|tira una moneda|lanza una moneda|echa una moneda|echa cara o cruz)$`), args: noArgs, lang: langES, local: flipCoin},
	{re: regexp.MustCompile(`^(?:flip a coin|toss a coin|heads or tails)$`), args: noArgs, lang: langEN, local: flipCoin},
	{re: regexp.MustCompile(`^(?:dime|elige|piensa)(?: un)? numero (?:del|de|entre) (?P<a>[0-9]+) (?:al|a|y) (?P<b>[0-9]+)$`), args: noArgs, lang: langES, local: pickNumber},
	{re: regexp.MustCompile(`^(?:pick|choose|say)(?: me)?(?: a| me a)? number (?:from|between) (?P<a>[0-9]+) (?:to|and) (?P<b>[0-9]+)$`), args: noArgs, lang: langEN, local: pickNumber},
	// What is listened to most.
	{re: regexp.MustCompile(`^(?:pon|ponme|reproduce|quiero escuchar)(?: algo de)? (?:lo que mas escucho|lo que mas escuchamos|mi musica|mis favoritos|mis favoritas|lo de siempre|mis artistas favoritos|lo que suelo escuchar|mi musica favorita|lo que mas oigo)$`),
		tool: "play_music", args: func([]string) map[string]any { return map[string]any{"favorites": true} }, lang: langES, music: true},
	{re: regexp.MustCompile(`^(?:play|put on)(?: some of)? (?:my favorites|my favourites|my favorite music|my favourite music|the usual|what i listen to most|my usual music|my music)$`),
		tool: "play_music", args: func([]string) map[string]any { return map[string]any{"favorites": true} }, lang: langEN, music: true},
	{re: regexp.MustCompile(`^(?:cuanto es|cuanto son|cuanto da|cuanto suman|cuanto sale|cuanto vale|calcula|dime cuanto es|dime cuanto son)(?: la suma de| el resultado de)? (?P<e>.+)$`),
		args: exprArgs, lang: langES, local: answerCalc},
	{re: regexp.MustCompile(`^(?:how much is|what is|what s|calculate|tell me how much is|whats) (?P<e>.+)$`),
		args: exprArgs, lang: langEN, local: answerCalc},
	{re: regexp.MustCompile(`^(?P<e>[0-9]+(?:dec[0-9]+)? [a-z ]*?[0-9]+(?:dec[0-9]+)?.*)$`),
		args: exprArgs, lang: langES, local: answerCalc},
	{re: regexp.MustCompile(`^(?P<e>[0-9]+(?:dec[0-9]+)? [a-z ]*?[0-9]+(?:dec[0-9]+)?.*)$`),
		args: exprArgs, lang: langEN, local: answerCalc},
	// The last resort: "pon Camilo" plays that artist. Only a name that cannot be anything else counts
	// ("pon la mesa" or "pon la tele" do not), and Spotify's answer has to match it (see artistMatches).
	{re: regexp.MustCompile(`^(?:pon|ponme|pone|reproduce|play|put on|quiero escuchar|i want to hear)(?: me)?(?: a)? (?P<q>[a-z0-9]+(?: [a-z0-9]+){0,3})$`),
		tool: "play_music", args: func(m []string) map[string]any { return map[string]any{"query": strings.TrimSpace(m[0])} }, music: true,
		valid: looksLikeAName},
}

// randInt picks a number in [0, n); tests replace it.
var randInt = rand.Intn

func rollDie(lang, _ string) (string, bool) {
	n := randInt(6) + 1
	if lang == langEN {
		return fmt.Sprintf("It's a %d.", n), true
	}
	return fmt.Sprintf("Ha salido un %d.", n), true
}

func flipCoin(lang, _ string) (string, bool) {
	heads := randInt(2) == 0
	switch {
	case lang == langEN && heads:
		return "Heads.", true
	case lang == langEN:
		return "Tails.", true
	case heads:
		return "Cara.", true
	}
	return "Cruz.", true
}

func pickNumber(lang, text string) (string, bool) {
	f := strings.Fields(text)
	if len(f) != 2 {
		return "", false
	}
	lo, err1 := strconv.Atoi(f[0])
	hi, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || lo > hi || hi-lo > 1_000_000 {
		return "", false
	}
	n := lo + randInt(hi-lo+1)
	if lang == langEN {
		return fmt.Sprintf("%d.", n), true
	}
	return fmt.Sprintf("El %d.", n), true
}

func timerArgs(lang string) func([]string) map[string]any {
	return func(m []string) map[string]any {
		secs, _, _ := parseDuration(lang, m[0])
		return map[string]any{"seconds": secs}
	}
}

func durationOK(lang string) func([]string) bool {
	return func(m []string) bool { _, _, ok := parseDuration(lang, m[0]); return ok }
}

func sayTimer(lang string, args, _ map[string]any) (string, bool) {
	secs, ok := args["seconds"].(int)
	if !ok {
		return "", false
	}
	if lang == langEN {
		return "Timer for " + sayDuration(lang, secs) + ".", true
	}
	return "Temporizador de " + sayDuration(lang, secs) + ".", true
}

func sayNowPlaying(lang string, _, data map[string]any) (string, bool) {
	playing, _ := data["playing"].(bool)
	track, _ := data["track"].(string)
	switch {
	case playing && track != "" && lang == langEN:
		return "Playing " + track + ".", true
	case playing && track != "":
		return "Suena " + track + ".", true
	case lang == langEN:
		return "Nothing is playing.", true
	}
	return "No está sonando nada.", true
}

func intOf(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	}
	return 0, false
}

func sayVolume(lang string, _, data map[string]any) (string, bool) {
	level, ok := intOf(data["volume"])
	if !ok {
		return "", false
	}
	if lang == langEN {
		return fmt.Sprintf("The volume is at %d out of 10.", (level+5)/10), true
	}
	return fmt.Sprintf("El volumen está en %d de 10.", (level+5)/10), true
}

func sayUptime(lang string, _, data map[string]any) (string, bool) {
	secs, ok := intOf(data["seconds"])
	if !ok || secs < 0 {
		return "", false
	}
	d := ""
	switch days := secs / 86400; {
	case days > 0 && lang == langEN:
		d = fmt.Sprintf("%d day", days)
		if days > 1 {
			d += "s"
		}
		secs %= 86400
		if h := sayDuration(lang, secs/3600*3600); secs >= 3600 {
			d += " and " + h
		}
		return "I have been on for " + d + ".", true
	case days > 0:
		d = fmt.Sprintf("%d día", days)
		if days > 1 {
			d += "s"
		}
		secs %= 86400
		if h := sayDuration(lang, secs/3600*3600); secs >= 3600 {
			d += " y " + h
		}
		return "Llevo encendido " + d + ".", true
	}
	if secs < 60 {
		secs = 60
	}
	dur := sayDuration(lang, secs/60*60)
	if lang == langEN {
		return "I have been on for " + dur + ".", true
	}
	return "Llevo encendido " + dur + ".", true
}

func sayIP(lang string, _, data map[string]any) (string, bool) {
	ip, _ := data["ip"].(string)
	if ip == "" {
		return "", false
	}
	spoken := strings.ReplaceAll(ip, ".", " punto ")
	if lang == langEN {
		return "My address is " + strings.ReplaceAll(ip, ".", " dot ") + ".", true
	}
	return "Mi dirección es " + spoken + ".", true
}

func sayPending(lang string, _, data map[string]any) (string, bool) {
	n, ok := intOf(data["pending"])
	if !ok {
		return "", false
	}
	urgent, _ := intOf(data["urgent"])
	var msg string
	switch {
	case lang == langEN && n == 0:
		msg = "You have no new messages."
	case lang == langEN && n == 1:
		msg = "You have 1 new message."
	case lang == langEN:
		msg = fmt.Sprintf("You have %d new messages.", n)
	case n == 0:
		msg = "No tienes mensajes nuevos."
	case n == 1:
		msg = "Tienes 1 mensaje nuevo."
	default:
		msg = fmt.Sprintf("Tienes %d mensajes nuevos.", n)
	}
	if urgent > 0 && lang == langEN {
		msg += fmt.Sprintf(" %d of them urgent.", urgent)
	} else if urgent > 0 {
		msg += fmt.Sprintf(" %d urgentes.", urgent)
	}
	return msg, true
}

// notNames are the first words of requests that are not an artist: determiners and pronouns, words for
// music itself, and things in the house.
var notNames = map[string]bool{
	"el": true, "la": true, "los": true, "las": true, "un": true, "una": true, "unos": true, "unas": true, "mi": true, "mis": true,
	"tu": true, "tus": true, "su": true, "sus": true, "lo": true, "le": true, "me": true, "te": true, "se": true, "nos": true,
	"algo": true, "alguna": true, "algun": true, "esto": true, "eso": true, "esa": true, "ese": true, "esta": true, "este": true,
	"the": true, "a": true, "an": true, "my": true, "your": true, "some": true, "any": true, "something": true, "anything": true,
	"this": true, "that": true, "it": true,
	"musica": true, "music": true, "cancion": true, "canciones": true, "song": true, "songs": true, "tema": true, "temas": true,
	"volumen": true, "volume": true, "luz": true, "luces": true, "tele": true, "television": true, "radio": true, "ventilador": true,
	"calefaccion": true, "agua": true, "cena": true, "comida": true, "lavadora": true, "mesa": true, "ropa": true,
	"playlist": true, "lista": true, "alarma": true, "temporizador": true, "timer": true, "dado": true, "dados": true,
	// words that start a sentence, not a name
	"with": true, "for": true, "to": true, "in": true, "at": true, "on": true, "up": true, "down": true, "off": true, "back": true,
	"con": true, "para": true, "por": true, "en": true, "de": true, "del": true, "hasta": true, "ya": true, "ahora": true,
}

// looksLikeAName reports whether the words after "pon" could be an artist's name.
func looksLikeAName(m []string) bool {
	f := strings.Fields(m[0])
	if len(f) == 0 || notNames[f[0]] || len(m[0]) < 2 || !regexp.MustCompile(`[a-z]`).MatchString(m[0]) {
		return false
	}
	for _, w := range f { // numbers are quantities ("pon cinco"), not part of a name
		if _, isNumber := numberWordES[w]; isNumber || digitsRe.MatchString(w) {
			return false
		}
		if _, isNumber := numberWordEN[w]; isNumber {
			return false
		}
	}
	return true
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
		if r.valid != nil && !r.valid(m[1:]) {
			continue
		}
		a := r.args(m[1:])
		if r.local != nil {
			ans, good := r.local(r.lang, strings.Join(m[1:], " "))
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
	if it.repeat {
		answer = e.lastAnswer()
	}
	var res actions.Result
	if it.tool != "" {
		var err error
		kind := actions.ActorAssistant
		if it.device {
			kind = actions.ActorDevice
		}
		res, err = e.actions.Execute(ctx, actions.Actor{Kind: kind}, it.tool, args)
		if err == nil && res.Refused {
			// A reason Milo knows is said at once, in one short sentence: the model would take seconds to
			// say the same thing, and often a lot more.
			if why, ok := sayRefusal(it.lang, res.Data); ok {
				e.log.Info("assistant: refused, said without the model", "tool", it.tool, "reason", res.Data["reason"])
				e.record(ctx, sess, "user", text, "")
				e.record(ctx, sess, "assistant", why, "")
				e.say(ctx, why) //nolint:errcheck // say already plays a tone when it cannot speak
				sess.finished = true
				return true
			}
		}
		if err != nil || res.Refused {
			e.log.Info("assistant: command left to the model", "tool", it.tool, "refused", res.Refused, "error", err)
			return false
		}
		if pending, _ := res.Data["pending"].(string); pending == "artist_search" {
			answer = sayPendingSearch(it.lang, res.Data) // the music starts by itself when Spotify allows the search
		}
		if it.say != nil {
			var ok bool
			if answer, ok = it.say(it.lang, args, res.Data); !ok {
				e.log.Info("assistant: could not word the answer; left to the model", "tool", it.tool)
				return false
			}
		}
	}
	e.log.Info("assistant: done without the model", "tool", it.tool, "answer", answer != "", "silent", it.silent)
	e.record(ctx, sess, "user", text, "")
	if it.tool != "" {
		out, _ := json.Marshal(map[string]any{"ok": true, "result": res.Message})
		e.record(ctx, sess, "tool", string(out), it.tool)
	}
	switch {
	case answer != "":
		e.record(ctx, sess, "assistant", answer, "")
		t0 := time.Now()
		e.say(ctx, answer) //nolint:errcheck // say already plays a tone when it cannot speak
		e.log.Info("assistant: spoke the answer", "speak_ms", time.Since(t0).Milliseconds())
	case it.silent || it.repeat: // goodbye, "be quiet", or nothing to repeat: nothing more is said or played
	case it.tool != "set_volume": // set_volume already beeps
		e.note(ctx, audio.NotifyOK)
	}
	// Something was asked and answered: mention what is waiting, once. Silent endings (goodbye, "para")
	// and music starting do not.
	if !it.silent && !it.music && (answer != "" || it.tool != "") {
		e.offerBacklog(ctx, sess)
	}
	// A command or a short answer is the end of the exchange: do not keep the microphone open for
	// another question (the person says the wake word again if there is one).
	sess.finished = true
	return true
}
