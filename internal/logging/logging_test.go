package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"", slog.LevelInfo, false},
		{"info", slog.LevelInfo, false},
		{"INFO", slog.LevelInfo, false},
		{"debug", slog.LevelDebug, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"trace", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseLevel(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestRedactsSensitiveTopLevel(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelDebug)

	log.Info("auth attempt",
		"user", "alice",
		"password", "hunter2",
		"PASS", "literal-secret",
		"api_key", "sk-abc",
		"token", "t-123",
	)

	out := buf.String()
	if strings.Contains(out, "hunter2") {
		t.Errorf("password leaked: %s", out)
	}
	if strings.Contains(out, "literal-secret") {
		t.Errorf("PASS (case-insensitive) leaked: %s", out)
	}
	// "key" is sensitive; "api_key" is not the same literal key — it should NOT be redacted.
	if !strings.Contains(out, "sk-abc") {
		t.Errorf("api_key (not literal 'key') was incorrectly redacted: %s", out)
	}
	if strings.Contains(out, "t-123") {
		t.Errorf("token leaked: %s", out)
	}
	if !strings.Contains(out, "user=alice") {
		t.Errorf("non-sensitive attr missing: %s", out)
	}
}

func TestRedactsInsideGroup(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelDebug)

	log.Info("exec",
		slog.Group("creds",
			slog.String("password", "pw1"),
			slog.String("user", "bob"),
		),
		slog.Group("cmd",
			slog.String("argv", "op read op://x/y"),
		),
	)

	out := buf.String()
	if strings.Contains(out, "pw1") {
		t.Errorf("grouped password leaked: %s", out)
	}
	if strings.Contains(out, "op read op://x/y") {
		t.Errorf("argv leaked: %s", out)
	}
	if !strings.Contains(out, "bob") {
		t.Errorf("non-sensitive grouped attr dropped: %s", out)
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelWarn)

	log.Debug("debug-line")
	log.Info("info-line")
	log.Warn("warn-line")
	log.Error("error-line")

	out := buf.String()
	if strings.Contains(out, "debug-line") || strings.Contains(out, "info-line") {
		t.Errorf("below-threshold records should be filtered: %s", out)
	}
	if !strings.Contains(out, "warn-line") || !strings.Contains(out, "error-line") {
		t.Errorf("at/above-threshold records should pass: %s", out)
	}
}
