package i18n

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var placeholderRe = regexp.MustCompile(`\{[a-z_]+\}`)

func placeholders(s string) string {
	found := placeholderRe.FindAllString(s, -1)
	sort.Strings(found)
	return strings.Join(found, ",")
}

// Every language must have exactly the keys English has, with the same placeholders, so a
// translation can never show a raw key or lose a value.
func TestEveryLocaleMatchesEnglish(t *testing.T) {
	en := catalog[Default]
	if len(en) == 0 {
		t.Fatal("English catalog is empty")
	}
	for code, m := range catalog {
		if m[keyName] == "" || m[keyEnglishName] == "" {
			t.Errorf("%s: locale.name and locale.english_name are required", code)
		}
		for k, v := range en {
			tv, ok := m[k]
			switch {
			case !ok:
				t.Errorf("%s: missing key %q", code, k)
			case placeholders(v) != placeholders(tv):
				t.Errorf("%s: %q placeholders %q differ from English %q", code, k, placeholders(tv), placeholders(v))
			}
		}
		for k := range m {
			if _, ok := en[k]; !ok {
				t.Errorf("%s: key %q does not exist in English", code, k)
			}
		}
	}
}

func TestFallbackToEnglishAndToKey(t *testing.T) {
	old := catalog
	defer func() { catalog = old }()
	catalog = map[string]map[string]string{
		"en": {"a": "Hello {name}", "only_en": "English only"},
		"fr": {"a": "Bonjour {name}"},
	}
	cases := []struct{ lang, key, want string }{
		{"fr", "a", "Bonjour Ann"},
		{"fr", "only_en", "English only"}, // missing translation -> English
		{"xx", "a", "Hello Ann"},          // unknown language -> English
		{"", "a", "Hello Ann"},
		{"fr", "nothing", "nothing"}, // unknown key -> the key, never a crash
	}
	for _, c := range cases {
		if got := T(c.lang, c.key, map[string]any{"name": "Ann"}); got != c.want {
			t.Errorf("T(%q, %q) = %q, want %q", c.lang, c.key, got, c.want)
		}
	}
}

func TestNormalizeAndSupported(t *testing.T) {
	for in, want := range map[string]string{"en": "en", "ES": "es", "es-MX": "es", "es_ES": "es", " en ": "en"} {
		if got, ok := Normalize(in); !ok || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "auto", "xx", "-"} {
		if _, ok := Normalize(bad); ok {
			t.Errorf("Normalize(%q) must fail", bad)
		}
	}
	got := Supported()
	if len(got) < 2 || got[0].Code != "en" || got[0].Name != "English" || got[1].Code != "es" || got[1].Name != "Español" {
		t.Errorf("Supported() = %+v", got)
	}
	if EnglishName("es") != "Spanish" || EnglishName("zz") != "English" {
		t.Errorf("EnglishName wrong: %q, %q", EnglishName("es"), EnglishName("zz"))
	}
}

func TestSpecExamples(t *testing.T) {
	for _, c := range []struct{ lang, key, want string }{
		{"en", "telegram.not_authorized", "You are not authorized to use this device."},
		{"es", "telegram.not_authorized", "No tienes autorización para utilizar este dispositivo."},
		{"es", "telegram.emergency_confirm", "¿Enviar alerta de emergencia?"},
	} {
		if got := T(c.lang, c.key, nil); !strings.HasPrefix(got, c.want) {
			t.Errorf("%s %s = %q", c.lang, c.key, got)
		}
	}
}

type fakeSettings map[string]string

func (f fakeSettings) GetSetting(_ context.Context, k string) (string, error) {
	if v, ok := f[k]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}

func TestSettingsDefaultToEnglishAndUserOverrides(t *testing.T) {
	ctx := context.Background()
	if Interface(ctx, fakeSettings{}) != "en" || Notification(ctx, fakeSettings{}) != "en" {
		t.Error("defaults must be English")
	}
	s := fakeSettings{KeyInterface: "es", KeyNotification: "es"}
	if Interface(ctx, s) != "es" || ForUser(ctx, s, "") != "es" || ForUser(ctx, s, "en") != "en" || ForUser(ctx, s, "junk") != "es" {
		t.Error("language resolution wrong")
	}
}

// Every translation key used in Go code must exist in English.
func TestKeysUsedInCodeExist(t *testing.T) {
	re := regexp.MustCompile(`"(web\.(?:err|notice)\.[a-z0-9_]+|assistant\.reply_[a-z_]+)"|\b(?:tr|Tf|T)\([^"]*"((?:telegram|assistant|skills)\.[a-z0-9_]+)"`)
	root := filepath.Join("..")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, _ := os.ReadFile(p)
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			key := m[1] + m[2]
			if _, ok := catalog[Default][key]; !ok {
				t.Errorf("%s uses unknown key %q", p, key)
			}
		}
		return nil
	})
}
