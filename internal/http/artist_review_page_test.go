package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestArtistReviewPageBeyondFiveHundredAndCorrectionExcluded(t *testing.T) {
	_, s, handler, _, cookie, _ := rateLimitTestApp(t)
	ctx := context.Background()
	library, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 501; i++ {
		name := fmt.Sprintf("A%03d", i)
		if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: name + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: name, Album: name, Artists: []string{name}, AlbumArtists: []string{name}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"ZZTarget", "ZZTarget & Other"} {
		if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: name + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: name, Album: name, Artists: []string{name}, AlbumArtists: []string{name}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	artists, err := s.ArtistsForMatching(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, artist := range artists {
		if strings.HasPrefix(artist.Name, "ZZTarget") {
			if err := s.ReplaceArtistCandidates(ctx, artist.ID, []storage.ArtistCandidate{{Source: "musicbrainz", ExternalID: fmt.Sprint(artist.ID), DisplayName: "ReviewCandidate", Score: 85}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/matches?q=ZZTarget&source=musicbrainz", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "ReviewCandidate") {
		t.Fatalf("status=%d body=%s", rec.Code, body)
	}
	// 合作署名艺术家出现在独立的“需先修正署名”组，链接到艺术家详情，
	// 不提供确认/拒绝表单，不再被静默隐藏。
	if !strings.Contains(body, "需先修正署名") || !strings.Contains(body, "ZZTarget &amp; Other") {
		t.Fatalf("correction group missing: %s", body)
	}
	correction := body[strings.Index(body, "需先修正署名"):]
	if strings.Contains(correction, "/confirm/") || strings.Contains(correction, "/reject/") {
		t.Fatalf("correction group must not carry confirm/reject forms: %s", correction)
	}
}
