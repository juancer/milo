package assistant

import (
	"testing"

	"github.com/juancer/milo/internal/store"
)

func TestComposeRulesMatchOnlyRequestsWithoutAMessage(t *testing.T) {
	for _, c := range []struct {
		said    string
		compose bool
		who     string
	}{
		{"Mándale un mensaje a Papi", true, "papi"},
		{"envía un mensaje de voz a papá", true, "papa"},
		{"Hey Milo, manda un audio a Papi", true, "papi"},
		{"quiero mandar un mensaje", true, ""},
		{"escríbele a papi", true, "papi"},
		{"send a message to Dad", true, "dad"},
		{"I want to send a voice note to Mum", true, "mum"},
		{"manda un mensaje a papi que llego tarde", false, ""},
		{"mándale un mensaje a papi diciéndole que ya voy", false, ""},
		{"send a message to dad that I am late", false, ""},
		{"escribe un cuento de dragones", false, ""},
		{"manda a papi", false, ""},
	} {
		it, args, ok := matchIntent(c.said)
		if ok && it.compose != c.compose {
			t.Errorf("%q matched a rule that is not compose: %+v", c.said, it)
		}
		if (ok && it.compose) != c.compose {
			t.Errorf("%q: compose = %v, want %v", c.said, ok && it.compose, c.compose)
		}
		if c.compose {
			if got, _ := args["q"].(string); got != c.who {
				t.Errorf("%q: recipient = %q, want %q", c.said, got, c.who)
			}
		}
	}
}

func TestWhichKind(t *testing.T) {
	for said, want := range map[string]composeKind{
		"de voz": composeVoice, "una nota de voz": composeVoice, "audio": composeVoice, "voice": composeVoice,
		"de texto": composeText, "texto": composeText, "escrito": composeText, "text please": composeText,
		"mensaje de voz, no de texto": composeVoice,
		"":                            composeUnknown, "lo que sea": composeUnknown, "papi": composeUnknown,
	} {
		if got := whichKind(said); got != want {
			t.Errorf("whichKind(%q) = %v, want %v", said, got, want)
		}
	}
}

func TestPickChat(t *testing.T) {
	papi := store.Chat{ID: 1, Name: "Papi", Enabled: true}
	mami := store.Chat{ID: 2, Name: "Mamá", Enabled: true}
	off := store.Chat{ID: 3, Name: "Abuelo", Enabled: false}
	both := []store.Chat{papi, mami, off}
	for _, c := range []struct {
		chats []store.Chat
		said  string
		want  int64
	}{
		{both, "papi", 1}, {both, "Papi", 1}, {both, "a mamá", 2}, {both, "mama", 2}, {both, "mi mama", 2},
		{both, "abuelo", 0}, {both, "tio", 0}, {both, "", 0},
		{[]store.Chat{papi, off}, "", 1},
	} {
		got, ok := pickChat(c.chats, c.said)
		if (c.want != 0) != ok || (ok && got.ID != c.want) {
			t.Errorf("pickChat(%q) = %v %v, want id %d", c.said, got.ID, ok, c.want)
		}
	}
}
