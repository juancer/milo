package speech

import "testing"

func TestFamily(t *testing.T) {
	for in, want := range map[string]string{"es": "es", "es-ES": "es", "es_ES": "es", " EN_us ": "en", "": ""} {
		if got := Family(in); got != want {
			t.Errorf("Family(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModelFamily(t *testing.T) {
	for in, want := range map[string]string{
		"/var/lib/milo/voices/es_ES-davefx-medium.onnx": "es",
		"en_US-amy-low.onnx":                            "en",
		"/opt/piper/custom.onnx":                        "",
		"/x/voice.onnx":                                 "",
		"":                                              "",
	} {
		if got := ModelFamily(in); got != want {
			t.Errorf("ModelFamily(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectLanguage(t *testing.T) {
	both := []string{"es", "en"}
	for _, tc := range []struct {
		name, text string
		cands      []string
		want       string
	}{
		{"spanish", "Hola, hoy hace sol en Esplugues y la temperatura es de veinte grados.", both, "es"},
		{"english", "Hello, it is sunny today and the temperature is twenty degrees.", both, "en"},
		{"spanish with accents only", "¡Qué día más bonito!", both, "es"},
		{"short english", "Yes, I can help you with that.", both, "en"},
		{"spanish question", "¿Cómo estás? Te quiero mucho y gracias por todo.", both, "es"},
		{"numbers and names are unsure", "Milo 42 Esplugues", both, ""},
		{"empty", "", both, ""},
		{"a tie is unsure", "the el", both, ""},
		{"language without a list is ignored", "Hello, this is a test of the thing.", []string{"en", "xx"}, "en"},
		{"no known candidate", "Hello there", []string{"xx", "yy"}, ""},
		{"no candidates", "Hello there", nil, ""},
		{"french among others", "Bonjour, je suis très content de vous voir et merci pour tout.", []string{"es", "en", "fr"}, "fr"},
		{"german", "Hallo, ich bin sehr froh und danke dir für die Hilfe.", []string{"es", "de"}, "de"},
		{"portuguese with tilde", "Olá, não sei o que você quer, mas estou aqui para ajudar.", []string{"es", "pt"}, "pt"},
		{"curly apostrophe", "I’m sure it’s fine and you can do this.", both, "en"},
	} {
		if got := DetectLanguage(tc.text, tc.cands); got != tc.want {
			t.Errorf("%s: DetectLanguage(%q, %v) = %q, want %q", tc.name, tc.text, tc.cands, got, tc.want)
		}
	}
}

func TestDetectLanguageIsDeterministic(t *testing.T) {
	text := "Hola, ¿qué tal? Hello, how are you? Estoy muy bien, thanks."
	first := DetectLanguage(text, []string{"es", "en", "fr", "de"})
	for i := 0; i < 50; i++ {
		if got := DetectLanguage(text, []string{"de", "fr", "en", "es"}); got != first {
			t.Fatalf("run %d gave %q, first gave %q", i, got, first)
		}
	}
}

func TestPiperVoicesPicksTheVoiceOfTheLanguage(t *testing.T) {
	const es, en, other = "/v/es_ES-davefx-low.onnx", "/v/en_US-amy-low.onnx", "/v/fr_FR-siwis-low.onnx"
	spanish := "Hola, hoy hace sol y la temperatura es muy agradable para salir."
	english := "Hello, today it is sunny and the temperature is very nice to go out."
	mk := func(voices map[string]string, fallback, lang string) *PiperVoices {
		p, err := NewPiperVoices("piper", voices, fallback)
		if err != nil {
			t.Fatal(err)
		}
		return p.WithOptions(TTSOptions{Language: lang, Speed: 1, Volume: 100})
	}
	both := map[string]string{"es": es, "en": en}

	for _, tc := range []struct {
		name string
		p    *PiperVoices
		text string
		want string
	}{
		{"follows a Spanish answer", mk(both, "", ""), spanish, es},
		{"follows an English answer", mk(both, "", ""), english, en},
		{"a fixed language wins over the text", mk(both, "", "en-US"), spanish, en},
		{"unsure text uses the fallback", mk(both, other, ""), "42", other},
		{"unsure text with no fallback is deterministic", mk(both, "", ""), "42", en},
		{"a language with no voice uses the fallback", mk(map[string]string{"es": es}, other, "en"), english, other},
		{"a single voice needs no detection", mk(map[string]string{"es": es}, "", ""), english, es},
		{"the old single voice joins the choice by its name", mk(map[string]string{"en": en}, es, ""), spanish, es},
		{"the old single voice does not replace an explicit one", mk(both, "/v/es_ES-other-low.onnx", ""), spanish, es},
		{"a fallback without a language in its name", mk(map[string]string{"en": en}, "/opt/piper/mine.onnx", ""), spanish, "/opt/piper/mine.onnx"},
	} {
		if got := tc.p.modelFor(tc.text); got != tc.want {
			t.Errorf("%s: model = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNewPiperVoicesValidates(t *testing.T) {
	if _, err := NewPiperVoices("piper", nil, ""); err == nil {
		t.Error("no voice accepted")
	}
	if _, err := NewPiperVoices("piper", map[string]string{"es": "../x.onnx"}, ""); err == nil {
		t.Error("path traversal accepted")
	}
	if _, err := NewPiperVoices("piper", map[string]string{"es": "/v/es_ES-a-low.onnx"}, "not-a-model"); err == nil {
		t.Error("bad fallback accepted")
	}
	if _, err := NewPiperVoices("./piper", map[string]string{"es": "/v/es_ES-a-low.onnx"}, ""); err == nil {
		t.Error("relative binary accepted")
	}
}
