package enrichment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
	"github.com/lux032/032music-server/internal/websearch"
)

func makeValidAvatarPNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 300, 300))
	img.Set(0, 0, color.RGBA{R: 50, G: 100, B: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

type stubImageWebClient struct {
	results []websearch.SearchResult
}

func (s *stubImageWebClient) Search(_ context.Context, _ string) ([]websearch.SearchResult, error) {
	return s.results, nil
}

func (s *stubImageWebClient) Fetch(_ context.Context, _ string) (string, error) {
	return "", nil
}

type stubImageChatClient struct {
	content string
}

func (s *stubImageChatClient) Complete(_ context.Context, _ tagging.ChatRequest) (tagging.ChatResult, error) {
	return tagging.ChatResult{Content: s.content, Usage: storage.AIUsage{Requests: 1, PromptTokens: 100, CompletionTokens: 30}}, nil
}

func TestComprehensiveImageBackfill_BangumiTier2(t *testing.T) {
	pngBytes := makeValidAvatarPNG(t)
	var bgmCalls, imgCalls int
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Host, "api.bgm.tv") && strings.Contains(r.URL.Path, "/v0/search/persons"):
			bgmCalls++
			return cannedResponse(http.StatusOK, http.Header{}, `{"data":[{"id":12345,"name":"星街すいせい","career":["artist"],"images":{"large":"http://93.184.216.34/suisei.png"}}]}`), nil
		case r.URL.Host == "93.184.216.34":
			imgCalls++
			return cannedResponse(http.StatusOK, http.Header{}, pngBytes), nil
		default:
			return cannedResponse(http.StatusNotFound, http.Header{}, `{}`), nil
		}
	}))
	ctx := context.Background()
	setting, err := manager.store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil {
		t.Fatal(err)
	}
	setting.Enabled = true
	if err := manager.store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := manager.store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.store.ImportTrack(ctx, storage.ImportInput{
		LibraryID:    lib.ID,
		RelativePath: "suisei/01.flac",
		FileSize:     1,
		ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title:        "Stellar Stellar",
			Album:        "Still Still Stellar",
			Artists:      []string{"星街すいせい"},
			AlbumArtists: []string{"星街すいせい"},
			DiscNumber:   1,
			TrackNumber:  1,
		},
	}); err != nil {
		t.Fatal(err)
	}

	runID, err := manager.StartArtistImageBackfillWithMode(ctx, "all")
	if err != nil {
		t.Fatal(err)
	}
	run := waitImageBackfillRun(t, manager.store, runID)
	if run.Status != "completed" || run.Cached != 1 || bgmCalls != 1 || imgCalls != 1 {
		t.Fatalf("unexpected run=%+v bgmCalls=%d imgCalls=%d", run, bgmCalls, imgCalls)
	}
}

func TestMatchedImageBackfillFallsBackToBangumiPersonAlias(t *testing.T) {
	for _, name := range []string{"confirmed", "identity removed during download"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			manager, artistID := imageBackfillManager(t, newBackfillTransport(), "三好真亜沙", "")
			confirmBackfillIdentity(t, manager.store, artistID, "lastfm", "matched-lastfm-id", "")
			enableBackfillSource(t, manager.store, "lastfm", false)
			enableBackfillSource(t, manager.store, "bangumi", true)
			manager.setBangumiIntervalForTest(0)
			pngBytes := makeValidAvatarPNG(t)
			var requestErr error
			manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch {
				case r.URL.Host == "api.bgm.tv" && r.URL.Path == "/v0/search/persons":
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["keyword"] != "三好真亜沙" || len(body) != 1 {
						requestErr = fmt.Errorf("unexpected Bangumi search body: %v, err=%v", body, err)
					}
					return cannedResponse(http.StatusOK, http.Header{}, `{"data":[
						{"id":24,"name":"三好真亜沙","career":["writer"],"images":{"large":"http://93.184.216.34/wrong.png"}},
						{"id":123,"name":"Miyoshi","career":["seiyu"],"images":{"large":"https://lain.bgm.tv/pic/no_icon.png","grid":"http://93.184.216.34/portrait.png"}}
					]}`), nil
				case r.URL.Host == "api.bgm.tv" && r.URL.Path == "/v0/persons/123":
					return cannedResponse(http.StatusOK, http.Header{}, `{"infobox":[{"key":"艺名","value":[{"v":"三好　真・亜沙"}]}]}`), nil
				case r.URL.Host == "93.184.216.34" && r.URL.Path == "/portrait.png":
					if name != "confirmed" {
						requestErr = manager.store.ResetArtistIdentity(ctx, artistID, "lastfm", "matched-lastfm-id")
					}
					return cannedResponse(http.StatusOK, http.Header{}, pngBytes), nil
				default:
					requestErr = fmt.Errorf("unexpected backfill request: %s", r.URL)
					return nil, requestErr
				}
			})

			runID, err := manager.StartArtistImageBackfillWithMode(ctx, "matched")
			if err != nil {
				t.Fatal(err)
			}
			run := waitImageBackfillRun(t, manager.store, runID)
			manager.Wait()
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			_, cached := imageBackfillCacheRow(t, manager, artistID)
			if name == "confirmed" {
				if run.Status != "completed" || run.Cached != 1 || !cached {
					t.Fatalf("expected matched backfill to cache the Bangumi portrait, got run=%+v cached=%v", run, cached)
				}
			} else if run.Status != "completed" || run.Skipped != 1 || cached {
				t.Fatalf("expected reset identity to block caching, got run=%+v cached=%v", run, cached)
			}
		})
	}
}

func TestComprehensiveImageBackfill_WebFallbackQueuesMediumCandidate(t *testing.T) {
	pngBytes := makeValidAvatarPNG(t)
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "93.184.216.34":
			if strings.HasSuffix(r.URL.Path, "/profile") {
				hdr := http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}
				return cannedResponse(http.StatusOK, hdr, `<html><head><title>Indie Circle</title><meta property="og:image" content="http://93.184.216.34/avatar.png"></head></html>`), nil
			}
			return cannedResponse(http.StatusOK, http.Header{}, pngBytes), nil
		default:
			return cannedResponse(http.StatusNotFound, http.Header{}, `{}`), nil
		}
	}))
	ctx := context.Background()
	if err := manager.store.SaveAISettings(ctx, storage.AISettings{BaseURL: "https://api.example.com/v1", APIKey: "sk-test", Model: "test-model", RequestsPerMinute: 60, Concurrency: 1}); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.SaveArtistImageSettings(ctx, storage.ArtistImageSettings{
		BangumiEnabled:       false,
		DeezerEnabled:        false,
		WikipediaEnabled:     false,
		WebFallbackEnabled:   true,
		WebAutoApplyHigh:     true,
		MaxWebRequestsPerRun: 10,
		NegativeCacheDays:    30,
	}); err != nil {
		t.Fatal(err)
	}
	manager.SetBiographyWebFactory(func(storage.ArtistAliasSettings, storage.AISettings) (websearch.Client, error) {
		return &stubImageWebClient{results: []websearch.SearchResult{{URL: "http://93.184.216.34/profile", Title: "Indie Circle"}}}, nil
	})
	manager.SetAIChatFactory(func(storage.AISettings) (tagging.Chat, error) {
		return &stubImageChatClient{content: `{"sameArtist":true,"selectedImageIndex":0,"confidence":"medium","reason":"Official profile page match"}`}, nil
	})

	if err := manager.store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := manager.store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.store.ImportTrack(ctx, storage.ImportInput{
		LibraryID:    lib.ID,
		RelativePath: "indie/01.flac",
		FileSize:     1,
		ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title:        "Track 1",
			Album:        "First EP",
			Artists:      []string{"Indie Circle"},
			AlbumArtists: []string{"Indie Circle"},
			DiscNumber:   1,
			TrackNumber:  1,
		},
	}); err != nil {
		t.Fatal(err)
	}

	runID, err := manager.StartArtistImageBackfillWithMode(ctx, "web_fallback")
	if err != nil {
		t.Fatal(err)
	}
	run := waitImageBackfillRun(t, manager.store, runID)
	if run.Status != "completed" || run.Candidate != 1 {
		t.Fatalf("expected 1 candidate queued, got run=%+v", run)
	}
	cands, err := manager.store.PendingArtistImageCandidates(ctx, 10)
	if err != nil || len(cands) != 1 || cands[0].Confidence != "medium" {
		t.Fatalf("unexpected candidates: %+v err=%v", cands, err)
	}
	data, mimeType, err := manager.ArtistImageCandidatePreview(ctx, cands[0].ID)
	if err != nil || mimeType != "image/png" || string(data) != pngBytes {
		t.Fatalf("candidate preview: bytes=%d mime=%q err=%v", len(data), mimeType, err)
	}
}
