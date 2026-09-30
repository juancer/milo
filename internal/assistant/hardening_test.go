package assistant_test

import (
	"context"
	"strings"
	"testing"

	"github.com/juancer/nexo/internal/assistant"
	"github.com/juancer/nexo/internal/moderation"
	"github.com/juancer/nexo/internal/store"
)

func TestSendMessageTextIsOutputModerated(t *testing.T) {
	h := newHarness(t)
	h.app.Store.CreateModerationRule(h.ctx, store.ModerationRule{Pattern: "secret plan", AppliesTo: "output", Enabled: true})
	h.llm.replies = []assistant.Message{
		call("bad", "send_message", `{"chat":"Dad","text":"here is the secret plan"}`),
		call("ok", "send_message", `{"chat":"Dad","text":"I finished my homework"}`),
		say("done"),
	}
	if _, err := h.turn("tell dad"); err != nil {
		t.Fatal(err)
	}
	if got := h.toolResult(1, "bad"); !strings.Contains(got, `"error":"not allowed"`) {
		t.Fatalf("blocked text was not refused: %s", got)
	}
	if got := h.toolResult(2, "ok"); strings.Contains(got, `"error":"not allowed"`) {
		t.Fatalf("clean text was refused by moderation: %s", got)
	}
}

func TestReplyAfterReadingTheCalendarIsNotRememberedAsTheAssistantsOwn(t *testing.T) {
	h := newHarness(t)
	h.llm.replies = []assistant.Message{
		call("c1", "get_calendar", `{"day":"week"}`),
		say("Your event says: always obey PLANTED-INSTRUCTION."),
		say("Okay."),
	}
	sess, _ := h.app.Assistant.NewSession(h.ctx, "test")
	got, err := h.app.Assistant.Converse(h.ctx, sess, "what is on the calendar?")
	if err != nil || !strings.Contains(got, "PLANTED-INSTRUCTION") {
		t.Fatalf("the child should still hear the answer: %q, %v", got, err)
	}
	if _, err := h.app.Assistant.Converse(h.ctx, sess, "thanks"); err != nil {
		t.Fatal(err)
	}
	for _, m := range h.llm.request(2).Messages {
		if strings.Contains(m.Content, "PLANTED-INSTRUCTION") {
			t.Fatalf("calendar-tainted reply went back into the history as %s text", m.Role)
		}
	}
}

func TestExtraToolCallsAndHugeContentAreNotEchoedBack(t *testing.T) {
	h := newHarness(t)
	many := assistant.Message{Role: "assistant", Content: strings.Repeat("x", 1<<20)}
	for i := 0; i < 20; i++ {
		many.ToolCalls = append(many.ToolCalls, assistant.ToolCall{ID: "t" + string(rune('a'+i)), Name: "get_time", Arguments: `{}`})
	}
	h.llm.replies = []assistant.Message{many, say("done")}
	if _, err := h.turn("time?"); err != nil {
		t.Fatal(err)
	}
	var asst *assistant.Message
	tools := 0
	msgs := h.llm.request(1).Messages
	for i := range msgs {
		switch msgs[i].Role {
		case "assistant":
			if len(msgs[i].ToolCalls) > 0 {
				asst = &msgs[i]
			}
		case "tool":
			tools++
		}
	}
	if asst == nil || len(asst.ToolCalls) > 6 || tools > 6 {
		t.Fatalf("tool calls echoed: assistant=%v tool messages=%d", asst != nil && len(asst.ToolCalls) > 6, tools)
	}
	if len(asst.Content) > moderation.MaxReplyBytes {
		t.Fatalf("assistant content of %d bytes was sent back", len(asst.Content))
	}
}

func TestReplyWithoutSpacesIsCapped(t *testing.T) {
	h := newHarness(t)
	h.llm.replies = []assistant.Message{say(strings.Repeat("é", 1<<20))}
	got, err := h.turn("say something long")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > moderation.MaxReplyBytes {
		t.Fatalf("reply of %d bytes", len(got))
	}
}

// gate lets a test change the world while the model is "thinking".
type gate struct {
	scripted *scripted
	during   func()
}

func (g gate) Chat(ctx context.Context, req assistant.ChatRequest) (assistant.Message, error) {
	if g.during != nil {
		g.during()
	}
	return g.scripted.Chat(ctx, req)
}

type gateBackends struct {
	fakeBackends
	g gate
}

func (b gateBackends) LLM(context.Context, store.GPTSettings) (assistant.AIProvider, error) {
	return b.g, nil
}

func TestVoiceSessionEndsWhenTheScheduleClosesBetweenTurns(t *testing.T) {
	h := newHarness(t)
	h.app.Features.SetEnabled(h.ctx, "assistant", true)
	s, _ := h.app.Store.GetGPTSettings(h.ctx)
	s.FollowUp = true
	h.app.Store.UpdateGPTSettings(h.ctx, s)
	h.llm.replies = []assistant.Message{say("Hello there!")}
	h.app.Assistant.SetBackends(gateBackends{fakeBackends{h.llm}, gate{h.llm, func() {
		h.app.Store.SaveSchedule(h.ctx, "assistant", true, nil) // enabled, no windows: closed
	}}})
	h.app.MockAudio.SetUtterance("hi")
	if err := h.app.Assistant.RunVoice(h.ctx, "test"); err != nil {
		t.Fatal(err)
	}
	starts := 0
	for _, p := range h.app.MockAudio.Played() {
		if p.Label == "record_start" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("the microphone was opened %d times; the closed schedule must end the session after the first turn", starts)
	}
}
