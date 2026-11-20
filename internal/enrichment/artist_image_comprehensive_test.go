package enrichment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
	"github.com/lux032/032music-server/internal/websearch"
)

func makeValidAvatarPNG(t *testing.T) string {
	t.Helper()
	return makeAvatarPNG(t, 300, 300)
}

func makeAvatarPNG(t *testing.T, width, height int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
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

func TestComprehensiveImageBackfill_Tier2SourceSelection(t *testing.T) {
	const deezerXL = "http://93.184.216.34/deezer-xl.png"
	const bangumiMedium = "http://93.184.216.34/bangumi-medium.png"
	const search = "/search/artist"
	const firstAlbums = "/artist/1/albums"
	const secondAlbums = "/artist/2/albums"
	const bangumiSearch = "/v0/search/persons"

	largePortrait := makeAvatarPNG(t, 1000, 1000)
	smallPortrait := makeAvatarPNG(t, 100, 200)
	tinyIcon := makeAvatarPNG(t, 32, 32)
	for _, tt := range []struct {
		name        string
		candidates  string
		secondAlbum string
		deezerImage string
		wantSource  string
		wantURL     string
		wantReason  string
		wantPaths   []string
	}{
		{
			name: "verified Deezer XL wins",
			candidates: `{"data":[
				{"id":1,"name":"神前暁","nb_fan":500,"picture_xl":"http://93.184.216.34/wrong.png"},
				{"id":2,"name":"神前暁","nb_fan":1,"picture_xl":"` + deezerXL + `","picture_big":"http://93.184.216.34/deezer-big.png"}
			]}`,
			secondAlbum: "Album", deezerImage: largePortrait,
			wantSource: "deezer", wantURL: deezerXL, wantReason: "cached",
			wantPaths: []string{search, firstAlbums, secondAlbums, "/deezer-xl.png"},
		},
		{
			name: "multiple homonyms without album overlap",
			candidates: `{"data":[
				{"id":1,"name":"神前暁","nb_fan":500,"picture_xl":"http://93.184.216.34/wrong.png"},
				{"id":2,"name":"神前暁","nb_fan":1,"picture_xl":"` + deezerXL + `"}
			]}`,
			secondAlbum: "Other release",
			wantSource:  "bangumi", wantURL: bangumiMedium, wantReason: "no_match",
			wantPaths: []string{search, firstAlbums, secondAlbums, bangumiSearch, "/bangumi-medium.png"},
		},
		{
			name:       "single low-fan homonym without album overlap",
			candidates: `{"data":[{"id":1,"name":"神前暁","nb_fan":1,"picture_xl":"` + deezerXL + `"}]}`,
			wantSource: "bangumi", wantURL: bangumiMedium, wantReason: "no_match",
			wantPaths: []string{search, firstAlbums, bangumiSearch, "/bangumi-medium.png"},
		},
		{
			name:        "verified Deezer image below quality threshold",
			candidates:  `{"data":[{"id":2,"name":"神前暁","picture_xl":"` + deezerXL + `"}]}`,
			secondAlbum: "Album", deezerImage: tinyIcon,
			wantSource: "bangumi", wantURL: bangumiMedium, wantReason: "quality_rejected",
			wantPaths: []string{search, secondAlbums, "/deezer-xl.png", bangumiSearch, "/bangumi-medium.png"},
		},
		{
			name:        "verified Deezer image download fails",
			candidates:  `{"data":[{"id":2,"name":"神前暁","picture_xl":"` + deezerXL + `"}]}`,
			secondAlbum: "Album",
			wantSource:  "bangumi", wantURL: bangumiMedium, wantReason: "download_failed",
			wantPaths: []string{search, secondAlbums, "/deezer-xl.png", bangumiSearch, "/bangumi-medium.png"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			manager, artistID := imageBackfillManager(t, newBackfillTransport(), "神前暁", "")
			enableBackfillSource(t, manager.store, "bangumi", true)
			manager.setBangumiIntervalForTest(0)
			var logs bytes.Buffer
			manager.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

			var paths []string
			manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				paths = append(paths, r.URL.Path)
				status, body := http.StatusOK, ""
				switch r.URL.Path {
				case search:
					body = tt.candidates
				case firstAlbums:
					body = `{"data":[{"title":"Album tribute"}]}`
				case secondAlbums:
					body = fmt.Sprintf(`{"data":[{"title":%q}]}`, tt.secondAlbum)
				case "/deezer-xl.png":
					body = tt.deezerImage
					if body == "" {
						status = http.StatusNotFound
					}
				case bangumiSearch:
					body = `{"data":[
						{"id":11,"name":"神前暁","career":["writer"],"images":{"large":"http://93.184.216.34/wrong.png"}},
						{"id":12,"name":"神前暁","career":["seiyu"],"images":{"large":"http://93.184.216.34/no_icon.png"}},
						{"id":13,"name":"神前暁","career":["artist"]},
						{"id":14,"name":"神前 暁","career":["producer"],"images":{"medium":"` + bangumiMedium + `"}}
					]}`
				case "/bangumi-medium.png":
					body = smallPortrait
				default:
					return nil, fmt.Errorf("unexpected request: %s", r.URL)
				}
				return cannedResponse(status, http.Header{}, body), nil
			})

			runID, err := manager.StartArtistImageBackfillWithMode(ctx, "all")
			if err != nil {
				t.Fatal(err)
			}
			run := waitImageBackfillRun(t, manager.store, runID)
			manager.Wait()
			if run.Status != "completed" || run.Cached != 1 {
				t.Fatalf("expected a cached image, got %+v", run)
			}
			if !slices.Equal(paths, tt.wantPaths) {
				t.Fatalf("request paths = %v, want %v", paths, tt.wantPaths)
			}
			var source, remoteURL string
			db := writableSafetyDB(t, manager)
			err = db.QueryRowContext(ctx, "SELECT source, remote_url FROM artist_image_cache WHERE artist_id=?", artistID).Scan(&source, &remoteURL)
			if err != nil || source != tt.wantSource || remoteURL != tt.wantURL {
				t.Fatalf("cached source=%q url=%q err=%v, want %q %q", source, remoteURL, err, tt.wantSource, tt.wantURL)
			}
			if !strings.Contains(logs.String(), "source=deezer reason="+tt.wantReason) {
				t.Fatalf("missing Deezer result %q in logs: %s", tt.wantReason, &logs)
			}
		})
	}
}

func TestArtistAutoMatchFillsMissingImageWithTier2(t *testing.T) {
	const artist = "三好真亜沙"
	const mbid = "55555555-5555-4555-8555-555555555555"
	const deezerURL = "http://93.184.216.34/deezer-xl.png"
	const bangumiURL = "http://93.184.216.34/bangumi.png"
	portrait := makeAvatarPNG(t, 100, 200)
	for _, tt := range []struct {
		name          string
		deezerAlbum   string
		resetIdentity bool
		wantSource    string
	}{
		{name: "verified Deezer portrait", deezerAlbum: "Album", wantSource: "deezer"},
		{name: "Bangumi after unverified Deezer", deezerAlbum: "Unrelated", wantSource: "bangumi"},
		{name: "identity reset during Bangumi download", deezerAlbum: "Unrelated", resetIdentity: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			bt := newBackfillTransport()
			bt.mbBody = `{"id":"` + mbid + `","name":"` + artist + `","relations":[]}`
			manager, artistID := imageBackfillManager(t, bt, artist, mbid)
			enableBackfillSource(t, manager.store, "musicbrainz", true)
			enableBackfillSource(t, manager.store, "bangumi", true)
			manager.setBangumiIntervalForTest(0)
			var paths []string
			var resetErr error
			manager.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Host {
				case "api.deezer.com", "api.bgm.tv", "93.184.216.34":
					paths = append(paths, r.URL.Path)
				default:
					return bt.roundTrip(r)
				}
				var body string
				switch r.URL.Path {
				case "/search/artist":
					body = `{"data":[{"id":1,"name":"三好 真亜沙","picture_xl":"` + deezerURL + `"}]}`
				case "/artist/1/albums":
					body = fmt.Sprintf(`{"data":[{"title":%q}]}`, tt.deezerAlbum)
				case "/v0/search/persons":
					body = `{"data":[{"id":1,"name":"三好　真亜沙","career":["artist"],"images":{"medium":"` + bangumiURL + `"}}]}`
				case "/deezer-xl.png", "/bangumi.png":
					if tt.resetIdentity {
						resetErr = manager.store.ResetArtistIdentity(ctx, artistID, "musicbrainz", mbid)
					}
					body = portrait
				default:
					return nil, fmt.Errorf("unexpected image request: %s", r.URL)
				}
				return cannedResponse(http.StatusOK, http.Header{}, body), nil
			})

			runID, err := manager.StartAll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			run := waitArtistMatchRun(t, manager.store, runID)
			manager.Wait()
			if run.Status != "completed" || run.Matched != 1 || resetErr != nil {
				t.Fatalf("identity run=%+v reset error=%v", run, resetErr)
			}
			wantPaths := []string{"/search/artist", "/artist/1/albums", "/deezer-xl.png"}
			if tt.wantSource != "deezer" {
				wantPaths = []string{"/search/artist", "/artist/1/albums", "/v0/search/persons", "/bangumi.png"}
			}
			if !slices.Equal(paths, wantPaths) {
				t.Fatalf("request paths = %v, want %v", paths, wantPaths)
			}
			if tt.resetIdentity {
				if _, ok := imageBackfillCacheRow(t, manager, artistID); ok {
					t.Fatal("a reset identity must not receive the downloaded portrait")
				}
				return
			}
			var source string
			err = writableSafetyDB(t, manager).QueryRowContext(ctx, "SELECT source FROM artist_image_cache WHERE artist_id=?", artistID).Scan(&source)
			if err != nil || source != tt.wantSource {
				t.Fatalf("cached source=%q err=%v, want %q", source, err, tt.wantSource)
			}
		})
	}
}

func TestArtistImageSearchNormalizesJapaneseNames(t *testing.T) {
	for _, tt := range []struct {
		local, query, remote string
	}{
		{"神前暁 (MONACA)", "神前暁", "神前　暁"},
		{"花澤香菜（ＣＶ）", "花澤香菜", "花澤　香菜"},
		{"三好真亜沙", "三好真亜沙", "三好 真･亜沙"},
		{"ａｉｍｅｒ", "aimer", "AiMer"},
	} {
		t.Run(tt.local, func(t *testing.T) {
			anchor := storage.ArtistBiographyAnchor{Name: tt.local, Aliases: []string{"別名（歌手）"}}
			queries := buildArtistSearchNames(anchor)
			if !slices.Contains(queries, tt.query) || !slices.Contains(queries, "別名") {
				t.Fatalf("unexpected search queries: %v", queries)
			}
			if !normalizedAnchorNameSet(anchor)[compactArtistName(tt.remote)] {
				t.Fatalf("%q should match %q", tt.remote, tt.local)
			}
		})
	}
}

func TestBangumiPersonImageUsesCleanedQueryAndRejectsAmbiguousPortraits(t *testing.T) {
	const portrait = "http://93.184.216.34/portrait.png"
	for _, tt := range []struct {
		name       string
		candidates string
		wantImage  string
	}{
		{
			name:       "one matching portrait",
			candidates: `{"id":1,"name":"花澤　香菜","career":["seiyu"],"images":{"large":"` + portrait + `"}}`,
			wantImage:  portrait,
		},
		{
			name: "two plausible portraits",
			candidates: `{"id":1,"name":"花澤　香菜","career":["seiyu"],"images":{"large":"` + portrait + `"}},
				{"id":2,"name":"花澤香菜","career":["artist"],"images":{"medium":"http://93.184.216.34/other.png"}}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var queries []string
			var requestErr error
			manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || body["keyword"] == "" || r.Method != http.MethodPost || r.URL.Path != "/v0/search/persons" {
					requestErr = fmt.Errorf("unexpected Bangumi request: method=%s path=%s body=%v err=%v", r.Method, r.URL.Path, body, err)
					return nil, requestErr
				}
				keyword := body["keyword"]
				queries = append(queries, keyword)
				if keyword != "花澤香菜" {
					return cannedResponse(http.StatusOK, http.Header{}, `{"data":[]}`), nil
				}
				return cannedResponse(http.StatusOK, http.Header{}, `{"data":[`+tt.candidates+`]}`), nil
			}))
			enableBackfillSource(t, manager.store, "bangumi", true)
			manager.setBangumiIntervalForTest(0)

			img, _, err := manager.bangumiPersonImage(context.Background(), storage.ArtistBiographyAnchor{Name: "花澤香菜（ＣＶ）"})
			if requestErr != nil || err != nil || img != tt.wantImage || !slices.Equal(queries, []string{"花澤香菜(CV)", "花澤香菜"}) {
				t.Fatalf("image=%q want=%q queries=%v err=%v requestErr=%v", img, tt.wantImage, queries, err, requestErr)
			}
		})
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
			if err := manager.store.SaveArtistImageSettings(ctx, storage.ArtistImageSettings{BangumiEnabled: true}); err != nil {
				t.Fatal(err)
			}
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
