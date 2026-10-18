package speech

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// PiperVoices speaks with one Piper voice per language: the voice of the language the text is in.
// A voice is one model file and a model speaks one language, so a Spanish voice reading English
// sounds wrong. Fallback is used when no voice is set for the language.
type PiperVoices struct {
	binary   string
	voices   map[string]string // language family -> model path
	fallback string            // model path, may be ""
	opts     TTSOptions
}

// NewPiperVoices validates the paths. At least one of voices and fallback must give a model.
func NewPiperVoices(binary string, voices map[string]string, fallback string) (*PiperVoices, error) {
	if binary == "" {
		binary = "piper"
	}
	if !validBinary(binary) {
		return nil, fmt.Errorf("speech: invalid piper binary %q", binary)
	}
	clean := map[string]string{}
	for lang, model := range voices {
		if !ValidModelPath(model) {
			return nil, fmt.Errorf("speech: invalid Piper voice model %q (expected a path to a .onnx file)", model)
		}
		clean[Family(lang)] = model
	}
	if fallback != "" && !ValidModelPath(fallback) {
		return nil, fmt.Errorf("speech: invalid Piper voice model %q (expected a path to a .onnx file)", fallback)
	}
	if len(clean) == 0 && fallback == "" {
		return nil, errors.New("speech: no Piper voice is set")
	}
	return &PiperVoices{binary: binary, voices: clean, fallback: fallback, opts: DefaultTTSOptions()}, nil
}

// WithOptions sets speed, volume and the language to speak: a fixed Language picks that language's
// voice; an empty one makes the language follow the text.
func (p *PiperVoices) WithOptions(o TTSOptions) *PiperVoices {
	p.opts = o
	return p
}

// modelFor picks the voice for text.
func (p *PiperVoices) modelFor(text string) string {
	langs := make([]string, 0, len(p.voices)+1)
	for l := range p.voices {
		langs = append(langs, l)
	}
	if f := ModelFamily(p.fallback); f != "" && p.voices[f] == "" {
		langs = append(langs, f) // the single voice chosen before per-language voices existed
	}
	sort.Strings(langs)

	lang := Family(p.opts.Language)
	if lang == "" && len(langs) > 1 {
		lang = DetectLanguage(text, langs)
	}
	if lang != "" && p.voices[lang] != "" {
		return p.voices[lang]
	}
	if p.fallback != "" {
		return p.fallback
	}
	first := ""
	for l := range p.voices {
		if first == "" || l < first {
			first = l
		}
	}
	return p.voices[first] // deterministic when nothing else decides
}

// Synthesize implements TTS.
func (p *PiperVoices) Synthesize(ctx context.Context, text string) ([]byte, error) {
	one, err := NewPiper(p.binary, p.modelFor(text))
	if err != nil {
		return nil, err
	}
	return one.WithOptions(p.opts).Synthesize(ctx, text)
}
