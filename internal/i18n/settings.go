package i18n

import "context"

// Setting keys in system_settings.
const (
	KeyInterface    = "i18n.interface_language"    // the admin web UI
	KeyNotification = "i18n.notification_language" // Telegram messages and other notices, unless a person has their own
)

// Settings is the part of the store that language lookups need.
type Settings interface {
	GetSetting(ctx context.Context, key string) (string, error)
}

func setting(ctx context.Context, s Settings, key string) string {
	v, err := s.GetSetting(ctx, key)
	if err != nil {
		return Default
	}
	return Resolve(v)
}

// Interface returns the language of the admin web UI.
func Interface(ctx context.Context, s Settings) string { return setting(ctx, s, KeyInterface) }

// Notification returns the default language of messages sent to people.
func Notification(ctx context.Context, s Settings) string { return setting(ctx, s, KeyNotification) }

// ForUser returns a person's own language, or the notification language when they have none.
func ForUser(ctx context.Context, s Settings, userLanguage string) string {
	if c, ok := Normalize(userLanguage); ok {
		return c
	}
	return Notification(ctx, s)
}
