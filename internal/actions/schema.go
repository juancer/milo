package actions

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// ParamType is the JSON type of a parameter.
type ParamType string

const (
	String  ParamType = "string"
	Integer ParamType = "integer"
	Number  ParamType = "number"
	Boolean ParamType = "boolean"
	// Bytes carries binary data between in-process callers (for example a
	// Telegram voice note). It is never offered to an LLM.
	Bytes ParamType = "bytes"
)

// Param describes one parameter. It doubles as a JSON-schema fragment for LLM tools.
type Param struct {
	Name        string
	Description string
	Type        ParamType
	Required    bool
	Enum        []string // for strings
	Min, Max    *float64 // for numbers and integers
	MaxLen      int      // for strings; 0 means 500
}

// Range is a helper for Min/Max literals.
func Range(min, max float64) (*float64, *float64) { return &min, &max }

const defaultMaxLen = 500

func validateSchema(params []Param) error {
	seen := map[string]bool{}
	for _, p := range params {
		if p.Name == "" || seen[p.Name] {
			return fmt.Errorf("parameter %q is empty or duplicated", p.Name)
		}
		seen[p.Name] = true
		switch p.Type {
		case String, Integer, Number, Boolean, Bytes:
		default:
			return fmt.Errorf("parameter %q has unsupported type %q", p.Name, p.Type)
		}
	}
	return nil
}

// validateArgs checks raw (JSON-decoded) parameters against the schema and
// returns cleaned values: integers as int, numbers as float64. Unknown
// parameters are rejected, so an LLM cannot smuggle extra fields through.
func validateArgs(params []Param, raw map[string]any) (Args, error) {
	byName := make(map[string]Param, len(params))
	for _, p := range params {
		byName[p.Name] = p
	}
	out := Args{}
	for name, v := range raw {
		p, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown parameter %q", name)
		}
		cv, err := coerce(p, v)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", name, err)
		}
		out[name] = cv
	}
	for _, p := range params {
		if _, ok := out[p.Name]; p.Required && !ok {
			return nil, fmt.Errorf("missing required parameter %q", p.Name)
		}
	}
	return out, nil
}

func coerce(p Param, v any) (any, error) {
	switch p.Type {
	case String:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("must be a string")
		}
		limit := p.MaxLen
		if limit == 0 {
			limit = defaultMaxLen
		}
		if len([]rune(s)) > limit {
			return nil, fmt.Errorf("must be at most %d characters", limit)
		}
		if strings.ContainsRune(s, 0) {
			return nil, errors.New("must not contain NUL")
		}
		if len(p.Enum) > 0 {
			for _, e := range p.Enum {
				if e == s {
					return s, nil
				}
			}
			return nil, fmt.Errorf("must be one of %v", p.Enum)
		}
		return s, nil
	case Bytes:
		b, ok := v.([]byte)
		if !ok {
			return nil, errors.New("must be binary data")
		}
		limit := p.MaxLen
		if limit == 0 {
			limit = 8 << 20
		}
		if len(b) > limit {
			return nil, fmt.Errorf("must be at most %d bytes", limit)
		}
		return b, nil
	case Boolean:
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("must be a boolean")
		}
		return b, nil
	case Integer, Number:
		f, ok := toFloat(v)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, errors.New("must be a number")
		}
		if p.Min != nil && f < *p.Min || p.Max != nil && f > *p.Max {
			return nil, fmt.Errorf("out of range %v..%v", deref(p.Min), deref(p.Max))
		}
		if p.Type == Integer {
			if f != math.Trunc(f) {
				return nil, errors.New("must be a whole number")
			}
			return int(f), nil
		}
		return f, nil
	}
	return nil, errors.New("unsupported type")
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func deref(p *float64) any {
	if p == nil {
		return "any"
	}
	return *p
}

// JSONSchema renders the parameters as a JSON-schema object for LLM tool definitions.
func JSONSchema(params []Param) map[string]any {
	props := map[string]any{}
	var required []string
	for _, p := range params {
		if p.Type == Bytes {
			continue
		}
		prop := map[string]any{"type": string(p.Type), "description": p.Description}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		if p.Min != nil {
			prop["minimum"] = *p.Min
		}
		if p.Max != nil {
			prop["maximum"] = *p.Max
		}
		if p.Type == String {
			limit := p.MaxLen
			if limit == 0 {
				limit = defaultMaxLen
			}
			prop["maxLength"] = limit
		}
		props[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
