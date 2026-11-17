package aiclient

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

type stubChat struct {
	fn func(tagging.ChatRequest) (tagging.ChatResult, error)
}

func (s stubChat) Complete(_ context.Context, req tagging.ChatRequest) (tagging.ChatResult, error) {
	return s.fn(req)
}

func TestCompleteWithFallbackAndFormatDowngrade(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var seen []string
	chat := stubChat{fn: func(req tagging.ChatRequest) (tagging.ChatResult, error) {
		seen = append(seen, req.Model+":"+req.Format)
		if req.Model == "primary" && req.Format == "json_schema" {
			return tagging.ChatResult{}, &tagging.LLMError{Kind: tagging.KindFormat, Status: 400, Message: "json_schema unsupported"}
		}
		if req.Model == "primary" {
			return tagging.ChatResult{}, &tagging.LLMError{Kind: tagging.KindRateLimit, Status: 429, Message: "rate limit"}
		}
		return tagging.ChatResult{
			Content: `{"ok":true}`,
			Usage:   storage.AIUsage{Requests: 1, PromptTokens: 120, CompletionTokens: 18},
		}, nil
	}}

	st := storage.AISettings{
		BaseURL:           "https://api.example.test/v1",
		APIKey:            "sk-test",
		Model:             "primary",
		FallbackModel:     "backup",
		RequestsPerMinute: 6000,
	}
	res, used, err := CompleteWithFallback(ctx, store, chat, NewLimiter(6000), st, tagging.ChatRequest{
		System: "sys",
		User:   "usr",
	}, "album_review")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if used != "backup" || res.Content != `{"ok":true}` {
		t.Fatalf("got model=%q content=%q", used, res.Content)
	}
	if len(seen) != 3 || seen[0] != "primary:json_schema" || seen[1] != "primary:json_object" || seen[2] != "backup:json_object" {
		t.Fatalf("unexpected attempt sequence: %v", seen)
	}
	totals, err := store.AIUsageTotals(ctx, "")
	if err != nil || len(totals) != 1 || totals[0].Purpose != "album_review" || totals[0].Usage.Requests != 1 {
		t.Fatalf("unexpected usage totals: %+v, err=%v", totals, err)
	}
}
