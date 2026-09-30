package moderation

import (
	"strings"
	"testing"
)

func TestBuiltinBlockedAndConcernInput(t *testing.T) {
	c := New(nil)
	cases := []struct {
		text         string
		blocked, con bool
	}{
		{"what is the tallest mountain?", false, false},
		{"tell me a story about a dragon", false, false},
		{"how to make a bomb", true, false},
		{"HOW   TO MAKE A BOMB!!", true, false},
		{"I want to die", true, true},
		{"sometimes I want to die when I am sad", true, true},
		{"me quiero morir", true, true},
		{"someone touched me in a bad way", true, true},
		{"I like the word bombastic", false, false}, // whole words only
		{"the class did a bomb-shell presentation", false, false},
	}
	for _, tc := range cases {
		v := c.Check(tc.text, Input)
		if v.Blocked != tc.blocked || v.Concern != tc.con {
			t.Errorf("%q = %+v, want blocked=%v concern=%v", tc.text, v, tc.blocked, tc.con)
		}
		if v.Blocked && v.Reply == "" {
			t.Errorf("%q blocked without a reply", tc.text)
		}
	}
	if v := c.Check("I want to die", Input); v.Reply != ReplyConcern {
		t.Errorf("concern must get the supportive reply, got %q", v.Reply)
	}
}

func TestConcernPhrasesOnlyApplyToWhatTheChildSays(t *testing.T) {
	// A model saying "if you want to die..." style text is handled by output rules, not concern alerts.
	if v := New(nil).Check("people who want to die need help", Output); v.Concern {
		t.Fatal("concern alerts must only come from the child's own words")
	}
}

func TestParentRulesRespectDirectionAndEnabled(t *testing.T) {
	c := New([]Rule{
		{Pattern: "Voldemort", AppliesTo: "input", Enabled: true},
		{Pattern: "scary clown", AppliesTo: "output", Enabled: true},
		{Pattern: "both ways", AppliesTo: "both", Enabled: true},
		{Pattern: "disabled word", AppliesTo: "both", Enabled: false},
		{Pattern: "   ", AppliesTo: "both", Enabled: true},
	})
	if !c.Check("tell me about voldemort please", Input).Blocked {
		t.Error("input rule missed (case-insensitive)")
	}
	if c.Check("Voldemort", Output).Blocked {
		t.Error("input-only rule applied to output")
	}
	if !c.Check("a scary clown appeared", Output).Blocked {
		t.Error("output rule missed")
	}
	if c.Check("scary clown", Input).Blocked {
		t.Error("output-only rule applied to input")
	}
	if !c.Check("this works both ways", Input).Blocked || !c.Check("this works both ways", Output).Blocked {
		t.Error("both-direction rule missed")
	}
	if c.Check("a disabled word here", Input).Blocked {
		t.Error("disabled rule applied")
	}
	if v := c.Check("Voldemort", Input); v.Rule != "Voldemort" {
		t.Errorf("rule label = %q", v.Rule)
	}
	if c.Check("hello there", Input).Blocked {
		t.Error("an empty pattern must not match everything")
	}
}

func TestRegexMetacharactersInParentRulesAreLiteral(t *testing.T) {
	c := New([]Rule{{Pattern: "a.c (x)*", AppliesTo: "both", Enabled: true}})
	if c.Check("abc xx", Input).Blocked {
		t.Error("pattern was interpreted as a regular expression")
	}
	if !c.Check("say a c x", Input).Blocked {
		t.Error("literal words should match ignoring punctuation")
	}
}

func TestClamp(t *testing.T) {
	long := "One two three four five. Six seven eight nine ten. Eleven twelve thirteen fourteen fifteen."
	if got := Clamp(long, 10); got != "One two three four five. Six seven eight nine ten." {
		t.Errorf("clamp at sentence = %q", got)
	}
	if got := Clamp("no punctuation here at all for the whole thing", 4); got != "no punctuation here at." {
		t.Errorf("clamp without sentence = %q", got)
	}
	if got := Clamp("short one", 50); got != "short one" {
		t.Errorf("no clamp needed = %q", got)
	}
	if strings.Count(Clamp(strings.Repeat("word ", 500), 60), " ")+1 > 61 {
		t.Error("clamp too long")
	}
}

func TestZeroWidthDoesNotBypassAndCustomRuleKeepsConcern(t *testing.T) {
	c := New([]Rule{{Pattern: "die", AppliesTo: "both", Enabled: true}})
	if v := New(nil).Check("how to make a bo​mb", Input); !v.Blocked {
		t.Errorf("zero-width split bypassed the blocklist: %+v", v)
	}
	if v := c.Check("I want to die", Input); !v.Concern || v.Reply != ReplyConcern {
		t.Errorf("custom rule hid the concern: %+v", v)
	}
	if v := c.Check("die hard", Input); v.Concern || !v.Blocked {
		t.Errorf("plain custom match wrong: %+v", v)
	}
}
