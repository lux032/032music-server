package httpapi

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

type fallbackTranslator struct {
	calls []string
	mode  string
}

func (f *fallbackTranslator) Complete(ctx context.Context, req tagging.ChatRequest) (tagging.ChatResult, error) {
	f.calls = append(f.calls, req.Model)
	if req.Model == "primary" && f.mode == "timeout" {
		<-ctx.Done()
		return tagging.ChatResult{}, ctx.Err()
	}
	if f.mode == "both-fail" || (req.Model == "primary" && f.mode != "success") {
		return tagging.ChatResult{}, &tagging.LLMError{Kind: tagging.KindServer}
	}
	return tagging.ChatResult{Content: `{"lines":["你好"]}`, Usage: storage.AIUsage{Requests: 1, PromptTokens: 10, CompletionTokens: 5}}, nil
}

func TestLyricsFallback(t *testing.T) {
	for _, mode := range []string{"success", "fallback", "both-fail", "timeout", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			app, store, _, _, id := lyricsTestApp(t, "Song", "hello")
			ctx := context.Background()
			if err := store.SaveLyricsTranslationSettings(ctx, storage.LyricsTranslationSettings{FallbackModel: "backup"}); err != nil {
				t.Fatal(err)
			}
			fake := &fallbackTranslator{mode: mode}
			app.newTranslationChat = func(storage.AISettings) (tagging.Chat, error) { return fake, nil }
			if mode == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 300*time.Millisecond)
				defer cancel()
			}
			if mode == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := app.runLyricsTranslation(ctx, storage.AISettings{}, "primary", storage.LyricsTranslation{TrackID: id, TargetLanguage: "zh", SourceHash: "hash", SourceLines: []string{"hello"}})
			if mode == "canceled" {
				if err == nil || len(fake.calls) != 0 {
					t.Fatalf("calls=%v err=%v", fake.calls, err)
				}
				return
			}
			wantCalls := 2
			if mode == "success" {
				wantCalls = 1
			}
			if len(fake.calls) != wantCalls {
				t.Fatalf("calls=%v", fake.calls)
			}
			if mode == "both-fail" {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantModel := "backup"
			if mode == "success" {
				wantModel = "primary"
			}
			saved, err := store.LyricsTranslationByID(ctx, got.ID)
			if err != nil || saved.Model != wantModel || saved.TranslatedLines[0] != "你好" {
				t.Fatalf("saved=%+v err=%v", saved, err)
			}
		})
	}
}

func TestFallbackSettingsForms(t *testing.T) {
	app, store, handler, cookie, _ := lyricsTestApp(t, "Song", "hello")
	postSecurity(t, app, handler, cookie, "/admin/ai-tagging/settings", url.Values{"fallback_model": {" tag-backup "}})
	ai, err := store.AISettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ai.FallbackModel != "tag-backup" {
		t.Fatalf("AI fallback=%q", ai.FallbackModel)
	}
	postSecurity(t, app, handler, cookie, lyricsTranslationPath+"/settings", url.Values{"fallback_model": {" translation-backup "}})
	tr, err := store.LyricsTranslationSettings(context.Background())
	if err != nil || tr.FallbackModel != "translation-backup" {
		t.Fatalf("translation=%+v err=%v", tr, err)
	}
	ai, _ = store.AISettings(context.Background())
	if ai.FallbackModel != "tag-backup" {
		t.Fatal("translation settings overwrote tagging")
	}
	for _, path := range []string{"/admin/ai-tagging", lyricsTranslationPath} {
		page := getWithCookie(handler, path, cookie)
		if page.Code != 200 || !strings.Contains(page.Body.String(), `name="fallback_model"`) {
			t.Fatalf("page %s: %d", path, page.Code)
		}
	}
}
