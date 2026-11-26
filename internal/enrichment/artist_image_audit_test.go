package enrichment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
	"github.com/lux032/032music-server/internal/websearch"
)

type scriptedAuditChat struct {
	mu      sync.Mutex
	handler func(req tagging.ChatRequest) (tagging.ChatResult, error)
	calls   []tagging.ChatRequest
}

func (c *scriptedAuditChat) Complete(_ context.Context, req tagging.ChatRequest) (tagging.ChatResult, error) {
	c.mu.Lock()
	c.calls = append(c.calls, req)
	h := c.handler
	c.mu.Unlock()
	return h(req)
}

func waitForArtistImageRun(t *testing.T, m *Manager, runID int64) storage.ArtistImageBackfillRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, err := m.store.DurableArtistImageBackfillRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == "completed" || run.Status == "failed" || run.Status == "cancelled" {
			return run
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for artist image run %d", runID)
	return storage.ArtistImageBackfillRun{}
}

func seedAuditArtist(t *testing.T, m *Manager, libID int64, relPath, name string) int64 {
	t.Helper()
	if err := m.store.ImportTrack(context.Background(), storage.ImportInput{
		LibraryID:    libID,
		RelativePath: relPath,
		FileSize:     1000,
		ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title:        "Track " + name,
			Album:        "Album " + name,
			Artists:      []string{name},
			AlbumArtists: []string{name},
			DiscNumber:   1,
			TrackNumber:  1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := writableSafetyDB(t, m).QueryRowContext(context.Background(), `SELECT id FROM artists WHERE display_name=?`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedCachedAvatar(t *testing.T, m *Manager, artistID int64, source, remoteURL, raw string) string {
	t.Helper()
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	cachePath, err := writeCacheFile(m.imageDirectory, hash+".png", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.store.SaveArtistImage(context.Background(), storage.ArtistImageInput{
		ArtistID:  artistID,
		Source:    source,
		RemoteURL: remoteURL,
		Hash:      hash,
		MIMEType:  "image/png",
		CachePath: cachePath,
		ByteSize:  int64(len(raw)),
	}); err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestArtistImageAuditFullPipelineAndPolicies(t *testing.T) {
	oldCoverPNG := makeAvatarPNG(t, 280, 280)
	newPortraitPNG := makeAvatarPNG(t, 360, 360)
	goodPortraitPNG := makeAvatarPNG(t, 320, 320)
	garbageLogoPNG := makeAvatarPNG(t, 260, 260)
	wrongPersonPNG := makeAvatarPNG(t, 270, 270)
	suspiciousPNG := makeAvatarPNG(t, 290, 290)

	var webSearches int
	var mu sync.Mutex

	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Host == "93.184.216.34" && r.URL.Path == "/profile/replace":
			html := `<html><head><title>ReplaceMe Official</title><meta property="og:image" content="http://93.184.216.34/img/old-cover.png"><meta property="og:image" content="http://93.184.216.34/img/new-portrait.png"></head></html>`
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"text/html"}}, html), nil
		case r.URL.Host == "93.184.216.34" && r.URL.Path == "/img/old-cover.png":
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"image/png"}}, oldCoverPNG), nil
		case r.URL.Host == "93.184.216.34" && r.URL.Path == "/img/new-portrait.png":
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"image/png"}}, newPortraitPNG), nil
		default:
			return cannedResponse(http.StatusNotFound, http.Header{}, `{}`), nil
		}
	}))
	manager.sleep = func(context.Context, time.Duration) error { return nil }
	ctx := context.Background()

	if err := manager.store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := manager.store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.store.SaveAISettings(ctx, storage.AISettings{
		BaseURL: "https://api.openai.com/v1", APIKey: "sk-test", Model: "gpt-6-luna",
		RequestsPerMinute: 600, Concurrency: 2,
	}); err != nil {
		t.Fatal(err)
	}
	imgSt, _ := manager.store.ArtistImageSettings(ctx)
	imgSt.BangumiEnabled = false
	imgSt.DeezerEnabled = false
	imgSt.FanartEnabled = false
	imgSt.WebFallbackEnabled = true
	imgSt.WebAutoApplyHigh = true
	imgSt.VisionEnabled = true
	imgSt.AuditAutoConfidence = 85
	if err := manager.store.SaveArtistImageSettings(ctx, imgSt); err != nil {
		t.Fatal(err)
	}

	customID := seedAuditArtist(t, manager, lib.ID, "1/01.flac", "CustomArtist")
	keepID := seedAuditArtist(t, manager, lib.ID, "2/01.flac", "KeepArtist")
	replaceID := seedAuditArtist(t, manager, lib.ID, "3/01.flac", "ReplaceMe")
	garbageID := seedAuditArtist(t, manager, lib.ID, "4/01.flac", "GarbageArtist")
	garbageSrcID := seedAuditArtist(t, manager, lib.ID, "4b/01.flac", "GarbageMergedSrc")
	wrongID := seedAuditArtist(t, manager, lib.ID, "5/01.flac", "WrongSubjectArtist")
	suspiciousID := seedAuditArtist(t, manager, lib.ID, "6/01.flac", "SuspiciousArtist")

	// 1. CustomArtist has custom image -> must never be audited.
	if _, err := manager.store.SaveCustomArtistImage(ctx, customID, storage.CustomImageInput{
		Hash: "custom-keep-hash", MIMEType: "image/png", FileName: "cust.png", Width: 500, Height: 500, ByteSize: 100,
	}); err != nil {
		t.Fatal(err)
	}
	// 2. KeepArtist has good portrait.
	keepHash := seedCachedAvatar(t, manager, keepID, "deezer", "http://93.184.216.34/img/good.png", goodPortraitPNG)
	// 3. ReplaceMe has an album cover that should be replaced via web fallback (while skipping old-cover.png!).
	oldCoverHash := seedCachedAvatar(t, manager, replaceID, "deezer", "http://93.184.216.34/img/old-cover.png", oldCoverPNG)
	// 4. GarbageArtist inherits garbage logo from merged source -> must be cleared (1.A).
	seedCachedAvatar(t, manager, garbageSrcID, "lastfm", "http://93.184.216.34/img/logo.png", garbageLogoPNG)
	if _, err := manager.store.MergeArtists(ctx, garbageSrcID, garbageID); err != nil {
		t.Fatal(err)
	}
	// 5. WrongSubjectArtist has high-confidence wrong_subject with no replacement -> kept + audit_review (1.A).
	seedCachedAvatar(t, manager, wrongID, "deezer", "http://93.184.216.34/img/wrong.png", wrongPersonPNG)
	// 6. SuspiciousArtist has medium confidence 70 -> kept + audit_review WITHOUT calling web search!
	seedCachedAvatar(t, manager, suspiciousID, "deezer", "http://93.184.216.34/img/susp.png", suspiciousPNG)

	manager.bioWebFactory = func(storage.ArtistAliasSettings, storage.AISettings) (websearch.Client, error) {
		return &countingStubWeb{
			onSearch: func(q string) []websearch.SearchResult {
				mu.Lock()
				webSearches++
				mu.Unlock()
				if strings.Contains(q, "ReplaceMe") {
					return []websearch.SearchResult{{URL: "http://93.184.216.34/profile/replace", Title: "ReplaceMe Official"}}
				}
				return nil
			},
		}, nil
	}

	chat := &scriptedAuditChat{
		handler: func(req tagging.ChatRequest) (tagging.ChatResult, error) {
			usage := storage.AIUsage{Requests: 1, PromptTokens: 120, CompletionTokens: 30}
			// Text selection call (no images).
			if len(req.Images) == 0 {
				var doc struct {
					CandidateImages []webImageFlatCandidate `json:"candidateImages"`
				}
				_ = json.Unmarshal([]byte(req.User), &doc)
				// Old excluded URL must have been stripped before sending to LLM!
				for _, ci := range doc.CandidateImages {
					if ci.ImageURL == "http://93.184.216.34/img/old-cover.png" {
						t.Errorf("excluded old URL was still sent to web selector: %+v", doc.CandidateImages)
					}
				}
				return tagging.ChatResult{Content: `{"sameArtist":true,"selectedImageIndex":0,"confidence":"high","reason":"官方艺人肖像"}`, Usage: usage}, nil
			}
			// Vision verification call (no currentSource field in user payload).
			if !strings.Contains(req.User, `"currentSource"`) {
				return tagging.ChatResult{Content: `{"valid":true,"confidence":95,"reason":"清晰的艺人肖像照"}`, Usage: usage}, nil
			}
			// Audit call.
			switch {
			case strings.Contains(req.User, `"KeepArtist"`):
				return tagging.ChatResult{Content: `{"verdict":"keep","issueType":"none","confidence":96,"reason":"清晰人像"}`, Usage: usage}, nil
			case strings.Contains(req.User, `"ReplaceMe"`):
				return tagging.ChatResult{Content: `{"verdict":"replace","issueType":"album_cover","confidence":93,"reason":"这是专辑封面而非肖像"}`, Usage: usage}, nil
			case strings.Contains(req.User, `"GarbageArtist"`):
				return tagging.ChatResult{Content: `{"verdict":"replace","issueType":"garbage","confidence":98,"reason":"默认占位 Logo"}`, Usage: usage}, nil
			case strings.Contains(req.User, `"WrongSubjectArtist"`):
				return tagging.ChatResult{Content: `{"verdict":"replace","issueType":"wrong_subject","confidence":91,"reason":"明显非本人"}`, Usage: usage}, nil
			case strings.Contains(req.User, `"SuspiciousArtist"`):
				return tagging.ChatResult{Content: `{"verdict":"replace","issueType":"low_quality","confidence":70,"reason":"略微模糊，建议人工确认"}`, Usage: usage}, nil
			default:
				return tagging.ChatResult{Content: `{"verdict":"keep","issueType":"none","confidence":90,"reason":"ok"}`, Usage: usage}, nil
			}
		},
	}
	manager.aiChatFactory = func(storage.AISettings) (tagging.Chat, error) { return chat, nil }

	runID, err := manager.StartArtistImageBackfillWithMode(ctx, "audit_all")
	if err != nil {
		t.Fatalf("StartArtistImageBackfillWithMode(audit_all): %v", err)
	}
	run := waitForArtistImageRun(t, manager, runID)
	if run.Status != "completed" {
		t.Fatalf("run status = %+v", run)
	}
	if run.Total != 5 || run.Kept != 1 || run.Replaced != 1 || run.Cleared != 1 || run.Candidate != 2 || run.Failed != 0 {
		t.Fatalf("unexpected run counts: %+v", run)
	}

	// Verify ReplaceMe got new portrait, old image rejected, and undo snapshot recorded.
	eff, err := manager.store.ArtistEffectiveImageCache(ctx, replaceID)
	if err != nil || eff.RemoteURL != "http://93.184.216.34/img/new-portrait.png" || eff.ContentHash == oldCoverHash {
		t.Fatalf("ReplaceMe effective cache = %+v err=%v", eff, err)
	}
	// Verify GarbageArtist inherited cache was completely cleared.
	if _, _, _, pErr := manager.store.ArtistImagePath(ctx, garbageID); pErr == nil {
		t.Fatal("GarbageArtist inherited cache should be cleared")
	}
	// Verify WrongSubjectArtist & SuspiciousArtist kept their old images and have pending audit_review cards.
	for _, id := range []int64{wrongID, suspiciousID} {
		if _, _, _, pErr := manager.store.ArtistImagePath(ctx, id); pErr != nil {
			t.Fatalf("artist %d should keep existing cache under 1.A/medium policy: %v", id, pErr)
		}
	}
	cands, err := manager.store.PendingArtistImageCandidates(ctx, 20)
	if err != nil || len(cands) != 2 {
		t.Fatalf("pending candidates = %+v err=%v, want 2", cands, err)
	}
	for _, c := range cands {
		if c.Kind != "audit_review" {
			t.Fatalf("expected audit_review candidate, got %+v", c)
		}
	}
	// SuspiciousArtist (confidence=70) must NOT have triggered a web search (only ReplaceMe, GarbageArtist, WrongSubjectArtist = 3 searches).
	if webSearches != 3 {
		t.Fatalf("webSearches = %d, want 3 (medium-confidence must not trigger web search)", webSearches)
	}
	// Verify artist_image_audit usage was recorded.
	usageRows, err := manager.store.AIUsageTotals(ctx, "")
	var auditReqs int64
	for _, r := range usageRows {
		if r.Purpose == "artist_image_audit" {
			auditReqs += r.Usage.Requests
		}
	}
	if err != nil || auditReqs != 5 {
		t.Fatalf("artist_image_audit usage = %d (rows=%+v) err=%v, want 5", auditReqs, usageRows, err)
	}
	// Verify KeepArtist review persisted and a second audit_all skips KeepArtist + ReplaceMe idempotently!
	rev, ok, _ := manager.store.ArtistImageReviewByArtist(ctx, keepID)
	if !ok || rev.ImageHash != keepHash || rev.Verdict != "keep" {
		t.Fatalf("KeepArtist review = (%+v,%v)", rev, ok)
	}
	if _, err := manager.StartArtistImageBackfillWithMode(ctx, "audit_all"); err != ErrNoArtistImageBackfillCandidates {
		t.Fatalf("second audit_all should have no remaining candidates, got err=%v", err)
	}
	// audit_force must re-audit all 5 non-custom artists (including kept ones).
	forceRunID, err := manager.StartArtistImageBackfillWithMode(ctx, "audit_force")
	if err != nil {
		t.Fatalf("audit_force start: %v", err)
	}
	forceRun := waitForArtistImageRun(t, manager, forceRunID)
	if forceRun.Total != 5 {
		t.Fatalf("audit_force total = %d, want 5 (custom artist excluded)", forceRun.Total)
	}
}

func TestArtistImageAuditNonSilentFailureOnVLM400(t *testing.T) {
	portraitPNG := makeAvatarPNG(t, 300, 300)
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(http.StatusNotFound, http.Header{}, `{}`), nil
	}))
	manager.sleep = func(context.Context, time.Duration) error { return nil }
	ctx := context.Background()
	if err := manager.store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := manager.store.LibraryByRoot(ctx, "/music")
	_ = manager.store.SaveAISettings(ctx, storage.AISettings{
		BaseURL: "https://api.openai.com/v1", APIKey: "sk-test", Model: "gpt-6-luna",
		RequestsPerMinute: 600, Concurrency: 2,
	})
	artistID := seedAuditArtist(t, manager, lib.ID, "1/01.flac", "VLMErrorArtist")
	seedCachedAvatar(t, manager, artistID, "deezer", "http://93.184.216.34/p.png", portraitPNG)

	manager.aiChatFactory = func(storage.AISettings) (tagging.Chat, error) {
		return &scriptedAuditChat{
			handler: func(tagging.ChatRequest) (tagging.ChatResult, error) {
				return tagging.ChatResult{}, &tagging.LLMError{Kind: tagging.KindBadRequest, Status: 400, Message: "image_url rejected"}
			},
		}, nil
	}

	runID, err := manager.StartArtistImageBackfillWithMode(ctx, "audit_all")
	if err != nil {
		t.Fatal(err)
	}
	run := waitForArtistImageRun(t, manager, runID)
	if run.Failed != 1 || run.Kept != 0 {
		t.Fatalf("expected non-silent failure on VLM 400, got %+v", run)
	}
	if _, ok, _ := manager.store.ArtistImageReviewByArtist(ctx, artistID); ok {
		t.Fatal("failed VLM audit must not record keep review")
	}
	if _, _, _, pErr := manager.store.ArtistImagePath(ctx, artistID); pErr != nil {
		t.Fatalf("failed VLM audit must preserve existing cached avatar: %v", pErr)
	}
}

type countingStubWeb struct {
	onSearch func(string) []websearch.SearchResult
}

func (c *countingStubWeb) Search(_ context.Context, q string) ([]websearch.SearchResult, error) {
	return c.onSearch(q), nil
}

func (c *countingStubWeb) Fetch(context.Context, string) (string, error) { return "", nil }
