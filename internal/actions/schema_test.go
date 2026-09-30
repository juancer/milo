package actions

import "testing"

func TestNumericParamsNeedBounds(t *testing.T) {
	lo, hi := Range(0, 10)
	for _, typ := range []ParamType{Integer, Number} {
		if err := validateSchema([]Param{{Name: "n", Type: typ}}); err == nil {
			t.Errorf("%s without Min/Max accepted", typ)
		}
		if err := validateSchema([]Param{{Name: "n", Type: typ, Min: lo}}); err == nil {
			t.Errorf("%s without Max accepted", typ)
		}
		if err := validateSchema([]Param{{Name: "n", Type: typ, Min: lo, Max: hi}}); err != nil {
			t.Errorf("%s with bounds rejected: %v", typ, err)
		}
	}
}
