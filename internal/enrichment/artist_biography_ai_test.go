package enrichment

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
	"github.com/lux032/032music-server/internal/websearch"
)

type fakeBioChat struct {
	fn func(ctx context.Context, req tagging.ChatRequest) (tagging.ChatResult, error)
}

func (f fakeBioChat) Complete(ctx context.Context, req tagging.ChatRequest) (tagging.ChatResult, error) {
	return f.fn(ctx, req)
}

type fakeBioWeb struct {
	results []websearch.SearchResult
	pages   map[string]string
}

func (f fakeBioWeb) Search(_ context.Context, _ string) ([]websearch.SearchResult, error) {
	return f.results, nil
}

func (f fakeBioWeb) Fetch(_ context.Context, pageURL string) (string, error) {
	return f.pages[pageURL], nil
}

func TestBiographyInfoScoreAndSkipDecision(t *testing.T) {
	zhStub := biographyCandidate{
		Source:   "wikipedia",
		Language: "zh",
		Text:     "米津玄师是一位日本歌手。",
	}
	zhStub.Score = biographyInfoScore(zhStub.Source, zhStub.Text)

	jaFull := biographyCandidate{
		Source:   "wikipedia",
		Language: "ja",
		Text:     strings.Repeat("米津玄師は日本のシンガーソングライター、音楽プロデューサー、イラストレーターである。", 6),
	}
	jaFull.Score = biographyInfoScore(jaFull.Source, jaFull.Text)

	if shouldSkipAITranslation(zhStub, jaFull) {
		t.Fatalf("expected short zh stub (score %.1f) not to skip translation of rich ja bio (score %.1f)", zhStub.Score, jaFull.Score)
	}

	zhComplete := biographyCandidate{
		Source:   "wikipedia",
		Language: "zh",
		Text:     strings.Repeat("米津玄师是日本创作歌手、音乐制作人及插画家，早期以ハチ名义在网络发表作品。", 6),
	}
	zhComplete.Score = biographyInfoScore(zhComplete.Source, zhComplete.Text)
	if !shouldSkipAITranslation(zhComplete, jaFull) {
		t.Fatalf("expected complete zh bio (score %.1f) to skip translation against ja bio (score %.1f)", zhComplete.Score, jaFull.Score)
	}
}

func TestBiographyCandidateLanguages(t *testing.T) {
	sitelinks := map[string]string{
		"ja": "米津玄師",
		"en": "Kenshi Yonezu",
		"zh": "米津玄師",
		"fr": "Kenshi Yonezu",
		"de": "Kenshi Yonezu",
	}
	got := biographyCandidateLanguages(sitelinks, "JP", "zh", []string{"zh", "ja", "en"})
	want := []string{"zh", "ja", "en"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCleanBiographyForScoringStripsLastFMBanner(t *testing.T) {
	raw := `Aimer is a Japanese pop singer. <a href="https://www.last.fm/music/Aimer">Read more on Last.fm</a>.`
	got := cleanBiographyForScoring("lastfm", raw)
	if strings.Contains(got, "Read more on Last.fm") {
		t.Fatalf("expected Last.fm boilerplate stripped, got %q", got)
	}
}

func TestParseJSONObjectStripsThinkBlock(t *testing.T) {
	raw := "<think>internal reasoning</think>\n```json\n{\"biography\":\"测试简介\"}\n```"
	var out struct {
		Biography string `json:"biography"`
	}
	if err := parseJSONObject(raw, &out); err != nil {
		t.Fatalf("parseJSONObject failed: %v", err)
	}
	if out.Biography != "测试简介" {
		t.Fatalf("got %q, want 测试简介", out.Biography)
	}
}

func TestContainsWebMetaPhrase(t *testing.T) {
	if !containsWebMetaPhrase("根据搜索结果，该歌手是一位日本作曲家。") {
		t.Fatalf("expected Chinese meta phrase to be rejected")
	}
	if containsWebMetaPhrase("该歌手出生于日本东京，主要活跃于独立摇滚领域。") {
		t.Fatalf("unexpected rejection of clean encyclopedic biography")
	}
}

func TestBiographyBackfillRetranslateUsesCachedSources(t *testing.T) {
	ctx := context.Background()
	bt := newBioTransport()
	manager, artist := bioBackfillManager(t, bt, "Bio Artist")
	confirmBioIdentity(t, manager.store, artist, "mbid-bio-1")
	configureBiographyAI(t, manager, true)
	enableBackfillSource(t, manager.store, "lastfm", true)
	const original = "A cached Japanese artist biography."
	if err := manager.store.UpsertArtistBiography(ctx, artist, storage.ArtistBiography{Source: "wikipedia", Language: "ja", Biography: original}); err != nil {
		t.Fatal(err)
	}
	manager.SetAIChatFactory(func(storage.AISettings) (tagging.Chat, error) {
		return fakeBioChat{fn: func(_ context.Context, req tagging.ChatRequest) (tagging.ChatResult, error) {
			if !strings.Contains(req.User, original) {
				return tagging.ChatResult{}, fmt.Errorf("translation input missing cached text: %s", req.User)
			}
			return tagging.ChatResult{Content: `{"biography":"这是一位日本歌手。"}`}, nil
		}}, nil
	})
	runID, err := manager.StartArtistBiographyBackfillWithMode(ctx, "retranslate")
	if err != nil {
		t.Fatal(err)
	}
	finished := waitBioBackfillRun(t, manager.store, runID)
	manager.Wait()
	if finished.Filled != 1 || bt.mbReqs.Load()+bt.wikidataReqs.Load()+bt.wikiReqs.Load()+bt.lastfmReqs.Load() != 0 {
		t.Fatalf("expected one translation with no source requests, run=%+v", finished)
	}
	detail, err := manager.store.ArtistDetail(ctx, artist)
	if err != nil || detail.BiographySource != "ai_translated" || detail.BiographyOriginLanguage != "ja" || detail.BiographyLanguage != "zh-Hans" {
		t.Fatalf("biography source=%q language=%q origin language=%q err=%v", detail.BiographySource, detail.BiographyLanguage, detail.BiographyOriginLanguage, err)
	}
}

func TestManualWebBiographyWithBatchDisabled(t *testing.T) {
	const pageURL = "https://example.com/bio-artist"
	for _, sameArtist := range []bool{false, true} {
		t.Run(fmt.Sprintf("sameArtist=%t", sameArtist), func(t *testing.T) {
			ctx := context.Background()
			bt := newBioTransport()
			bt.mbBody = `{"id":"mbid-bio-1","name":"Bio Artist"}`
			manager, artist := bioBackfillManager(t, bt, "Bio Artist")
			confirmBioIdentity(t, manager.store, artist, "mbid-bio-1")
			configureBiographyAI(t, manager, false)
			manager.SetBiographyWebFactory(func(storage.ArtistAliasSettings, storage.AISettings) (websearch.Client, error) {
				return fakeBioWeb{results: []websearch.SearchResult{{URL: pageURL, Content: strings.Repeat("Bio Artist recorded music in Japan. ", 4)}}}, nil
			})
			manager.SetAIChatFactory(func(storage.AISettings) (tagging.Chat, error) {
				return fakeBioChat{fn: func(_ context.Context, _ tagging.ChatRequest) (tagging.ChatResult, error) {
					return tagging.ChatResult{Content: fmt.Sprintf(`{"sameArtist":%t,"confidence":"high","evidencePages":[0],"biography":"这是一位活跃于日本独立音乐界的歌手，创作并录制了多首歌曲，其作品涵盖流行和摇滚风格。"}`, sameArtist)}, nil
				}}, nil
			})
			if err := manager.RefreshArtistBiographiesWithOptions(ctx, artist, true, BiographyRefreshOptions{AllowWebFallback: true}); err != nil {
				t.Fatal(err)
			}
			detail, err := manager.store.ArtistDetail(ctx, artist)
			if err != nil {
				t.Fatal(err)
			}
			if sameArtist && (detail.BiographySource != "ai_web" || detail.BiographyURL != pageURL) {
				t.Fatalf("expected visible generated biography, source=%q url=%q", detail.BiographySource, detail.BiographyURL)
			}
			if !sameArtist && detail.Biography != "" {
				t.Fatalf("unexpected biography for mismatched artist: %q", detail.Biography)
			}
		})
	}
}

func configureBiographyAI(t *testing.T, manager *Manager, translate bool) {
	t.Helper()
	ctx := context.Background()
	if err := manager.store.SaveBiographySettings(ctx, storage.BiographySettings{WikipediaEnabled: true, TargetLanguage: "zh-Hans", AITranslateEnabled: translate}); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.SaveAISettings(ctx, storage.AISettings{BaseURL: "https://example.com", APIKey: "test", Model: "test-model", RequestsPerMinute: 60, Concurrency: 1}); err != nil {
		t.Fatal(err)
	}
}
