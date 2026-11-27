package enrichment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
	"github.com/lux032/032music-server/internal/websearch"
)

func TestArtistImageHostRateLimitIsolation(t *testing.T) {
	if got := artistImageDownloadSource("https://Upload.Wikimedia.org:443/wiki/File.jpg"); got != "artist image:upload.wikimedia.org" {
		t.Fatalf("artistImageDownloadSource = %q", got)
	}
	if got := artistImageDownloadSource("not-a-url"); got != "artist image" {
		t.Fatalf("invalid url fallback = %q", got)
	}

	goodPNG := makeAvatarPNG(t, 280, 280)
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "93.184.216.34":
			return cannedResponse(http.StatusTooManyRequests, http.Header{"Retry-After": {"60"}}, ""), nil
		case "93.184.216.35":
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"image/png"}}, goodPNG), nil
		default:
			return cannedResponse(http.StatusNotFound, http.Header{}, ""), nil
		}
	}))

	ctx := context.Background()
	_, _, _, err := manager.downloadPublicImage(ctx, "http://93.184.216.34/a.png", "artist image")
	rate := asRateLimited(err)
	if rate == nil || rate.Source != "artist image:93.184.216.34" {
		t.Fatalf("host A rate error = (%+v, %v), want artist image:93.184.216.34", rate, err)
	}
	if manager.checkSourceCooldown("artist image:93.184.216.34") == nil {
		t.Fatal("expected host A to be in cooldown")
	}
	if manager.checkSourceCooldown("artist image:93.184.216.35") != nil {
		t.Fatal("host B must not inherit host A cooldown")
	}
	data, _, _, err := manager.downloadPublicImage(ctx, "http://93.184.216.35/b.png", "artist image")
	if err != nil || len(data) == 0 {
		t.Fatalf("host B download failed despite host isolation: len=%d err=%v", len(data), err)
	}

	// Verify expired host cooldown entries are pruned once map grows past 256.
	past := time.Now().Add(-time.Minute)
	for i := 0; i < 270; i++ {
		manager.blockSourceUntil(fmt.Sprintf("artist image:expired-%d.example", i), past)
	}
	manager.blockSourceUntil("artist image:live.example", time.Now().Add(time.Minute))
	manager.cooldownMu.Lock()
	remaining := len(manager.blockedUntil)
	_, hasLive := manager.blockedUntil["artist image:live.example"]
	manager.cooldownMu.Unlock()
	if remaining > 32 || !hasLive {
		t.Fatalf("expected expired host cooldowns pruned and live entry kept, remaining=%d hasLive=%v", remaining, hasLive)
	}
}

func TestArtistImageMultiSourceAndTier3RateLimitCascadesWithoutStalling(t *testing.T) {
	ctx := context.Background()
	goodWebPNG := makeAvatarPNG(t, 360, 360)
	goodWebHash := fmt.Sprintf("%x", sha256.Sum256([]byte(goodWebPNG)))

	var hostAReqs atomic.Int32
	var hostCReqs atomic.Int32
	var sawFilteredHostAInLLM atomic.Bool

	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/v0/search/persons"):
			var body struct {
				Keyword string `json:"keyword"`
			}
			if r.Body != nil {
				_ = json.NewDecoder(r.Body).Decode(&body)
			}
			switch body.Keyword {
			case "CascadeArtist":
				return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"application/json"}}, `{"data":[{"id":1,"name":"CascadeArtist","career":["artist"],"images":{"large":"http://93.184.216.34/bgm-429.png"}}]}`), nil
			case "Host429OnlyArtist":
				return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"application/json"}}, `{"data":[{"id":2,"name":"Host429OnlyArtist","career":["artist"],"images":{"large":"http://93.184.216.34/bgm-429-2.png"}}]}`), nil
			default:
				return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"application/json"}}, `{"data":[]}`), nil
			}
		case r.URL.Host == "93.184.216.34":
			hostAReqs.Add(1)
			return cannedResponse(http.StatusTooManyRequests, http.Header{"Retry-After": {"120"}}, ""), nil
		case r.URL.Host == "93.184.216.35" && r.URL.Path == "/page/cascade":
			html := `<html><head><title>CascadeArtist</title><meta property="og:image" content="http://93.184.216.35/img/good.png"></head></html>`
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"text/html"}}, html), nil
		case r.URL.Host == "93.184.216.35" && r.URL.Path == "/img/good.png":
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"image/png"}}, goodWebPNG), nil
		case r.URL.Host == "93.184.216.36" && r.URL.Path == "/page/web429":
			// Includes both already-cooled-down host A URL and fresh host C URL that returns 429.
			html := `<html><head><title>Tier3BlockedArtist</title><meta property="og:image" content="http://93.184.216.34/cooled.png"><meta name="twitter:image" content="http://93.184.216.36/blocked.png"></head></html>`
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"text/html"}}, html), nil
		case r.URL.Host == "93.184.216.36" && r.URL.Path == "/blocked.png":
			hostCReqs.Add(1)
			return cannedResponse(http.StatusTooManyRequests, http.Header{"Retry-After": {"180"}}, ""), nil
		default:
			return cannedResponse(http.StatusNotFound, http.Header{}, `{}`), nil
		}
	}))
	manager.sleep = func(context.Context, time.Duration) error { return nil }

	chat := &scriptedAuditChat{
		handler: func(req tagging.ChatRequest) (tagging.ChatResult, error) {
			if len(req.Images) == 0 {
				if strings.Contains(req.User, "Tier3BlockedArtist") && strings.Contains(req.User, "93.184.216.34/cooled.png") {
					sawFilteredHostAInLLM.Store(true)
				}
				return tagging.ChatResult{Content: `{"sameArtist":true,"selectedImageIndex":0,"confidence":"high","reason":"官方头像"}`}, nil
			}
			return tagging.ChatResult{Content: `{"valid":true,"confidence":95,"reason":"清晰本人头像"}`}, nil
		},
	}
	manager.SetAIChatFactory(func(storage.AISettings) (tagging.Chat, error) { return chat, nil })
	manager.bioWebFactory = func(storage.ArtistAliasSettings, storage.AISettings) (websearch.Client, error) {
		return queueStubWebClient{
			searchFn: func(_ context.Context, query string) ([]websearch.SearchResult, error) {
				switch {
				case strings.Contains(query, "CascadeArtist"):
					return []websearch.SearchResult{{URL: "http://93.184.216.35/page/cascade", Title: "CascadeArtist"}}, nil
				case strings.Contains(query, "Tier3BlockedArtist"):
					return []websearch.SearchResult{{URL: "http://93.184.216.36/page/web429", Title: "Tier3BlockedArtist"}}, nil
				default:
					return nil, nil
				}
			},
		}, nil
	}

	if err := manager.store.SaveMetadataSourceSetting(ctx, storage.MetadataSourceSetting{Source: "bangumi", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.SaveAISettings(ctx, storage.AISettings{
		BaseURL: "https://llm.example/v1", APIKey: "sk-cascade", Model: "gpt-6-luna", RequestsPerMinute: 600, Concurrency: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.SaveArtistImageSettings(ctx, storage.ArtistImageSettings{
		BangumiEnabled:       true,
		DeezerEnabled:        false,
		WebFallbackEnabled:   true,
		WebAutoApplyHigh:     true,
		VisionEnabled:        true,
		MaxWebRequestsPerRun: 50,
		NegativeCacheDays:    30,
		AuditAutoConfidence:  85,
		AutoIngestAIEnabled:  true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.EnsureLibrary(ctx, "CascadeLib", "/cascade-music"); err != nil {
		t.Fatal(err)
	}
	lib, err := manager.store.LibraryByRoot(ctx, "/cascade-music")
	if err != nil {
		t.Fatal(err)
	}

	cascadeID := seedAuditArtist(t, manager, lib.ID, "c/1.flac", "CascadeArtist")
	tier3BlockedID := seedAuditArtist(t, manager, lib.ID, "c/2.flac", "Tier3BlockedArtist")
	host429OnlyID := seedAuditArtist(t, manager, lib.ID, "c/3.flac", "Host429OnlyArtist")
	_ = tier3BlockedID

	runID, err := manager.StartArtistImageBackfillWithMode(ctx, "audit_all")
	if err != nil {
		t.Fatalf("StartArtistImageBackfillWithMode: %v", err)
	}
	run := waitForArtistImageRun(t, manager, runID)
	if run.Status != "completed" || run.Processed != 3 || run.Cached != 1 || run.NoURL != 2 {
		t.Fatalf("run = %+v, want completed processed=3 cached=1 noUrl=2", run)
	}
	if run.WaitTotalMS != 0 || run.WaitSource != "" {
		t.Fatalf("image host 429 must not stall multi-source audit run: waitTotal=%d waitSource=%q", run.WaitTotalMS, run.WaitSource)
	}
	// CascadeArtist must have fallen through Bangumi 429 to Tier 3 web portrait on host B.
	eff, err := manager.store.ArtistEffectiveImageCache(ctx, cascadeID)
	if err != nil || eff.ContentHash != goodWebHash || eff.Source != "ai_web" {
		t.Fatalf("CascadeArtist cache = (%+v, %v), want ai_web %s", eff, err, goodWebHash)
	}
	// Host A should only be hit ONCE (for CascadeArtist); subsequent artists must skip via per-host cooldown.
	if hostAReqs.Load() != 1 {
		t.Fatalf("host A requests = %d, want 1 (subsequent artists skipped via host cooldown)", hostAReqs.Load())
	}
	if sawFilteredHostAInLLM.Load() {
		t.Fatal("cooled-down host A candidate URL should have been filtered before LLM selection")
	}
	if hostCReqs.Load() != 1 {
		t.Fatalf("host C requests = %d, want 1", hostCReqs.Load())
	}
	// Host429OnlyArtist hit transient host 429 -> must NOT write a 30-day negative cache cooldown!
	var attemptCount int
	if err := writableSafetyDB(t, manager).QueryRowContext(ctx, `SELECT count(*) FROM artist_image_attempts WHERE artist_id=?`, host429OnlyID).Scan(&attemptCount); err != nil {
		t.Fatal(err)
	}
	if attemptCount != 0 {
		t.Fatalf("Host429OnlyArtist should not get long negative cache after transient host 429, got %d rows", attemptCount)
	}
}
