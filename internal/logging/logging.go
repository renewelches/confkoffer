// Package logging configures the process-wide slog logger.
//
// The handler scrubs the values of any attribute whose key matches a
// sensitive name (password, key, secret, etc.) — the redaction is
// applied recursively into groups so a nested attr like
// slog.Group("creds", "password", pw) is also redacted.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// sensitiveKeys are attribute keys whose values must never appear in logs.
// Matched case-insensitively against the leaf key name.
var sensitiveKeys = map[string]struct{}{
	"pass":     {},
	"password": {},
	"key":      {},
	"secret":   {},
	"argv":     {},
	"token":    {},
}

const redacted = "[REDACTED]"

// ParseLevel maps a string ("debug"/"info"/"warn"/"error") to a slog.Level.
// Unknown values return an error; callers should treat that as a config error.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q", s)
	}
}

// New returns a slog.Logger that writes text records to w at the given level
// and redacts sensitive attribute values.
func New(w io.Writer, level slog.Level) *slog.Logger {
	h := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redactAttr,
	})
	return slog.New(h)
}

// Init constructs a logger and installs it as slog.Default.
func Init(w io.Writer, level slog.Level) *slog.Logger {
	l := New(w, level)
	slog.SetDefault(l)
	return l
}

// InitFromEnv is a small convenience for callers that just want stderr
// at info level — used by tests and as a safe fallback.
func InitDefault() *slog.Logger {
	return Init(os.Stderr, slog.LevelInfo)
}

// redactAttr is the ReplaceAttr hook. It rewrites the value of any leaf
// attribute whose key is in sensitiveKeys; for group attrs it recurses.
func redactAttr(_ []string, a slog.Attr) slog.Attr {
	if isSensitive(a.Key) && a.Value.Kind() != slog.KindGroup {
		a.Value = slog.StringValue(redacted)
		return a
	}
	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		out := make([]slog.Attr, len(attrs))
		for i, child := range attrs {
			out[i] = redactAttr(nil, child)
		}
		a.Value = slog.GroupValue(out...)
	}
	return a
}

func isSensitive(key string) bool {
	_, ok := sensitiveKeys[strings.ToLower(key)]
	return ok
}
