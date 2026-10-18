package speech

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Family returns the language family of a locale code: "es" for "es", "es-ES" and "es_ES".
func Family(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(code, "-_"); i >= 0 {
		code = code[:i]
	}
	return code
}

// ModelFamily returns the language family a Piper voice file belongs to, from its catalog name
// (es_ES-davefx-medium.onnx -> "es"), or "" when the name does not say.
func ModelFamily(modelPath string) string {
	base := strings.TrimSuffix(filepath.Base(modelPath), ".onnx")
	loc, _, ok := strings.Cut(base, "-")
	if !ok || len(loc) != 5 || loc[2] != '_' {
		return ""
	}
	return Family(loc)
}

// stopwords are very common words that mark a language. A word several languages share counts for
// each of them; the accents below break most of those ties.
var stopwords = map[string]string{
	"en": "the and is are to of that it you for with this was on be have not but they what your i'm it's don't hello thanks please can how why where when who will would could there their about from or an at as do does did my me we he she his her them then than so if just like yes",
	"es": "el los las una es por con para se lo como pero está soy eres qué cómo gracias muy también yo tú hola que del al más sí esto esta estás tengo quiero puedo vamos bien porque cuando donde quién mi tu su nos ya hay son fue ser",
	"pt": "o os as um uma é não com para por mas está sou você obrigado obrigada muito também como que do da dos das ao mais sim isso esta estou tenho quero posso vamos bem porque quando onde quem meu seu nós já há são foi ser olá",
	"fr": "le les des une est pas pour avec que qui dans sur vous nous je il elle mais très merci bonjour du au aux plus oui ça cette suis ai veux peux allons bien parce quand où mon ton son ils sont était être",
	"it": "il gli le una è non per con che sono mi ma molto grazie ciao anche del della dei delle più sì questo questa sto ho voglio posso andiamo bene perché quando dove chi mio tuo suo noi già c'è era essere",
	"de": "der die das und ist nicht ich du sie wir mit für auf ein eine aber auch sehr danke hallo den dem des mehr ja dies diese bin habe will kann gut weil wenn wo wer mein dein sein uns schon sind war",
	"ca": "el els les una és per amb que sóc ets molt també però gràcies hola del al més sí això aquesta estic tinc vull puc anem bé perquè quan on qui meu teu seu nosaltres ja hi són va ser",
	"nl": "de het een en is niet ik je we met voor van op maar ook zeer dank hallo den dat dit deze ben heb wil kan goed omdat als waar wie mijn jouw zijn ons al was",
}

// accents are characters that point to some languages more than to others; each one found adds
// to the score of the languages that use it.
var accents = map[string]string{
	"es": "ñ¿¡",
	"pt": "ãõ",
	"fr": "œëïùûâêîô",
	"it": "ìò",
	"de": "ßäöü",
	"ca": "·ç",
}

var stopsets = func() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for lang, words := range stopwords {
		set := map[string]bool{}
		for _, w := range strings.Fields(words) {
			set[w] = true
		}
		out[lang] = set
	}
	return out
}()

// DetectLanguage guesses which of the candidate language families (es, en, ...) text is written in.
// It is a cheap, deterministic word-list count, meant for short spoken answers and for choosing
// between a handful of installed voices; it returns "" when it is not sure, and then the caller
// falls back to a default voice. Only candidates it has word lists for are considered, and short
// or mixed text, names and numbers stay unsure. Close languages (Spanish and Catalan, Spanish and
// Portuguese) are the weak spot: a short sentence can be unsure or wrong.
func DetectLanguage(text string, candidates []string) string {
	type scored struct {
		lang  string
		score int
	}
	var known []string
	for _, c := range candidates {
		if f := Family(c); stopsets[f] != nil {
			known = append(known, f)
		}
	}
	if len(known) == 0 {
		return ""
	}
	sort.Strings(known)
	text = strings.ToLower(text)
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && r != '\'' && r != '’' })
	scores := make([]scored, 0, len(known))
	for _, lang := range known {
		n := 0
		for _, w := range words {
			if stopsets[lang][strings.ReplaceAll(w, "’", "'")] {
				n++
			}
		}
		for _, r := range text {
			if strings.ContainsRune(accents[lang], r) {
				n += 2
			}
		}
		scores = append(scores, scored{lang, n})
	}
	sort.SliceStable(scores, func(i, j int) bool { return scores[i].score > scores[j].score })
	best := scores[0]
	if best.score < 2 || (len(scores) > 1 && scores[1].score == best.score) {
		return ""
	}
	return best.lang
}
