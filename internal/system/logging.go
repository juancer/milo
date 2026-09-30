package system

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// IsSensitiveKey reports whether a log or audit field name is likely to carry
// a credential. Values under such keys are always redacted.
func IsSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, marker := range []string{"token", "password", "passphrase", "secret", "apikey", "api_key", "credential",
		"psk", "authorization", "cookie", "bearer", "passwd", "pwd"} {
		if strings.Contains(k, marker) {
			return true
		}
	}
	return k == "key" || strings.HasSuffix(k, "_key")
}

// NewLogger builds the structured logger. format is "text" or "json".
func NewLogger(w io.Writer, level, format string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid log level %q (use debug, info, warn or error)", level)
	}
	opts := &slog.HandlerOptions{
		Level: lvl,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if IsSensitiveKey(a.Key) {
				return slog.String(a.Key, "[redacted]")
			}
			return a
		},
	}
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("invalid log format %q (use text or json)", format)
	}
}
