package enrichment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
	"github.com/lux032/032music-server/internal/websearch"
)

func TestArtistImageUnifiedVLMGateBlocksBadStructuredAndFallsBackToWeb(t *testing.T) {
	ctx := context.Background()
	badCoverPNG := makeAvatarPNG(t, 280, 280)
	goodWebPNG := makeAvatarPNG(t, 360, 360)
	medBgmPNG := makeAvatarPNG(t, 310, 310)
	badCoverHash := fmt.Sprintf("%x", sha256.Sum256([]byte(badCoverPNG)))
	goodWebHash := fmt.Sprintf("%x", sha256.Sum256([]byte(goodWebPNG)))

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
			case "MedArtist":
				return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"application/json"}}, `{"data":[{"id":2,"name":"MedArtist","career":["artist"],"images":{"large":"http://93.184.216.34/img/med-bgm.png"}}]}`), nil
			case "Fail400Artist":
				return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"application/json"}}, `{"data":[{"id":3,"name":"Fail400Artist","career":["artist"],"images":{"large":"http://93.184.216.34/img/med-bgm.png"}}]}`), nil
			default:
				return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"application/json"}}, `{"data":[{"id":1,"name":"GateArtist","career":["artist"],"images":{"large":"http://93.184.216.34/img/bad-bgm-cover.png"}}]}`), nil
			}
		case r.URL.Host == "93.184.216.34" && r.URL.Path == "/img/bad-bgm-cover.png":
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"image/png"}}, badCoverPNG), nil
		case r.URL.Host == "93.184.216.34" && r.URL.Path == "/img/med-bgm.png":
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"image/png"}}, medBgmPNG), nil
		case r.URL.Host == "93.184.216.34" && r.URL.Path == "/profile/gate":
			html := `<html><head><title>GateArtist Official</title><meta property="og:image" content="http://93.184.216.34/img/good-web.png"></head></html>`
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"text/html"}}, html), nil
		case r.URL.Host == "93.184.216.34" && r.URL.Path == "/img/good-web.png":
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"image/png"}}, goodWebPNG), nil
		default:
			return cannedResponse(http.StatusNotFound, http.Header{}, `{}`), nil
		}
	}))
	manager.sleep = func(context.Context, time.Duration) error { return nil }

	var vlmCalls atomic.Int32
	chat := &scriptedAuditChat{
		handler: func(req tagging.ChatRequest) (tagging.ChatResult, error) {
			if len(req.Images) == 0 {
				return tagging.ChatResult{Content: `{"sameArtist":true,"selectedImageIndex":0,"confidence":"high","reason":"官方艺人头像"}`}, nil
			}
			n := vlmCalls.Add(1)
			if strings.Contains(req.User, "Fail400Artist") {
				return tagging.ChatResult{}, &tagging.LLMError{Kind: tagging.KindBadRequest, Status: 400, Message: "bad image payload"}
			}
			if strings.Contains(req.User, "MedArtist") {
				return tagging.ChatResult{Content: `{"valid":true,"confidence":70,"reason":"略微偏暗，建议人工确认"}`}, nil
			}
			if n == 1 {
				// First VLM call is for Bangumi album cover -> reject!
				return tagging.ChatResult{Content: `{"valid":false,"confidence":95,"reason":"明显是专辑封面，非艺人头像"}`}, nil
			}
			// Second VLM call is for web portrait -> accept high confidence!
			return tagging.ChatResult{Content: `{"valid":true,"confidence":93,"reason":"清晰本人肖像"}`}, nil
		},
	}
	manager.SetAIChatFactory(func(storage.AISettings) (tagging.Chat, error) { return chat, nil })
	manager.bioWebFactory = func(storage.ArtistAliasSettings, storage.AISettings) (websearch.Client, error) {
		return queueStubWebClient{
			searchFn: func(_ context.Context, query string) ([]websearch.SearchResult, error) {
				if strings.Contains(query, "GateArtist") {
					return []websearch.SearchResult{{URL: "http://93.184.216.34/profile/gate", Title: "GateArtist Official"}}, nil
				}
				return nil, nil
			},
		}, nil
	}

	if err := manager.store.SaveMetadataSourceSetting(ctx, storage.MetadataSourceSetting{
		Source: "bangumi", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.SaveAISettings(ctx, storage.AISettings{
		BaseURL: "https://llm.example/v1", APIKey: "sk-gate", Model: "gpt-6-luna", RequestsPerMinute: 600, Concurrency: 2,
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
	if err := manager.store.EnsureLibrary(ctx, "GateLib", "/gate-music"); err != nil {
		t.Fatal(err)
	}
	lib, err := manager.store.LibraryByRoot(ctx, "/gate-music")
	if err != nil {
		t.Fatal(err)
	}

	gateID := seedAuditArtist(t, manager, lib.ID, "g/1.flac", "GateArtist")
	medID := seedAuditArtist(t, manager, lib.ID, "g/2.flac", "MedArtist")
	fail400ID := seedAuditArtist(t, manager, lib.ID, "g/3.flac", "Fail400Artist")

	runID, err := manager.StartArtistImageBackfillWithMode(ctx, "all")
	if err != nil {
		t.Fatalf("StartArtistImageBackfillWithMode: %v", err)
	}
	run := waitForArtistImageRun(t, manager, runID)
	if run.Cached != 1 || run.Candidate != 1 || run.Failed != 1 {
		t.Fatalf("run = %+v, want cached=1 candidate=1 failed=1", run)
	}

	// GateArtist: bad Bangumi cover rejected & blacklisted; web portrait cached + keep review persisted!
	rej, err := manager.store.ArtistImageRejections(ctx, gateID)
	if err != nil || !rej.URLs["http://93.184.216.34/img/bad-bgm-cover.png"] || !rej.Hashes[badCoverHash] {
		t.Fatalf("expected bad Bangumi cover blacklisted, got %+v err=%v", rej, err)
	}
	eff, err := manager.store.ArtistEffectiveImageCache(ctx, gateID)
	if err != nil || eff.ContentHash != goodWebHash || eff.Source != "ai_web" {
		t.Fatalf("GateArtist cache = (%+v, %v), want ai_web %s", eff, err, goodWebHash)
	}
	rev, ok, err := manager.store.ArtistImageReviewByArtist(ctx, gateID)
	if err != nil || !ok || rev.Verdict != "keep" || rev.ImageHash != goodWebHash {
		t.Fatalf("GateArtist keep review = (%+v, %v, %v)", rev, ok, err)
	}

	// MedArtist: medium-confidence Bangumi image NOT auto-cached, queued to pending candidates.
	if _, _, _, pErr := manager.store.ArtistImagePath(ctx, medID); pErr == nil {
		t.Fatal("expected MedArtist not auto-cached")
	}
	cands, _ := manager.store.PendingArtistImageCandidates(ctx, 10)
	if len(cands) != 1 || cands[0].ArtistID != medID || cands[0].Kind != "replacement" {
		t.Fatalf("expected 1 replacement candidate for MedArtist, got %+v", cands)
	}

	// Fail400Artist: VLM 400 MUST NOT silently cache the unverified image!
	if _, _, _, pErr := manager.store.ArtistImagePath(ctx, fail400ID); pErr == nil {
		t.Fatal("Fail400Artist must not cache image on VLM 400")
	}
}

func TestArtistImageAutoQueueFullPipelineRetryAndRecovery(t *testing.T) {
	ctx := context.Background()
	oldCoverPNG := makeAvatarPNG(t, 280, 280)
	badBgmAutoPNG := makeAvatarPNG(t, 290, 290)
	goodPortraitPNG := makeAvatarPNG(t, 360, 360)
	keptPNG := makeAvatarPNG(t, 320, 320)
	badBgmAutoHash := fmt.Sprintf("%x", sha256.Sum256([]byte(badBgmAutoPNG)))
	goodHash := fmt.Sprintf("%x", sha256.Sum256([]byte(goodPortraitPNG)))

	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/v0/search/persons"):
			var body struct {
				Keyword string `json:"keyword"`
			}
			if r.Body != nil {
				_ = json.NewDecoder(r.Body).Decode(&body)
			}
			if body.Keyword == "NewPerformer" {
				return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"application/json"}}, `{"data":[{"id":11,"name":"NewPerformer","career":["artist"],"images":{"large":"http://93.184.216.34/img/bad-bgm-auto.png"}}]}`), nil
			}
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"application/json"}}, `{"data":[]}`), nil
		case r.URL.Host == "93.184.216.34" && r.URL.Path == "/img/bad-bgm-auto.png":
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"image/png"}}, badBgmAutoPNG), nil
		case r.URL.Host == "93.184.216.34" && strings.HasPrefix(r.URL.Path, "/profile/"):
			html := `<html><head><title>Artist Official</title><meta property="og:image" content="http://93.184.216.34/img/portrait.png"></head></html>`
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"text/html"}}, html), nil
		case r.URL.Host == "93.184.216.34" && r.URL.Path == "/img/portrait.png":
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"image/png"}}, goodPortraitPNG), nil
		default:
			return cannedResponse(http.StatusNotFound, http.Header{}, `{}`), nil
		}
	}))
	manager.sleep = func(context.Context, time.Duration) error { return nil }
	manager.ensureAutoImageQueue().retryBackoff = func(int) time.Duration { return 50 * time.Millisecond }

	var keptVLMCalls atomic.Int32
	var retryWebCalls atomic.Int32
	var perfBgmRejected atomic.Bool
	chat := &scriptedAuditChat{
		handler: func(req tagging.ChatRequest) (tagging.ChatResult, error) {
			if strings.Contains(req.User, "AlreadyKeptArtist") {
				keptVLMCalls.Add(1)
			}
			if len(req.Images) == 0 {
				return tagging.ChatResult{Content: `{"sameArtist":true,"selectedImageIndex":0,"confidence":"high","reason":"匹配官方肖像"}`}, nil
			}
			if strings.Contains(req.User, `"currentSource"`) {
				// Auditing existing unaudited avatar -> judge album_cover high confidence!
				return tagging.ChatResult{Content: `{"verdict":"replace","issueType":"album_cover","confidence":96,"reason":"旧图是专辑封面"}`}, nil
			}
			if strings.Contains(req.User, "NewPerformer") && !perfBgmRejected.Swap(true) {
				// Tier 2 Bangumi image is an album cover -> reject & blacklist, cascade to Tier 3 web!
				return tagging.ChatResult{Content: `{"valid":false,"confidence":95,"reason":"Bangumi 返回的是专辑封面，非本人头像"}`}, nil
			}
			return tagging.ChatResult{Content: `{"valid":true,"confidence":94,"reason":"清晰本人头像"}`}, nil
		},
	}
	manager.SetAIChatFactory(func(storage.AISettings) (tagging.Chat, error) { return chat, nil })
	manager.bioWebFactory = func(storage.ArtistAliasSettings, storage.AISettings) (websearch.Client, error) {
		return queueStubWebClient{
			searchFn: func(_ context.Context, query string) ([]websearch.SearchResult, error) {
				if strings.Contains(query, "RetryArtist") && retryWebCalls.Add(1) == 1 {
					return nil, errors.New("temporary tavily 503")
				}
				return []websearch.SearchResult{{URL: "http://93.184.216.34/profile/ok", Title: "Official"}}, nil
			},
		}, nil
	}

	if err := manager.store.SaveMetadataSourceSetting(ctx, storage.MetadataSourceSetting{
		Source: "bangumi", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.SaveAISettings(ctx, storage.AISettings{
		BaseURL: "https://llm.example/v1", APIKey: "sk-auto", Model: "gpt-6-luna", RequestsPerMinute: 600, Concurrency: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.SaveArtistImageSettings(ctx, storage.ArtistImageSettings{
		BangumiEnabled:      true,
		DeezerEnabled:       false,
		WebFallbackEnabled:  true,
		WebAutoApplyHigh:    true,
		VisionEnabled:       true,
		NegativeCacheDays:   30,
		AuditAutoConfidence: 85,
		AutoIngestAIEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.EnsureLibrary(ctx, "AutoLib", "/auto-music"); err != nil {
		t.Fatal(err)
	}
	lib, err := manager.store.LibraryByRoot(ctx, "/auto-music")
	if err != nil {
		t.Fatal(err)
	}

	// Import performer + backstage composer + unaudited existing avatar + already-kept + transient retry artist.
	if err := manager.store.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "a/1.flac", FileSize: 1000, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Song", Album: "Alb", Artists: []string{"NewPerformer"}, AlbumArtists: []string{"NewPerformer"},
			Composer: "NewComposer", DiscNumber: 1, TrackNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	findArtist := func(name string) int64 {
		var id int64
		if err := writableSafetyDB(t, manager).QueryRowContext(ctx, `SELECT id FROM artists WHERE display_name=?`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	performerID := findArtist("NewPerformer")
	composerID := findArtist("NewComposer")
	unauditedID := seedAuditArtist(t, manager, lib.ID, "a/2.flac", "UnauditedOldArtist")
	keptID := seedAuditArtist(t, manager, lib.ID, "a/3.flac", "AlreadyKeptArtist")
	retryID := seedAuditArtist(t, manager, lib.ID, "a/4.flac", "RetryArtist")

	bindMB := func(aid int64, mbid string) {
		if err := manager.store.UpsertExternalArtistProfile(ctx, aid, storage.ExternalArtistProfile{
			Source: "musicbrainz", ExternalID: mbid,
		}); err != nil {
			t.Fatal(err)
		}
	}
	bindMB(performerID, "mb-perf-1")
	bindMB(composerID, "mb-comp-1")
	bindMB(unauditedID, "mb-unau-1")
	bindMB(keptID, "mb-kept-1")
	bindMB(retryID, "mb-retry-1")

	seedCachedAvatar(t, manager, unauditedID, "deezer", "http://93.184.216.34/old/cover.png", oldCoverPNG)
	keptHash := seedCachedAvatar(t, manager, keptID, "deezer", "http://93.184.216.34/old/kept.png", keptPNG)
	if err := manager.store.SaveArtistImageReview(ctx, storage.ArtistImageReview{
		ArtistID: keptID, ImageHash: keptHash, Verdict: "keep", IssueType: "none", Confidence: 95, Model: "gpt-6-luna",
	}, nil); err != nil {
		t.Fatal(err)
	}

	// Enqueue all 5 via QueueAutoConfirmedArtistImage.
	for _, aid := range []int64{performerID, composerID, unauditedID, keptID, retryID} {
		if !manager.QueueAutoConfirmedArtistImage(ctx, aid, "auto_match") {
			t.Fatalf("expected artist %d queued", aid)
		}
	}

	// Wait until all items (including retryID's retry after 50ms) drain completely.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		st := manager.ArtistImageAutoQueueStatus(ctx)
		if st.Pending == 0 && st.Running == 0 && st.Processed >= 5 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	manager.Wait()

	st := manager.ArtistImageAutoQueueStatus(ctx)
	if st.Pending != 0 || st.Running != 0 || st.Failed != 0 || st.Cached < 3 || st.Replaced != 1 || st.Skipped != 1 {
		t.Fatalf("auto queue final status = %+v", st)
	}

	// Verify performer, composer, unaudited old avatar (replaced!), and retry artist all hold the verified portrait.
	for _, aid := range []int64{performerID, composerID, unauditedID, retryID} {
		eff, err := manager.store.ArtistEffectiveImageCache(ctx, aid)
		if err != nil || eff.ContentHash != goodHash {
			t.Fatalf("artist %d cache = (%+v, %v), want hash %s", aid, eff, err, goodHash)
		}
		rev, ok, err := manager.store.ArtistImageReviewByArtist(ctx, aid)
		if err != nil || !ok || rev.Verdict != "keep" || rev.ImageHash != goodHash {
			t.Fatalf("artist %d keep review = (%+v, %v, %v)", aid, rev, ok, err)
		}
	}
	// Verify NewPerformer's rejected Tier 2 Bangumi cover was blacklisted before cascading to Tier 3 web.
	rej, err := manager.store.ArtistImageRejections(ctx, performerID)
	if err != nil || !rej.URLs["http://93.184.216.34/img/bad-bgm-auto.png"] || !rej.Hashes[badBgmAutoHash] {
		t.Fatalf("NewPerformer rejections = (%+v, %v), want bad Bangumi cover blacklisted", rej, err)
	}
	// Verify unaudited replacement left a reversible audit snapshot!
	actions, err := manager.store.RecentArtistImageAuditActions(ctx, 10)
	if err != nil || len(actions) != 1 || actions[0].ArtistID != unauditedID || actions[0].Action != "replaced" {
		t.Fatalf("audit actions = (%+v, %v)", actions, err)
	}
	// Verify AlreadyKeptArtist incurred ZERO VLM calls!
	if keptVLMCalls.Load() != 0 {
		t.Fatalf("AlreadyKeptArtist should skip at 0 VLM calls, got %d", keptVLMCalls.Load())
	}
	if retryWebCalls.Load() != 2 {
		t.Fatalf("expected RetryArtist to retry once and succeed on 2nd attempt, got %d calls", retryWebCalls.Load())
	}
}
