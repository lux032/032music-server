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

func TestMatchReviewWorkbenchTabsSortingAndSettingsReturnTo(t *testing.T) {
	_, s, handler, _, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	artists, err := s.ArtistsForMatching(ctx)
	if err != nil || len(artists) == 0 {
		t.Fatalf("artists=%v err=%v", artists, err)
	}
	artistID := artists[0].ID
	if err := s.ReplaceArtistCandidatesForSources(ctx, artistID, []storage.ArtistCandidate{
		{Source: "lastfm", ExternalID: "lf-1", DisplayName: "FirstLowScore", Score: 95},
		{Source: "musicbrainz", ExternalID: "mb-1", DisplayName: "AIRecTarget", Score: 80, MBID: "mb-1"},
	}, []string{"musicbrainz", "lastfm"}); err != nil {
		t.Fatal(err)
	}
	cands, err := s.ArtistCandidates(ctx, artistID)
	if err != nil || len(cands) != 2 {
		t.Fatalf("cands=%+v err=%v", cands, err)
	}
	var mbID int64
	for _, c := range cands {
		if c.Source == "musicbrainz" {
			mbID = c.ID
		}
	}
	if _, err := s.SaveAIReviewDecision(ctx, storage.AIReviewDecision{
		TargetKind: storage.AITargetArtistMatch, TargetID: artistID,
		Action: storage.AIActionRecommendAccept, SelectedCandidateID: mbID,
		Confidence: 94, Reason: "精确命中代表作",
	}); err != nil {
		t.Fatal(err)
	}

	// Default tab is identity; AI-recommended candidate is sorted ahead of higher raw score.
	body := adminGetBody(t, handler, cookie, "/admin/matches")
	if !strings.Contains(body, `data-active-tab="identity"`) {
		t.Fatal("expected default active tab identity")
	}
	if !strings.Contains(body, `id="artist-image-settings-dialog"`) {
		t.Fatal("expected native settings dialog")
	}
	recPos := strings.Index(body, "<b>AIRecTarget</b>")
	otherPos := strings.Index(body, "<b>FirstLowScore</b>")
	if recPos < 0 || otherPos < 0 || recPos > otherPos {
		t.Fatalf("expected AI recommended candidate sorted first: rec=%d other=%d", recPos, otherPos)
	}

	// Explicit ?tab=avatars and runsPage=2 -> tasks tab.
	avatarsBody := adminGetBody(t, handler, cookie, "/admin/matches?tab=avatars")
	if !strings.Contains(avatarsBody, `data-active-tab="avatars"`) {
		t.Fatal("expected avatars tab active")
	}
	runsPageBody := adminGetBody(t, handler, cookie, "/admin/matches?runsPage=2")
	if !strings.Contains(runsPageBody, `data-active-tab="tasks"`) {
		t.Fatal("expected runsPage=2 to activate tasks tab")
	}

	// Saving avatar settings honors safe returnTo and rejects external URLs.
	rec := postAdminForm(t, handler, cookie, csrf, "/admin/settings/artist-images", map[string][]string{
		"bangumi_enabled": {"1"},
		"returnTo":        {"/admin/matches?tab=avatars"},
	})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/matches?tab=avatars&notice=") {
		t.Fatalf("settings redirect=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	unsafeRec := postAdminForm(t, handler, cookie, csrf, "/admin/settings/artist-images", map[string][]string{
		"bangumi_enabled": {"1"},
		"returnTo":        {"https://evil.example/phish"},
	})
	if unsafeRec.Code != http.StatusSeeOther || !strings.HasPrefix(unsafeRec.Header().Get("Location"), "/admin/matches?notice=") {
		t.Fatalf("unsafe settings redirect=%d loc=%q", unsafeRec.Code, unsafeRec.Header().Get("Location"))
	}
}
