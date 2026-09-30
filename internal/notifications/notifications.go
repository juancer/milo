// Package notifications plays Milo's notification sounds, honouring the
// parents' settings (sounds on/off, volume). Sounds never reveal message
// content or the sender.
package notifications

import (
	"context"
	"strconv"

	"github.com/juancer/milo/internal/audio"
)

// Setting keys in system_settings.
const (
	KeyMessageSound      = "notifications.new_message_sound_enabled"
	KeyEmergencySound    = "notifications.emergency_sound_enabled"
	KeyNotifyAfterWindow = "notifications.notify_pending_after_schedule"
	KeyVolume            = "notifications.volume"
)

// Config are the parent-editable notification settings.
type Config struct {
	NewMessageSound    bool `json:"new_message_sound_enabled"`
	EmergencySound     bool `json:"emergency_sound_enabled"`
	NotifyPendingAfter bool `json:"notify_pending_after_schedule"`
	Volume             int  `json:"volume"` // 0-100, applied to notification sounds only
}

// Settings reads and writes settings.
type Settings interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Load returns the current configuration; unset keys use safe defaults.
func Load(ctx context.Context, s Settings) Config {
	get := func(key string, def bool) bool {
		v, err := s.GetSetting(ctx, key)
		if err != nil {
			return def
		}
		return v != "0"
	}
	c := Config{
		NewMessageSound:    get(KeyMessageSound, true),
		EmergencySound:     get(KeyEmergencySound, true),
		NotifyPendingAfter: get(KeyNotifyAfterWindow, true),
		Volume:             100,
	}
	if v, err := s.GetSetting(ctx, KeyVolume); err == nil {
		if n, err := strconv.Atoi(v); err == nil {
			c.Volume = max(0, min(100, n))
		}
	}
	return c
}

// Save persists the configuration.
func Save(ctx context.Context, s Settings, c Config) error {
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	for k, v := range map[string]string{
		KeyMessageSound:      b(c.NewMessageSound),
		KeyEmergencySound:    b(c.EmergencySound),
		KeyNotifyAfterWindow: b(c.NotifyPendingAfter),
		KeyVolume:            strconv.Itoa(max(0, min(100, c.Volume))),
	} {
		if err := s.SetSetting(ctx, k, v); err != nil {
			return err
		}
	}
	return nil
}

// Notifier plays notification sounds.
type Notifier struct {
	audio    audio.Audio
	settings Settings
}

// New returns a Notifier.
func New(a audio.Audio, s Settings) *Notifier { return &Notifier{audio: a, settings: s} }

// Notify plays a built-in sound. Emergency sounds interrupt whatever is playing.
// A disabled sound type is silently skipped.
func (n *Notifier) Notify(ctx context.Context, kind audio.Notification) error {
	cfg := Load(ctx, n.settings)
	switch kind {
	case audio.NotifyMessage:
		if !cfg.NewMessageSound {
			return nil
		}
	case audio.NotifyEmergency:
		if !cfg.EmergencySound {
			return nil
		}
		n.audio.Stop()
	}
	return n.audio.PlayNotification(ctx, kind, cfg.Volume)
}
