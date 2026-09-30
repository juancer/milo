// Package i18n turns translation keys into text for the current locale.
//
// Each language is one folder, locales/<code>/messages.json, holding a flat map of
// "dotted.key": "text with {placeholders}". Adding a language means adding a folder;
// no Go code changes. English is the source language and the fallback: a missing
// locale or key falls back to English, and a key missing there is returned as is, so
// a gap in the translations never crashes anything or shows an empty message.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Default is the source and fallback language.
const Default = "en"

// Auto is the assistant-only language setting that follows the language the child speaks.
const Auto = "auto"

// Reserved keys inside each messages.json describing the language itself.
const (
	keyName        = "locale.name"         // native name, e.g. "Español"
	keyEnglishName = "locale.english_name" // used in prompts for the AI, e.g. "Spanish"
)

//go:embed locales/*/messages.json
var localeFS embed.FS

// Locale describes a supported language.
type Locale struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

var catalog = load(localeFS)

func load(fsys fs.FS) map[string]map[string]string {
	out := map[string]map[string]string{}
	dirs, _ := fs.ReadDir(fsys, "locales")
	for _, d := range dirs {
		raw, err := fs.ReadFile(fsys, "locales/"+d.Name()+"/messages.json")
		if err != nil {
			continue
		}
		m := map[string]string{}
		if json.Unmarshal(raw, &m) != nil {
			continue // a broken file must not take the others down; the test suite catches it
		}
		out[strings.ToLower(d.Name())] = m
	}
	return out
}

// Normalize maps "es", "ES", "es-MX" or "es_ES" to a supported code ("es").
func Normalize(code string) (string, bool) {
	c := strings.ToLower(strings.TrimSpace(code))
	if _, ok := catalog[c]; ok {
		return c, true
	}
	if i := strings.IndexAny(c, "-_"); i > 0 {
		if _, ok := catalog[c[:i]]; ok {
			return c[:i], true
		}
	}
	return "", false
}

// Resolve returns the supported code for lang, or Default when it is empty or unknown.
func Resolve(lang string) string {
	if c, ok := Normalize(lang); ok {
		return c
	}
	return Default
}

// Supported lists the installed languages, sorted by code.
func Supported() []Locale {
	out := make([]Locale, 0, len(catalog))
	for code, m := range catalog {
		name := m[keyName]
		if name == "" {
			name = code
		}
		out = append(out, Locale{Code: code, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// EnglishName returns the language's name in English ("es" -> "Spanish"), for AI prompts.
func EnglishName(code string) string {
	c := Resolve(code)
	if n := catalog[c][keyEnglishName]; n != "" {
		return n
	}
	return c
}

// T returns the text for key in lang, substituting {name} placeholders from args.
func T(lang, key string, args map[string]any) string {
	text, ok := catalog[Resolve(lang)][key]
	if !ok {
		if text, ok = catalog[Default][key]; !ok {
			return key
		}
	}
	if len(args) == 0 || !strings.Contains(text, "{") {
		return text
	}
	pairs := make([]string, 0, len(args)*2)
	for k, v := range args {
		pairs = append(pairs, "{"+k+"}", fmt.Sprint(v))
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

// Tf is shorthand for T with alternating name, value arguments.
func Tf(lang, key string, kv ...any) string {
	args := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			args[k] = kv[i+1]
		}
	}
	return T(lang, key, args)
}
