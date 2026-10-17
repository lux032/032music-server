package tagging

import (
	"context"
	"errors"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func TestShouldFallback(t *testing.T) {
	for _, kind := range []ErrorKind{KindAuth, KindModel, KindRateLimit, KindQuota, KindServer, KindNetwork, KindBadRequest, KindFormat, KindFiltered} {
		want := kind != KindBadRequest && kind != KindFormat && kind != KindFiltered
		err := &LLMError{Kind: kind}
		if got := ShouldFallback(context.Background(), "main", "backup", err); got != want {
			t.Errorf("%s: %v", kind, got)
		}
		for _, backup := range []string{"", " main "} {
			if ShouldFallback(context.Background(), "main", backup, err) {
				t.Fatal("empty or identical model retried")
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ShouldFallback(ctx, "main", "backup", &LLMError{Kind: KindNetwork}) {
		t.Fatal("canceled task retried")
	}
	for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, errors.New("invalid result")} {
		if ShouldFallback(context.Background(), "main", "backup", err) {
			t.Fatalf("retried %v", err)
		}
	}
}

func TestTaggingFallback(t *testing.T) {
	for _, mode := range []string{"success", "fallback", "both-fail", "format"} {
		t.Run(mode, func(t *testing.T) {
			fx := newFixture(t, 1)
			fx.settings(t, func(v *storage.AISettings) { v.TaggingEnabled = true; v.FallbackModel = "backup" })
			fx.chat.respond = func(req ChatRequest, _ int) (ChatResult, error) {
				if mode == "both-fail" || (mode != "success" && req.Model == "lite") {
					return ChatResult{}, &LLMError{Kind: KindServer, Message: "unavailable"}
				}
				if mode == "format" && req.Format == "json_schema" {
					return ChatResult{}, &LLMError{Kind: KindFormat}
				}
				return okResult(goodJSON)
			}
			fx.drain(t, 10)
			wantCalls := 1
			if mode != "success" {
				wantCalls = 2
			}
			if mode == "format" {
				wantCalls = 3
			}
			if fx.chat.count() != wantCalls {
				t.Fatalf("calls=%d want=%d", fx.chat.count(), wantCalls)
			}
			meta, found, err := fx.store.TrackTagMeta(fx.ctx, fx.ids[0])
			if err != nil {
				t.Fatal(err)
			}
			if mode == "both-fail" {
				if found {
					t.Fatal("saved failed result")
				}
				return
			}
			want := "backup"
			if mode == "success" {
				want = "lite"
			}
			if !found || meta.Model != want {
				t.Fatalf("meta=%+v found=%v", meta, found)
			}
		})
	}
}
