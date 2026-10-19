package assistant

import "testing"

func TestSayTimeSpanish(t *testing.T) {
	for _, c := range []struct {
		h, m int
		want string
	}{
		{7, 15, "Son las siete y cuarto de la mañana."},
		{7, 52, "Son las ocho menos ocho de la mañana."},
		{13, 5, "Es la una y cinco de la tarde."},
		{12, 0, "Son las doce en punto del mediodía."},
		{15, 30, "Son las tres y media de la tarde."},
		{21, 45, "Son las diez menos cuarto de la noche."},
		{0, 10, "Son las doce y diez de la madrugada."},
		{9, 0, "Son las nueve en punto de la mañana."},
		{23, 59, "Son las doce menos uno de la noche."},
	} {
		if got := sayTime(langES, clock{Hour: c.h, Minute: c.m}); got != c.want {
			t.Errorf("%02d:%02d -> %q, want %q", c.h, c.m, got, c.want)
		}
	}
}

func TestSayTimeAndDateEnglish(t *testing.T) {
	if got := sayTime(langEN, clock{Hour: 15, Minute: 5}); got != "It is three five in the afternoon." {
		t.Errorf("got %q", got)
	}
	if got := sayTime(langEN, clock{Hour: 8, Minute: 0}); got != "It is eight o'clock in the morning." {
		t.Errorf("got %q", got)
	}
	c := clock{Weekday: 1, Day: 19, Month: 10}
	if got := sayDate(langEN, c); got != "Today is Monday, October 19." {
		t.Errorf("got %q", got)
	}
	if got := sayDate(langES, c); got != "Hoy es lunes, 19 de octubre." {
		t.Errorf("got %q", got)
	}
}

func TestSayWeather(t *testing.T) {
	r := report{Place: "Esplugues de Llobregat, Catalonia, Spain", Day: "today", Unit: "celsius", NowTemp: 16, NowCode: 61, Code: 3, High: 19, Low: 12, RainPct: 40}
	if got, want := sayWeather(langES, r, false), "En Esplugues de Llobregat ahora hay 16 grados y está con lluvia. Hoy: cubierto, máxima de 19 y mínima de 12, con un 40 por ciento de probabilidad de lluvia."; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	r.Day = "tomorrow"
	if got, want := sayWeather(langES, r, false), "Mañana en Esplugues de Llobregat: cubierto, máxima de 19 y mínima de 12, con un 40 por ciento de probabilidad de lluvia."; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if got, want := sayWeather(langES, r, true), "En Esplugues de Llobregat mañana hay un 40 por ciento de probabilidad de lluvia."; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if got, want := sayWeather(langEN, r, false), "Tomorrow in Esplugues de Llobregat: overcast, a high of 19 and a low of 12, with a 40 percent chance of rain."; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestReportAndClockFromActionData(t *testing.T) {
	if _, ok := reportFrom(map[string]any{"report": map[string]any{"place": "X", "high": 20.0}}); !ok {
		t.Error("a report in a data map was not read")
	}
	if _, ok := reportFrom(map[string]any{}); ok {
		t.Error("no report, yet one was read")
	}
	if c, ok := clockFrom(map[string]any{"hour": 7, "minute": 52, "weekday": 1, "day": 19, "month": 10, "year": 2026}); !ok || c.Minute != 52 {
		t.Errorf("clock = %+v %v", c, ok)
	}
	if _, ok := clockFrom(map[string]any{"hour": 7}); ok {
		t.Error("an incomplete clock was accepted")
	}
}

func TestAnswerCalc(t *testing.T) {
	for _, c := range []struct{ in, lang, want string }{
		{"4 por 2", langES, "4 por 2 es 8."},
		{"cuanto es 4*2", langES, "4 por 2 es 8."},
		{"12 mas 7", langES, "12 más 7 es 19."},
		{"cuanto son 20 menos 5", langES, "20 menos 5 es 15."},
		{"cuanto es 9 entre 2", langES, "9 entre 2 es 4,5."},
		{"cuanto es 2 mas 3 por 4", langES, "2 más 3 por 4 es 14."},
		{"cuanto es treinta y cinco mas siete", langES, "35 más 7 es 42."},
		{"cuanto es 7 dividido entre 0", langES, "No se puede dividir entre cero."},
		{"cuanto es 3 multiplicado por 4", langES, "3 por 4 es 12."},
		{"cuanto es 1,5 por 2", langES, "1,5 por 2 es 3."},
		{"what is 4 times 2", langEN, "4 times 2 is 8."},
		{"how much is thirty five plus seven", langEN, "35 plus 7 is 42."},
		{"what s 10 divided by 4", langEN, "10 divided by 4 is 2.5."},
		{"what is 6 divided by 0", langEN, "I can't divide by zero."},
	} {
		// the router's own normalisation turns "4*2" and "1,5" into words first
		it, _, ans, ok := matchIntentAnswer(c.in)
		if !ok || it.lang != c.lang || ans != c.want {
			t.Errorf("%q -> lang=%q ok=%v %q, want %q (%s)", c.in, it.lang, ok, ans, c.want, c.lang)
		}
	}
}

func TestQuestionsAreRecognisedInBothLanguages(t *testing.T) {
	for in, want := range map[string]struct{ tool, lang string }{
		"¿Qué hora es?":                 {"get_time", langES},
		"Oye Milo, dime la hora":        {"get_time", langES},
		"What time is it?":              {"get_time", langEN},
		"¿Qué día es hoy?":              {"get_time", langES},
		"What's the date":               {"get_time", langEN},
		"Dime el clima":                 {"get_weather", langES},
		"¿Qué tiempo hace mañana?":      {"get_weather", langES},
		"¿Va a llover mañana?":          {"get_weather", langES},
		"What's the weather":            {"get_weather", langEN},
		"What's the weather tomorrow?":  {"get_weather", langEN},
		"Is it going to rain tomorrow?": {"get_weather", langEN},
	} {
		it, _, _, ok := matchIntentAnswer(in)
		if !ok || it.tool != want.tool || it.lang != want.lang {
			t.Errorf("%q -> %q %q %v, want %q %q", in, it.tool, it.lang, ok, want.tool, want.lang)
		}
	}
	// Not sums, not questions: they go to the model.
	for _, in := range []string{"cuanto es mucho", "pon 3 canciones 4", "que hora del dia es mejor para dormir", "mas menos", "2 3", "what is the capital of France", "what is love"} {
		if it, _, ans, ok := matchIntentAnswer(in); ok {
			t.Errorf("%q was taken as %q %q", in, it.tool, ans)
		}
	}
}
