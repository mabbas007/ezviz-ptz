// Package logging configures the process-wide structured logger from LOG_LEVEL.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// Setup installs a text slog handler at the requested level. Unknown or empty
// levels fall back to INFO, matching the Python bridge.
func Setup(level string) {
	var parsed slog.Level
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "DEBUG":
		parsed = slog.LevelDebug
	case "WARNING", "WARN":
		parsed = slog.LevelWarn
	case "ERROR":
		parsed = slog.LevelError
	default:
		parsed = slog.LevelInfo
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: parsed,
	})))
}
