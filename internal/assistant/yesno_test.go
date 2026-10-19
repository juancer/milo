package assistant_test

import (
	"testing"

	"github.com/juancer/milo/internal/assistant"
)

func TestMatchAnswer(t *testing.T) {
	cases := []struct {
		said string
		want assistant.Answer
	}{
		// Spanish
		{"sí", assistant.AnswerYes},
		{"Sí, léelo", assistant.AnswerYes},
		{"claro que sí", assistant.AnswerYes},
		{"vale", assistant.AnswerYes},
		{"ok", assistant.AnswerYes},
		{"Dale, ponlo", assistant.AnswerYes},
		{"por favor léemelo", assistant.AnswerYes},
		{"no", assistant.AnswerNo},
		{"No, gracias", assistant.AnswerNo},
		{"ahora no", assistant.AnswerNo},
		{"luego lo escucho", assistant.AnswerNo},
		{"sí, no, luego", assistant.AnswerNo}, // a refusal wins
		// English
		{"yes", assistant.AnswerYes},
		{"Yes please", assistant.AnswerYes},
		{"sure", assistant.AnswerYes},
		{"go ahead", assistant.AnswerYes},
		{"no thanks", assistant.AnswerNo},
		{"not now", assistant.AnswerNo},
		{"later", assistant.AnswerNo},
		// Not an answer
		{"", assistant.AnswerNone},
		{"qué hora es", assistant.AnswerNone},
		{"what time is it", assistant.AnswerNone},
		{"nothing", assistant.AnswerNone},
		{"nobody", assistant.AnswerNone},
		{"sientate", assistant.AnswerNone},
	}
	for _, c := range cases {
		if got := assistant.MatchAnswer(c.said); got != c.want {
			t.Errorf("MatchAnswer(%q) = %d, want %d", c.said, got, c.want)
		}
	}
}
