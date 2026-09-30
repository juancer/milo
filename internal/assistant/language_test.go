package assistant_test

import (
	"strings"
	"testing"

	"github.com/juancer/milo/internal/assistant"
)

// The configured language is stated to the model explicitly, and "auto" follows the child.
func TestLanguageDirective(t *testing.T) {
	if got := assistant.LanguageDirective("es"); !strings.Contains(got, "Spanish") || !strings.Contains(got, "unless") {
		t.Errorf("es directive = %q", got)
	}
	if got := assistant.LanguageDirective("en"); !strings.Contains(got, "English") {
		t.Errorf("en directive = %q", got)
	}
	for _, auto := range []string{"auto", "", "klingon"} {
		if got := assistant.LanguageDirective(auto); !strings.Contains(got, "language the child speaks") {
			t.Errorf("%q directive = %q", auto, got)
		}
	}
}
