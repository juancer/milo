// Package moderation is Nexo's own safety layer around the assistant. It runs
// in the application, in addition to whatever the model does, and never trusts
// the model: what the child says and what the model answers are both checked.
//
// It is a safety net, not a guarantee. It combines
//   - parent-defined blocked phrases (moderation_rules),
//   - a small built-in list of things a child assistant should not discuss, and
//   - "concern" phrases where a child may need help from a trusted adult; those
//     get a fixed, kind answer instead of a model-generated one, and can alert
//     the parents.
package moderation

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Direction says what is being checked.
type Direction string

const (
	Input  Direction = "input"  // what the child said
	Output Direction = "output" // what the model wants to say
)

// Verdict is the result of a check.
type Verdict struct {
	Blocked bool
	Concern bool   // the child may need a trusted adult
	Rule    string // which rule matched (a label, or the parent's phrase)
	Reply   string // what to say instead
}

// Reply texts.
const (
	ReplyBlocked = "Let's talk about something else! Ask me about animals, space, stories or games."
	ReplyConcern = "That sounds important. Please tell a grown-up you trust right now. I'm here too, and you are not alone."
)

// Rule is a parent-defined blocked phrase.
type Rule struct {
	ID        int64
	Pattern   string
	AppliesTo string // input, output, both
	Enabled   bool
}

// Checker evaluates text.
type Checker struct {
	custom  []compiled
	blocked []*regexp.Regexp
	concern []*regexp.Regexp
}

type compiled struct {
	re      *regexp.Regexp
	pattern string
	input   bool
	output  bool
}

// Built-in lists: deliberately short and conservative. Parents extend them with
// their own phrases. Matching is case-insensitive on whole words.
var (
	builtinBlocked = []string{
		"how to make a bomb", "how to make a weapon", "how to buy drugs", "where to buy drugs", "how to get a gun",
		"porn", "pornography", "sex video", "naked pictures", "send nudes",
		"cómo hacer una bomba", "como hacer una bomba", "comprar drogas", "fotos desnudos",
	}
	builtinConcern = []string{
		"kill myself", "want to die", "wanna die", "hurt myself", "hate my life", "nobody loves me",
		"someone touched me", "touched me in a bad way", "hits me", "hurts me at home", "i am scared of my",
		"me quiero morir", "quiero morirme", "hacerme daño", "me pegan", "me tocó", "me toco donde no debe",
	}
)

// New builds a Checker from parent-defined rules.
func New(rules []Rule) *Checker {
	c := &Checker{}
	for _, r := range rules {
		if !r.Enabled || strings.TrimSpace(r.Pattern) == "" {
			continue
		}
		c.custom = append(c.custom, compiled{
			re: phrase(r.Pattern), pattern: strings.TrimSpace(r.Pattern),
			input: r.AppliesTo != "output", output: r.AppliesTo != "input",
		})
	}
	for _, p := range builtinBlocked {
		c.blocked = append(c.blocked, phrase(p))
	}
	for _, p := range builtinConcern {
		c.concern = append(c.concern, phrase(p))
	}
	return c
}

// phrase matches the words in p, case-insensitively, on word boundaries.
func phrase(p string) *regexp.Regexp {
	fields := strings.FieldsFunc(strings.ToLower(p), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' })
	quoted := make([]string, len(fields))
	for i, f := range fields {
		quoted[i] = regexp.QuoteMeta(f)
	}
	return regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}])` + strings.Join(quoted, `[^\p{L}\p{N}']+`) + `($|[^\p{L}\p{N}])`)
}

// Check evaluates text travelling in the given direction.
func (c *Checker) Check(text string, dir Direction) Verdict {
	// Zero-width and other format characters (Cf) split words without being
	// visible; drop them. No golang.org/x/text here, so no NFKC: homoglyphs
	// from other scripts are not folded.
	text = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
	concern := func() bool {
		if dir != Input {
			return false
		}
		for _, re := range c.concern {
			if re.MatchString(text) {
				return true
			}
		}
		return false
	}
	for _, r := range c.custom {
		if (dir == Input && r.input || dir == Output && r.output) && r.re.MatchString(text) {
			if concern() { // a parent rule must not hide a concern from the parents
				return Verdict{Blocked: true, Concern: true, Rule: "concern", Reply: ReplyConcern}
			}
			return Verdict{Blocked: true, Rule: r.pattern, Reply: ReplyBlocked}
		}
	}
	if concern() {
		return Verdict{Blocked: true, Concern: true, Rule: "concern", Reply: ReplyConcern}
	}
	for _, re := range c.blocked {
		if re.MatchString(text) {
			return Verdict{Blocked: true, Rule: "built-in", Reply: ReplyBlocked}
		}
	}
	return Verdict{}
}

// MaxReplyBytes bounds any model reply that is kept or spoken; the speech engines cut at about
// this size anyway.
const MaxReplyBytes = 4000

// Cap shortens text to at most MaxReplyBytes, on a character boundary. It bounds text that has no
// spaces, which a word count does not.
func Cap(text string) string {
	if len(text) <= MaxReplyBytes {
		return text
	}
	cut := MaxReplyBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// Clamp shortens text to at most maxWords words (and MaxReplyBytes bytes), ending at a sentence
// boundary when there is one, so a spoken answer never rambles.
func Clamp(text string, maxWords int) string {
	text = Cap(text)
	words := strings.Fields(text)
	if maxWords <= 0 || len(words) <= maxWords {
		return strings.TrimSpace(text)
	}
	short := strings.Join(words[:maxWords], " ")
	if i := strings.LastIndexAny(short, ".!?"); i > len(short)/2 {
		return short[:i+1]
	}
	return short + "."
}
