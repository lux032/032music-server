package aiclient

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

const defaultTimeout = 90 * time.Second

// Limiter is a shared uniform-spacing rate limiter across AI features using
// the same configured provider RPM budget.
type Limiter struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

func NewLimiter(perMinute int) *Limiter {
	l := &Limiter{}
	l.SetRate(perMinute)
	return l
}

func (l *Limiter) SetRate(perMinute int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if perMinute <= 0 {
		perMinute = 60
	}
	l.interval = time.Minute / time.Duration(perMinute)
}

func (l *Limiter) Wait(ctx context.Context) error {
	if l == nil {
		return ctx.Err()
	}
	l.mu.Lock()
	now := time.Now()
	t := l.next
	if t.Before(now) {
		t = now
	}
	l.next = t.Add(l.interval)
	l.mu.Unlock()

	d := t.Sub(now)
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ChatFactory constructs a Chat client from global AI settings.
type ChatFactory func(storage.AISettings) (tagging.Chat, error)

func DefaultChatFactory(st storage.AISettings) (tagging.Chat, error) {
	return tagging.NewChatClient(st.BaseURL, st.APIKey, st.ProxyURL, defaultTimeout)
}

// ResolveReviewModel picks the model for AI review tasks: explicit override
// first, then global review_model, then global primary model.
func ResolveReviewModel(ai storage.AISettings, rev storage.AIReviewSettings) string {
	if m := strings.TrimSpace(rev.Model); m != "" {
		return m
	}
	if m := strings.TrimSpace(ai.ReviewModel); m != "" {
		return m
	}
	return strings.TrimSpace(ai.Model)
}

// CompleteWithFallback executes a structured chat request, handling rate
// limiting, json_schema -> json_object downgrade, FallbackModel failover, and
// ai_usage accounting.
func CompleteWithFallback(
	ctx context.Context,
	store *storage.Store,
	chat tagging.Chat,
	limiter *Limiter,
	st storage.AISettings,
	req tagging.ChatRequest,
	purpose string,
) (tagging.ChatResult, string, error) {
	if strings.TrimSpace(req.Model) == "" {
		req.Model = strings.TrimSpace(st.Model)
	}
	if req.Format == "" {
		req.Format = "json_schema"
	}
	if req.ReasoningEffort == "" {
		req.ReasoningEffort = st.ReasoningEffort
	}

	runAttempt := func(model string, format string) (tagging.ChatResult, error) {
		if err := limiter.Wait(ctx); err != nil {
			return tagging.ChatResult{}, err
		}
		attemptReq := req
		attemptReq.Model = model
		attemptReq.Format = format
		res, err := chat.Complete(ctx, attemptReq)
		if store != nil && res.Usage.Requests > 0 {
			usageCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = store.AddAIUsage(usageCtx, time.Now(), model, purpose, res.Usage)
			cancel()
		}
		return res, err
	}

	model := req.Model
	format := req.Format
	res, err := runAttempt(model, format)
	if err != nil {
		var le *tagging.LLMError
		if errors.As(err, &le) && le.Kind == tagging.KindFormat && format == "json_schema" {
			format = "json_object"
			res, err = runAttempt(model, format)
		}
	}
	if err != nil && tagging.ShouldFallback(ctx, model, st.FallbackModel, err) {
		fallback := strings.TrimSpace(st.FallbackModel)
		fbRes, fbErr := runAttempt(fallback, format)
		if fbErr != nil {
			var le *tagging.LLMError
			if errors.As(fbErr, &le) && le.Kind == tagging.KindFormat && format == "json_schema" {
				fbRes, fbErr = runAttempt(fallback, "json_object")
			}
		}
		if fbErr == nil {
			return fbRes, fallback, nil
		}
		return fbRes, fallback, fbErr
	}
	return res, model, err
}
