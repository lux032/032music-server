package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestMetadataCanonicalNamingSetting(t *testing.T) {
	store := credentialTestStore(t)
	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	page := getAdmin(t, handler, cookie, "/admin/settings/metadata")
	for _, want := range []string{`value="artist_identity"`, `name="canonical_native_name_enabled"`, `自动统一为 MusicBrainz 原文名`} {
		if !strings.Contains(page, want) {
			t.Fatalf("metadata settings page missing %q", want)
		}
	}

	// Disable canonical naming.
	rec := postSecurity(t, app, handler, cookie, "/admin/settings/metadata", url.Values{
		"scope": {"artist_identity"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("disable canonical naming status=%d, want 303", rec.Code)
	}
	st, err := store.ArtistIdentitySettings(context.Background())
	if err != nil || st.CanonicalNativeNameEnabled {
		t.Fatalf("expected CanonicalNativeNameEnabled=false, got %+v err=%v", st, err)
	}

	// Re-enable canonical naming.
	rec = postSecurity(t, app, handler, cookie, "/admin/settings/metadata", url.Values{
		"scope":                         {"artist_identity"},
		"canonical_native_name_enabled": {"on"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("re-enable canonical naming status=%d, want 303", rec.Code)
	}
	st, err = store.ArtistIdentitySettings(context.Background())
	if err != nil || !st.CanonicalNativeNameEnabled {
		t.Fatalf("expected CanonicalNativeNameEnabled=true, got %+v err=%v", st, err)
	}
}

func TestAIArtistMergeModeSetting(t *testing.T) {
	store := credentialTestStore(t)
	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	page := getAdmin(t, handler, cookie, "/admin/settings/ai")
	if !strings.Contains(page, `name="artist_merge_mode"`) || !strings.Contains(page, "艺术家冲突合并") {
		t.Fatalf("ai settings page missing artist_merge_mode control")
	}

	rec := postSecurity(t, app, handler, cookie, "/admin/settings/ai", url.Values{
		"base_url":          {"https://api.example.test/v1"},
		"api_key":           {"sk-secret"},
		"model":             {"gemini-2.5-flash-lite"},
		"rpm":               {"120"},
		"concurrency":       {"2"},
		"ai_review_enabled": {"on"},
		"album_mode":        {"auto"},
		"track_mode":        {"auto"},
		"work_mode":         {"auto"},
		"artist_mode":       {"auto"},
		"artist_merge_mode": {"recommend"},
		"credit_mode":       {"recommend"},
		"series_mode":       {"recommend"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save ai settings status=%d, want 303", rec.Code)
	}
	rev, err := store.AIReviewSettings(context.Background())
	if err != nil || rev.ArtistMergeMode != storage.AIReviewModeRecommend {
		t.Fatalf("expected ArtistMergeMode=recommend, got %+v err=%v", rev, err)
	}
}

func TestConvergeArtistConflictsHandlerAndMergeHistoryBadge(t *testing.T) {
	ctx := context.Background()
	store := credentialTestStore(t)
	if err := store.EnsureLibrary(ctx, "main", "/music"); err != nil {
		t.Fatalf("ensure library: %v", err)
	}
	const mbid = "11111111-1111-4111-8111-111111111111"
	importArtist := func(path, name, tagMBID string) int64 {
		raw := map[string][]string{"ARTIST": {name}}
		if tagMBID != "" {
			raw["MUSICBRAINZ_ARTISTID"] = []string{tagMBID}
		}
		if err := store.ImportTrack(ctx, storage.ImportInput{
			LibraryID:    1,
			RelativePath: path,
			FileSize:     100,
			ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{
				Title:        path,
				Album:        "Album " + path,
				DiscNumber:   1,
				TrackNumber:  1,
				Container:    "flac",
				Artists:      []string{name},
				AlbumArtists: []string{name},
				Raw:          raw,
			},
		}); err != nil {
			t.Fatalf("import %s: %v", path, err)
		}
		all, _ := store.ArtistsForMatching(ctx)
		for _, a := range all {
			if a.Name == name {
				return a.ID
			}
		}
		t.Fatalf("artist %q not found", name)
		return 0
	}

	jpID := importArtist("jp.flac", "梶浦由記", mbid)
	if err := store.UpsertExternalArtistProfile(ctx, jpID, storage.ExternalArtistProfile{
		Source:      "musicbrainz",
		ExternalID:  mbid,
		DisplayName: "梶浦由記",
		Country:     "JP",
		Aliases:     []string{"Yuki Kajiura"},
	}); err != nil {
		t.Fatalf("upsert owner profile: %v", err)
	}
	_ = importArtist("romaji.flac", "Yuki Kajiura", mbid)

	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	// /admin/matches renders the converge button with count (1).
	matchesPage := getAdmin(t, handler, cookie, "/admin/matches")
	if !strings.Contains(matchesPage, "一键收敛同 MBID 重复艺人 (1)") {
		t.Fatalf("matches page missing converge button with count 1")
	}

	// Missing CSRF token is rejected with 403.
	badReq := httptest.NewRequest(http.MethodPost, "/admin/matches/converge-conflicts", strings.NewReader("returnTo=/admin/matches"))
	badReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badReq.AddCookie(cookie)
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without CSRF, got %d", badRec.Code)
	}

	// Valid POST converges duplicate artist and redirects with notice.
	rec := postSecurity(t, app, handler, cookie, "/admin/matches/converge-conflicts", url.Values{
		"returnTo": {"/admin/matches"},
	})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), url.QueryEscape("已一键收敛 1 位")) {
		t.Fatalf("unexpected converge response: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// /admin/merges renders the backfill trigger badge ("一键收敛").
	mergesPage := getAdmin(t, handler, cookie, "/admin/merges")
	if !strings.Contains(mergesPage, "一键收敛") || !strings.Contains(mergesPage, "Yuki Kajiura") || !strings.Contains(mergesPage, "梶浦由記") {
		t.Fatalf("merges page missing backfill badge or artist names")
	}
}
