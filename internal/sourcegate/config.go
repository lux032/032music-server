package sourcegate

import (
	"log/slog"
	"net/url"
	"os"
	"strings"
)

// BaseURLFromEnv permits only HTTP(S) overrides, primarily for isolated tests.
func BaseURLFromEnv(logger *slog.Logger, key, fallback string) string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		logger.Warn("invalid source base URL, using default", "variable", key)
		return fallback
	}
	return strings.TrimRight(raw, "/")
}
