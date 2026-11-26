package enrichment

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
	"github.com/lux032/032music-server/internal/websearch"
)

type queueStubWebClient struct {
	searchFn func(ctx context.Context, query string) ([]websearch.SearchResult, error)
}

func (s queueStubWebClient) Search(ctx context.Context, query string) ([]websearch.SearchResult, error) {
	return s.searchFn(ctx, query)
}

func (s queueStubWebClient) Fetch(context.Context, string) (string, error) { return "", nil }

func waitForSearchQueueState(t *testing.T, m *Manager, want ...string) ArtistImageSearchQueueSnapshot {
	t.Helper()
	wantSet := map[string]bool{}
	for _, s := range want {
		wantSet[s] = true
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap := m.ArtistImageSearchQueueSnapshot()
		if wantSet[snap.Status] {
			return snap
		}
		time.Sleep(15 * time.Millisecond)
	}
	snap := m.ArtistImageSearchQueueSnapshot()
	t.Fatalf("timed out waiting for queue state %v; last=%+v", want, snap)
	return snap
}

func TestArtistImageSearchQueueLifecycleAndAutoTagging(t *testing.T) {
	ctx := context.Background()
	oldPNG := makeAvatarPNG(t, 280, 280)
	newPNG := makeAvatarPNG(t, 360, 360)

	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Host == "93.184.216.34" && r.URL.Path == "/profile/found":
			html := `<html><head><title>FoundArtist Official</title><meta property="og:image" content="http://93.184.216.34/img/found-new.png"></head></html>`
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"text/html"}}, html), nil
		case r.URL.Host == "93.184.216.34" && r.URL.Path == "/img/found-new.png":
			return cannedResponse(http.StatusOK, http.Header{"Content-Type": {"image/png"}}, newPNG), nil
		default:
			return cannedResponse(http.StatusNotFound, http.Header{}, `{}`), nil
		}
	}))
	manager.sleep = func(_ context.Context, _ time.Duration) error { return nil }

	chat := &scriptedAuditChat{
		handler: func(req tagging.ChatRequest) (tagging.ChatResult, error) {
			switch {
			case strings.Contains(req.User, "AutoMissArtist") && strings.Contains(req.User, "currentSource"):
				return tagging.ChatResult{Content: `{"verdict":"replace","issueType":"album_cover","confidence":96,"reason":"明显专辑封面"}`}, nil
			case strings.Contains(req.User, "LowConfArtist") && strings.Contains(req.User, "currentSource"):
				return tagging.ChatResult{Content: `{"verdict":"replace","issueType":"album_cover","confidence":72,"reason":"疑似专辑封面"}`}, nil
			case strings.Contains(req.User, "FoundArtist") && len(req.Images) == 0:
				return tagging.ChatResult{Content: `{"sameArtist":true,"selectedImageIndex":0,"confidence":"high","reason":"匹配官方肖像"}`}, nil
			case len(req.Images) > 0:
				return tagging.ChatResult{Content: `{"valid":true,"confidence":93,"reason":"清晰本人肖像"}`}, nil
			default:
				return tagging.ChatResult{Content: `{"sameArtist":false,"selectedImageIndex":-1,"confidence":"low","reason":"未找到合适图片"}`}, nil
			}
		},
	}
	manager.SetAIChatFactory(func(storage.AISettings) (tagging.Chat, error) { return chat, nil })
	manager.bioWebFactory = func(storage.ArtistAliasSettings, storage.AISettings) (websearch.Client, error) {
		return queueStubWebClient{
			searchFn: func(_ context.Context, query string) ([]websearch.SearchResult, error) {
				switch {
				case strings.Contains(query, "FoundArtist"):
					return []websearch.SearchResult{{URL: "http://93.184.216.34/profile/found", Title: "FoundArtist Official"}}, nil
				case strings.Contains(query, "FailArtist"):
					return nil, errors.New("tavily timeout")
				default:
					return nil, nil
				}
			},
		}, nil
	}

	if err := manager.store.SaveAISettings(ctx, storage.AISettings{
		BaseURL: "https://llm.example/v1", APIKey: "sk-test", Model: "gpt-6-luna", RequestsPerMinute: 600, Concurrency: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.SaveArtistImageSettings(ctx, storage.ArtistImageSettings{
		WebFallbackEnabled:   true,
		WebAutoApplyHigh:     true,
		VisionEnabled:        true,
		MaxWebRequestsPerRun: 50,
		NegativeCacheDays:    30,
		AuditAutoConfidence:  85,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.EnsureLibrary(ctx, "QueueLib", "/queue-music"); err != nil {
		t.Fatal(err)
	}
	lib, err := manager.store.LibraryByRoot(ctx, "/queue-music")
	if err != nil {
		t.Fatal(err)
	}

	// Part 1: Verify auto-audit sets not_found_auto ONLY when Tier 3 web search ran.
	autoMissID := seedAuditArtist(t, manager, lib.ID, "a/1.flac", "AutoMissArtist")
	lowConfID := seedAuditArtist(t, manager, lib.ID, "a/2.flac", "LowConfArtist")
	seedCachedAvatar(t, manager, autoMissID, "deezer", "http://93.184.216.34/old/auto.png", oldPNG)
	seedCachedAvatar(t, manager, lowConfID, "deezer", "http://93.184.216.34/old/low.png", oldPNG)

	runID, err := manager.StartArtistImageBackfillWithMode(ctx, "audit_all")
	if err != nil {
		t.Fatalf("StartArtistImageBackfillWithMode: %v", err)
	}
	waitForArtistImageRun(t, manager, runID)

	cands, err := manager.store.PendingArtistImageCandidates(ctx, 20)
	if err != nil || len(cands) != 2 {
		t.Fatalf("pending candidates after audit = (%+v, %v)", cands, err)
	}
	byArtist := map[int64]storage.ArtistImageCandidate{}
	for _, c := range cands {
		byArtist[c.ArtistID] = c
	}
	if byArtist[autoMissID].WebSearchState != "not_found_auto" || byArtist[autoMissID].WebSearchNote == "" {
		t.Fatalf("AutoMissArtist candidate = %+v, want not_found_auto", byArtist[autoMissID])
	}
	if byArtist[lowConfID].WebSearchState != "" {
		t.Fatalf("LowConfArtist candidate = %+v, want empty web_search_state", byArtist[lowConfID])
	}

	// Part 2: Queue lifecycle with found, not_found_manual, failed, append-while-running, remove-item, and stop.
	foundArtistID := seedAuditArtist(t, manager, lib.ID, "q/1.flac", "FoundArtist")
	missArtistID := seedAuditArtist(t, manager, lib.ID, "q/2.flac", "MissArtist")
	failArtistID := seedAuditArtist(t, manager, lib.ID, "q/3.flac", "FailArtist")
	dropArtistID := seedAuditArtist(t, manager, lib.ID, "q/4.flac", "DropArtist")
	for _, aid := range []int64{foundArtistID, missArtistID, failArtistID, dropArtistID} {
		seedCachedAvatar(t, manager, aid, "deezer", "http://93.184.216.34/old/cover.png", oldPNG)
	}
	addAuditCard := func(aid int64, issue string) int64 {
		id, err := manager.store.UpsertArtistImageCandidate(ctx, storage.ArtistImageCandidate{
			ArtistID:        aid,
			Source:          "ai_audit",
			ImageURL:        "http://93.184.216.34/old/cover.png",
			Confidence:      "medium",
			ConfidenceScore: 72,
			Kind:            "audit_review",
			IssueType:       issue,
			Reason:          "疑似专辑封面",
		})
		if err != nil || id == 0 {
			t.Fatalf("UpsertArtistImageCandidate(%d) = (%d, %v)", aid, id, err)
		}
		return id
	}
	foundCardID := addAuditCard(foundArtistID, "album_cover")
	missCardID := addAuditCard(missArtistID, "album_cover")
	failCardID := addAuditCard(failArtistID, "low_quality")
	dropCardID := addAuditCard(dropArtistID, "album_cover")

	gateStarted := make(chan int64, 1)
	gateRelease := make(chan struct{})
	manager.ensureImageSearchQueue().testItemHook = func(candidateID int64) {
		if candidateID == foundCardID {
			gateStarted <- candidateID
			<-gateRelease
		}
	}

	added, snap, err := manager.EnqueueArtistImageCandidateSearches(ctx, []int64{foundCardID, missCardID, dropCardID, foundCardID})
	if err != nil || added != 3 {
		t.Fatalf("Enqueue initial = (%d, %+v, %v), want added=3", added, snap, err)
	}
	<-gateStarted

	// While foundCardID is running, verify positions, append failCardID, and remove dropCardID.
	midSnap := manager.ArtistImageSearchQueueSnapshot()
	if midSnap.Status != "running" || midSnap.CurrentID != foundCardID || midSnap.QueuedPositions[foundCardID] != 1 || midSnap.QueuedPositions[missCardID] != 2 {
		t.Fatalf("midSnap = %+v", midSnap)
	}
	appended, snapAfterAppend, err := manager.EnqueueArtistImageCandidateSearches(ctx, []int64{failCardID})
	if err != nil || appended != 1 || snapAfterAppend.Total != 4 {
		t.Fatalf("append while running = (%d, %+v, %v)", appended, snapAfterAppend, err)
	}
	removed, snapAfterRemove := manager.RemoveArtistImageCandidateSearch(dropCardID)
	if !removed || snapAfterRemove.Total != 3 || snapAfterRemove.QueuedPositions[dropCardID] != 0 {
		t.Fatalf("remove waiting item = (%v, %+v)", removed, snapAfterRemove)
	}

	close(gateRelease)
	doneSnap := waitForSearchQueueState(t, manager, "completed")
	if doneSnap.Completed != 3 || doneSnap.Found != 1 || doneSnap.NotFound != 1 || doneSnap.Failed != 1 {
		t.Fatalf("doneSnap = %+v, want completed=3 found=1 notFound=1 failed=1", doneSnap)
	}

	// Verify DB state after queue finishes.
	missRow, err := manager.store.ArtistImageCandidateByID(ctx, missCardID)
	if err != nil || missRow.WebSearchState != "not_found_manual" || missRow.WebSearchedAt == "" {
		t.Fatalf("MissArtist row = %+v err=%v", missRow, err)
	}
	failRow, err := manager.store.ArtistImageCandidateByID(ctx, failCardID)
	if err != nil || failRow.WebSearchState != "failed" || !strings.Contains(failRow.WebSearchNote, "tavily timeout") {
		t.Fatalf("FailArtist row = %+v err=%v", failRow, err)
	}
	oldFoundRow, err := manager.store.ArtistImageCandidateByID(ctx, foundCardID)
	if err != nil || oldFoundRow.Status != "rejected" {
		t.Fatalf("originating FoundArtist audit card should be closed, got %+v err=%v", oldFoundRow, err)
	}
	allPending, err := manager.store.PendingArtistImageCandidates(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	var foundReplacement *storage.ArtistImageCandidate
	for i := range allPending {
		if allPending[i].ArtistID == foundArtistID {
			foundReplacement = &allPending[i]
		}
	}
	if foundReplacement == nil || foundReplacement.Kind != "replacement" || foundReplacement.IssueType != "album_cover" {
		t.Fatalf("expected single replacement card for FoundArtist, got %+v", foundReplacement)
	}

	// Verify Stop + Dismiss on a new run.
	stopGate := make(chan struct{})
	stopStarted := make(chan struct{}, 1)
	manager.ensureImageSearchQueue().testItemHook = func(candidateID int64) {
		if candidateID == dropCardID {
			stopStarted <- struct{}{}
			<-stopGate
		}
	}
	added2, _, err := manager.EnqueueArtistImageCandidateSearches(ctx, []int64{dropCardID, missCardID})
	if err != nil || added2 != 2 {
		t.Fatalf("second enqueue = (%d, %v)", added2, err)
	}
	<-stopStarted
	stopSnap := manager.StopArtistImageCandidateSearchQueue()
	if stopSnap.Waiting != 0 || stopSnap.Total != 1 {
		t.Fatalf("stopSnap while current running = %+v", stopSnap)
	}
	close(stopGate)
	finalStopped := waitForSearchQueueState(t, manager, "stopped")
	if finalStopped.Completed != 1 {
		t.Fatalf("finalStopped = %+v", finalStopped)
	}
	dismissed := manager.DismissArtistImageCandidateSearchQueue()
	if dismissed.Status != "idle" || dismissed.Total != 0 {
		t.Fatalf("dismissed = %+v", dismissed)
	}
}
