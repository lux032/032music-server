package enrichment

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestArtistScanIncrementalSkipsRejectedExpiredAndCompositesWhileFullRescans(t *testing.T) {
	const freshUUID = "11111111-1111-4111-8111-111111111111"
	var requests atomic.Int32
	var returnNewMB atomic.Bool
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if strings.Contains(r.URL.Path, "/ws/2/artist/"+freshUUID) {
			return cannedResponse(200, http.Header{}, `{"id":"`+freshUUID+`","name":"Fresh Artist","sort-name":"Fresh Artist","aliases":[],"tags":[],"relations":[]}`), nil
		}
		q := r.URL.Query().Get("query")
		if strings.Contains(q, "Rejected Artist") {
			if returnNewMB.Load() {
				return cannedResponse(200, http.Header{}, `{"artists":[{"id":"mbid-rejected-1","name":"Rejected Artist","score":85},{"id":"mbid-new-2","name":"Rejected Artist Alt","score":84}]}`), nil
			}
			return cannedResponse(200, http.Header{}, `{"artists":[{"id":"mbid-rejected-1","name":"Rejected Artist","score":85}]}`), nil
		}
		return cannedResponse(200, http.Header{}, `{"artists":[]}`), nil
	}))
	ctx := context.Background()
	s := manager.store
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ path, artist string }{
		{"A/01.flac", "Rejected Artist"},
		{"B/01.flac", "Miss Artist"},
		{"C/01.flac", "Alpha & Beta"},
	} {
		if err = s.ImportTrack(ctx, storage.ImportInput{
			LibraryID:    lib.ID,
			RelativePath: item.path,
			FileSize:     1,
			ModifiedAtNS: 1,
			Metadata:     metadata.AudioMetadata{Title: "Song", Album: "Alb", Artists: []string{item.artist}, AlbumArtists: []string{item.artist}, DiscNumber: 1, TrackNumber: 1},
		}); err != nil {
			t.Fatal(err)
		}
	}
	mb, _ := s.MetadataSourceSetting(ctx, "musicbrainz")
	mb.Enabled = true
	mb.AutoMatch = true
	if err = s.SaveMetadataSourceSetting(ctx, mb); err != nil {
		t.Fatal(err)
	}

	// First incremental run scans Rejected Artist (review), Miss Artist (no_result), Alpha & Beta (skipped_composite).
	run1, err := manager.StartAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	r1, err := s.DurableArtistRun(ctx, run1)
	if err != nil || r1.Status != "completed" || r1.ScanMode != "incremental" || r1.Review != 1 || r1.NoResult != 1 || r1.Skipped != 1 {
		t.Fatalf("run1=%+v err=%v", r1, err)
	}

	// User manually rejects all candidates for Rejected Artist.
	artists, _ := s.ArtistsForMatching(ctx)
	var rejectedID int64
	for _, a := range artists {
		if a.Name == "Rejected Artist" {
			rejectedID = a.ID
		}
	}
	if _, err = s.RejectAllArtistCandidates(ctx, rejectedID, ""); err != nil {
		t.Fatal(err)
	}

	// Simulate CacheDays expiration across all artist_source_match_state rows.
	db := writableSafetyDB(t, manager)
	if _, err = db.ExecContext(ctx, `UPDATE artist_source_match_state SET checked_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}

	// Incremental / startup scan finds zero eligible artists and makes zero HTTP calls.
	beforeReqs := requests.Load()
	if _, err = manager.StartAll(ctx); !errors.Is(err, ErrNoEligibleArtists) {
		t.Fatalf("expected ErrNoEligibleArtists, got %v", err)
	}
	manager.StartAuto(ctx)
	manager.Wait()
	if requests.Load() != beforeReqs {
		t.Fatalf("expected 0 requests on incremental/StartAuto, got %d", requests.Load()-beforeReqs)
	}

	// Importing one brand-new artist only scans that new artist (skips rejected, no_result, and skipped_composite).
	if err = s.ImportTrack(ctx, storage.ImportInput{
		LibraryID:    lib.ID,
		RelativePath: "D/01.flac",
		FileSize:     1,
		ModifiedAtNS: 1,
		Metadata:     metadata.AudioMetadata{Title: "Song", Album: "Alb2", Artists: []string{"Fresh Artist"}, AlbumArtists: []string{"Fresh Artist"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"MUSICBRAINZ_ARTISTID": {freshUUID}}},
	}); err != nil {
		t.Fatal(err)
	}
	run2, err := manager.StartAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	r2, _ := s.DurableArtistRun(ctx, run2)
	if r2.Total != 1 || r2.Processed != 1 || r2.Matched != 1 {
		t.Fatalf("expected only Fresh Artist in incremental run2, got %+v", r2)
	}

	// Explicit full scan ("全局补全扫描") re-scans Rejected Artist, Miss Artist, and composite Alpha & Beta,
	// while never re-scanning already-matched Fresh Artist and preserving rejected status on mbid-rejected-1.
	var inlineCalled atomic.Bool
	manager.SetInlineAIReviewer(func(ctx context.Context, kind string, id int64) bool {
		if kind == storage.AITargetArtistMatch {
			inlineCalled.Store(true)
		}
		return false
	})
	run3, err := manager.StartAllWithMode(ctx, storage.ArtistScanFull)
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	r3, _ := s.DurableArtistRun(ctx, run3)
	if r3.ScanMode != "full" || r3.Total != 3 || r3.NoResult != 2 || r3.Skipped != 1 || inlineCalled.Load() {
		t.Fatalf("expected full scan to re-check 3 unconfirmed artists without AI review when all candidates rejected, got %+v inlineAI=%v", r3, inlineCalled.Load())
	}

	// If full scan discovers a NEW external candidate, previously rejected ID stays rejected while new ID appears as candidate.
	returnNewMB.Store(true)
	run4, err := manager.StartAllWithMode(ctx, storage.ArtistScanFull)
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	r4, _ := s.DurableArtistRun(ctx, run4)
	if r4.Review != 1 {
		t.Fatalf("expected new candidate review in run4, got %+v", r4)
	}
	cands, _ := s.ArtistCandidates(ctx, rejectedID)
	statuses := map[string]string{}
	for _, c := range cands {
		statuses[c.ExternalID] = c.Status
	}
	if statuses["mbid-rejected-1"] != "rejected" || statuses["mbid-new-2"] != "candidate" {
		t.Fatalf("unexpected candidate statuses: %+v", statuses)
	}
}

func TestMergedArtistInheritedIdentitySkippedInAllScanModes(t *testing.T) {
	var requests atomic.Int32
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return cannedResponse(200, http.Header{}, `{"artists":[]}`), nil
	}))
	ctx := context.Background()
	s := manager.store
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ path, artist string }{
		{"A/01.flac", "Source Artist"},
		{"B/01.flac", "Target Artist"},
	} {
		if err = s.ImportTrack(ctx, storage.ImportInput{
			LibraryID:    lib.ID,
			RelativePath: item.path,
			FileSize:     1,
			ModifiedAtNS: 1,
			Metadata:     metadata.AudioMetadata{Title: "Song", Album: "Alb", Artists: []string{item.artist}, AlbumArtists: []string{item.artist}, DiscNumber: 1, TrackNumber: 1},
		}); err != nil {
			t.Fatal(err)
		}
	}
	mb, _ := s.MetadataSourceSetting(ctx, "musicbrainz")
	mb.Enabled = true
	mb.AutoMatch = true
	if err = s.SaveMetadataSourceSetting(ctx, mb); err != nil {
		t.Fatal(err)
	}
	artists, _ := s.ArtistsForMatching(ctx)
	var sourceID, targetID int64
	for _, a := range artists {
		if a.Name == "Source Artist" {
			sourceID = a.ID
		} else if a.Name == "Target Artist" {
			targetID = a.ID
		}
	}
	if err = s.UpsertExternalArtistProfile(ctx, sourceID, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: safetyMBID, DisplayName: "Source Artist"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MergeArtists(ctx, sourceID, targetID); err != nil {
		t.Fatal(err)
	}

	for _, mode := range []storage.ArtistScanMode{storage.ArtistScanIncremental, storage.ArtistScanFull} {
		if _, err = manager.StartAllWithMode(ctx, mode); !errors.Is(err, ErrNoEligibleArtists) {
			t.Fatalf("mode=%s expected ErrNoEligibleArtists, got %v", mode, err)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("expected 0 requests, got %d", requests.Load())
	}
}

func TestArtistScanSurfacesCompositeCreditsAndEnrichAutoConfirmedRules(t *testing.T) {
	pngBytes := makeValidAvatarPNG(t)
	var bgmCalls, bioCalls atomic.Int32
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Host, "api.bgm.tv") && strings.Contains(r.URL.Path, "/v0/search/persons"):
			bgmCalls.Add(1)
			return cannedResponse(http.StatusOK, http.Header{}, `{"data":[{"id":8888,"name":"Backstage Composer","career":["producer"],"images":{"large":"http://93.184.216.34/composer.png"}}]}`), nil
		case r.URL.Host == "93.184.216.34":
			return cannedResponse(http.StatusOK, http.Header{}, pngBytes), nil
		case strings.Contains(r.URL.Host, "musicbrainz.org") && strings.Contains(r.URL.Path, "/ws/2/artist/mb-comp-1"):
			return cannedResponse(http.StatusOK, http.Header{}, `{"id":"mb-comp-1","name":"Backstage Composer","sort-name":"Backstage Composer","aliases":[],"tags":[],"relations":[{"type":"wikidata","url":{"resource":"https://www.wikidata.org/wiki/Q1"}}]}`), nil
		case strings.Contains(r.URL.Host, "musicbrainz.org"):
			return cannedResponse(http.StatusOK, http.Header{}, `{"artists":[]}`), nil
		case strings.Contains(r.URL.Host, "wikidata.org"):
			bioCalls.Add(1)
			return cannedResponse(http.StatusOK, http.Header{}, bioWikidataBody), nil
		case strings.Contains(r.URL.Host, "wikipedia.org"):
			bioCalls.Add(1)
			return cannedResponse(http.StatusOK, http.Header{}, `{"extract":"幕后作曲家简介","content_urls":{"desktop":{"page":""}}}`), nil
		default:
			return cannedResponse(http.StatusNotFound, http.Header{}, `{}`), nil
		}
	}))
	ctx := context.Background()
	s := manager.store
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := s.LibraryByRoot(ctx, "/music")
	// 1. After an incremental scan, a skipped_composite artist (with 0 match candidates)
	// must surface in PendingArtistReviewSplitPages Tab 2 (复合署名).
	for _, item := range []struct{ path, artist string }{
		{"solo/01.flac", "Solo Check"},
		{"comp/01.flac", "Alpha & Beta"},
	} {
		if err := s.ImportTrack(ctx, storage.ImportInput{
			LibraryID: lib.ID, RelativePath: item.path, FileSize: 1, ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{Title: "Song", Album: "Alb", Artists: []string{item.artist}, AlbumArtists: []string{item.artist}, DiscNumber: 1, TrackNumber: 1},
		}); err != nil {
			t.Fatal(err)
		}
	}
	mb, _ := s.MetadataSourceSetting(ctx, "musicbrainz")
	mb.Enabled = true
	mb.AutoMatch = true
	_ = s.SaveMetadataSourceSetting(ctx, mb)
	bgm, _ := s.MetadataSourceSetting(ctx, "bangumi")
	bgm.Enabled = true
	_ = s.SaveMetadataSourceSetting(ctx, bgm)

	runID, err := manager.StartAll(ctx)
	if err != nil {
		t.Fatalf("incremental scan failed: %v", err)
	}
	manager.Wait()
	r, err := s.DurableArtistRun(ctx, runID)
	if err != nil || r.Status != "completed" || r.Total != 2 || r.Skipped != 1 {
		t.Fatalf("unexpected run: %+v err=%v", r, err)
	}
	// Composite artist now shows up in PendingArtistReviewSplitPages Tab 2 even without candidates.
	_, creditsPage, err := s.PendingArtistReviewSplitPages(ctx, storage.ArtistReviewFilter{Limit: 20}, 20, 0)
	if err != nil || creditsPage.Total != 1 || len(creditsPage.Items) != 1 || creditsPage.Items[0].Artist.Name != "Alpha & Beta" {
		t.Fatalf("expected scanned composite artist on credits tab, got %+v err=%v", creditsPage, err)
	}

	// 2. EnrichAutoConfirmedArtist fetches both Tier 2 Bangumi avatar and biography
	// for pure behind-the-scenes credit artists after identity confirmation.
	if err := s.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "credit/01.flac", FileSize: 1, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Lead Singer"}, AlbumArtists: []string{"Lead Singer"}, Composer: "Backstage Composer", DiscNumber: 1, TrackNumber: 1},
	}); err != nil {
		t.Fatal(err)
	}
	allArtists, _ := s.ArtistsForMatching(ctx)
	var composerID int64
	for _, a := range allArtists {
		if a.Name == "Backstage Composer" {
			composerID = a.ID
		}
	}
	if err := s.UpsertExternalArtistProfile(ctx, composerID, storage.ExternalArtistProfile{
		Source: "musicbrainz", ExternalID: "mb-comp-1", DisplayName: "Backstage Composer",
	}); err != nil {
		t.Fatal(err)
	}
	imgSt, _ := s.ArtistImageSettings(ctx)
	imgSt.DeezerEnabled = false
	imgSt.BangumiEnabled = true
	_ = s.SaveArtistImageSettings(ctx, imgSt)

	manager.EnrichAutoConfirmedArtist(ctx, composerID)
	manager.Wait()
	if bgmCalls.Load() != 1 {
		t.Fatalf("expected Tier 2 Bangumi avatar lookup for AI-confirmed credit artist, calls=%d", bgmCalls.Load())
	}
	if bioCalls.Load() == 0 {
		t.Fatalf("expected biography lookups for credit-only artist, got 0")
	}
	if detail, dErr := s.ArtistDetail(ctx, composerID); dErr != nil || detail.Biography == "" {
		t.Fatalf("expected biography for credit artist, bio=%q err=%v", detail.Biography, dErr)
	}
	if path, _, _, imgErr := s.ArtistImagePath(ctx, composerID); imgErr != nil || path == "" {
		t.Fatalf("expected cached avatar for credit artist, path=%q err=%v", path, imgErr)
	}
}
