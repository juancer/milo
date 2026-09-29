package system

import (
	"bytes"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != DefaultDataDir || cfg.Listen != ":8080" || cfg.KeyPath != DefaultKeyPath || cfg.Hardware != "real" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.DBPath() != "/var/lib/nexo/nexo.db" {
		t.Fatalf("DBPath = %s", cfg.DBPath())
	}
}

func TestLoadConfigKeyPrecedence(t *testing.T) {
	cfg, _ := LoadConfig(env(map[string]string{"CREDENTIALS_DIRECTORY": "/run/creds"}))
	if cfg.KeyPath != "/run/creds/master.key" {
		t.Fatalf("systemd credential not used: %s", cfg.KeyPath)
	}
	cfg, _ = LoadConfig(env(map[string]string{"CREDENTIALS_DIRECTORY": "/run/creds", "NEXO_MASTER_KEY_FILE": "/x.key"}))
	if cfg.KeyPath != "/x.key" {
		t.Fatalf("explicit key file must win: %s", cfg.KeyPath)
	}
}

func TestLoadConfigRejectsBadHardware(t *testing.T) {
	if _, err := LoadConfig(env(map[string]string{"NEXO_HARDWARE": "quantum"})); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoggerRedactsSensitiveFields(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		var buf bytes.Buffer
		log, err := NewLogger(&buf, "info", format)
		if err != nil {
			t.Fatal(err)
		}
		log.Info("x", "telegram_token", "SUPERSECRET1", "password", "SUPERSECRET2", "openrouter_api_key", "SUPERSECRET3", "user", "visible")
		out := buf.String()
		if strings.Contains(out, "SUPERSECRET") {
			t.Fatalf("%s log leaked a secret: %s", format, out)
		}
		if !strings.Contains(out, "visible") {
			t.Fatalf("%s log dropped a normal field: %s", format, out)
		}
	}
}

func TestNewLoggerValidation(t *testing.T) {
	var buf bytes.Buffer
	if _, err := NewLogger(&buf, "loud", "text"); err == nil {
		t.Fatal("bad level accepted")
	}
	if _, err := NewLogger(&buf, "info", "xml"); err == nil {
		t.Fatal("bad format accepted")
	}
}

func TestIsSensitiveKey(t *testing.T) {
	for _, k := range []string{"token", "Bot_Token", "wifi_password", "key", "master_key", "client_secret"} {
		if !IsSensitiveKey(k) {
			t.Errorf("%q should be sensitive", k)
		}
	}
	for _, k := range []string{"user", "monkey_count", "feature", "keyboard"} {
		if IsSensitiveKey(k) {
			t.Errorf("%q should not be sensitive", k)
		}
	}
}
