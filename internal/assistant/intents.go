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
}

var (
	accentFold = strings.NewReplacer("á", "a", "à", "a", "ä", "a", "é", "e", "è", "e", "ë", "e", "í", "i", "ï", "i", "ó", "o", "ò", "o", "ö", "o", "ú", "u", "ü", "u", "ñ", "n", "ç", "c", "'", " ", "’", " ")
	nonWord    = regexp.MustCompile(`[^a-z0-9]+`)
	fillerHead = regexp.MustCompile(`^(?:(?:oye|hey|eh|ok|okay|vale|venga|milo|por favor|porfa|please|puedes|podrias|puedes poner|me puedes|quiero que|ahora|ya|and|y) )+`)
	fillerTail = regexp.MustCompile(`(?: (?:por favor|porfa|please|ahora|ya|milo|gracias|thanks))+$`)
)

// normalizeUtterance lower-cases, removes accents and punctuation and drops polite filler.
func normalizeUtterance(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = accentFold.Replace(s)
	s = strings.TrimSpace(nonWord.ReplaceAllString(s, " "))
	s = strings.TrimSpace(fillerHead.ReplaceAllString(s+" ", ""))
	s = strings.TrimSpace(fillerTail.ReplaceAllString(s, ""))
	return s
}

// matchIntent returns the tool and arguments a request maps to, if it is one of the known commands.
func matchIntent(text string) (it intent, args map[string]any, ok bool) {
	n := normalizeUtterance(text)
	if n == "" || len(n) > 120 {
		return intent{}, nil, false
	}
	for _, r := range intentRules {
		if m := r.re.FindStringSubmatch(n); m != nil {
			return r, r.args(m[1:]), true
		}
	}
	return intent{}, nil, false
}

// tryIntent carries out a recognised command without the model and reports whether it did. It does
// nothing when the request is not a known command, is blocked by the moderation rules, or when the
// action refuses; the model then gets the request as usual.
func (e *Engine) tryIntent(ctx context.Context, sess *Session, text string) bool {
	it, args, ok := matchIntent(text)
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
	res, err := e.actions.Execute(ctx, actions.Actor{Kind: actions.ActorAssistant}, it.tool, args)
	if err != nil || res.Refused {
		e.log.Info("assistant: command left to the model", "tool", it.tool, "refused", res.Refused, "error", err)
		return false
	}
	e.log.Info("assistant: command done without the model", "tool", it.tool)
	e.record(ctx, sess, "user", text, "")
	out, _ := json.Marshal(map[string]any{"ok": true, "result": res.Message})
	e.record(ctx, sess, "tool", string(out), it.tool)
	if it.music {
		sess.finished = true // sound is on: do not keep a microphone open over it
	}
	if it.tool != "set_volume" { // set_volume already beeps
		e.note(ctx, audio.NotifyOK)
	}
	return true
}
