package tagging

import (
	"context"
	"errors"
	"strings"
)

// ShouldFallback only retries availability errors, never invalid output, rejected
// content or canceled tasks. Identical model names must not cause a second call.
func ShouldFallback(ctx context.Context, primary, fallback string, err error) bool {
	fallback = strings.TrimSpace(fallback)
	if err == nil || ctx.Err() != nil || fallback == "" || fallback == strings.TrimSpace(primary) {
		return false
	}
	var le *LLMError
	if !errors.As(err, &le) {
		return false
	}
	switch le.Kind {
	case KindAuth, KindModel, KindRateLimit, KindQuota, KindServer, KindNetwork:
		return true
	default:
		return false
	}
}
