package assistant

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Answers that need no model: the time, the date, the weather and sums are worded here, in Spanish
// or English, from numbers the actions (or the calculator below) produce. The rule that recognised
// the request says which language to use.

const (
	langES = "es"
	langEN = "en"
)

var (
	esUnits = []string{"cero", "uno", "dos", "tres", "cuatro", "cinco", "seis", "siete", "ocho", "nueve", "diez",
		"once", "doce", "trece", "catorce", "quince", "dieciseis", "diecisiete", "dieciocho", "diecinueve", "veinte"}
	esTens  = map[int]string{30: "treinta", 40: "cuarenta", 50: "cincuenta", 60: "sesenta", 70: "setenta", 80: "ochenta", 90: "noventa"}
	enUnits = []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten",
		"eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen", "twenty"}
	enTens = map[int]string{30: "thirty", 40: "forty", 50: "fifty", 60: "sixty", 70: "seventy", 80: "eighty", 90: "ninety"}
)

// words spells a number from 0 to 99 (as the speech engine should say it).
func words(lang string, n int) string {
	units, tens := esUnits, esTens
	if lang == langEN {
		units, tens = enUnits, enTens
	}
	switch {
	case n < 0 || n > 99:
		return strconv.Itoa(n)
	case n <= 20:
		return units[n]
	case lang == langES && n < 30:
		return map[int]string{21: "veintiuno", 22: "veintidos", 23: "veintitres", 24: "veinticuatro", 25: "veinticinco",
			26: "veintiseis", 27: "veintisiete", 28: "veintiocho", 29: "veintinueve"}[n]
	case n%10 == 0:
		return tens[n]
	case lang == langES:
		return tens[n/10*10] + " y " + units[n%10]
	}
	return tens[n/10*10] + " " + units[n%10]
}

var (
	esWeekdays = []string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"}
	enWeekdays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	esMonths   = []string{"", "enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}
	enMonths   = []string{"", "January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
)

// clock is what get_time reports.
type clock struct{ Hour, Minute, Weekday, Day, Month, Year int }

func clockFrom(data map[string]any) (clock, bool) {
	get := func(k string) (int, bool) {
		switch v := data[k].(type) {
		case int:
			return v, true
		case float64:
			return int(v), true
		}
		return 0, false
	}
	var c clock
	var ok [6]bool
	c.Hour, ok[0] = get("hour")
	c.Minute, ok[1] = get("minute")
	c.Weekday, ok[2] = get("weekday")
	c.Day, ok[3] = get("day")
	c.Month, ok[4] = get("month")
	c.Year, ok[5] = get("year")
	for _, k := range ok {
		if !k {
			return clock{}, false
		}
	}
	return c, c.Month >= 1 && c.Month <= 12 && c.Weekday >= 0 && c.Weekday <= 6
}

func sayTime(lang string, c clock) string {
	if lang == langEN {
		h12 := c.Hour % 12
		if h12 == 0 {
			h12 = 12
		}
		part := "in the morning"
		switch {
		case c.Hour >= 18:
			part = "in the evening"
		case c.Hour >= 12:
			part = "in the afternoon"
		case c.Hour < 5:
			part = "at night"
		}
		if c.Minute == 0 {
			return fmt.Sprintf("It is %s o'clock %s.", words(langEN, h12), part)
		}
		return fmt.Sprintf("It is %s %s %s.", words(langEN, h12), words(langEN, c.Minute), part)
	}
	// Spanish: "Son las siete y cuarto de la mañana."
	hour, min := c.Hour, c.Minute
	if min > 30 { // "menos veinte" counts towards the next hour
		hour = (hour + 1) % 24
	}
	h12 := hour % 12
	if h12 == 0 {
		h12 = 12
	}
	article, verb := "las", "Son"
	if h12 == 1 {
		article, verb = "la", "Es"
	}
	hourWords := words(langES, h12)
	if h12 == 1 {
		hourWords = "una"
	}
	var minutes string
	switch {
	case min == 0:
		minutes = " en punto"
	case min == 15:
		minutes = " y cuarto"
	case min == 30:
		minutes = " y media"
	case min == 45:
		minutes = " menos cuarto"
	case min > 30:
		minutes = " menos " + words(langES, 60-min)
	default:
		minutes = " y " + words(langES, min)
	}
	part := "de la mañana"
	switch {
	case hour >= 20 || hour < 1 && c.Hour >= 20:
		part = "de la noche"
	case hour >= 13:
		part = "de la tarde"
	case hour == 12:
		part = "del mediodía"
	case hour < 6:
		part = "de la madrugada"
	}
	return fmt.Sprintf("%s %s %s%s %s.", verb, article, hourWords, minutes, part)
}

func sayDate(lang string, c clock) string {
	if lang == langEN {
		return fmt.Sprintf("Today is %s, %s %d.", enWeekdays[c.Weekday], enMonths[c.Month], c.Day)
	}
	return fmt.Sprintf("Hoy es %s, %d de %s.", esWeekdays[c.Weekday], c.Day, esMonths[c.Month])
}

// report is the forecast get_weather returns in its data (the same fields as skills.WeatherReport).
type report struct {
	Place   string `json:"place"`
	Day     string `json:"day"`
	Unit    string `json:"unit"`
	NowTemp int    `json:"now_temp"`
	NowCode int    `json:"now_code"`
	Code    int    `json:"code"`
	High    int    `json:"high"`
	Low     int    `json:"low"`
	RainPct int    `json:"rain_pct"`
}

func reportFrom(data map[string]any) (report, bool) {
	raw, ok := data["report"]
	if !ok {
		return report{}, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return report{}, false
	}
	var r report
	if json.Unmarshal(b, &r) != nil || r.Place == "" {
		return report{}, false
	}
	return r, true
}

// town keeps the first part of a saved place such as "Esplugues de Llobregat, Catalonia, Spain".
func town(place string) string {
	first, _, _ := strings.Cut(place, ",")
	return strings.TrimSpace(first)
}

// skyWords describes a WMO weather code.
func skyWords(lang string, code int) string {
	type pair struct{ es, en string }
	var p pair
	switch {
	case code == 0:
		p = pair{"despejado", "clear"}
	case code == 1:
		p = pair{"mayormente despejado", "mostly clear"}
	case code == 2:
		p = pair{"parcialmente nublado", "partly cloudy"}
	case code == 3:
		p = pair{"cubierto", "overcast"}
	case code == 45 || code == 48:
		p = pair{"con niebla", "foggy"}
	case code >= 51 && code <= 57:
		p = pair{"con llovizna", "drizzly"}
	case code >= 61 && code <= 67:
		p = pair{"con lluvia", "rainy"}
	case code >= 71 && code <= 77, code == 85 || code == 86:
		p = pair{"con nieve", "snowy"}
	case code >= 80 && code <= 82:
		p = pair{"con chubascos", "showery"}
	case code >= 95:
		p = pair{"con tormenta", "stormy"}
	default:
		p = pair{"inestable", "unsettled"}
	}
	if lang == langEN {
		return p.en
	}
	return p.es
}

func sayWeather(lang string, r report, rainOnly bool) string {
	deg := "grados"
	if r.Unit == "fahrenheit" {
		deg = "grados Fahrenheit"
	}
	place := town(r.Place)
	if lang == langEN {
		deg = "degrees"
		if r.Unit == "fahrenheit" {
			deg = "degrees Fahrenheit"
		}
		day := "today"
		if r.Day == "tomorrow" {
			day = "tomorrow"
		}
		if rainOnly {
			return fmt.Sprintf("In %s %s there is a %d percent chance of rain.", place, day, r.RainPct)
		}
		rain := ""
		if r.RainPct > 0 {
			rain = fmt.Sprintf(", with a %d percent chance of rain", r.RainPct)
		}
		daily := fmt.Sprintf("%s, a high of %d and a low of %d%s", skyWords(langEN, r.Code), r.High, r.Low, rain)
		if r.Day == "tomorrow" {
			return fmt.Sprintf("Tomorrow in %s: %s.", place, daily)
		}
		return fmt.Sprintf("In %s it is %d %s and %s. Today: %s.", place, r.NowTemp, deg, skyWords(langEN, r.NowCode), daily)
	}
	day := "hoy"
	if r.Day == "tomorrow" {
		day = "mañana"
	}
	if rainOnly {
		return fmt.Sprintf("En %s %s hay un %d por ciento de probabilidad de lluvia.", place, day, r.RainPct)
	}
	rain := ""
	if r.RainPct > 0 {
		rain = fmt.Sprintf(", con un %d por ciento de probabilidad de lluvia", r.RainPct)
	}
	daily := fmt.Sprintf("%s, máxima de %d y mínima de %d%s", skyWords(langES, r.Code), r.High, r.Low, rain)
	if r.Day == "tomorrow" {
		return fmt.Sprintf("Mañana en %s: %s.", place, daily)
	}
	return fmt.Sprintf("En %s ahora hay %d %s y está %s. Hoy: %s.", place, r.NowTemp, deg, skyWords(langES, r.NowCode), daily)
}

// ---------------------------------------------------------------- sums

type calcToken struct {
	num float64
	op  byte // '+', '-', '*', '/', or 0 for a number
}

var (
	numberWordES = func() map[string]int {
		m := map[string]int{}
		for i, w := range esUnits {
			m[w] = i
		}
		for n := 21; n <= 29; n++ {
			m[words(langES, n)] = n
		}
		for n, w := range esTens {
			m[w] = n
		}
		m["un"], m["una"], m["cien"] = 1, 1, 100
		return m
	}()
	numberWordEN = func() map[string]int {
		m := map[string]int{}
		for i, w := range enUnits {
			m[w] = i
		}
		for n, w := range enTens {
			m[w] = n
		}
		m["hundred"] = 100
		return m
	}()
	digitsRe = regexp.MustCompile(`^[0-9]+(?:dec[0-9]+)?$`)
)

// parseCalc turns a spoken sum ("4 por 2", "treinta y cinco mas 7", "twelve divided by 4") into
// tokens. It accepts only numbers and the four operations: anything else is not a sum.
func parseCalc(lang, text string) ([]calcToken, string, bool) {
	fields := strings.Fields(text)
	var toks []calcToken
	var spoken []string
	numWords := numberWordES
	ops := map[string]byte{"mas": '+', "menos": '-', "por": '*', "entre": '/', "x": '*', "star": '*', "plusop": '+', "minusop": '-', "slashop": '/'}
	opSay := map[byte]string{'+': "más", '-': "menos", '*': "por", '/': "entre"}
	if lang == langEN {
		numWords = numberWordEN
		ops = map[string]byte{"plus": '+', "minus": '-', "times": '*', "over": '/', "x": '*', "star": '*', "plusop": '+', "minusop": '-', "slashop": '/'}
		opSay = map[byte]string{'+': "plus", '-': "minus", '*': "times", '/': "divided by"}
	}
	expectNumber := true
	for i := 0; i < len(fields); i++ {
		w := fields[i]
		if expectNumber {
			if digitsRe.MatchString(w) {
				v, err := strconv.ParseFloat(strings.Replace(w, "dec", ".", 1), 64)
				if err != nil {
					return nil, "", false
				}
				toks = append(toks, calcToken{num: v})
				spoken = append(spoken, strings.Replace(w, "dec", map[string]string{langES: ",", langEN: "."}[lang], 1))
				expectNumber = false
				continue
			}
			// a number in words: "treinta y cinco", "thirty five", "cien", "twenty"
			if n, used, ok := wordsNumber(lang, numWords, fields[i:]); ok {
				toks = append(toks, calcToken{num: float64(n)})
				spoken = append(spoken, strconv.Itoa(n))
				i += used - 1
				expectNumber = false
				continue
			}
			return nil, "", false
		}
		// an operator; some are two words ("multiplicado por", "dividido entre", "multiplied by", "divided by")
		switch {
		case lang == langES && (w == "multiplicado" || w == "dividido") && i+1 < len(fields) && (fields[i+1] == "por" || fields[i+1] == "entre"):
			op := byte('*')
			if w == "dividido" {
				op = '/'
			}
			toks = append(toks, calcToken{op: op})
			spoken = append(spoken, opSay[op])
			i++
		case lang == langEN && (w == "multiplied" || w == "divided") && i+1 < len(fields) && fields[i+1] == "by":
			op := byte('*')
			if w == "divided" {
				op = '/'
			}
			toks = append(toks, calcToken{op: op})
			spoken = append(spoken, opSay[op])
			i++
		default:
			op, ok := ops[w]
			if !ok {
				return nil, "", false
			}
			toks = append(toks, calcToken{op: op})
			spoken = append(spoken, opSay[op])
		}
		expectNumber = true
	}
	if expectNumber || len(toks) < 3 || len(toks) > 21 {
		return nil, "", false
	}
	return toks, strings.Join(spoken, " "), true
}

// wordsNumber reads a number (0-99, or "cien"/"hundred") spelled in words at the start of fields.
func wordsNumber(lang string, table map[string]int, fields []string) (n, used int, ok bool) {
	if len(fields) == 0 {
		return 0, 0, false
	}
	first, found := table[fields[0]]
	if !found {
		return 0, 0, false
	}
	if lang == langES && first >= 30 && first%10 == 0 && len(fields) >= 3 && fields[1] == "y" {
		if u, ok := table[fields[2]]; ok && u >= 1 && u <= 9 {
			return first + u, 3, true
		}
	}
	if lang == langEN && first >= 30 && first%10 == 0 && len(fields) >= 2 {
		if u, ok := table[fields[1]]; ok && u >= 1 && u <= 9 {
			return first + u, 2, true
		}
	}
	return first, 1, true
}

// evalCalc evaluates the tokens with the usual precedence. It reports division by zero.
func evalCalc(toks []calcToken) (float64, error) {
	nums := []float64{toks[0].num}
	var ops []byte
	apply := func() error {
		b, a := nums[len(nums)-1], nums[len(nums)-2]
		op := ops[len(ops)-1]
		nums, ops = nums[:len(nums)-2], ops[:len(ops)-1]
		var r float64
		switch op {
		case '+':
			r = a + b
		case '-':
			r = a - b
		case '*':
			r = a * b
		case '/':
			if b == 0 {
				return errDivideByZero
			}
			r = a / b
		}
		nums = append(nums, r)
		return nil
	}
	prec := func(op byte) int {
		if op == '*' || op == '/' {
			return 2
		}
		return 1
	}
	for i := 1; i+1 < len(toks); i += 2 {
		op := toks[i].op
		for len(ops) > 0 && prec(ops[len(ops)-1]) >= prec(op) {
			if err := apply(); err != nil {
				return 0, err
			}
		}
		ops = append(ops, op)
		nums = append(nums, toks[i+1].num)
	}
	for len(ops) > 0 {
		if err := apply(); err != nil {
			return 0, err
		}
	}
	return nums[0], nil
}

var errDivideByZero = fmt.Errorf("divide by zero")

// sayNumber words a result: whole numbers plainly, others with up to two decimals.
func sayNumber(lang string, v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	s := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 2, 64), "0"), ".")
	if lang == langES {
		s = strings.Replace(s, ".", ",", 1)
	}
	return s
}

// answerCalc answers a sum: "4 por 2 es 8."
func answerCalc(lang, text string) (string, bool) {
	toks, spoken, ok := parseCalc(lang, text)
	if !ok {
		return "", false
	}
	v, err := evalCalc(toks)
	switch {
	case err != nil && lang == langEN:
		return "I can't divide by zero.", true
	case err != nil:
		return "No se puede dividir entre cero.", true
	case math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1e12:
		return "", false
	case lang == langEN:
		return fmt.Sprintf("%s is %s.", spoken, sayNumber(lang, v)), true
	}
	return fmt.Sprintf("%s es %s.", spoken, sayNumber(lang, v)), true
}

// ---------------------------------------------------------------- durations (timers)

var (
	unitSeconds = map[string]int{
		"segundo": 1, "segundos": 1, "minuto": 60, "minutos": 60, "hora": 3600, "horas": 3600,
		"second": 1, "seconds": 1, "minute": 60, "minutes": 60, "hour": 3600, "hours": 3600,
	}
)

// parseDuration reads "5 minutos", "una hora y 30 minutos", "media hora", "an hour and a half",
// "thirty seconds" into seconds (at most 24 hours) and a spoken form.
func parseDuration(lang, text string) (secs int, spoken string, ok bool) {
	f := strings.Fields(text)
	table := numberWordES
	half := map[string]bool{"media": true, "medio": true}
	if lang == langEN {
		table = numberWordEN
		half = map[string]bool{"half": true}
	}
	i := 0
	for i < len(f) {
		w := f[i]
		switch {
		case w == "y" || w == "and" || w == "con":
			i++
			continue
		case lang == langEN && w == "a" && i+1 < len(f) && half[f[i+1]] && lastUnit(f[:i]) > 0: // "an hour and a half"
			i++
			continue
		case half[w]: // "media hora", "half an hour", and "y media" after an hour or a minute
			if lang == langEN && i+2 < len(f)+1 && i+1 < len(f) && (f[i+1] == "an" || f[i+1] == "a") && i+2 < len(f) && unitSeconds[f[i+2]] > 0 {
				secs += unitSeconds[f[i+2]] / 2
				i += 3
				continue
			}
			if i+1 < len(f) && unitSeconds[f[i+1]] > 0 {
				secs += unitSeconds[f[i+1]] / 2
				i += 2
				continue
			}
			// "una hora y media" / "an hour and a half": half of the unit just read
			if last := lastUnit(f[:i]); last > 0 {
				secs += last / 2
				i++
				if lang == langEN && i < len(f) && (f[i] == "hour" || f[i] == "minute") { // "a half hour"
					i++
				}
				continue
			}
			return 0, "", false
		}
		n, used := 0, 0
		if digitsRe.MatchString(w) && !strings.Contains(w, "dec") {
			v, err := strconv.Atoi(w)
			if err != nil || v > 100000 {
				return 0, "", false
			}
			n, used = v, 1
		} else if v, u, found := wordsNumber(lang, table, f[i:]); found {
			n, used = v, u
		} else if (lang == langEN && (w == "a" || w == "an")) && i+1 < len(f) && unitSeconds[f[i+1]] > 0 {
			n, used = 1, 1
		} else {
			return 0, "", false
		}
		i += used
		if i >= len(f) || unitSeconds[f[i]] == 0 {
			return 0, "", false
		}
		secs += n * unitSeconds[f[i]]
		i++
	}
	if secs < 1 || secs > 24*3600 {
		return 0, "", false
	}
	return secs, sayDuration(lang, secs), true
}

// lastUnit is the length, in seconds, of the last unit word in f ("una hora" -> 3600).
func lastUnit(f []string) int {
	for i := len(f) - 1; i >= 0; i-- {
		if u := unitSeconds[f[i]]; u > 0 {
			return u
		}
	}
	return 0
}

// sayDuration words a number of seconds: "1 hora y 30 minutos", "5 minutos", "30 segundos".
func sayDuration(lang string, secs int) string {
	h, m, s := secs/3600, secs%3600/60, secs%60
	names := [3][2]string{{"hora", "horas"}, {"minuto", "minutos"}, {"segundo", "segundos"}}
	and := " y "
	if lang == langEN {
		names = [3][2]string{{"hour", "hours"}, {"minute", "minutes"}, {"second", "seconds"}}
		and = " and "
	}
	var parts []string
	for i, n := range []int{h, m, s} {
		if n == 0 {
			continue
		}
		name := names[i][1]
		if n == 1 {
			name = names[i][0]
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, name))
	}
	return strings.Join(parts, and)
}

// ---------------------------------------------------------------- refusals

// sayRefusal words, in the language asked in, why an action could not be done, for the reasons the
// skills name. Anything else is left to the model to explain.
func sayRefusal(lang string, data map[string]any) (string, bool) {
	reason, _ := data["reason"].(string)
	query, _ := data["query"].(string)
	es := map[string]string{
		"voice_control_off":  "El control por voz de Spotify está apagado. Un adulto puede encenderlo en el panel.",
		"not_connected":      "Spotify todavía no está conectado.",
		"artist_not_found":   "No he encontrado a " + query + " en Spotify.",
		"search_unavailable": "Ahora mismo no puedo buscar a " + query + " en Spotify. Inténtalo en un momento.",
		"no_music":           "Todavía no hay música en el dispositivo.",
		"no_playlists":       "No encuentro ninguna lista de reproducción en tu Spotify.",
		"playlist_not_found": "No encuentro una lista que se llame " + query + ".",
		"no_history":         "Todavía no sé qué es lo que más escuchas. Pon música unos días y lo aprendo.",
	}
	en := map[string]string{
		"voice_control_off":  "Spotify voice control is switched off. A grown-up can turn it on in the panel.",
		"not_connected":      "Spotify is not connected yet.",
		"artist_not_found":   "I could not find " + query + " on Spotify.",
		"search_unavailable": "I cannot look up " + query + " on Spotify right now. Try again in a moment.",
		"no_music":           "There is no music on the device yet.",
		"no_playlists":       "I cannot find any playlist on your Spotify.",
		"playlist_not_found": "I cannot find a playlist called " + query + ".",
		"no_history":         "I do not know yet what you listen to most. Play some music for a few days and I will learn.",
	}
	table := es
	if lang == langEN {
		table = en
	}
	msg, ok := table[reason]
	return msg, ok
}

// sayPendingSearch tells the person that an artist is being looked for and will start when found.
func sayPendingSearch(lang string, data map[string]any) string {
	query, _ := data["query"].(string)
	if lang == langEN {
		return "I am looking for " + query + ". It may take a moment."
	}
	return "Voy a buscar a " + query + ". Puede tardar un poco."
}
