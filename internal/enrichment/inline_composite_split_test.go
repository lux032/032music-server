package enrichment

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestInlineCompositeCreditAutoSplitDoesNotOverwriteSkipState(t *testing.T) {
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "musicbrainz.org") {
			return cannedResponse(http.StatusOK, http.Header{}, `{"artists":[]}`), nil
		}
		return cannedResponse(http.StatusNotFound, http.Header{}, `{}`), nil
	}))
	ctx := context.Background()
	s := manager.store

	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := s.LibraryByRoot(ctx, "/music")
	for _, item := range []struct{ path, artist, composer string }{
		{"solo/01.flac", "Solo Check", ""},
		{"comp/01.flac", "Lead Singer", "ACE, 亀岡夏海"},
	} {
		if err := s.ImportTrack(ctx, storage.ImportInput{
			LibraryID: lib.ID, RelativePath: item.path, FileSize: 1, ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{
				Title: "Song", Album: "Alb",
				Artists: []string{item.artist}, AlbumArtists: []string{item.artist},
				Composer: item.composer, DiscNumber: 1, TrackNumber: 1,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	mb, _ := s.MetadataSourceSetting(ctx, "musicbrainz")
	mb.Enabled = true
	mb.AutoMatch = true
	_ = s.SaveMetadataSourceSetting(ctx, mb)

	var inlineCalled int
	manager.SetInlineAIReviewer(func(ctx context.Context, kind string, targetID int64) bool {
		if kind != storage.AITargetCreditSplit {
			return false
		}
		inlineCalled++
		_, _, err := s.ApplyCompositeCreditSplitByAI(ctx, targetID, []string{"ACE", "亀岡夏海"})
		return err == nil
	})

	runID, err := manager.StartAll(ctx)
	if err != nil {
		t.Fatalf("StartAll failed: %v", err)
	}
	manager.Wait()

	r, err := s.DurableArtistRun(ctx, runID)
	if err != nil || r.Status != "completed" {
		t.Fatalf("unexpected durable run: %+v err=%v", r, err)
	}
	if inlineCalled != 1 {
		t.Fatalf("expected inlineAI called once for composite credit, got %d", inlineCalled)
	}
	// Composite artist must NOT be surfaced in Tab 2 and must NOT have skipped_composite written back.
	_, creditsPage, err := s.PendingArtistReviewSplitPages(ctx, storage.ArtistReviewFilter{Limit: 20}, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if creditsPage.Total != 0 {
		t.Fatalf("expected 0 pending composite credits after inline auto-split, got %+v", creditsPage.Items)
	}
}
