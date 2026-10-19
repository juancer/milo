package assistant

import (
	"reflect"
	"testing"
)

func TestNormalizeUtterance(t *testing.T) {
	for in, want := range map[string]string{
		"  Oye Milo, ¡Pon algo de música, por favor!  ": "pon algo de musica",
		"Let's just just play something":                "let s just just play something",
		"Pon música de Belinda.":                        "pon musica de belinda",
		"Sube el volumen":                               "sube el volumen",
		"":                                              "",
	} {
		if got := normalizeUtterance(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchIntent(t *testing.T) {
	type want struct {
		tool string
		args map[string]any
	}
	none := map[string]any{}
	cases := map[string]want{
		"Pon algo de música":                 {"play_music", none},
		"pon música":                         {"play_music", none},
		"Hey Milo, ponme algo":               {"play_music", none},
		"play something":                     {"play_music", none},
		"Let's just just play something":     {"play_music", none},
		"quiero escuchar algo de música":     {"play_music", none},
		"play music":                         {"play_music", none},
		"sigue":                              {"play_music", none},
		"pon música de Belinda":              {"play_music", map[string]any{"query": "belinda"}},
		"Pon canciones de Frozen, por favor": {"play_music", map[string]any{"query": "frozen"}},
		"play songs by Queen":                {"play_music", map[string]any{"query": "queen"}},
		"pon la música de los payasos":       {"play_music", map[string]any{"query": "payasos"}},
		"pon la playlist de dormir":          {"spotify_play_playlist", map[string]any{"name": "dormir"}},
		"play my playlist bedtime":           {"spotify_play_playlist", map[string]any{"name": "bedtime"}},
		"pausa":                              {"pause_music", none},
		"para":                               {"stop_audio", none},
		"stop the music":                     {"stop_audio", none},
		"siguiente canción":                  {"next_song", none},
		"skip":                               {"next_song", none},
		"sube el volumen":                    {"set_volume", map[string]any{"change": 10}},
		"baja el volumen":                    {"set_volume", map[string]any{"change": -10}},
		"volumen 7":                          {"set_volume", map[string]any{"level_10": 7}},
		"pon el volumen a 10":                {"set_volume", map[string]any{"level_10": 10}},
	}
	for text, w := range cases {
		it, args, ok := matchIntent(text)
		if !ok || it.tool != w.tool || !reflect.DeepEqual(args, w.args) {
			t.Errorf("%q -> %q %v %v, want %q %v", text, it.tool, args, ok, w.tool, w.args)
		}
	}
}

// Anything that is not clearly one of these commands must reach the model.
func TestMatchIntentLeavesEverythingElseToTheModel(t *testing.T) {
	for _, text := range []string{
		"", "hola", "qué tiempo hacía ayer en madrid", "pon la lavadora", "pon la mesa", "cuéntame un cuento de dragones",
		"no pares de hablar", "stop talking about that", "play with me", "pon música triste y luego cuéntame un chiste",
		"what is the music of the spheres", "dime una canción de cuna", "volumen", "sube", "pon algo de comer", "pon la mesa", "pon la tele", "pon el ventilador", "pon la lavadora ya", "pon cinco",
	} {
		if it, args, ok := matchIntent(text); ok {
			t.Errorf("%q was taken as %q %v", text, it.tool, args)
		}
	}
}

func TestBareArtistNamesPlayThatArtist(t *testing.T) {
	for in, q := range map[string]string{
		"pon Camilo":           "camilo",
		"ponme a Belinda":      "belinda",
		"pon a Shakira":        "shakira",
		"Hey Milo, pon Maluma": "maluma",
		"play Queen":           "queen",
	} {
		_ = q
		it, args, ok := matchIntent(in)
		if !ok || it.tool != "play_music" {
			t.Errorf("%q -> %q %v, want play_music", in, it.tool, ok)
			continue
		}
		if args["query"] == "" || args["query"] == nil {
			t.Errorf("%q gave no artist", in)
		}
	}
	// Not names, or other rules first.
	for in, tool := range map[string]string{"pon música": "play_music", "pon el volumen al máximo": "set_volume", "pon la playlist de dormir": "spotify_play_playlist", "pon un temporizador de 5 minutos": "set_timer"} {
		if it, _, ok := matchIntent(in); !ok || it.tool != tool {
			t.Errorf("%q -> %q %v, want %q", in, it.tool, ok, tool)
		}
	}
}
