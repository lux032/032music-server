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

func TestMatchReviewSplitCountsUncertainBadgeAndJSONAuth(t *testing.T) {
	_, s, handler, _, cookie, _ := rateLimitTestApp(t)
	ctx := context.Background()
	library, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	// Create 52 solo artists (A00..A51) and 1 composite artist (ZZZ & Partner) beyond page 1's 50-item window.
	for i := 0; i < 52; i++ {
		name := fmt.Sprintf("B%02d", i)
		if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: name + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: name, Album: name, Artists: []string{name}, AlbumArtists: []string{name}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "comp/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Duet", Album: "Duet", Artists: []string{"ZZZ & Partner"}, AlbumArtists: []string{"ZZZ & Partner"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	artists, err := s.ArtistsForMatching(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var b00ID int64
	for _, a := range artists {
		if strings.HasPrefix(a.Name, "B") || a.Name == "ZZZ & Partner" {
			if err := s.ReplaceArtistCandidatesForSources(ctx, a.ID, []storage.ArtistCandidate{
				{Source: "lastfm", ExternalID: fmt.Sprintf("lf-%d", a.ID), DisplayName: "HighScoreFirst", Score: 92},
				{Source: "musicbrainz", ExternalID: fmt.Sprintf("mb-%d", a.ID), DisplayName: "UncertainTarget", Score: 80},
			}, []string{"musicbrainz", "lastfm"}); err != nil {
				t.Fatal(err)
			}
			if a.Name == "B00" {
				b00ID = a.ID
			}
		}
	}
	cands, err := s.ArtistCandidates(ctx, b00ID)
	if err != nil || len(cands) != 2 {
		t.Fatalf("cands=%+v err=%v", cands, err)
	}
	var uncertainCandID int64
	for _, c := range cands {
		if c.Source == "musicbrainz" {
			uncertainCandID = c.ID
		}
	}
	if _, err := s.SaveAIReviewDecision(ctx, storage.AIReviewDecision{
		TargetKind: storage.AITargetArtistMatch, TargetID: b00ID,
		Action: storage.AIActionUncertain, SelectedCandidateID: uncertainCandID,
		Confidence: 60, Reason: "同名艺人需核对",
	}); err != nil {
		t.Fatal(err)
	}

	// Page 1 shows ZZ & Partner in Tab 2 despite >50 regular candidates, and keeps tab=identity with runsPage=2.
	body := adminGetBody(t, handler, cookie, "/admin/matches?runsPage=2&tab=identity")
	if !strings.Contains(body, `data-active-tab="identity"`) {
		t.Fatal("explicit tab=identity must not be hijacked by runsPage=2")
	}
	if !strings.Contains(body, "ZZZ &amp; Partner") {
		t.Fatal("composite credit artist past 50th overall position must still appear on credits page 1")
	}
	if strings.Contains(body, "★ AI 推荐") || strings.Contains(body, "确认（AI 推荐）") {
		t.Fatal("uncertain AI decision must not render AI recommended badge or primary CTA")
	}

	// JSON endpoints use requireAdminJSON (401 without session cookie, not 303 redirect to login).
	for _, ep := range []struct{ method, path string }{
		{http.MethodGet, "/admin/matches/images/candidates.json"},
		{http.MethodGet, "/admin/settings/artist-images.json"},
		{http.MethodPost, fmt.Sprintf("/admin/artists/%d/image/web-search", b00ID)},
	} {
		req := httptest.NewRequest(ep.method, ep.path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s unauthenticated status=%d, want 401", ep.method, ep.path, rec.Code)
		}
	}
}

func TestMatchReviewTooltipsCompositeFlaggedPageClampAndModeLabels(t *testing.T) {
	_, s, handler, _, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	// Enable AI so all 3 scan buttons + tooltips render.
	aiCfg := storage.DefaultAISettings()
	aiCfg.BaseURL = "https://api.example.test/v1"
	aiCfg.APIKey = "sk-test"
	aiCfg.Model = "m1"
	if err := s.SaveAISettings(ctx, aiCfg); err != nil {
		t.Fatal(err)
	}
	rev := storage.DefaultAIReviewSettings()
	rev.Enabled = true
	if err := s.SaveAIReviewSettings(ctx, rev); err != nil {
		t.Fatal(err)
	}

	// Import a solo artist with pending candidate + a composite artist flagged as skipped_composite (no candidates).
	_ = s.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "solo/01.flac", FileSize: 1, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "SoloSong", Album: "SoloAlb", Artists: []string{"SoloArtist"}, AlbumArtists: []string{"SoloArtist"}, DiscNumber: 1, TrackNumber: 1},
	})
	_ = s.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "comp/01.flac", FileSize: 1, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "CollabSong", Album: "CollabAlb", Artists: []string{"Singer A & Singer B"}, AlbumArtists: []string{"Singer A"}, DiscNumber: 1, TrackNumber: 1},
	})
	artists, _ := s.ArtistsForMatching(ctx)
	var soloID, compID int64
	var compInput storage.ArtistMatchInput
	for _, a := range artists {
		switch a.Name {
		case "SoloArtist":
			soloID = a.ID
		case "Singer A & Singer B":
			compID = a.ID
			compInput = a
		}
	}
	_ = s.ReplaceArtistCandidatesForSources(ctx, soloID, []storage.ArtistCandidate{
		{Source: "musicbrainz", ExternalID: "mb-solo", DisplayName: "SoloArtist", Score: 82},
	}, []string{"musicbrainz"})
	if err := s.SaveArtistCompositeSkip(ctx, compInput, nil); err != nil {
		t.Fatal(err)
	}
	// Create an active image backfill run with mode=rescan_all to check localized Chinese mode label.
	if _, err := s.CreateArtistImageBackfillRunWithMode(ctx, "rescan_all", []storage.ArtistImageBackfillCandidate{{ID: soloID, Name: "SoloArtist"}}); err != nil {
		t.Fatal(err)
	}

	// Out-of-range page=99 must clamp back to page 1 and still show SoloArtist, plus all 3 scan tooltips and composite credit.
	body := adminGetBody(t, handler, cookie, "/admin/matches?page=99&creditsPage=99")
	for _, want := range []string{
		`id="tip-scan-incremental"`,
		`id="tip-scan-full"`,
		`id="tip-scan-ai"`,
		`#icon-alert-circle`,
		`data-pending-text="启动中…"`,
		`SoloArtist`,
		`Singer A &amp; Singer B`,
		`全库强制重扫`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in /admin/matches body", want)
		}
	}
	if strings.Contains(body, `（模式：rescan_all）`) {
		t.Fatal("raw english backfill mode leaked in active card")
	}

	// Applying credit split cleans up the resolved composite artist from Tab 2.
	rec := postAdminForm(t, handler, cookie, csrf, fmt.Sprintf("/admin/artists/%d/apply-credit-split", compID), map[string][]string{
		"part":     {"Singer A", "Singer B"},
		"returnTo": {"/admin/matches?tab=credits"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("apply-credit-split status=%d", rec.Code)
	}
	afterBody := adminGetBody(t, handler, cookie, "/admin/matches?tab=credits")
	if strings.Contains(afterBody, `Singer A &amp; Singer B`) {
		t.Fatal("resolved composite credit artist should leave Tab 2 after split")
	}
}
